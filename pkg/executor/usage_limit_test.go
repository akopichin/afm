package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
)

// limitNow — момент отказа в реальном логе: четверг, 23:48:15 UTC.
var limitNow = time.Date(2026, time.October, 22, 23, 48, 15, 0, time.UTC)

func utc(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func TestParseUsageLimitText(t *testing.T) {
	plus3 := time.FixedZone("UTC+3", 3*60*60)
	newYork, nyErr := time.LoadLocation("America/New_York")

	tests := []struct {
		name       string
		msg        string
		now        time.Time
		loc        *time.Location
		wantOK     bool
		wantReset  time.Time // zero = неизвестно
		wantWindow string
		wantNoRst  bool
	}{
		{
			name:   "claude session limit with explicit UTC",
			msg:    "You've hit your session limit · resets 11:50pm (UTC)",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 22, 23, 50), wantWindow: "session",
		},
		{
			name:   "time already passed today means tomorrow",
			msg:    "You've hit your session limit · resets 11:50pm (UTC)",
			now:    limitNow.Add(10 * time.Minute),
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 23, 23, 50), wantWindow: "session",
		},
		{
			name:   "claude weekly limit with weekday",
			msg:    "You've hit your weekly limit · resets Mon 12:00am",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 26, 0, 0), wantWindow: "weekly",
		},
		{
			name:   "same weekday with time already passed means next week",
			msg:    "You've hit your weekly limit · resets Thu 12:00am",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 29, 0, 0), wantWindow: "weekly",
		},
		{
			name:   "no window word, hour only",
			msg:    "You've hit your limit · resets 3pm",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 23, 15, 0),
		},
		{
			name:   "time without zone uses the agent location",
			msg:    "You've hit your limit · resets 3pm",
			now:    limitNow,
			loc:    plus3, // 23:48Z is already 02:48 on Oct 23 there
			wantOK: true, wantReset: utc(2026, 10, 23, 12, 0),
		},
		{
			name:   "explicit zone wins over the agent location",
			msg:    "You've hit your limit · resets 3pm (UTC)",
			now:    limitNow,
			loc:    plus3,
			wantOK: true, wantReset: utc(2026, 10, 23, 15, 0),
		},
		{
			name:   "codex try again at clock time",
			msg:    "You've hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro) or try again at 3:45 PM.",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 23, 15, 45),
		},
		{
			name:   "codex date with ordinal suffix and year",
			msg:    "You've hit your usage limit. Try again at Oct 24th, 2026 3:45 PM.",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 24, 15, 45),
		},
		{
			name:   "codex date without year uses the nearest future",
			msg:    "You've hit your usage limit. Try again at Nov 1st 9:05 AM.",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 11, 1, 9, 5),
		},
		{
			name:   "date without year already past rolls to next year",
			msg:    "You've hit your usage limit. Try again at Jan 5th 9:00 AM.",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2027, 1, 5, 9, 0),
		},
		{
			name:   "24-hour clock",
			msg:    "You've hit your usage limit. Try again at 15:45.",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 23, 15, 45),
		},
		{
			name:   "try again later keeps reset unknown",
			msg:    "You've hit your usage limit. Try again later.",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true,
		},
		{
			name:   "legacy epoch seconds",
			msg:    "Claude AI usage limit reached|1792713000",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 22, 23, 50),
		},
		{
			name:   "legacy epoch milliseconds",
			msg:    "Claude AI usage limit reached|1792713000000",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 22, 23, 50),
		},
		{
			name:   "impossible hour keeps reset unknown",
			msg:    "You've hit your session limit · resets 13pm (UTC)",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantWindow: "session",
		},
		{
			name:   "impossible calendar date keeps reset unknown",
			msg:    "You've hit your usage limit. Try again at Feb 30th, 2027 3:45 PM.",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true,
		},
		{
			name:   "unknown zone keeps reset unknown",
			msg:    "You've hit your session limit · resets 11:50pm (Mars/Base)",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantWindow: "session",
		},
		{
			name:   "openai insufficient_quota has no reset",
			msg:    `429 {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`,
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantNoRst: true,
		},
		{name: "temporary throttle is not a usage limit", msg: "Server is temporarily limiting requests (not your usage limit)", now: limitNow, loc: time.UTC},
		{name: "plain rate limit", msg: "Rate limit reached for requests in organization org-x on requests per min (RPM): Limit 3, Used 3.", now: limitNow, loc: time.UTC},
		{name: "rate_limit_exceeded code", msg: `429 {"error":{"code":"rate_limit_exceeded","message":"Rate limit reached"}}`, now: limitNow, loc: time.UTC},
		{name: "too many requests", msg: "429 Too Many Requests", now: limitNow, loc: time.UTC},
		{name: "hit your rate limit", msg: "You have hit your rate limit.", now: limitNow, loc: time.UTC},
		{name: "hit your rate limit with contraction", msg: "You've hit your rate limit, slow down.", now: limitNow, loc: time.UTC},
		{name: "hit your rate limit with the explicit code", msg: `429 {"error":{"code":"rate_limit_exceeded","message":"You have hit your rate limit"}}`, now: limitNow, loc: time.UTC},
		{name: "explicit rate_limit_exceeded beats a usage-limit wording", msg: `rate_limit_exceeded: You've hit your usage limit`, now: limitNow, loc: time.UTC},
		{name: "requests per minute", msg: "Rate limit exceeded: 60 requests per minute", now: limitNow, loc: time.UTC},
		{name: "tokens per min", msg: "Limit 40000 tokens per min (TPM). You have hit your token limit.", now: limitNow, loc: time.UTC},
		{name: "too many requests wording", msg: "Too many requests, please slow down. You've hit your request limit.", now: limitNow, loc: time.UTC},
		{
			name:   "a later usage-limit phrase still wins over an earlier rate phrase",
			msg:    "You have hit your rate limit. Later: You've hit your session limit · resets 11:50pm (UTC)",
			now:    limitNow,
			loc:    time.UTC,
			wantOK: true, wantReset: utc(2026, 10, 22, 23, 50), wantWindow: "session",
		},
		{name: "codex retry limit", msg: "exceeded retry limit, last status: 429 Too Many Requests", now: limitNow, loc: time.UTC},
		{name: "overloaded", msg: `{"type":"overloaded_error","message":"Overloaded"}`, now: limitNow, loc: time.UTC},
		{name: "garbage", msg: "boom", now: limitNow, loc: time.UTC},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseUsageLimitText(tc.msg, tc.now, tc.loc)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if !got.ResetAt.Equal(tc.wantReset) {
				t.Errorf("ResetAt = %v, want %v", got.ResetAt, tc.wantReset)
			}
			if got.Window != tc.wantWindow {
				t.Errorf("Window = %q, want %q", got.Window, tc.wantWindow)
			}
			if got.NoReset != tc.wantNoRst {
				t.Errorf("NoReset = %v, want %v", got.NoReset, tc.wantNoRst)
			}
			if got.Source != UsageLimitSourceText {
				t.Errorf("Source = %q, want %q", got.Source, UsageLimitSourceText)
			}
		})
	}

	t.Run("named IANA zone", func(t *testing.T) {
		if nyErr != nil {
			t.Skipf("tzdata unavailable: %v", nyErr)
		}
		got, ok := parseUsageLimitText("You've hit your session limit · resets 11:50pm (America/New_York)", limitNow, newYork)
		if !ok || !got.ResetAt.Equal(utc(2026, 10, 23, 3, 50)) { // 23:50 EDT (UTC-4)
			t.Fatalf("got %+v ok=%v, want reset 2026-10-23T03:50Z", got, ok)
		}
	})
}

func TestAgentLocation(t *testing.T) {
	t.Setenv("TZ", "UTC")
	if got := agentLocation(); got != time.UTC {
		t.Errorf("TZ=UTC: got %v", got)
	}
	t.Setenv("TZ", "Not/AZone")
	if got := agentLocation(); got != time.Local {
		t.Errorf("unparsable TZ must fall back to time.Local, got %v", got)
	}
	t.Setenv("TZ", "")
	if got := agentLocation(); got != time.Local {
		t.Errorf("unset TZ must fall back to time.Local, got %v", got)
	}
}

// --- поток агента через реальные Run* -----------------------------------------------------------------------

const (
	rejectedEventSec = `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1792713000,"rateLimitType":"five_hour","overageStatus":"rejected","isUsingOverage":false}}`
	rejectedEventMs  = `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1792713000000,"rateLimitType":"five_hour"}}`
	warningEvent     = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1792713000,"rateLimitType":"five_hour","utilization":0.92}}`
	limitAssistant   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"You've hit your session limit · resets 11:50pm (UTC)"}]},"error":"rate_limit","is_api_error_message":true,"api_error_status":429,"api_error":"usage_limit_reached","api_error_params":{"rate_limit_info":{"status":"rejected","resetsAt":1792713000,"rateLimitType":"five_hour"}}}`
	limitResult      = `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"api_error":"usage_limit_reached","result":"You've hit your session limit · resets 11:50pm (UTC)"}`
	limitToolUseLine = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go build ./..."}}]}}`
)

// emitScript печатает lines в stdout и завершается с кодом exit.
func emitScript(exit string, lines ...string) string {
	return "cat <<'EOF'\n" + strings.Join(lines, "\n") + "\nEOF\nexit " + exit
}

// limitProvider — Config.Provider тестовых executor'ов: Run* обязаны перенести его в UsageLimitError.
const limitProvider = "test-provider"

// limitExecutor фиксирует TZ процесса в UTC: время без зоны в сообщениях агента («Try again at 11:55 PM»)
// читается в поясе агента, и ожидаемые моменты не должны зависеть от пояса машины.
func limitExecutor(t *testing.T, script string) *Executor {
	t.Helper()
	t.Setenv("TZ", "UTC")
	return New(Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", script},
		IdleTimeout: 5 * time.Second,
		Clock:       clock.NewFake(limitNow),
		Provider:    limitProvider,
	})
}

func runLimitAgent(t *testing.T, script string) error {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "agent.log")
	return limitExecutor(t, script).RunAgent(context.Background(), "implementation", "s1", "go", logFile)
}

func TestRunAgent_UsageLimit(t *testing.T) {
	reset := utc(2026, 10, 22, 23, 50)
	tests := []struct {
		name     string
		script   string
		wantOK   bool
		want     UsageLimitError // Message проверяется отдельно
		wantProg bool
	}{
		{
			name:   "real claude stream, exit 1",
			script: emitScript("1", warningEvent, rejectedEventSec, limitAssistant, limitResult),
			wantOK: true,
			want:   UsageLimitError{ResetAt: reset, Window: "five_hour", Source: UsageLimitSourceStructured},
		},
		{
			name:   "real claude stream, exit 0 with subtype success",
			script: emitScript("0", rejectedEventSec, limitAssistant, limitResult),
			wantOK: true,
			want:   UsageLimitError{ResetAt: reset, Window: "five_hour", Source: UsageLimitSourceStructured},
		},
		{
			name:   "resetsAt in milliseconds",
			script: emitScript("1", rejectedEventMs, limitAssistant, limitResult),
			wantOK: true,
			want:   UsageLimitError{ResetAt: reset, Window: "five_hour", Source: UsageLimitSourceStructured},
		},
		{
			name:   "assistant error alone carries the structured reset",
			script: emitScript("1", limitAssistant),
			wantOK: true,
			want:   UsageLimitError{ResetAt: reset, Window: "five_hour", Source: UsageLimitSourceStructured},
		},
		{
			name: "api_error without rate_limit_info falls back to the message text",
			script: emitScript("1", `{"type":"result","subtype":"success","is_error":true,"api_error":"usage_limit_reached",`+
				`"result":"You've hit your session limit · resets 11:50pm (UTC)"}`),
			wantOK: true,
			want:   UsageLimitError{ResetAt: reset, Window: "session", Source: UsageLimitSourceText},
		},
		{
			name: "rejected event followed by a plain error result",
			script: emitScript("1", rejectedEventSec,
				`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"request failed"}`),
			wantOK: true,
			want:   UsageLimitError{ResetAt: reset, Window: "five_hour", Source: UsageLimitSourceStructured},
		},
		{
			name:   "rejected event does not turn an unrelated error into a limit",
			script: emitScript("1", rejectedEventSec, `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"max turns"}`),
		},
		{
			name: "text only: error result of an adapter",
			script: emitScript("1",
				`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"You've hit your usage limit. Try again at 11:55 PM."}`),
			wantOK: true,
			want:   UsageLimitError{ResetAt: utc(2026, 10, 22, 23, 55), Source: UsageLimitSourceText},
		},
		{
			name: "insufficient_quota has no reset",
			script: emitScript("1",
				`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"429 insufficient_quota: You exceeded your current quota"}`),
			wantOK: true,
			want:   UsageLimitError{NoReset: true, Source: UsageLimitSourceText},
		},
		{
			name:   "stderr-only failure uses the same parser",
			script: "echo \"You've hit your usage limit. Try again at 11:55 PM.\" >&2\nexit 1",
			wantOK: true,
			want:   UsageLimitError{ResetAt: utc(2026, 10, 22, 23, 55), Source: UsageLimitSourceText},
		},
		{
			name:     "progress before the limit is reported",
			script:   emitScript("1", limitToolUseLine, rejectedEventSec, limitAssistant, limitResult),
			wantOK:   true,
			want:     UsageLimitError{ResetAt: reset, Window: "five_hour", Source: UsageLimitSourceStructured},
			wantProg: true,
		},
		{name: "allowed_warning does not stop a successful turn", script: emitScript("0", warningEvent, `{"type":"result","subtype":"success","is_error":false}`)},
		{
			name:   "rejected event that the agent survived is not a limit",
			script: emitScript("0", rejectedEventSec, `{"type":"result","subtype":"success","is_error":false}`),
		},
		{
			name:   "529 overloaded stays transient",
			script: emitScript("1", `{"type":"assistant","message":{"content":[{"type":"text","text":"Overloaded"}]},"error":"overloaded","api_error_status":529}`, `{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"api_error":"overloaded","result":"Overloaded"}`),
		},
		{
			name:   "rejected event does not turn a 529 into a limit",
			script: emitScript("1", rejectedEventSec, `{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"api_error":"overloaded","result":"Overloaded"}`),
		},
		{
			name:   "api_retry events are not a limit",
			script: emitScript("1", `{"type":"system","subtype":"api_retry","attempt":1,"error_status":429,"error":"rate_limit"}`),
		},
		{
			name: "not your usage limit",
			script: emitScript("1",
				`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"Server is temporarily limiting requests (not your usage limit)"}`),
		},
		{
			name: "wrapper result with rate_limit_exceeded and hit-your-rate-limit wording",
			script: emitScript("1",
				`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429,"result":"429 {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"You have hit your rate limit\"}}"}`),
		},
		{
			name: "wrapper result with only hit-your-rate-limit wording",
			script: emitScript("1",
				`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"You have hit your rate limit."}`),
		},
		{
			name:   "error event with hit-your-rate-limit wording",
			script: emitScript("1", `{"type":"error","message":"You've hit your rate limit"}`),
		},
		{
			name:   "stderr hit-your-rate-limit is transient",
			script: "echo 'You have hit your rate limit (rate_limit_exceeded)' >&2\nexit 1",
		},
		{
			name:   "stderr requests per minute is transient",
			script: "echo 'Limit 3, Used 3: requests per minute; tokens per min; Too Many Requests' >&2\nexit 1",
		},
		{
			name:   "stderr rate limit is transient",
			script: "echo 'Rate limit reached for requests' >&2\nexit 1",
		},
		{
			name:   "agent prose about limits in a normal answer is not an error",
			script: emitScript("0", `{"type":"assistant","message":{"content":[{"type":"text","text":"You've hit your usage limit, said the docs."}]}}`, `{"type":"result","subtype":"success","is_error":false}`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runLimitAgent(t, tc.script)
			var ule *UsageLimitError
			if got := errors.As(err, &ule); got != tc.wantOK {
				t.Fatalf("errors.As(*UsageLimitError) = %v, want %v (err = %v)", got, tc.wantOK, err)
			}
			if !tc.wantOK {
				return
			}
			if !ule.ResetAt.Equal(tc.want.ResetAt) || ule.Window != tc.want.Window ||
				ule.NoReset != tc.want.NoReset || ule.Source != tc.want.Source || ule.Progress != tc.wantProg {
				t.Errorf("got %+v, want %+v progress=%v", *ule, tc.want, tc.wantProg)
			}
			if ule.Provider != limitProvider {
				t.Errorf("Provider = %q, want %q from Config.Provider", ule.Provider, limitProvider)
			}
			if ule.Message == "" || !strings.Contains(err.Error(), ule.Message) {
				t.Errorf("Message %q must be non-empty and part of Error() %q", ule.Message, err)
			}
		})
	}
}

func TestRunAgent_UsageLimitKeepsRawLog(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "agent.log")
	err := limitExecutor(t, emitScript("1", rejectedEventSec, limitAssistant, limitResult)).
		RunAgent(context.Background(), "implementation", "s1", "go", logFile)
	var ule *UsageLimitError
	if !errors.As(err, &ule) {
		t.Fatalf("want UsageLimitError, got %v", err)
	}
	raw, rerr := os.ReadFile(strings.TrimSuffix(logFile, ".log") + ".jsonl")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(raw), `"status":"rejected"`) || !strings.Contains(string(raw), "usage_limit_reached") {
		t.Errorf("raw stream must keep every line, got %s", raw)
	}
}

func TestRunAgent_UsageLimitMessageIsRedacted(t *testing.T) {
	secret := "sk-very-secret-token" //nolint:gosec // test fixture, not a real secret
	line := `{"type":"result","subtype":"success","is_error":true,"api_error":"usage_limit_reached","result":"You've hit your session limit · resets 11:50pm (UTC) key=` + secret + `"}`
	logFile := filepath.Join(t.TempDir(), "agent.log")
	ex := New(Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", emitScript("1", line)},
		IdleTimeout: 5 * time.Second,
		Clock:       clock.NewFake(limitNow),
		Redact:      func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") },
	})
	err := ex.RunAgent(context.Background(), "implementation", "s1", "go", logFile)
	var ule *UsageLimitError
	if !errors.As(err, &ule) {
		t.Fatalf("want UsageLimitError, got %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(ule.Message, secret) {
		t.Errorf("secret leaked: %v", err)
	}
}

func TestRunAgent_UserInterruptIsNotAUsageLimit(t *testing.T) {
	interrupt := make(chan struct{}, 1)
	interrupt <- struct{}{}
	ex := New(Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", emitScript("1", rejectedEventSec, limitAssistant, limitResult)},
		IdleTimeout: 5 * time.Second,
		Clock:       clock.NewFake(limitNow),
		InterruptCh: interrupt,
	})
	err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "agent.log"))
	var ule *UsageLimitError
	if errors.As(err, &ule) {
		t.Fatalf("interrupt must not become a usage limit: %v", err)
	}
}

func TestRunPlanning_UsageLimit(t *testing.T) {
	dir := t.TempDir()
	err := limitExecutor(t, emitScript("1", rejectedEventSec, limitAssistant, limitResult)).
		RunPlanning(context.Background(), "s1", "plan", filepath.Join(dir, "out.md"), filepath.Join(dir, "plan.log"))
	var ule *UsageLimitError
	if !errors.As(err, &ule) || !ule.ResetAt.Equal(utc(2026, 10, 22, 23, 50)) || ule.Provider != limitProvider {
		t.Fatalf("want UsageLimitError with reset 23:50Z and provider %q, got %v", limitProvider, err)
	}
}

func TestRunVerifyAgent_UsageLimit(t *testing.T) {
	for _, exit := range []string{"0", "1"} {
		t.Run("exit "+exit, func(t *testing.T) {
			dir := t.TempDir()
			outcome, err := limitExecutor(t, emitScript(exit, rejectedEventSec, limitAssistant, limitResult)).
				RunVerifyAgent(context.Background(), "claude", "s1", "verify", filepath.Join(dir, "v.log"), filepath.Join(dir, "r.json"))
			var ule *UsageLimitError
			if !errors.As(err, &ule) || !ule.ResetAt.Equal(utc(2026, 10, 22, 23, 50)) || ule.Provider != limitProvider {
				t.Fatalf("want UsageLimitError with reset 23:50Z and provider %q, got %v", limitProvider, err)
			}
			if outcome.ProcessOK || outcome.Result != nil {
				t.Errorf("a limited verify run must not look like a verdict: %+v", outcome)
			}
		})
	}
}

func TestRunVerifyAgent_TransientExitIsNotAUsageLimit(t *testing.T) {
	dir := t.TempDir()
	outcome, err := limitExecutor(t, "echo boom >&2\nexit 1").
		RunVerifyAgent(context.Background(), "codex", "s1", "verify", filepath.Join(dir, "v.log"), filepath.Join(dir, "r.json"))
	var ule *UsageLimitError
	if err != nil || errors.As(err, &ule) || outcome.ProcessOK {
		t.Fatalf("transient exit keeps the old contract (nil error, !ProcessOK): outcome=%+v err=%v", outcome, err)
	}
}

func TestRunHelperAgent_UsageLimit(t *testing.T) {
	for _, exit := range []string{"0", "1"} {
		t.Run("exit "+exit, func(t *testing.T) {
			dir := t.TempDir()
			outcome, err := limitExecutor(t, emitScript(exit, rejectedEventSec, limitAssistant, limitResult)).
				RunHelperAgent(context.Background(), "t-1", "reviewer", "help", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
			var ule *UsageLimitError
			if !errors.As(err, &ule) || !ule.ResetAt.Equal(utc(2026, 10, 22, 23, 50)) || ule.Provider != limitProvider {
				t.Fatalf("want UsageLimitError with reset 23:50Z and provider %q, got %v", limitProvider, err)
			}
			if outcome.ProcessOK {
				t.Errorf("a limited helper turn must not be ProcessOK: %+v", outcome)
			}
		})
	}
}

func TestRunHelperAgent_UsageLimitStderrIsRedacted(t *testing.T) {
	secret := "sk-helper-secret" //nolint:gosec // test fixture, not a real secret
	dir := t.TempDir()
	ex := New(Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", "echo \"You've hit your usage limit. Try again later. " + secret + "\" >&2\nexit 1"},
		IdleTimeout: 5 * time.Second,
		Clock:       clock.NewFake(limitNow),
		Secrets:     func() []string { return []string{secret} },
	})
	_, err := ex.RunHelperAgent(context.Background(), "t-1", "reviewer", "help", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
	var ule *UsageLimitError
	if !errors.As(err, &ule) {
		t.Fatalf("want UsageLimitError, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("secret leaked into the error: %v", err)
	}
}

// --- allowed_warning: узкий callback Config.OnLimitWarning ---------------------------------------------------

// warningRecorder собирает предупреждения, пришедшие в Config.OnLimitWarning.
type warningRecorder struct {
	mu    sync.Mutex
	items []LimitWarning
}

func (r *warningRecorder) record(w LimitWarning) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, w)
}

func (r *warningRecorder) all() []LimitWarning {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.items)
}

func warningExecutor(t *testing.T, rec *warningRecorder, script string) *Executor {
	t.Helper()
	t.Setenv("TZ", "UTC")
	return New(Config{
		Command:        "bash",
		ExtraArgs:      []string{"-c", script},
		IdleTimeout:    5 * time.Second,
		Clock:          clock.NewFake(limitNow),
		Provider:       limitProvider,
		OnLimitWarning: rec.record,
	})
}

func checkWarning(t *testing.T, rec *warningRecorder) {
	t.Helper()
	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("OnLimitWarning calls = %d, want 1: %+v", len(got), got)
	}
	w := got[0]
	if w.Provider != limitProvider || w.Window != "five_hour" || !w.ResetAt.Equal(utc(2026, 10, 22, 23, 50)) || w.Utilization != 0.92 {
		t.Errorf("warning = %+v, want provider %q window five_hour reset 23:50Z utilization 0.92", w, limitProvider)
	}
}

func TestRunAgent_AllowedWarningCallsOnLimitWarning(t *testing.T) {
	rec := &warningRecorder{}
	script := emitScript("0", warningEvent, `{"type":"result","subtype":"success","is_error":false}`)
	if err := warningExecutor(t, rec, script).RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "agent.log")); err != nil {
		t.Fatalf("a warning must not stop the turn: %v", err)
	}
	checkWarning(t, rec)
}

func TestRunPlanning_AllowedWarningCallsOnLimitWarning(t *testing.T) {
	rec := &warningRecorder{}
	dir := t.TempDir()
	script := emitScript("0", warningEvent, `{"type":"result","subtype":"success","is_error":false}`)
	_ = warningExecutor(t, rec, script).RunPlanning(context.Background(), "s1", "plan", filepath.Join(dir, "out.md"), filepath.Join(dir, "plan.log"))
	checkWarning(t, rec)
}

func TestRunVerifyAgent_AllowedWarningCallsOnLimitWarning(t *testing.T) {
	rec := &warningRecorder{}
	dir := t.TempDir()
	script := emitScript("0", warningEvent, `{"type":"result","subtype":"success","is_error":false}`)
	_, _ = warningExecutor(t, rec, script).RunVerifyAgent(context.Background(), "claude", "s1", "verify", filepath.Join(dir, "v.log"), filepath.Join(dir, "r.json"))
	checkWarning(t, rec)
}

func TestRunHelperAgent_AllowedWarningCallsOnLimitWarning(t *testing.T) {
	rec := &warningRecorder{}
	dir := t.TempDir()
	script := emitScript("0", warningEvent, `{"type":"result","subtype":"success","is_error":false}`)
	_, _ = warningExecutor(t, rec, script).RunHelperAgent(context.Background(), "t1", "reviewer", "go", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
	checkWarning(t, rec)
}

// A rejected event is the limit itself (UsageLimitError), never a warning.
func TestRunAgent_RejectedEventIsNotAWarning(t *testing.T) {
	rec := &warningRecorder{}
	script := emitScript("1", rejectedEventSec, limitAssistant, limitResult)
	_ = warningExecutor(t, rec, script).RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "agent.log"))
	if got := rec.all(); len(got) != 0 {
		t.Errorf("rejected must not warn: %+v", got)
	}
}

// OnLimitWarning is optional: nil must not panic.
func TestRunAgent_AllowedWarningWithoutCallback(t *testing.T) {
	script := emitScript("0", warningEvent, `{"type":"result","subtype":"success","is_error":false}`)
	if err := runLimitAgent(t, script); err != nil {
		t.Fatalf("got %v", err)
	}
}
