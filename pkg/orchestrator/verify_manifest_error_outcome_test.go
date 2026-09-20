package orchestrator

import (
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

// TestVerifyManifestErrorOutcome_RecognizesWrappedInterrupted — регрессия на
// находку финального ревью AI-verify: verifyManifestErrorOutcome сравнивал
// err == executor.ErrUserInterrupted через ==, тогда как соседнее место
// (~verify.go:219) уже использует errors.Is. Голое == ломается на обёрнутой
// ошибке (fmt.Errorf("...: %w", executor.ErrUserInterrupted)) — верификатор,
// прерванный извне, но с обёрнутым err, ошибочно помечался бы "error", а не
// "interrupted".
func TestVerifyManifestErrorOutcome_RecognizesWrappedInterrupted(t *testing.T) {
	wrapped := fmt.Errorf("verify step failed: %w", executor.ErrUserInterrupted)
	if got := verifyManifestErrorOutcome(wrapped); got != execErrorKindInterrupted {
		t.Errorf("verifyManifestErrorOutcome(wrapped ErrUserInterrupted) = %q, want %q", got, execErrorKindInterrupted)
	}
}

func TestVerifyManifestErrorOutcome_BareInterrupted(t *testing.T) {
	if got := verifyManifestErrorOutcome(executor.ErrUserInterrupted); got != execErrorKindInterrupted {
		t.Errorf("verifyManifestErrorOutcome(ErrUserInterrupted) = %q, want %q", got, execErrorKindInterrupted)
	}
}

func TestVerifyManifestErrorOutcome_OtherError(t *testing.T) {
	if got := verifyManifestErrorOutcome(fmt.Errorf("boom")); got != verifyOutcomeError {
		t.Errorf("verifyManifestErrorOutcome(other) = %q, want %q", got, verifyOutcomeError)
	}
}
