package control

import (
	"errors"
	"fmt"
	"testing"

	"github.com/akopichin/afm/contracts/activity"
	"github.com/akopichin/afm/contracts/events"
)

func TestPublicErrorsRetainTheirIdentity(t *testing.T) {
	var incomplete *IncompleteWorkError
	if !errors.As(fmt.Errorf("wrapped: %w", &activity.IncompleteWorkError{}), &incomplete) {
		t.Fatal("incomplete work lost its public error identity")
	}
	var missing *MissingArtifactError
	if !errors.As(fmt.Errorf("wrapped: %w", &activity.MissingArtifactError{}), &missing) {
		t.Fatal("missing artifact lost its public error identity")
	}
	var storage *StorageError
	if !errors.As(fmt.Errorf("wrapped: %w", &events.StorageError{}), &storage) {
		t.Fatal("storage failure lost its public error identity")
	}
}
