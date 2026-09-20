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
	// ClassVerifyFailed — верификатор не смог выдать вердикт (транспорт/
	// протокол/таймаут/inconclusive/сбой хранения/ненулевой exit самого
	// верификатора). Отдельный класс, а НЕ ClassFatal/ClassRetryable: раз
	// проверка не смогла оценить работу, ни бесплатный incomplete-retry, ни
	// транспортный ретрай АВТОРА её не чинят — see VerifyExecError.
	ClassVerifyFailed
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

// VerifyRejectedError сообщает, что верификатор (AI-verify) нашёл
// блокирующие проблемы у уже "готовой" по мнению автора стадии. Встраивает
// *stagefiles.IncompleteWorkError (через Unwrap ниже), поэтому проходит через
// СУЩЕСТВУЮЩУЮ once-retry политику: errors.As(err, &IncompleteWorkError{})
// находит его, Classify возвращает ClassIncomplete, IsIncompleteWorkError —
// true. Отдельного пути в runWithRetry для verify не нужно — verify-отказ
// неотличим для существующей политики от любого другого incomplete-work.
// ReportID/Step — куда смотреть за подробностями (verify/<ReportID>/report.md,
// конкретный неудавшийся шаг), не участвуют в классификации.
type VerifyRejectedError struct {
	*stagefiles.IncompleteWorkError
	ReportID string
	Step     int
}

func (e *VerifyRejectedError) Error() string {
	return fmt.Sprintf("verify rejected (step %d, report %s): %s", e.Step, e.ReportID, e.IncompleteWorkError.Error())
}

// Unwrap делает встроенный *IncompleteWorkError видимым для errors.As —
// без этого метода embedding НЕ даёт автоматической цепочки Unwrap (у
// IncompleteWorkError своего Unwrap нет, значит промоции по цепочке не
// происходит), и Classify/IsIncompleteWorkError не нашли бы его.
func (e *VerifyRejectedError) Unwrap() error { return e.IncompleteWorkError }

// VerifyExecError сообщает, что верификация НЕ смогла произвести вердикт:
// транспортный сбой запуска, протокольная ошибка ответа модели, таймаут,
// inconclusive-вердикт или сбой сохранения результата на диск. Это НЕ
// ClassIncomplete (перезапуск АВТОРА не поможет — сама проверка не
// состоялась, дело не в его работе) и НЕ ClassRetryable (свободный текст
// Reason может случайно содержать слова вроде "rate limit" из summary
// модели — это НЕ повод для транспортного ретрая; Classify матчит этот тип
// ДО скана по подстрокам, поэтому текст безопасен). Step — 1-based индекс
// шага verify, на котором случился сбой (0, если сбой относится ко всему
// проходу целиком, а не к конкретному шагу).
type VerifyExecError struct {
	Reason string
	Step   int
}

func (e *VerifyExecError) Error() string {
	if e.Step > 0 {
		return fmt.Sprintf("verify execution failed at step %d: %s", e.Step, e.Reason)
	}
	return "verify execution failed: " + e.Reason
}

// StorageError переехал в pkg/orchestrator/bus (Task 4 orchestrator-split) —
// его конструирует FSM.Apply при сбое записи в лог. Алиас сохраняет identity
// типа для errors.As здесь (Classify) и в orchestrator.go (triggerWithSeq) —
// тот же паттерн, что и для stagefiles.IncompleteWorkError/MissingArtifactError
// выше.
type StorageError = bus.StorageError

// isVerifyDrivenError сообщает, что checkErr — настоящий verify-исход
// (needs_changes-исчерпание ИЛИ exec-ошибка самого верификатора), а не
// обычная ошибка completion-проверки (missing/empty plan.md, missing
// artifact, missing plan sections, любой прочий generic fatal). ТОЛЬКО для
// такой ошибки уместен атомарный EvVerifyFail/commitVerifyFailure (retry.go)
// — H2 (7-е код-ревью) нашёл, что после H1 (см. EvVerifyFail, bus/fsm.go)
// commitVerifyFailure вызывался БЕЗУСЛОВНО для ЛЮБОЙ терминальной ошибки
// completionCheck, включая планировочные — а planning вообще не проходит
// verify (verify.go's gateWithVerify никогда не оборачивает planning-
// completionCheck) и потому никогда не бывает *VerifyRejectedError/
// *VerifyExecError. EvVerifyFail's From намеренно не включает
// StatusPlanning (см. bus/fsm.go) — CAS отбрасывал переход, стадия
// зависала в "planning" НАВСЕГДА.
//
// *VerifyRejectedError включён наравне с *VerifyExecError — это тоже
// verify-исход (второе подряд needs_changes после исчерпания бесплатного
// retry), просто Classify группирует его как ClassIncomplete ради ПОЛИТИКИ
// ретраев (тот же путь, что у обычного IncompleteWorkError — см. doc-
// комментарий VerifyRejectedError выше); эта группировка не про то, какое
// FSM-событие атомарно фиксирует финальный fail, поэтому здесь проверка
// идёт напрямую по типу ошибки (errors.As), а не через Classify().
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
	// VerifyExecError — типизированная проверка ДО скана по подстрокам ниже:
	// свободный текст Reason (напр. summary модели, случайно содержащее
	// "rate limit") не должен пройти как транспортная ретраебельная ошибка.
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
