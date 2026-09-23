package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/progress"
)

// afmSecretEnvPrefix/afmSyspromptEnvPrefix — те же транспортные префиксы, что
// pkg/docker расставляет в окружении afm-процесса для autoShim-агентов
// (AFM_SECRET_<CMD>/AFM_SYSPROMPT_<CMD>, см. docker/launcher.go). Третий
// префикс, lifecyclehooks.HookSecretTransportPrefix, уже экспортирован из
// pkg/lifecyclehooks — переиспользуем его вместо третьей копии той же строки.
const (
	afmSecretEnvPrefix    = "AFM_SECRET_"    //nolint:gosec // это имя переменной окружения, а не сам секрет
	afmSyspromptEnvPrefix = "AFM_SYSPROMPT_" //nolint:gosec // аналогично
)

// IsCrossAgentTransportSecret сообщает, что kv (строка "ИМЯ=значение" из
// os.Environ()) — один из транспортных секретов, которыми afm передаёт
// чужие авторизационные данные СВОИМ ЖЕ дочерним процессам (autoShim-агенты,
// lifecycle-хуки): AFM_SECRET_*, AFM_HOOK_SECRET_*, AFM_SYSPROMPT_*. Нужна
// только для C2 (см. вызывающий код в run): непроверенный verify-верификатор
// не должен унаследовать секреты ЧУЖИХ агентов/хуков, которые сам afm-процесс
// держит в своём окружении только транзитом (см. AGENTS.md, "Docker Mode" /
// "Phase 3: секреты и env lifecycle-хуков"). Экспортирована (D2a второй раунд
// код-ревью): pkg/orchestrator/verify.go переиспользует ЭТУ ЖЕ функцию для
// санитизации окружения shell-verify шагов — единая точка вместо второй копии
// списка префиксов.
func IsCrossAgentTransportSecret(kv string) bool {
	return strings.HasPrefix(kv, afmSecretEnvPrefix) ||
		strings.HasPrefix(kv, afmSyspromptEnvPrefix) ||
		strings.HasPrefix(kv, lifecyclehooks.HookSecretTransportPrefix)
}

// afmSecretEnvName воспроизводит ТОТ ЖЕ transform, что pkg/docker/wrapper.go's
// envName делает над именем враппер-команды при построении переменной
// AFM_SECRET_<NAME> (uppercase; всё, кроме [A-Z0-9], → '_'). Дублируется
// здесь (а не импортируется из pkg/docker), чтобы не тянуть в pkg/executor
// докер-специфичный пакет ради одной маленькой чистой функции — держите оба
// места в синхроне, если правило санитизации когда-нибудь изменится.
func afmSecretEnvName(cmd string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(cmd) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// isVerifierOwnSecret сообщает, что kv — ИМЕННО собственный транспортный
// секрет верификатора (AFM_SECRET_<envName(verifyCommand)>), который его же
// сгенерированный autoShim-враппер (pkg/docker/wrapper.go) читает для
// авторизации (D2b код-ревью): голый VerifyMode-strip удалял ВСЕ AFM_SECRET_*
// без разбора, включая секрет самого верификатора — аутентифицированный
// codex-recipe верификатор стартовал бы без своего же токена. Сравнение по
// точному имени переменной (префикс + "="), а не просто по префиксу —
// иначе AFM_SECRET_MYCODEX2 ложно совпал бы с verifyCommand="mycodex".
func isVerifierOwnSecret(kv, verifyCommand string) bool {
	if verifyCommand == "" {
		return false
	}
	return strings.HasPrefix(kv, afmSecretEnvPrefix+afmSecretEnvName(verifyCommand)+"=")
}

// isClaudeAuthorCredential сообщает, что kv — одна из переменных окружения,
// которыми Docker forwards Claude-авторизацию АВТОРА внутрь контейнера
// (F4 код-ревью): config.ClaudeAuthEnvVars (CLAUDE_CODE_OAUTH_TOKEN/
// ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN) плюс ANTHROPIC_BASE_URL — тот же
// список, что pkg/docker/launcher.go's dockerForwardEnvVars форвардит в
// контейнер. Read-only codex-верификатор в них не нуждается: своя
// авторизация — либо смонтированный ~/.codex (type: codex), либо собственный
// AFM_SECRET_<CMD> recipe-секрет (см. isVerifierOwnSecret выше, D2b) — и не
// должен иметь возможность отразить ЧУЖИЕ (авторские) credentials в своём
// персистируемом JSON-ответе. Сравнение по точному имени переменной
// (name+"="), а не по префиксу — те же четыре имени целиком, без
// вариативности вроде AFM_SECRET_*.
func isClaudeAuthorCredential(kv string) bool {
	if strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
		return true
	}
	for _, name := range config.ClaudeAuthEnvVars {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

// Config configures the executor.
type Config struct {
	Command        string
	ExtraArgs      []string
	IdleTimeout    time.Duration
	TruncateOutput int                          // 0 = no truncation; max chars for Bash-command/tool detail (agent text narrative is never truncated)
	OnAction       func(tool, detail string)    // called for each parsed agent action (may be nil)
	SessionID      string                       // if non-empty, passed via --session-id (or --resume when Resume=true)
	Resume         bool                         // if true, --resume <SessionID> is used instead of --session-id
	StageDir       string                       // passed to agent as AFM_STAGE_DIR env var (file-based dialog protocol)
	WrapperDir     string                       // if set, prepended to PATH in agent env so generated wrapper scripts resolve
	Dir            string                       // if set, agent runs with this working directory (project root from flow.root_dir)
	Debug          bool                         // if true, log the exact agent input (prompt) to debug logs
	RunDir         string                       // run directory root; with Debug, <RunDir>/debug.log gets every agent input
	StageID        string                       // stage id for debug log tagging + per-stage prompt log path (decoupled from StageDir/AFM_STAGE_DIR)
	Phase          string                       // phase label for the accounting Observation this invocation produces
	UsageHint      accounting.UsageHint         // non-authoritative channel/model hint fed to accounting.NewCollector
	OnUsage        func(accounting.Observation) // called exactly once per RunPlanning/RunAgent invocation with the collected usage (nil = accounting disabled); never called by RunScript
	// InterruptCh, if set, is watched during RunAgent: a signal on this channel
	// sends SIGINT to the subprocess (not SIGKILL, not ctx cancellation) —
	// graceful, user-requested interrupt (agent_suggest), distinct from idle
	// timeout / full-run shutdown. nil channel is safe (select never fires).
	InterruptCh <-chan struct{}
	// VerifyMode, if true, signals to the underlying adapter (e.g.
	// scripts/codex-as-claude.sh via a narrow CODEX_VERIFY env var, see
	// RunVerifyAgent) that this invocation is a read-only AI-verify pass, not
	// a normal author/builder run — no bypass/full-access sandbox flags, no
	// editing tools. Set only by RunVerifyAgent; never by RunPlanning/RunAgent.
	VerifyMode bool
}

// ErrUserInterrupted signals that the agent process was stopped because the
// user requested an interrupt (via Config.InterruptCh) — not a real failure.
// Callers (runWithRetry) must distinguish this from retry/failure handling.
var ErrUserInterrupted = errors.New("user interrupted")

// ErrIdleTimeout — сигнальная ошибка простоя (за IdleTimeout не пришло ни
// одной строки stdout). run() оборачивает ею свою текстовую ошибку через
// %w, чтобы вызывающий код (RunVerifyAgent) мог отличить таймаут от прочих
// ошибок процесса через errors.Is, не разбирая текст сообщения.
var ErrIdleTimeout = errors.New("idle timeout")

// interruptGracePeriod bounds how long we wait for the subprocess to exit
// gracefully after SIGINT before force-killing it as a safety net against a
// hung/misbehaving process.
const interruptGracePeriod = 15 * time.Second

const (
	contentTypeText    = "text"
	contentTypeToolUse = "tool_use"
	toolNameBash       = "Bash"
	toolNameWrite      = "Write"
)

// DefaultClaudeArgs returns the standard claude stream-json invocation flags.
//
// --verbose is required by Claude Code 2.1.x when --print is combined with
// --output-format=stream-json, and is harmless on versions where it is
// optional. It also makes the stream contain tool_use events, which the
// executor's parser relies on.
func DefaultClaudeArgs() []string {
	return []string{"--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
}

// ResolveArgs prepends DefaultClaudeArgs to extra and drops exact duplicates.
// Used for interactive stages, which always need the claude flags regardless of
// user config; dedup avoids passing --verbose twice when the user also sets it.
func ResolveArgs(extra []string) []string {
	merged := append(append([]string{}, DefaultClaudeArgs()...), extra...)
	seen := make(map[string]bool, len(merged))
	out := make([]string, 0, len(merged))
	for _, a := range merged {
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// Executor spawns AI client subprocesses.
type Executor struct {
	cfg Config
	// stderrSinkOverride — test-only seam (see scriptStderrSink). nil in
	// production: New never sets it, so RunScript always opens the real
	// .stderr.log file.
	stderrSinkOverride func(logFile string) (io.Writer, func())
}

// New creates an Executor.
func New(cfg Config) *Executor {
	if cfg.Command == "" {
		cfg.Command = config.ClaudeCommand
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	// For claude-compatible commands, prepend default stream-json flags if no custom args set.
	if len(cfg.ExtraArgs) == 0 {
		cfg.ExtraArgs = DefaultClaudeArgs()
	}
	return &Executor{cfg: cfg}
}

type streamContent struct {
	Type  string          `json:"type"`  // "text" or "tool_use"
	Text  string          `json:"text"`  // for type=="text"
	Name  string          `json:"name"`  // for type=="tool_use"
	Input json.RawMessage `json:"input"` // for type=="tool_use"
}

type streamMessage struct {
	Content []streamContent `json:"content"`
}

// streamEvent is a minimal representation of a claude stream-json event.
type streamEvent struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype"`
	Message *streamMessage `json:"message"`
}

// toolInput holds the subset of tool call input fields we care about.
type toolInput struct {
	FilePath string `json:"file_path"`
	Path     string `json:"path"`
	Command  string `json:"command"`
	Pattern  string `json:"pattern"`
	Query    string `json:"query"`
}

// ParseToolAction parses a single stream-json line and returns a human-readable
// tool name and detail. Returns ok=false for events we don't log (result, system, etc.).
func ParseToolAction(line string, limit int) (toolName, detail string, ok bool) {
	ev, parsed := parseStreamEvent(line)
	if !parsed {
		return "", "", false
	}
	for _, c := range ev.Message.Content {
		if tool, detail, actionOK := contentToAction(c, limit); actionOK {
			return tool, detail, true
		}
	}
	return "", "", false
}

// parseStreamEvent parses a stream-json line into a streamEvent.
// Returns ok=false for non-assistant events or parse failures.
func parseStreamEvent(line string) (*streamEvent, bool) {
	var ev streamEvent
	if json.Unmarshal([]byte(line), &ev) != nil {
		return nil, false
	}
	if ev.Type != "assistant" || ev.Message == nil {
		return nil, false
	}
	return &ev, true
}

// isErrorLine checks if a non-assistant stream line represents an actual error.
// Avoids false positives from JSON keys like "is_error":false.
func isErrorLine(line string) bool {
	// Try structured check first: if line is valid JSON, only flag explicit errors.
	var obj map[string]any
	if json.Unmarshal([]byte(line), &obj) == nil {
		if isErr, _ := obj["is_error"].(bool); isErr {
			return true
		}
		if typ, _ := obj["type"].(string); typ == "error" {
			return true
		}
		return false
	}
	// Not JSON — fall back to substring check for raw error messages.
	return strings.Contains(line, "Error") || strings.Contains(line, "error")
}

// contentToAction converts a single content block to a loggable action.
func contentToAction(c streamContent, limit int) (toolName, detail string, ok bool) {
	switch c.Type {
	case contentTypeText:
		if c.Text == "" {
			return "", "", false
		}
		// Нарратив агента (его «мысли»/лог) НЕ обрезаем: limit бьёт только по
		// механическому выводу инструментов (Bash-команда, raw input) ниже.
		// Пользователю нужен полный текст рассуждений — иначе в ленте длинный
		// summary обрывался на «…», а таблицы/выводы за точкой обрезки терялись.
		return contentTypeText, c.Text, true
	case contentTypeToolUse:
		var inp toolInput
		json.Unmarshal(c.Input, &inp) //nolint:errcheck
		fp := inp.FilePath
		if fp == "" {
			fp = inp.Path
		}
		if fp == "" {
			fp = inp.Pattern
		}
		switch c.Name {
		case toolNameWrite, "Edit", "Read", "Glob", "Grep":
			return c.Name, fp, true
		case toolNameBash:
			cmd := inp.Command
			if limit > 0 && len(cmd) > limit {
				cmd = cmd[:limit] + "..."
			}
			return toolNameBash, cmd, true
		default:
			d := fp
			if d == "" {
				d = inp.Command
			}
			if d == "" {
				d = inp.Query
			}
			if d == "" {
				d = string(c.Input)
				if limit > 0 && len(d) > limit {
					d = d[:limit] + "..."
				}
			}
			return c.Name, d, true
		}
	default:
		return "", "", false
	}
}

// openStderrLog opens <logBase>.stderr.log for appending so the agent's stderr
// (e.g. claude "requires --verbose") is captured instead of lost. Returns nil
// on error — stderr is diagnostic only, callers fall back to io.Discard.
func openStderrLog(logFile string) *os.File {
	f, err := os.OpenFile(strings.TrimSuffix(logFile, ".log")+".stderr.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	return f
}

// dualWriter always writes to BOTH stream (the live line-streamer feeding
// Config.OnAction, which never errors) and file (the durable .stderr.log
// diagnostic sink, best-effort) — and always reports success to its caller
// (os/exec's stderr-copier). This is deliberately NOT io.MultiWriter: that
// helper stops at the FIRST writer that returns an error, so a full-disk (or
// otherwise failing) .stderr.log write would silently stop the live stream
// too — killing stderr visibility exactly when the disk is full and the
// build is failing. A file-sink error here is swallowed (diagnostic only,
// same trade-off openStderrLog itself already makes by degrading to nil on
// open failure).
type dualWriter struct {
	stream io.Writer
	file   io.Writer
}

func (d *dualWriter) Write(p []byte) (int, error) {
	_, _ = d.stream.Write(p) // lineWriter.Write never returns an error
	_, _ = d.file.Write(p)   // best-effort: a log-write failure must not stop streaming
	return len(p), nil
}

// scriptStderrSink opens the durable stderr sink for RunScript and returns it
// together with a closer. Overridable per-Executor via stderrSinkOverride
// (test-only, unexported) so tests in this package can simulate a file-write
// failure (e.g. ENOSPC) without depending on an OS-specific always-full
// device like /dev/full (absent on macOS).
func (e *Executor) scriptStderrSink(logFile string) (io.Writer, func()) {
	if e.stderrSinkOverride != nil {
		return e.stderrSinkOverride(logFile)
	}
	if sf := openStderrLog(logFile); sf != nil {
		return sf, func() { sf.Close() }
	}
	return io.Discard, func() {}
}

// RunPlanning runs the AI client with prompt via stdin, collects text output
// into outFile, writes human-readable log to logFile, and raw stream to logFile+".jsonl".
func (e *Executor) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	jf, err := os.OpenFile(jsonlFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open jsonl file: %w", err)
	}
	defer jf.Close()

	// Capture the agent's stderr to a sibling .stderr.log (diagnostic only).
	var stderr = io.Discard
	if sf := openStderrLog(logFile); sf != nil {
		stderr = sf
		defer sf.Close()
	}

	lg.LogStart("planning", stageName)

	absOut, err := filepath.Abs(outFile)
	if err != nil {
		absOut = outFile
	}

	var textBuf strings.Builder
	var firstErr string
	var agentWroteOutFile bool
	phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))
	collector := accounting.NewCollector(e.cfg.UsageHint)
	runErr := e.run(ctx, prompt, phase, stderr, func(line string) {
		collector.Observe([]byte(line))
		jf.WriteString(line + "\n") //nolint:errcheck
		ev, ok := parseStreamEvent(line)
		if !ok {
			if isErrorLine(line) {
				lg.LogAction("error", line)
				if firstErr == "" {
					firstErr = line
				}
			}
			return
		}
		for _, c := range ev.Message.Content {
			if c.Type == contentTypeText {
				textBuf.WriteString(c.Text)
			}
			if c.Type == contentTypeToolUse && c.Name == toolNameWrite {
				var inp toolInput
				json.Unmarshal(c.Input, &inp) //nolint:errcheck
				if abs, absErr := filepath.Abs(inp.FilePath); inp.FilePath != "" && absErr == nil && abs == absOut {
					agentWroteOutFile = true
				}
			}
			if tool, detail, actionOK := contentToAction(c, e.cfg.TruncateOutput); actionOK {
				lg.LogAction(tool, detail)
				if e.cfg.OnAction != nil {
					e.cfg.OnAction(tool, detail)
				}
			}
		}
	})

	lg.LogEnd(runErr)
	if e.cfg.OnUsage != nil {
		e.cfg.OnUsage(collector.Finish(runErr))
	}
	if runErr != nil {
		if firstErr != "" {
			return fmt.Errorf("%s: %w", firstErr, runErr)
		}
		return runErr
	}
	if agentWroteOutFile || textBuf.Len() == 0 {
		// Агент записал план через Write tool — текст чата (резюме,
		// комментарии) не должен затирать файл плана.
		return nil
	}
	return os.WriteFile(outFile, []byte(textBuf.String()), 0644)
}

// WrittenFiles возвращает пути файлов, записанных агентом через Write tool,
// в порядке появления событий в stream-json логе. Отсутствующий или
// нечитаемый лог даёт пустой список.
func WrittenFiles(jsonlPath string) []string {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var files []string
	sc := bufio.NewScanner(f)
	// Строки stream-json содержат полный контент Write-вызовов и легко
	// превышают дефолтный лимит сканера в 64 КБ.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ev, ok := parseStreamEvent(sc.Text())
		if !ok {
			continue
		}
		for _, c := range ev.Message.Content {
			if c.Type != contentTypeToolUse || c.Name != toolNameWrite {
				continue
			}
			var inp toolInput
			if json.Unmarshal(c.Input, &inp) != nil || inp.FilePath == "" {
				continue
			}
			files = append(files, inp.FilePath)
		}
	}
	return files
}

// RunAgent runs the AI client with prompt via stdin, writing human-readable
// actions to logFile and raw stream-json to logFile with .jsonl extension.
func (e *Executor) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	jf, err := os.OpenFile(jsonlFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open jsonl file: %w", err)
	}
	defer jf.Close()

	// Capture the agent's stderr to a sibling .stderr.log (diagnostic only).
	var stderr = io.Discard
	if sf := openStderrLog(logFile); sf != nil {
		stderr = sf
		defer sf.Close()
	}

	lg.LogStart(agentType, stageName)

	var firstErr string
	phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))
	collector := accounting.NewCollector(e.cfg.UsageHint)
	runErr := e.run(ctx, prompt, phase, stderr, func(line string) {
		collector.Observe([]byte(line))
		jf.WriteString(line + "\n") //nolint:errcheck
		ev, ok := parseStreamEvent(line)
		if !ok {
			if isErrorLine(line) {
				lg.LogAction("error", line)
				if firstErr == "" {
					firstErr = line
				}
			}
			return
		}
		for _, c := range ev.Message.Content {
			if tool, detail, actionOK := contentToAction(c, e.cfg.TruncateOutput); actionOK {
				lg.LogAction(tool, detail)
				if e.cfg.OnAction != nil {
					e.cfg.OnAction(tool, detail)
				}
			}
		}
	})

	lg.LogEnd(runErr)
	if e.cfg.OnUsage != nil {
		e.cfg.OnUsage(collector.Finish(runErr))
	}
	if runErr != nil && firstErr != "" {
		return fmt.Errorf("%s: %w", firstErr, runErr)
	}
	return runErr
}

// RunVerifyAgent запускает один проход AI-verify: свежую read-only сессию
// (без --resume/session-id, без AFM_STAGE_DIR — верификатор не участник
// файлового диалогового протокола, §7.3 плана AI-verify) и захватывает
// ФИНАЛЬНЫЙ ассистентский текстовый блок как машинный ответ модели, а не
// весь лог. Переиспользует общий run() — не копирует тело RunPlanning/RunAgent.
//
// В отличие от них, успех/провал самого процесса
// (ProcessOK/TimedOut/Interrupted) и вердикт модели (Result) — два
// независимых поля возвращаемого verify.RunOutcome: ненулевой exit code
// после "pass" НИКОГДА не считается пройденным, а пустой/битый ответ при
// штатном exit 0 — это ProtocolErr, а не молчаливое решение afm о коде
// (§5.4 плана). Текст модели никогда не решает cost/exit сам по себе.
//
// Возвращаемая error — только инфраструктурный сбой самого вызова (не
// открылся лог, не записался resultFile); исход самой проверки — целиком в
// RunOutcome, даже если error == nil.
func (e *Executor) RunVerifyAgent(ctx context.Context, agentType, stageName, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return verify.RunOutcome{}, err
	}
	defer lg.Close()

	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	jf, err := os.OpenFile(jsonlFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return verify.RunOutcome{}, fmt.Errorf("open jsonl file: %w", err)
	}
	defer jf.Close()

	var stderr = io.Discard
	if sf := openStderrLog(logFile); sf != nil {
		stderr = sf
		defer sf.Close()
	}

	lg.LogStart(agentType, stageName)

	// Верификатор — всегда свежая сессия без диалогового протокола:
	// принудительно обнуляем поля, которые могли бы просочиться из общего
	// Config (--resume/session-id и AFM_STAGE_DIR автора), независимо от
	// того, что несёт исходный e.cfg.
	verifyCfg := e.cfg
	verifyCfg.SessionID = ""
	verifyCfg.Resume = false
	verifyCfg.StageDir = ""
	verifyCfg.VerifyMode = true
	ve := &Executor{cfg: verifyCfg}

	var finalText strings.Builder
	var haveFinalText bool
	var firstErr string
	phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))
	collector := accounting.NewCollector(ve.cfg.UsageHint)
	runErr := ve.run(ctx, prompt, phase, stderr, func(line string) {
		collector.Observe([]byte(line))
		jf.WriteString(line + "\n") //nolint:errcheck
		ev, ok := parseStreamEvent(line)
		if !ok {
			if isErrorLine(line) {
				lg.LogAction("error", line)
				if firstErr == "" {
					firstErr = line
				}
			}
			return
		}
		for _, c := range ev.Message.Content {
			if c.Type == contentTypeText {
				// Финальный ответ — последний ассистентский текстовый
				// блок: каждый следующий ЗАМЕНЯЕТ предыдущий, а не
				// дописывается к нему — промежуточные рассуждения не
				// должны перемешаться с итоговым JSON.
				finalText.Reset()
				finalText.WriteString(c.Text)
				haveFinalText = true
			}
			if tool, detail, actionOK := contentToAction(c, ve.cfg.TruncateOutput); actionOK {
				lg.LogAction(tool, detail)
				if ve.cfg.OnAction != nil {
					ve.cfg.OnAction(tool, detail)
				}
			}
		}
	})

	lg.LogEnd(runErr)
	if ve.cfg.OnUsage != nil {
		ve.cfg.OnUsage(collector.Finish(runErr))
	}
	if firstErr != "" {
		lg.LogAction("verify-note", "process reported an error line; see log above: "+firstErr)
	}

	outcome := verify.RunOutcome{}
	switch {
	case errors.Is(runErr, ErrUserInterrupted):
		outcome.Interrupted = true
	case errors.Is(runErr, ErrIdleTimeout):
		outcome.TimedOut = true
	case runErr == nil:
		outcome.ProcessOK = true
	default:
		// Прочая ошибка запуска (не таймаут, не прерывание) — процесс
		// просто не отработал штатно; все три флага остаются false.
	}

	// Сохраняем сырой финальный текст, если он вообще появился, — даже если
	// процесс потом упал ненулевым exit code (аудиторский след того, что
	// модель успела написать, не более).
	if haveFinalText && strings.TrimSpace(finalText.String()) != "" {
		if werr := os.WriteFile(resultFile, []byte(finalText.String()), 0644); werr != nil {
			return outcome, fmt.Errorf("write result file: %w", werr)
		}
	}

	if !outcome.ProcessOK {
		// Процесс не завершился штатно (таймаут/прерывание/иная ошибка) —
		// что бы модель ни написала в текст, это не решение о коде: даже
		// "pass" в финальном тексте не засчитывается (§5.4 плана).
		return outcome, nil
	}

	if !haveFinalText || strings.TrimSpace(finalText.String()) == "" {
		outcome.ProtocolErr = errors.New("verifier produced no final answer")
		return outcome, nil
	}

	result, decErr := verify.DecodeModelResult([]byte(finalText.String()))
	if decErr != nil {
		outcome.ProtocolErr = decErr
		return outcome, nil
	}
	outcome.Result = &result
	return outcome, nil
}

// RunScript runs a plain shell script (no stream-json parsing, no session/
// resume args) with a hard, non-resetting timeout — unlike RunAgent's
// idle-timeout (reset per output line), timeout here bounds the whole run
// regardless of how much output streams. Each output line is logged via
// LogAction("stdout", line) and forwarded to Config.OnAction if set, so
// callers get the same per-line visibility RunAgent gives for tool actions.
func (e *Executor) RunScript(ctx context.Context, timeout time.Duration, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	// Durable stderr sink (the .stderr.log file) and, independently, the live
	// feed sink (lineWriter → OnAction("stderr", ...)). Built so that
	// streaming to OnAction survives even if the log file failed to open OR
	// a write to it fails mid-run (e.g. ENOSPC) — dualWriter (not
	// io.MultiWriter, which would stop at the first erroring writer) always
	// forwards to the live stream regardless of the file sink's outcome.
	fileSink, closeFileSink := e.scriptStderrSink(logFile)
	defer closeFileSink()
	var lw *lineWriter
	stderrWriter := fileSink
	if e.cfg.OnAction != nil {
		lw = newLineWriter(func(line string) { e.cfg.OnAction("stderr", line) })
		stderrWriter = &dualWriter{stream: lw, file: fileSink}
	}

	lg.LogStart("script", strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile)))

	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	runErr := e.run(runCtx, "", "script", stderrWriter, func(line string) {
		// "text" (не "stdout") — тип строки, который use-stage-log.ts's
		// TEXT_LINE_PATTERN на дашборде распознаёт как отображаемый в Log
		// panel; тот же тип, что и обычный текстовый вывод агента.
		lg.LogAction("text", line)
		if e.cfg.OnAction != nil {
			e.cfg.OnAction("stdout", line)
		}
	})
	if lw != nil {
		// Emit any trailing partial stderr line (no terminating '\n' yet) —
		// covers both the normal-exit case and timeout/interruption, where
		// e.run already returned (killProcessGroup + cmd.Wait joined the
		// internal stderr-copy goroutine) but the last line never saw a '\n'.
		lw.Flush()
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		runErr = fmt.Errorf("script timeout after %v", timeout)
	}

	lg.LogEnd(runErr)
	return runErr
}

// RunJSONQuery запускает команду с одним промптом в JSON-режиме.
// Не использует stream-json — просто захватывает stdout через cmd.Output().
// Предназначен для однократных LLM-вызовов (без логирования действий).
// Возвращает сырые байты stdout; парсинг конверта/полей остаётся за вызывающей стороной.
func (e *Executor) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	e.logAgentInput("supervisor", prompt)
	// Чистая one-shot JSON-инвокация. Намеренно НЕ наследуем e.cfg.ExtraArgs:
	// executor.New дефолтит их в DefaultClaudeArgs (--print --output-format stream-json
	// --verbose --dangerously-skip-permissions). Этот stream-json конфликтовал бы с
	// нашим --output-format json и триггерил --include-partial-messages во враппере
	// (→ claude exit 1: "requires --output-format=stream-json").
	args := []string{"-p", prompt, "--output-format", "json"}

	// Команда может лежать в WrapperDir; exec.LookPath в текущем процессе его не
	// видит, поэтому резолвим абсолютный путь сами — как в e.run.
	cmdPath := e.cfg.Command
	if e.cfg.WrapperDir != "" {
		if resolved, err := exec.LookPath(filepath.Join(e.cfg.WrapperDir, e.cfg.Command)); err == nil {
			cmdPath = resolved
		}
	}

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	if e.cfg.WrapperDir != "" {
		env := os.Environ()
		filtered := make([]string, 0, len(env)+1)
		pathSet := false
		for _, kv := range env {
			if strings.HasPrefix(kv, "PATH=") {
				filtered = append(filtered, "PATH="+e.cfg.WrapperDir+string(os.PathListSeparator)+kv[5:])
				pathSet = true
				continue
			}
			filtered = append(filtered, kv)
		}
		if !pathSet {
			filtered = append(filtered, "PATH="+e.cfg.WrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		cmd.Env = filtered
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("RunJSONQuery %q: %w; stderr: %s", e.cfg.Command, err, stderr.String())
	}
	return out, nil
}

// killProcessGroup and setProcessGroup are OS-specific — see executor_unix.go
// (real process-group kill via negative PID) and executor_windows.go
// (fallback: signal the direct child only, no process-group equivalent
// wired up for Windows).

// run spawns the AI client subprocess, feeds prompt via stdin, and calls
// lineCallback for each stdout line. Respects idle timeout.
func (e *Executor) run(ctx context.Context, prompt, phase string, stderr io.Writer, lineCallback func(string)) error {
	e.logAgentInput(phase, prompt)
	args := append([]string{}, e.cfg.ExtraArgs...)
	if e.cfg.SessionID != "" {
		if e.cfg.Resume {
			args = append(args, "--resume", e.cfg.SessionID)
		} else {
			args = append(args, "--session-id", e.cfg.SessionID)
		}
	}
	// Resolve the command via WrapperDir before exec. exec.Command does LookPath
	// against THIS process's PATH; the wrapper-dir is only added to the CHILD's
	// env below, so a generated wrapper command (e.g. glm47 inside the
	// wrapper-dir) would not be found without resolving it to an absolute path
	// here. For a command not present in the wrapper-dir (e.g. a mounted binary at
	// /usr/local/bin), LookPath fails and we fall back to the bare name — which
	// exec.Command then resolves via the parent $PATH as before.
	cmdPath := e.cfg.Command
	if e.cfg.WrapperDir != "" {
		if resolved, err := exec.LookPath(filepath.Join(e.cfg.WrapperDir, e.cfg.Command)); err == nil {
			cmdPath = resolved
		}
	}
	cmd := exec.CommandContext(ctx, cmdPath, args...)
	setProcessGroup(cmd)
	cmd.Stdin = strings.NewReader(prompt)
	// Пин рабочей директории агента к project root (flow.root_dir), чтобы
	// относительные пути проекта резолвились в одном корне для всех стадий.
	if e.cfg.Dir != "" {
		cmd.Dir = e.cfg.Dir
	}

	// Strip CLAUDECODE to allow nested sessions, expose stage directory and
	// wrapper-dir (prepended to PATH so generated wrapper scripts resolve).
	env := os.Environ()
	filtered := make([]string, 0, len(env)+3)
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "CLAUDECODE="):
			// always strip for nested sessions
		case strings.HasPrefix(kv, "AFM_STAGE_DIR="):
			// always strip inherited; re-added below only if cfg.StageDir != ""
		case strings.HasPrefix(kv, "CODEX_VERIFY="):
			// always strip inherited; re-added below only if cfg.VerifyMode
		case e.cfg.VerifyMode && isVerifierOwnSecret(kv, e.cfg.Command):
			// D2b код-ревью: собственный секрет ЭТОГО ЖЕ верификатора
			// (AFM_SECRET_<verifyCommand>) — исключение из ветки ниже.
			// e.cfg.Command здесь — алиас/recipe-ключ verify-шага
			// (RunVerifyAgent зовёт RunVerifyAgent(..., cmd, ...) с cmd,
			// оставляя verifyCfg.Command исходным), ТОЧНО тот же ключ, что
			// pkg/docker/wrapper.go использовал при генерации имени
			// переменной для враппера — без этого исключения
			// аутентифицированный codex-recipe верификатор стартовал бы без
			// своего же токена.
			filtered = append(filtered, kv)
		case e.cfg.VerifyMode && IsCrossAgentTransportSecret(kv):
			// C2: непроверенный verify-верификатор не должен унаследовать
			// транспортные секреты ЧУЖИХ агентов/хуков (см.
			// IsCrossAgentTransportSecret) — он мог бы отразить их в своём
			// JSON-ответе, который afm персистит как raw-result/report.
			// Обычный (не-verify) запуск это условие не задевает вовсе.
		case e.cfg.VerifyMode && isClaudeAuthorCredential(kv):
			// F4 (4-е код-ревью): Docker forwards Claude-авторизацию АВТОРА
			// (CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN/
			// ANTHROPIC_BASE_URL) в окружение afm-процесса — эти четыре имени
			// не покрывались ни isVerifierOwnSecret (другой namespace,
			// AFM_SECRET_<CMD>), ни IsCrossAgentTransportSecret выше (другие
			// префиксы). Read-only верификатору чужая авторизация автора не
			// нужна и не должна попасть в его персистируемый JSON-ответ.
			// Обычный (не-verify) запуск это условие не задевает вовсе.
		default:
			filtered = append(filtered, kv)
		}
	}
	if e.cfg.StageDir != "" {
		filtered = append(filtered, "AFM_STAGE_DIR="+e.cfg.StageDir)
	}
	if e.cfg.VerifyMode {
		// Сигнал адаптеру (напр. scripts/codex-as-claude.sh), что это
		// read-only AI-verify проход, а не обычный запуск автора — без
		// него VerifyMode оставался бы мёртвым полем: RunVerifyAgent
		// выставляет его в Config, но подпроцесс никогда бы не узнал об
		// этом и исполнялся бы в обычном (небезопасном для verify) режиме.
		filtered = append(filtered, "CODEX_VERIFY=1")
	}
	if e.cfg.WrapperDir != "" {
		pathSet := false
		for i, kv := range filtered {
			if strings.HasPrefix(kv, "PATH=") {
				filtered[i] = "PATH=" + e.cfg.WrapperDir + ":" + kv[5:]
				pathSet = true
				break
			}
		}
		if !pathSet {
			filtered = append(filtered, "PATH="+e.cfg.WrapperDir+":"+os.Getenv("PATH"))
		}
	}
	cmd.Env = filtered

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = stderr

	// D4 код-ревью: последняя неблокирующая проверка ПЕРЕД стартом процесса —
	// если ctx уже отменён (полная отмена рана) или сигнал на InterruptCh уже
	// пришёл (напр. Pause() успел durable зафиксировать переход и
	// просигналить ДО того, как мы дошли сюда), НИКОГДА не запускаем
	// subprocess. Полная TOCTOU-атомарность невозможна (сигнал может прийти
	// на долю секунды позже этой проверки), но закрыть уже-случившийся случай
	// дёшево и правильно — особенно для read-only verify-верификатора,
	// которому вообще не должно быть позволено начать работу над уже
	// приостановленной/отменённой стадией.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.cfg.InterruptCh:
		return ErrUserInterrupted
	default:
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", e.cfg.Command, err)
	}

	idleTimer := time.NewTimer(e.cfg.IdleTimeout)
	defer idleTimer.Stop()

	done := make(chan error, 1)
	go func() {
		done <- lineReader(stdout, func(line string) bool {
			lineCallback(line)
			idleTimer.Reset(e.cfg.IdleTimeout)
			return true
		})
	}()

	select {
	case readErr := <-done:
		waitErr := cmd.Wait()
		if readErr != nil {
			return readErr
		}
		return waitErr
	case <-idleTimer.C:
		killProcessGroup(cmd, syscall.SIGKILL)
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return fmt.Errorf("idle timeout after %v: %w", e.cfg.IdleTimeout, ErrIdleTimeout)
	case <-ctx.Done():
		killProcessGroup(cmd, syscall.SIGKILL)
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return ctx.Err()
	case <-e.cfg.InterruptCh:
		// Мягкое прерывание: SIGINT, а не Kill — claude сам грамотно
		// завершает текущую атомарную операцию (запись файла — один syscall,
		// его практически не рвёт сигналом на середине) и выходит.
		killProcessGroup(cmd, syscall.SIGINT)
		select {
		case <-done:
			_ = cmd.Wait()
			return ErrUserInterrupted
		case <-time.After(interruptGracePeriod):
			// Не среагировал на SIGINT вовремя — принудительно, как страховка.
			killProcessGroup(cmd, syscall.SIGKILL)
			<-done
			_ = cmd.Wait()
			return ErrUserInterrupted
		}
	}
}

// logAgentInput пишет точный промпт, уходящий в агента (stdin), в debug-логи —
// единый <RunDir>/debug.log (хронологически по всем стадиям) и по-стейджно
// <RunDir>/<StageID>/<phase>.prompt.log. Активно только при Config.Debug.
// StageID намеренно отделён от StageDir/AFM_STAGE_DIR: StageDir задаётся только
// для interactive/autonomous стадий (файловый диалоговый протокол), а StageID —
// для КАЖДОЙ стадии, иначе обычные (не-interactive) стадии теряли бы per-stage
// prompt.log и тег stage= в debug.log. Best-effort: ошибки записи не прерывают
// run (debug — вспомогательный тракт).
func (e *Executor) logAgentInput(phase, prompt string) {
	if !e.cfg.Debug {
		return
	}
	entry := fmt.Sprintf(
		"=== [%s] stage=%s phase=%s cmd=%s session=%s resume=%t ===\n--- BEGIN PROMPT ---\n%s\n--- END PROMPT ---\n\n",
		time.Now().UTC().Format(time.RFC3339Nano), e.cfg.StageID, phase, e.cfg.Command, e.cfg.SessionID, e.cfg.Resume, prompt,
	)
	if e.cfg.RunDir != "" {
		appendDebug(filepath.Join(e.cfg.RunDir, "debug.log"), entry)
	}
	if e.cfg.RunDir != "" && e.cfg.StageID != "" {
		appendDebug(filepath.Join(e.cfg.RunDir, e.cfg.StageID, phase+".prompt.log"), entry)
	}
}

// appendDebug дописывает строку в файл (создаёт при отсутствии). Ошибки —
// в stderr, без прерывания.
func appendDebug(path, s string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug: cannot open %s: %v\n", path, err)
		return
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.WriteString(s); err != nil {
		fmt.Fprintf(os.Stderr, "debug: cannot write %s: %v\n", path, err)
	}
}
