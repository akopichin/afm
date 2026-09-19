package orchestrator

import (
	"errors"
	"testing"
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
		{"incomplete", &IncompleteWorkError{Reason: "no .done"}, ClassIncomplete},
		{"missing artifact", &MissingArtifactError{Name: "api-contract"}, ClassMissingArtifact},
		{"missing sections", &MissingSectionsError{Missing: []string{sectionAssumptions}}, ClassMissingSections},
		{"storage fatal", &StorageError{Inner: errors.New("disk full")}, ClassStorageFatal},
		{"generic", errors.New("something broke"), ClassFatal},
		{"api error 529", errors.New("API Error: 529 Overloaded"), ClassRetryable},
		{"api error 502", errors.New("API Error: 502 Bad Gateway"), ClassRetryable},
		{"api error 503", errors.New("API Error: 503 Service Unavailable"), ClassRetryable},
		{"api error 504", errors.New("API Error: 504 Gateway Timeout"), ClassRetryable},
		{"api error 500 stays fatal", errors.New("API Error: 500"), ClassFatal},
		{"verify rejected routes through incomplete policy", &VerifyRejectedError{
			IncompleteWorkError: &IncompleteWorkError{Reason: "verify needs changes"},
			ReportID:            "v-1",
			Step:                1,
		}, ClassIncomplete},
		{"verify exec error is neither incomplete nor retryable", &VerifyExecError{
			Reason: "verify inconclusive: hit rate limit while checking",
			Step:   2,
		}, ClassVerifyFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestVerifyRejectedError_UnwrapsToIncompleteWorkError проверяет, что
// errors.As(&IncompleteWorkError{}) находит встроенный *IncompleteWorkError
// через Unwrap — именно это делает возможным ClassIncomplete в Classify выше
// и повторное использование СУЩЕСТВУЮЩЕЙ once-retry политики (runWithRetry,
// IsIncompleteWorkError) для отказа verify, без отдельной ветки кода.
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

// TestClassify_VerifyExecErrorFreeTextDoesNotTriggerTransportRetry защищает
// правило из брифа: свободный текст Reason у VerifyExecError (напр. "rate
// limit" внутри summary верификатора) НЕ должен пройти через substring-скан
// Classify как ретраебельная транспортная ошибка — типизированная проверка
// errors.As срабатывает раньше сканирования текста.
func TestClassify_VerifyExecErrorFreeTextDoesNotTriggerTransportRetry(t *testing.T) {
	err := &VerifyExecError{Reason: "verify inconclusive: model reported a rate limit warning", Step: 1}
	if got := Classify(err); got == ClassRetryable {
		t.Errorf("VerifyExecError free text must not classify as ClassRetryable, got %v", got)
	}
	if got := Classify(err); got == ClassIncomplete {
		t.Errorf("VerifyExecError must not classify as ClassIncomplete, got %v", got)
	}
}
