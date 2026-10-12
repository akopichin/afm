package executor

import (
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

// afmSecretEnvPrefix/afmSyspromptEnvPrefix — те же транспортные префиксы, что pkg/docker расставляет в
// окружении afm-процесса для autoShim-агентов.
const (
	afmSecretEnvPrefix    = "AFM_SECRET_"    //nolint:gosec // это имя переменной окружения, а не сам секрет
	afmSyspromptEnvPrefix = "AFM_SYSPROMPT_" //nolint:gosec // аналогично
)

// IsCrossAgentTransportSecret определяет секреты AFM_SECRET_*, AFM_HOOK_SECRET_* и AFM_SYSPROMPT_* для
// фильтрации verify-окружения.
func IsCrossAgentTransportSecret(kv string) bool {
	return strings.HasPrefix(kv, afmSecretEnvPrefix) ||
		strings.HasPrefix(kv, afmSyspromptEnvPrefix) ||
		strings.HasPrefix(kv, lifecyclehooks.HookSecretTransportPrefix)
}

// afmSecretEnvName воспроизводит ТОТ ЖЕ transform, что pkg/docker/wrapper.go's envName делает над именем
// враппер-команды при построении переменной AFM_SECRET_<NAME>.
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

// isVerifierOwnSecret сохраняет секрет verify-агента по точному имени переменной, исключая совпадение
// префиксов команд.
func isVerifierOwnSecret(kv, verifyCommand string) bool {
	if verifyCommand == "" {
		return false
	}
	return strings.HasPrefix(kv, afmSecretEnvPrefix+afmSecretEnvName(verifyCommand)+"=")
}

// isClaudeAuthorCredential определяет Claude credentials и BASE_URL автора, которые verify-агент не должен
// наследовать.
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
	// InterruptCh, if set, is watched during RunAgent: a signal on this channel sends SIGINT to the
	// subprocess — graceful, user-requested interrupt, distinct from idle timeout / full-run shutdown.
	InterruptCh <-chan struct{}
	// VerifyMode enables the adapter's verification mode through CODEX_VERIFY; set only by RunVerifyAgent.
	VerifyMode bool
	// BrowserMCP, if set, injects the managed Chrome DevTools MCP for this author runner: a per-invocation
	// descriptor file + --mcp-config, plus the non-secret AFM_BROWSER_* env.
	BrowserMCP *BrowserMCP
}

// ErrUserInterrupted signals that the agent process was stopped because the user requested an interrupt —
// not a real failure.
var ErrUserInterrupted = errors.New("user interrupted")

// ErrIdleTimeout — сигнальная ошибка простоя.
var ErrIdleTimeout = errors.New("idle timeout")

// interruptGracePeriod bounds how long we wait for the subprocess to exit gracefully after SIGINT before
// force-killing it as a safety net against a hung/misbehaving process.
const interruptGracePeriod = 15 * time.Second

const (
	contentTypeText    = "text"
	contentTypeToolUse = "tool_use"
	toolNameBash       = "Bash"
	toolNameWrite      = "Write"
)

// DefaultClaudeArgs returns the standard claude stream-json invocation flags. It also makes the stream
// contain tool_use events, which the executor's parser relies on.
func DefaultClaudeArgs() []string {
	return []string{"--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
}

// ResolveArgs prepends DefaultClaudeArgs to extra and drops exact duplicates.
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
	// stderrSinkOverride — test-only seam (see scriptStderrSink). nil in production: New never sets it, so
	// RunScript always opens the real .stderr.log file.
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
		// Нарратив агента НЕ обрезаем: limit бьёт только по механическому выводу инструментов ниже.
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

// openStderrLog opens <logBase>.stderr.log for appending so the agent's stderr is captured instead of
// lost. Returns nil on error — stderr is diagnostic only, callers fall back to io.Discard.
func openStderrLog(logFile string) *os.File {
	f, err := os.OpenFile(strings.TrimSuffix(logFile, ".log")+".stderr.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	return f
}

// dualWriter always writes to BOTH stream and file — and always reports success to its caller.
type dualWriter struct {
	stream io.Writer
	file   io.Writer
}

func (d *dualWriter) Write(p []byte) (int, error) {
	_, _ = d.stream.Write(p) // lineWriter.Write never returns an error
	_, _ = d.file.Write(p)   // best-effort: a log-write failure must not stop streaming
	return len(p), nil
}

// scriptStderrSink opens the durable stderr sink for RunScript and returns it together with a closer.
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

// WrittenFiles возвращает пути файлов, записанных агентом через Write tool, в порядке появления событий в
// stream-json логе. Отсутствующий или нечитаемый лог даёт пустой список.
func WrittenFiles(jsonlPath string) []string {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var files []string
	_ = lineReader(f, func(line string) bool {
		ev, ok := parseStreamEvent(line)
		if !ok {
			return true
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
		return true
	})
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

// RunVerifyAgent запускает один проход AI-verify: свежую read-only сессию и захватывает ФИНАЛЬНЫЙ
// ассистентский текстовый блок как машинный ответ модели, а не весь лог.
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

	// Верификатор — всегда свежая сессия без диалогового протокола: принудительно обнуляем поля, которые
	// могли бы просочиться из общего Config, независимо от того, что несёт исходный e.cfg.
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
				// Финальный ответ — последний ассистентский текстовый блок: каждый следующий ЗАМЕНЯЕТ предыдущий, а не
				// дописывается к нему — промежуточные рассуждения не должны перемешаться с итоговым JSON.
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

	// Сохраняем сырой финальный текст, если он вообще появился, — даже если процесс потом упал ненулевым exit
	// code (аудиторский след того, что модель успела написать, не более).
	if haveFinalText && strings.TrimSpace(finalText.String()) != "" {
		if werr := os.WriteFile(resultFile, []byte(finalText.String()), 0644); werr != nil {
			return outcome, fmt.Errorf("write result file: %w", werr)
		}
	}

	if !outcome.ProcessOK {
		// Процесс не завершился штатно (таймаут/прерывание/иная ошибка) — что бы модель ни написала в текст, это
		// не решение о коде: даже "pass" в финальном тексте не засчитывается (§5.4 плана).
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

// RunScript runs a plain shell script with a hard, non-resetting timeout — unlike RunAgent's idle-timeout,
// timeout here bounds the whole run regardless of how much output streams.
func (e *Executor) RunScript(ctx context.Context, timeout time.Duration, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	// Durable stderr sink and, independently, the live feed sink.
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
		// "text" (не "stdout") — тип строки, который use-stage-log.ts's TEXT_LINE_PATTERN на дашборде распознаёт
		// как отображаемый в Log panel; тот же тип, что и обычный текстовый вывод агента.
		lg.LogAction("text", line)
		if e.cfg.OnAction != nil {
			e.cfg.OnAction("stdout", line)
		}
	})
	if lw != nil {
		// Emit any trailing partial stderr line — covers both the normal-exit case and timeout/interruption,
		// where e.run already returned but the last line never saw a '\n'.
		lw.Flush()
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		runErr = fmt.Errorf("script timeout after %v", timeout)
	}

	lg.LogEnd(runErr)
	return runErr
}

// RunJSONQuery запускает команду с одним промптом в JSON-режиме. Не использует stream-json — просто
// захватывает stdout через cmd.Output().
func (e *Executor) jsonQueryEnv(ambient []string) []string {
	filtered := make([]string, 0, len(ambient)+1)
	pathSet := false
	for _, kv := range ambient {
		if IsBrowserTransportEnv(kv) {
			continue
		}
		if e.cfg.WrapperDir != "" && strings.HasPrefix(kv, "PATH=") {
			filtered = append(filtered, "PATH="+e.cfg.WrapperDir+string(os.PathListSeparator)+kv[5:])
			pathSet = true
			continue
		}
		filtered = append(filtered, kv)
	}
	if e.cfg.WrapperDir != "" && !pathSet {
		filtered = append(filtered, "PATH="+e.cfg.WrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return filtered
}

func (e *Executor) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	e.logAgentInput("supervisor", prompt)
	// Чистая one-shot JSON-инвокация.
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
	// RunJSONQuery is NOT an author runner — it must never inherit the browser transport.
	cmd.Env = e.jsonQueryEnv(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("RunJSONQuery %q: %w; stderr: %s", e.cfg.Command, err, stderr.String())
	}
	return out, nil
}

// killProcessGroup and setProcessGroup are OS-specific — see executor_unix.go and executor_windows.go.

// filterInheritedEnv strips control/browser vars and, for verify, foreign credentials. Callers re-add
// allowed invocation vars.
func (e *Executor) filterInheritedEnv(ambient []string) []string {
	filtered := make([]string, 0, len(ambient)+3)
	for _, kv := range ambient {
		switch {
		case strings.HasPrefix(kv, "CLAUDECODE="):
			// always strip for nested sessions
		case strings.HasPrefix(kv, "AFM_STAGE_DIR="):
			// always strip inherited; re-added by caller only if cfg.StageDir != ""
		case strings.HasPrefix(kv, "CODEX_VERIFY="):
			// always strip inherited; re-added by caller only if cfg.VerifyMode
		case IsBrowserTransportEnv(kv):
			// Strip AFM_BROWSER_* from all children; only author runners re-add non-secret browser vars.
		case e.cfg.VerifyMode && isVerifierOwnSecret(kv, e.cfg.Command):
			// D2b: the verifier's OWN secret (AFM_SECRET_<verifyCommand>) survives.
			filtered = append(filtered, kv)
		case e.cfg.VerifyMode && IsCrossAgentTransportSecret(kv):
			// C2: a read-only verifier must not inherit OTHER agents'/hooks'
			// transport secrets (it could reflect them in its persisted JSON).
		case e.cfg.VerifyMode && isClaudeAuthorCredential(kv):
			// F4: the author's forwarded Claude auth must not reach the verifier.
		default:
			filtered = append(filtered, kv)
		}
	}
	return filtered
}

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

	// Managed browser capability (author runners only; never in VerifyMode).
	var browserEnv []string
	if e.cfg.BrowserMCP != nil && !e.cfg.VerifyMode {
		bargs, benv, cleanup, berr := e.setupBrowserMCP()
		if berr != nil {
			return fmt.Errorf("browser mcp setup: %w", berr)
		}
		defer cleanup()
		args = append(args, bargs...)
		browserEnv = benv
	}
	// Resolve the command via WrapperDir before exec.
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
	filtered := e.filterInheritedEnv(os.Environ())
	if e.cfg.StageDir != "" {
		filtered = append(filtered, "AFM_STAGE_DIR="+e.cfg.StageDir)
	}
	if e.cfg.VerifyMode {
		// CODEX_VERIFY сообщает адаптеру о verify-режиме; одного поля Config.VerifyMode недостаточно.
		filtered = append(filtered, "CODEX_VERIFY=1")
	}
	// Re-add only the non-secret browser env for author runners (the token lives
	// solely in the 0600 descriptor file referenced by AFM_BROWSER_DESCRIPTOR).
	filtered = append(filtered, browserEnv...)
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

	// Перед запуском процесса повторно проверяем ctx и interrupt, чтобы не спавнить уже отменённый вызов.
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
		// Мягкое прерывание: SIGINT, а не Kill — claude сам грамотно завершает текущую атомарную операцию
		// (запись файла — один syscall, его практически не рвёт сигналом на середине) и выходит.
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

// logAgentInput пишет точный промпт, уходящий в агента, в debug-логи — единый <RunDir>/debug.log и
// по-стейджно <RunDir>/<StageID>/<phase>.prompt.log. Активно только при Config.Debug.
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
