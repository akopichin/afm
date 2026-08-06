package orchestrator

import (
	"errors"
	"strings"

	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

type Classification int

const (
	ClassNone Classification = iota
	ClassRetryable
	ClassIncomplete
	ClassMissingArtifact
	ClassMissingSections
	ClassFatal
	ClassStorageFatal
)

// Retryable error pattern substrings used by Classify and tests.
const (
	matchHitYourLimit        = "hit your limit"
	matchRateLimit           = "rate limit"
	matchTooManyRequests     = "too many requests"
	matchOverloaded          = "overloaded"
	matchAtCapacity          = "at capacity"
	matchHTTP500             = "http 500"
	matchStatus500           = "status 500"
	matchInternalServerError = "internal server error"
	matchAPIError529         = "api error: 529"
	matchAPIError502         = "api error: 502"
	matchAPIError503         = "api error: 503"
	matchAPIError504         = "api error: 504"
)

// IncompleteWorkError и MissingArtifactError переехали в stagefiles (Task 3
// orchestrator-split) — их конструируют completion-check'и CheckCompletion/
// CheckPlanCompletionFor/CheckAutonomousCompletion, которые тоже там живут.
// Алиасы сохраняют identity типа для errors.As здесь (Classify) и в
// retry.go/agents.go, где эти типы используются как есть без префикса
// пакета — без алиаса пришлось бы везде переходить на stagefiles.*, что не
// входило в бриф этой задачи.
type IncompleteWorkError = stagefiles.IncompleteWorkError
type MissingArtifactError = stagefiles.MissingArtifactError

type MissingSectionsError struct{ Missing []string }

func (e *MissingSectionsError) Error() string {
	return "plan missing sections: " + strings.Join(e.Missing, ", ")
}

type StorageError struct{ Inner error }

func (e *StorageError) Error() string { return "storage failure: " + e.Inner.Error() }
func (e *StorageError) Unwrap() error { return e.Inner }

func Classify(err error) Classification {
	if err == nil {
		return ClassNone
	}
	var inc *IncompleteWorkError
	if errors.As(err, &inc) {
		return ClassIncomplete
	}
	var miss *MissingArtifactError
	if errors.As(err, &miss) {
		return ClassMissingArtifact
	}
	var sec *MissingSectionsError
	if errors.As(err, &sec) {
		return ClassMissingSections
	}
	var store *StorageError
	if errors.As(err, &store) {
		return ClassStorageFatal
	}
	msg := strings.ToLower(err.Error())
	for _, p := range []string{
		matchHitYourLimit, matchRateLimit, matchTooManyRequests,
		matchOverloaded, matchAtCapacity,
		matchHTTP500, matchStatus500, matchInternalServerError,
		matchAPIError529, matchAPIError502, matchAPIError503, matchAPIError504,
	} {
		if strings.Contains(msg, p) {
			return ClassRetryable
		}
	}
	return ClassFatal
}
