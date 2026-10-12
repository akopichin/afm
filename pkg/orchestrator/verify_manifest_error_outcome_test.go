package orchestrator

import (
	"errors"
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

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
	if got := verifyManifestErrorOutcome(errors.New("boom")); got != verifyOutcomeError {
		t.Errorf("verifyManifestErrorOutcome(other) = %q, want %q", got, verifyOutcomeError)
	}
}
