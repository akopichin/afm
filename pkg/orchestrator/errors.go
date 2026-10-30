package orchestrator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
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
	// ClassVerifyFailed — верификатор не смог выдать вердикт.
	ClassVerifyFailed
	// ClassSummaryMissing — автономный автор вышел без непустого execution_summary.md (отдельный бюджет).
	ClassSummaryMissing
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

// Алиасы stagefiles сохраняют identity ошибок для errors.As и классификации ретраев.
type IncompleteWorkError = stagefiles.IncompleteWorkError
type MissingArtifactError = stagefiles.MissingArtifactError
type SummaryError = stagefiles.SummaryError

type MissingSectionsError struct{ Missing []string }

func (e *MissingSectionsError) Error() string {
	return "plan missing sections: " + strings.Join(e.Missing, ", ")
}

// VerifyRejectedError сообщает, что верификатор нашёл блокирующие проблемы у уже "готовой" по мнению
// автора стадии.
type VerifyRejectedError struct {
	*stagefiles.IncompleteWorkError
	ReportID string
	Step     int
}

func (e *VerifyRejectedError) Error() string {
	return fmt.Sprintf("verify rejected (step %d, report %s): %s", e.Step, e.ReportID, e.IncompleteWorkError.Error())
}

// Unwrap делает встроенный *IncompleteWorkError видимым для errors.As — без этого метода embedding НЕ даёт
// автоматической цепочки Unwrap, и Classify/IsIncompleteWorkError не нашли бы его.
func (e *VerifyRejectedError) Unwrap() error { return e.IncompleteWorkError }

// VerifyExecError означает отсутствие вердикта и не запускает retry автора; Step — 1-based индекс, 0 для
// ошибки всего прохода.
type VerifyExecError struct {
	Reason    string
	Step      int
	Transient bool // true ТОЛЬКО для транзиентного инфра-сбоя запуска/работы verify-процесса (ретраебельно)
}

func (e *VerifyExecError) Error() string {
	if e.Step > 0 {
		return fmt.Sprintf("verify execution failed at step %d: %s", e.Step, e.Reason)
	}
	return "verify execution failed: " + e.Reason
}

// StorageError переехал в pkg/orchestrator/bus — его конструирует FSM.Apply при сбое записи в лог.
type StorageError = bus.StorageError

// isVerifyDrivenError сообщает, что checkErr — настоящий verify-исход, а не обычная ошибка
// completion-проверки.
func isVerifyDrivenError(err error) bool {
	var rejected *VerifyRejectedError
	if errors.As(err, &rejected) {
		return true
	}
	var execErr *VerifyExecError
	return errors.As(err, &execErr)
}

func Classify(err error) Classification {
	if err == nil {
		return ClassNone
	}
	var inc *IncompleteWorkError
	if errors.As(err, &inc) {
		return ClassIncomplete
	}
	var sum *SummaryError
	if errors.As(err, &sum) {
		return ClassSummaryMissing
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
	// VerifyExecError — типизированная проверка ДО скана по подстрокам ниже: свободный текст Reason не должен
	// пройти как транспортная ретраебельная ошибка.
	var verr *VerifyExecError
	if errors.As(err, &verr) {
		return ClassVerifyFailed
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
