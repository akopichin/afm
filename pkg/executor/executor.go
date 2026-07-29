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

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/progress"
)

// Config configures the executor.
type Config struct {
	Command        string
	ExtraArgs      []string
	IdleTimeout    time.Duration
	TruncateOutput int                       // 0 = no truncation; max chars for logged agent text/Bash-command detail
	OnAction       func(tool, detail string) // called for each parsed agent action (may be nil)
	SessionID      string                    // if non-empty, passed via --session-id (or --resume when Resume=true)
	Resume         bool                      // if true, --resume <SessionID> is used instead of --session-id
	StageDir       string                    // passed to agent as AFM_STAGE_DIR env var (file-based dialog protocol)
	WrapperDir     string                    // if set, prepended to PATH in agent env so generated wrapper scripts resolve
	Dir            string                    // if set, agent runs with this working directory (project root from flow.root_dir)
	Debug          bool                      // if true, log the exact agent input (prompt) to debug logs
	RunDir         string                    // run directory root; with Debug, <RunDir>/debug.log gets every agent input
	StageID        string                    // stage id for debug log tagging + per-stage prompt log path (decoupled from StageDir/AFM_STAGE_DIR)
	// InterruptCh, if set, is watched during RunAgent: a signal on this channel
	// sends SIGINT to the subprocess (not SIGKILL, not ctx cancellation) —
	// graceful, user-requested interrupt (agent_suggest), distinct from idle
	// timeout / full-run shutdown. nil channel is safe (select never fires).
	InterruptCh <-chan struct{}
}

// ErrUserInterrupted signals that the agent process was stopped because the
// user requested an interrupt (via Config.InterruptCh) — not a real failure.
// Callers (runWithRetry) must distinguish this from retry/failure handling.
var ErrUserInterrupted = errors.New("user interrupted")

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
		d := c.Text
		if limit > 0 && len(d) > limit {
			d = d[:limit] + "..."
		}
		return contentTypeText, d, true
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
	runErr := e.run(ctx, prompt, phase, stderr, func(line string) {
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
	runErr := e.run(ctx, prompt, phase, stderr, func(line string) {
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
	if runErr != nil && firstErr != "" {
		return fmt.Errorf("%s: %w", firstErr, runErr)
	}
	return runErr
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

	var stderr = io.Discard
	if sf := openStderrLog(logFile); sf != nil {
		stderr = sf
		defer sf.Close()
	}

	lg.LogStart("script", strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile)))

	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	runErr := e.run(runCtx, "", "script", stderr, func(line string) {
		lg.LogAction("stdout", line)
		if e.cfg.OnAction != nil {
			e.cfg.OnAction("stdout", line)
		}
	})
	if errors.Is(runErr, context.DeadlineExceeded) {
		runErr = fmt.Errorf("script timeout after %v", timeout)
	}

	lg.LogEnd(runErr)
	return runErr
}

// RunJSONQuery запускает команду с одним промптом в JSON-режиме.
// Не использует stream-json — просто захватывает stdout через cmd.Output().
// Используется Supervisor для однократных LLM-вызовов (без логирования действий).
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
	cmd.Stdin = strings.NewReader(prompt)
	// Пин рабочей директории агента к project root (flow.root_dir), чтобы
	// относительные пути проекта резолвились в одном корне для всех стадий.
	if e.cfg.Dir != "" {
		cmd.Dir = e.cfg.Dir
	}

	// Strip CLAUDECODE to allow nested sessions, expose stage directory and
	// wrapper-dir (prepended to PATH so generated wrapper scripts resolve).
	env := os.Environ()
	filtered := make([]string, 0, len(env)+2)
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "CLAUDECODE="):
			// always strip for nested sessions
		default:
			filtered = append(filtered, kv)
		}
	}
	if e.cfg.StageDir != "" {
		filtered = append(filtered, "AFM_STAGE_DIR="+e.cfg.StageDir)
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
		cmd.Process.Kill()
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return fmt.Errorf("idle timeout after %v", e.cfg.IdleTimeout)
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return ctx.Err()
	case <-e.cfg.InterruptCh:
		// Мягкое прерывание: SIGINT, а не Kill — claude сам грамотно
		// завершает текущую атомарную операцию (запись файла — один syscall,
		// его практически не рвёт сигналом на середине) и выходит.
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-done:
			_ = cmd.Wait()
			return ErrUserInterrupted
		case <-time.After(interruptGracePeriod):
			// Не среагировал на SIGINT вовремя — принудительно, как страховка.
			cmd.Process.Kill()
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
