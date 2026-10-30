package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/progress"
	"github.com/akopichin/afm/pkg/redact"
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
	// ProcessErr is why the process did not finish cleanly when it was neither interrupted nor timed out
	// (e.g. it could not even start) — surfaced as the task's failure reason.
	ProcessErr error
	// ProtocolErr is set when the process reported ProcessOK but produced no usable final text block at all
	// (empty/missing) — it says nothing about whether RawText, if present, actually decodes as a valid
	// assistants envelope.
	ProtocolErr error
	// RawText is the final assistant text block, also durably written to resultFile when non-empty (even on
	// a non-zero exit — an audit trail of whatever the model produced, not a verdict). In Claude's native
	// schema mode it is the CLI's structured payload instead (StructuredPayload).
	RawText string
	// StructuredPayload is true when RawText is Claude's structured output rather than a text block. When native
	// mode was used and this is false, the CLI gave no payload and RawText is whatever text it wrote.
	StructuredPayload bool
	// NativeFallback is why a requested native schema mode was not used ("" when it was used or not requested):
	// the call then ran with the schema in the prompt alone.
	NativeFallback NativeSchemaFallback
	// NativeStarted is true only when the process really started in native mode: a failure before the start
	// cannot be the provider rejecting it.
	NativeStarted bool
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

	red := helperRedactor{redact: e.cfg.Redact, secrets: e.cfg.Secrets}

	var stderr = io.Discard
	var stderrFrom int64
	var stderrStream io.WriteCloser
	if sf, from := openStderrLogAt(logFile); sf != nil {
		defer sf.Close()
		stderrFrom = from
		stderrStream = red.stream(sf)
		defer func() { // deferred after sf.Close, so it runs first; a lost log tail is not fatal
			if stderrStream != nil {
				_ = stderrStream.Close()
			}
		}()
		stderr = stderrStream
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
	started := false
	delivered := helperCfg.OnDelivered
	helperCfg.OnDelivered = func() { // run calls it right after cmd.Start succeeds
		started = true
		if delivered != nil {
			delivered()
		}
	}
	he := &Executor{cfg: helperCfg}
	nativeFallback := he.selectNativeSchema()

	var finalText strings.Builder
	var haveFinalText, hasPayload bool
	var firstErr string
	phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))
	collector := accounting.NewCollector(he.cfg.UsageHint)
	limits := newUsageLimitScanner(he.clk().Now, he.cfg)
	runErr := he.run(ctx, prompt, phase, stderr, func(line string) {
		collector.Observe([]byte(line))
		// Everything below reads the redacted line, so no field of the event — text, tool name, tool input —
		// can carry a secret to a sink.
		line = red.jsonText(line)
		limits.observe(line)
		jf.WriteString(line + "\n") //nolint:errcheck
		ev, ok := parseStreamEvent(line)
		if !ok {
			if payload, found := structuredPayload(line); found && he.cfg.claudeNativeSchema() {
				// The payload arrives only in the result event, after every text block: it is the answer.
				finalText.Reset()
				finalText.WriteString(payload)
				haveFinalText, hasPayload = true, true
			} else if isErrorLine(line) {
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
				tool, detail = red.text(tool), red.text(detail)
				lg.LogAction(tool, detail)
				if he.cfg.OnAction != nil {
					he.cfg.OnAction(tool, detail)
				}
			}
		}
	})

	// The redacted stderr must be flushed before its tail is read for the limit text.
	if stderrStream != nil {
		_ = stderrStream.Close()
		stderrStream = nil
	}
	runErr = he.turnOutcome(limits, runErr, logFile, stderrFrom)
	lg.LogEnd(runErr)
	if he.cfg.OnUsage != nil {
		he.cfg.OnUsage(collector.Finish(runErr))
	}
	if firstErr != "" {
		lg.LogAction("helper-note", "process reported an error line; see log above: "+firstErr)
	}
	// A usage limit is neither an answer nor a crash: the caller waits for the reset instead of failing the task.
	if limit, ok := runErr.(*UsageLimitError); ok {
		return HelperOutcome{ProcessErr: limit}, limit
	}

	outcome := HelperOutcome{StructuredPayload: hasPayload, NativeFallback: nativeFallback, NativeStarted: started && he.cfg.nativeSchemaOn}
	if he.cfg.claudeNativeSchema() && !hasPayload && runErr == nil {
		lg.LogAction("helper-note", "native schema mode returned no structured payload; the text answer is validated as usual")
	}
	switch {
	case errors.Is(runErr, ErrUserInterrupted):
		outcome.Interrupted = true
	case errors.Is(runErr, ErrIdleTimeout):
		outcome.TimedOut = true
	case runErr == nil:
		outcome.ProcessOK = true
	default:
		// Other run error (not timeout, not interrupt): process just didn't finish cleanly; all three flags
		// stay false, the error itself is kept for the failure reason.
		outcome.ProcessErr = runErr
	}

	if haveFinalText && strings.TrimSpace(finalText.String()) != "" {
		outcome.RawText = red.jsonText(finalText.String())
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

// helperRedactor masks known secrets in everything RunHelperAgent writes or reports. Either source may be nil.
type helperRedactor struct {
	redact  func(string) string // Config.Redact
	secrets func() []string     // Config.Secrets
}

// text masks secrets in plain text — with known values, their JSON-escaped spellings too.
func (r helperRedactor) text(s string) string {
	if r.secrets != nil {
		s = redact.String(s, withJSONSpellings(r.secrets()))
	}
	if r.redact != nil {
		s = r.redact(s)
	}
	return s
}

// jsonText is text for a string that may hold JSON: the decoded strings are masked first, including JSON nested
// inside a string (the answer inside a stream line), so a secret the encoder escaped is masked by its real value.
func (r helperRedactor) jsonText(s string) string {
	return r.text(redactJSONStrings(s, r.jsonText))
}

// stream wraps w so a byte stream is masked before it reaches w. With known values a secret split across writes
// or lines is still caught; Redact alone can only mask line by line.
func (r helperRedactor) stream(w io.Writer) io.WriteCloser {
	var out io.WriteCloser = &lineRedactor{w: w, redact: r.text}
	if r.secrets != nil {
		out = &secretStream{next: out, secrets: r.secrets}
	}
	return out
}

// redactJSONStrings decodes s as one JSON value, masks every string in it (object keys too) with redactString and
// re-encodes it. s comes back unchanged when it is not a single JSON object, array or string, or nothing in it
// changed — so an ordinary line keeps its exact bytes.
func redactJSONStrings(s string, redactString func(string) string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || !strings.ContainsAny(trimmed[:1], `{["`) {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return s
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return s // trailing text: not a single JSON value
	}
	v, changed := redactJSONValue(v, redactString)
	if !changed {
		return s
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return s // cannot happen for a decoded value; the caller still masks s as plain text
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// redactJSONValue masks every string in a decoded JSON value and reports whether anything changed.
func redactJSONValue(v any, redactString func(string) string) (any, bool) {
	switch t := v.(type) {
	case string:
		r := redactString(t)
		return r, r != t
	case []any:
		changed := false
		for i, e := range t {
			var c bool
			t[i], c = redactJSONValue(e, redactString)
			changed = changed || c
		}
		return t, changed
	case map[string]any:
		out := make(map[string]any, len(t))
		changed := false
		for k, e := range t {
			rk := redactString(k)
			re, c := redactJSONValue(e, redactString)
			out[rk] = re
			changed = changed || c || rk != k
		}
		return out, changed
	}
	return v, false
}

// withJSONSpellings adds each secret's JSON-escaped spelling, with and without HTML escaping, so a secret
// quoted inside JSON that does not decode as a whole (an answer wrapped in prose) is still masked.
func withJSONSpellings(secrets []string) []string {
	out := slices.Clone(secrets)
	for _, s := range secrets {
		for _, escapeHTML := range []bool{false, true} {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(escapeHTML)
			if err := enc.Encode(s); err != nil {
				continue
			}
			quoted := strings.TrimSuffix(buf.String(), "\n")
			if escaped := quoted[1 : len(quoted)-1]; escaped != s {
				out = append(out, escaped)
			}
		}
	}
	return out
}

// secretStream masks known secret values in a byte stream. redact.Writer holds back at most the longest secret
// minus one byte, so a secret split across writes, lines or a flush is caught with bounded memory. The values
// are read on the first write: by then the process, and its own mesh token, exist.
type secretStream struct {
	next    io.WriteCloser
	secrets func() []string
	w       *redact.Writer
}

func (s *secretStream) Write(p []byte) (int, error) {
	if s.w == nil {
		s.w = redact.NewWriter(s.next, withJSONSpellings(s.secrets()))
	}
	return s.w.Write(p)
}

// Close writes the held-back tail, then closes next.
func (s *secretStream) Close() error {
	var err error
	if s.w != nil {
		err = s.w.Close()
	}
	return errors.Join(err, s.next.Close())
}

// lineRedactor writes whole lines through redact, so a secret within one line is never split across two
// writes; Close writes a last line that has no newline. A line longer than maxRedactedLine is written in pieces
// rather than held, so only secretStream catches a secret across lines or that flush.
type lineRedactor struct {
	w      io.Writer
	redact func(string) string
	buf    []byte
}

const maxRedactedLine = 64 * 1024

func (l *lineRedactor) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if _, err := io.WriteString(l.w, l.redact(string(l.buf[:i+1]))); err != nil {
			return 0, err
		}
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > maxRedactedLine {
		if err := l.Close(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Close writes whatever is buffered; it does not close the underlying writer.
func (l *lineRedactor) Close() error {
	if len(l.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(l.w, l.redact(string(l.buf)))
	l.buf = nil
	return err
}
