package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Classification
	}{
		{"nil", nil, ClassNone},
		{matchRateLimit, errors.New("rate limit exceeded"), ClassRetryable},
		{matchOverloaded, errors.New("overloaded"), ClassRetryable},
		{matchHTTP500, errors.New("http 500 internal server error"), ClassRetryable},
		// Remaining pre-existing patterns (regression: all 12 original substrings stay ClassRetryable).
		{matchHitYourLimit, errors.New("you have hit your limit"), ClassRetryable},
		{matchTooManyRequests, errors.New("too many requests"), ClassRetryable},
		{matchAtCapacity, errors.New("service at capacity"), ClassRetryable},
		{matchStatus500, errors.New("status 500"), ClassRetryable},
		{matchInternalServerError, errors.New("internal server error"), ClassRetryable},
		{"incomplete", &IncompleteWorkError{Reason: "no .done"}, ClassIncomplete},
		{"missing artifact", &MissingArtifactError{Name: "api-contract"}, ClassMissingArtifact},
		{"missing sections", &MissingSectionsError{Missing: []string{sectionAssumptions}}, ClassMissingSections},
		{"storage fatal", &StorageError{Inner: errors.New("disk full")}, ClassStorageFatal},
		// D1 inversion: unrecognized text from a process that started and crashed is no longer ClassFatal —
		// it falls into the limited-budget ClassRetryableUnknown. See errors.go's Classify fallback comment.
		{"generic unrecognized text is now unknown-retryable", errors.New("something broke"), ClassRetryableUnknown},
		{"api error 529", errors.New("API Error: 529 Overloaded"), ClassRetryable},
		{"api error 502", errors.New("API Error: 502 Bad Gateway"), ClassRetryable},
		{"api error 503", errors.New("API Error: 503 Service Unavailable"), ClassRetryable},
		{"api error 504", errors.New("API Error: 504 Gateway Timeout"), ClassRetryable},
		{"api error 500 is now unknown-retryable", errors.New("API Error: 500"), ClassRetryableUnknown},
		{"verify rejected routes through incomplete policy", &VerifyRejectedError{
			IncompleteWorkError: &IncompleteWorkError{Reason: "verify needs changes"},
			ReportID:            "v-1",
			Step:                1,
		}, ClassIncomplete},
		{"verify exec error is neither incomplete nor retryable", &VerifyExecError{
			Reason: "verify inconclusive: hit rate limit while checking",
			Step:   2,
		}, ClassVerifyFailed},
		{"verify exec error reason containing rate limit stays ClassVerifyFailed (type check wins)",
			&VerifyExecError{Reason: "inconclusive: rate limit hit mid-check", Step: 1}, ClassVerifyFailed},

		// New transient network patterns (T3).
		{matchConnectionReset, errors.New("read: connection reset by peer"), ClassRetryable},
		{matchTLSHandshake, errors.New("net/http: TLS handshake timeout"), ClassRetryable},
		{matchNoSuchHost, errors.New("dial tcp: lookup api.anthropic.com: no such host"), ClassRetryable},
		{matchFetchFailed, errors.New("TypeError: fetch failed"), ClassRetryable},
		{matchSocketHangUp, errors.New("Error: socket hang up"), ClassRetryable},
		{matchUnexpectedEOF, errors.New("unexpected EOF"), ClassRetryable},
		{matchETIMEDOUT, errors.New("connect ETIMEDOUT"), ClassRetryable},
		{matchEAIAgain, errors.New("getaddrinfo EAI_AGAIN"), ClassRetryable},
		{matchConnectionRefused, errors.New("connection refused"), ClassRetryable},
		{matchNetworkUnreachable, errors.New("network is unreachable"), ClassRetryable},
		{matchBrokenPipe, errors.New("broken pipe"), ClassRetryable},
		{matchIOTimeout, errors.New("i/o timeout"), ClassRetryable},
		{matchBadGateway502, errors.New("502 Bad Gateway"), ClassRetryable},
		{matchServiceUnavailable503, errors.New("503 Service Unavailable"), ClassRetryable},
		{matchGatewayTimeout504, errors.New("504 Gateway Timeout"), ClassRetryable},
		{matchTooManyRequests429, errors.New("429 Too Many Requests"), ClassRetryable},
		{matchUpstreamConnectError, errors.New("upstream connect error"), ClassRetryable},

		// Case-insensitivity.
		{"case insensitive connection reset", errors.New("Connection Reset By Peer"), ClassRetryable},

		// Typed sentinel checks (T2/T1 already merged into executor).
		{"agent start failure is fatal", fmt.Errorf("%w: claude: not found", executor.ErrAgentStart), ClassFatal},
		{"context canceled is fatal", context.Canceled, ClassFatal},
		{"idle timeout is unknown-retryable", fmt.Errorf("idle timeout after 30m0s: %w", executor.ErrIdleTimeout), ClassRetryableUnknown},
		{"exit status 1 is unknown-retryable (was fatal)", errors.New("exit status 1"), ClassRetryableUnknown},

		// Usage-limit: типизированная ошибка executor'а побеждает текстовые правила (в том числе wrapped).
		{"typed usage limit", &executor.UsageLimitError{Message: "x"}, ClassUsageLimit},
		{"wrapped usage limit", fmt.Errorf("stage failed: %w", &executor.UsageLimitError{Message: "x"}), ClassUsageLimit},
		{"typed usage limit beats rate-limit text", &executor.UsageLimitError{Message: "rate limit, too many requests"}, ClassUsageLimit},
		{"no-reset quota is a usage limit", &executor.UsageLimitError{NoReset: true, Message: "insufficient_quota"}, ClassUsageLimit},
		// Без типа текстовые классы не меняются: transient 429/529 и широкая подстрока «limit» не usage-limit.
		{"plain 429 stays retryable", errors.New("API Error: 429 Too Many Requests"), ClassRetryable},
		{"plain 529 stays retryable", errors.New("API Error: 529 Overloaded"), ClassRetryable},
		{"plain usage-limit text stays unknown-retryable", errors.New("You've hit your usage limit"), ClassRetryableUnknown},
		{"not-your-limit text stays unknown-retryable", errors.New("this is not your usage limit"), ClassRetryableUnknown},
		{"verify exec error with usage-limit text stays verify-failed", &VerifyExecError{Reason: "hit your usage limit", Step: 1}, ClassVerifyFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestVerifyRejectedError_UnwrapsToIncompleteWorkError(t *testing.T) {
	rejected := &VerifyRejectedError{
		IncompleteWorkError: &IncompleteWorkError{Reason: "blocking findings"},
		ReportID:            "v-42",
		Step:                3,
	}
	var target *IncompleteWorkError
	if !errors.As(error(rejected), &target) {
		t.Fatal("errors.As should find the embedded *IncompleteWorkError")
	}
	if target.Reason != "blocking findings" {
		t.Errorf("unexpected unwrapped reason: %q", target.Reason)
	}
}

// TestCheckCompletion_ClassificationUnchanged живёт в pkg/orchestrator (а не
// stagefiles), чтобы не заводить цикл импортов stagefiles -> orchestrator.
// Enriching the .done/execution_summary.md diagnostics with a path and a
// distinct missing/empty/unreadable reason (T7) must not change the error type
// or how it is classified: still *IncompleteWorkError -> ClassIncomplete.
func TestCheckCompletion_ClassificationUnchanged(t *testing.T) {
	stage := flow.Stage{ID: "s1"}

	t.Run("missing .done", func(t *testing.T) {
		dir := t.TempDir()
		err := stagefiles.CheckCompletion(dir, ".", stage)
		if got := Classify(err); got != ClassIncomplete {
			t.Errorf("Classify(%v) = %v, want ClassIncomplete", err, got)
		}
	})

	t.Run("empty .done", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".done"), []byte("   "), 0644); err != nil {
			t.Fatalf("write .done: %v", err)
		}
		err := stagefiles.CheckCompletion(dir, ".", stage)
		if got := Classify(err); got != ClassIncomplete {
			t.Errorf("Classify(%v) = %v, want ClassIncomplete", err, got)
		}
	})

	t.Run("unreadable .done", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".done"), 0755); err != nil {
			t.Fatalf("mkdir .done: %v", err)
		}
		err := stagefiles.CheckCompletion(dir, ".", stage)
		if got := Classify(err); got != ClassIncomplete {
			t.Errorf("Classify(%v) = %v, want ClassIncomplete", err, got)
		}
	})
}

func TestClassify_VerifyExecErrorFreeTextDoesNotTriggerTransportRetry(t *testing.T) {
	err := &VerifyExecError{Reason: "verify inconclusive: model reported a rate limit warning", Step: 1}
	if got := Classify(err); got == ClassRetryable {
		t.Errorf("VerifyExecError free text must not classify as ClassRetryable, got %v", got)
	}
	if got := Classify(err); got == ClassIncomplete {
		t.Errorf("VerifyExecError must not classify as ClassIncomplete, got %v", got)
	}
}
