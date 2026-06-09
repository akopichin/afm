package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/progress"
)

// Config configures the executor.
type Config struct {
	Command     string
	ExtraArgs   []string
	IdleTimeout time.Duration
	OnAction    func(tool, detail string) // called for each parsed agent action (may be nil)
	SessionID   string                    // if non-empty, passed via --session-id (or --resume when Resume=true)
	Resume      bool                      // if true, --resume <SessionID> is used instead of --session-id
	McpConfig   string                    // path to mcp.json, passed via --mcp-config when non-empty
}

const defaultCommand = "claude"

const (
	contentTypeText    = "text"
	contentTypeToolUse = "tool_use"
	toolNameBash       = "Bash"
)

// Executor spawns AI client subprocesses.
type Executor struct {
	cfg Config
}

// New creates an Executor.
func New(cfg Config) *Executor {
	if cfg.Command == "" {
		cfg.Command = defaultCommand
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	// For claude-compatible commands, prepend default stream-json flags if no custom args set.
	if len(cfg.ExtraArgs) == 0 {
		cfg.ExtraArgs = []string{"--print", "--output-format", "stream-json", "--dangerously-skip-permissions"}
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
func ParseToolAction(line string) (toolName, detail string, ok bool) {
	ev, parsed := parseStreamEvent(line)
	if !parsed {
		return "", "", false
	}
	for _, c := range ev.Message.Content {
		if tool, detail, actionOK := contentToAction(c); actionOK {
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
func contentToAction(c streamContent) (toolName, detail string, ok bool) {
	switch c.Type {
	case contentTypeText:
		if c.Text == "" {
			return "", "", false
		}
		d := c.Text
		if len(d) > 100 {
			d = d[:100] + "..."
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
		case "Write", "Edit", "Read", "Glob", "Grep":
			return c.Name, fp, true
		case toolNameBash:
			cmd := inp.Command
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
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
				if len(d) > 80 {
					d = d[:80] + "..."
				}
			}
			return c.Name, d, true
		}
	default:
		return "", "", false
	}
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

	lg.LogStart("planning", stageName)

	var textBuf strings.Builder
	var firstErr string
	runErr := e.run(ctx, prompt, func(line string) {
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
			if tool, detail, actionOK := contentToAction(c); actionOK {
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
	if textBuf.Len() == 0 {
		// Агент записал план через Write tool — не затираем файл пустой строкой.
		return nil
	}
	return os.WriteFile(outFile, []byte(textBuf.String()), 0644)
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

	lg.LogStart(agentType, stageName)

	var firstErr string
	runErr := e.run(ctx, prompt, func(line string) {
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
			if tool, detail, actionOK := contentToAction(c); actionOK {
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

// run spawns the AI client subprocess, feeds prompt via stdin, and calls
// lineCallback for each stdout line. Respects idle timeout.
func (e *Executor) run(ctx context.Context, prompt string, lineCallback func(string)) error {
	args := append([]string{}, e.cfg.ExtraArgs...)
	if e.cfg.McpConfig != "" {
		args = append(args, "--mcp-config", e.cfg.McpConfig)
	}
	if e.cfg.SessionID != "" {
		if e.cfg.Resume {
			args = append(args, "--resume", e.cfg.SessionID)
		} else {
			args = append(args, "--session-id", e.cfg.SessionID)
		}
	}
	cmd := exec.CommandContext(ctx, e.cfg.Command, args...)
	cmd.Stdin = strings.NewReader(prompt)

	// Strip CLAUDECODE to allow nested sessions
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CLAUDECODE=") {
			filtered = append(filtered, kv)
		}
	}
	cmd.Env = filtered

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard

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
	}
}
