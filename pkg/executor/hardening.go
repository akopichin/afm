package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/progress"
)

// RunMode names the isolation/behavior policy a subprocess is launched under. ModeAuthor is the default
// (implementation/planning/review agents, no extra isolation). ModeVerify and ModeHelper share the SAME
// env-filtering policy (see (Config).effectiveMode/filterInheritedEnv's single switch) — a read-only
// reviewer and a read-only helper agent both must not inherit foreign transport secrets or the author's
// forwarded Claude credentials.
type RunMode int

const (
	ModeAuthor RunMode = iota
	ModeVerify
	ModeHelper
)

// effectiveMode resolves the mode this Config actually runs under. VerifyMode stays the public surface
// field (existing callers/tests are untouched) and maps to ModeVerify; runMode is an internal-only field set
// exclusively by RunHelperAgent's config copy, mirroring how RunVerifyAgent sets VerifyMode on ITS copy.
// This is the ONLY place that decides "verify-like" vs "author" — filterInheritedEnv and run() both call
// through it instead of re-checking cfg.VerifyMode directly, so there is exactly one switch over env-var
// filtering, not two copies.
func (c Config) effectiveMode() RunMode {
	if c.runMode == ModeHelper {
		return ModeHelper
	}
	if c.VerifyMode {
		return ModeVerify
	}
	return ModeAuthor
}

// isolated reports whether mode m is subject to the verify/helper env-filtering policy (foreign transport
// secrets and the author's forwarded Claude credentials stripped) rather than the author's full inheritance.
func (m RunMode) isolated() bool { return m != ModeAuthor }

// HelperOutcome is RunHelperAgent's raw result: process-level success/failure plus the captured final
// assistant text block. Unlike verify.RunOutcome, this package does NOT decode the text into a domain
// envelope — pkg/executor stays decode-agnostic (like RunVerifyAgent's raw capture), and the orchestrator
// layer (T3.4) decodes RawText via assistants.DecodeResult, which is domain-aware and lives above this
// package.
type HelperOutcome struct {
	// ProcessOK is true only when the subprocess exited cleanly (no idle timeout, no interrupt, no other
	// run() error).
	ProcessOK bool
	// Interrupted is true when the run was stopped via InterruptCh (Pause/Cancel) — distinct from a timeout
	// or a plain process failure.
	Interrupted bool
	// TimedOut is true when the executor's idle timeout fired.
	TimedOut bool
	// ProtocolErr is set when the process reported ProcessOK but produced no usable final text block at all
	// (empty/missing) — it says nothing about whether RawText, if present, actually decodes as a valid
	// assistants envelope.
	ProtocolErr error
	// RawText is the final assistant text block, also durably written to resultFile when non-empty (even on
	// a non-zero exit — an audit trail of whatever the model produced, not a verdict).
	RawText string
}

// RunHelperAgent runs one helper-agent turn: a fresh, isolated session (no --resume, no AFM_STAGE_DIR, no
// browser capability) that captures the FINAL assistant text block as the answer, modeled on RunVerifyAgent.
// Read-only availability is NOT decided here — that gate lives in config.HelperCommandRejection, checked at
// task-admission time (T3.4); this method only implements the actual run-mode behavior for a profile that
// was already admitted.
func (e *Executor) RunHelperAgent(ctx context.Context, taskID, profileID, prompt, logFile, resultFile string) (HelperOutcome, error) {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return HelperOutcome{}, err
	}
	defer lg.Close()

	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	jf, err := os.OpenFile(jsonlFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return HelperOutcome{}, fmt.Errorf("open jsonl file: %w", err)
	}
	defer jf.Close()

	var stderr = io.Discard
	if sf := openStderrLog(logFile); sf != nil {
		stderr = sf
		defer sf.Close()
	}

	lg.LogStart(profileID, taskID)

	// A helper is always a fresh, isolated session — forcibly clear anything that might have leaked in from
	// e.cfg, exactly like RunVerifyAgent does for the verifier.
	helperCfg := e.cfg
	helperCfg.SessionID = ""
	helperCfg.Resume = false
	helperCfg.StageDir = ""
	helperCfg.VerifyMode = false
	helperCfg.runMode = ModeHelper
	helperCfg.BrowserMCP = nil // helpers never get the managed browser capability (§7.4)
	he := &Executor{cfg: helperCfg}

	var finalText strings.Builder
	var haveFinalText bool
	var firstErr string
	phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))
	collector := accounting.NewCollector(he.cfg.UsageHint)
	runErr := he.run(ctx, prompt, phase, stderr, func(line string) {
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
				// Same "replace, don't append" rule as RunVerifyAgent: only the LAST text block is the answer.
				finalText.Reset()
				finalText.WriteString(c.Text)
				haveFinalText = true
			}
			if tool, detail, actionOK := contentToAction(c, he.cfg.TruncateOutput); actionOK {
				lg.LogAction(tool, detail)
				if he.cfg.OnAction != nil {
					he.cfg.OnAction(tool, detail)
				}
			}
		}
	})

	lg.LogEnd(runErr)
	if he.cfg.OnUsage != nil {
		he.cfg.OnUsage(collector.Finish(runErr))
	}
	if firstErr != "" {
		lg.LogAction("helper-note", "process reported an error line; see log above: "+firstErr)
	}

	outcome := HelperOutcome{}
	switch {
	case errors.Is(runErr, ErrUserInterrupted):
		outcome.Interrupted = true
	case errors.Is(runErr, ErrIdleTimeout):
		outcome.TimedOut = true
	case runErr == nil:
		outcome.ProcessOK = true
	default:
		// Other run error (not timeout, not interrupt): process just didn't finish cleanly; all three flags
		// stay false.
	}

	if haveFinalText && strings.TrimSpace(finalText.String()) != "" {
		outcome.RawText = finalText.String()
		if werr := os.WriteFile(resultFile, []byte(outcome.RawText), 0644); werr != nil {
			return outcome, fmt.Errorf("write result file: %w", werr)
		}
	}

	if !outcome.ProcessOK {
		return outcome, nil
	}
	if !haveFinalText || strings.TrimSpace(finalText.String()) == "" {
		outcome.ProtocolErr = errors.New("helper produced no final answer")
	}
	return outcome, nil
}
