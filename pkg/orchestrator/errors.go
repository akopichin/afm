package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/executor"
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
	// ClassRetryableUnknown — процесс агента стартовал и упал с текстом ошибки, который Classify не узнал
	// (D1). В отличие от ClassRetryable, идёт по ОГРАНИЧЕННОМУ бюджету ретраев (MaxUnknownRetries) — иначе
	// детерминированный сбой (сломанный wrapper, отсутствующий jq) жёг бы полный бюджет наравне с настоящим
	// транспортным сбоем. Добавлена в конец const-блока, чтобы не сдвинуть значения существующих классов.
	ClassRetryableUnknown
	// ClassUsageLimit — аккаунт провайдера исчерпал лимит использования (типизированный
	// *executor.UsageLimitError). Это не транзиентный сбой: ретрай сразу бесполезен, нужно ждать сброса.
	// Добавлена в конец const-блока, чтобы не сдвинуть значения существующих классов.
	ClassUsageLimit
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

// Сетевые паттерны (T3, D1): формулировки транспортных сбоев из Node/Go/curl/разных CLI и прокси.
// Allowlist по подстрокам в принципе не может перечислить их все — отсюда инверсия дефолта в конце
// Classify, но узнанные паттерны всё ещё идут по полному бюджету ретраев, а не по ограниченному.
const (
	matchConnectionReset       = "connection reset by peer"
	matchTLSHandshake          = "tls handshake timeout"
	matchNoSuchHost            = "no such host"
	matchFetchFailed           = "fetch failed"
	matchSocketHangUp          = "socket hang up"
	matchUnexpectedEOF         = "unexpected eof"
	matchETIMEDOUT             = "etimedout"
	matchEAIAgain              = "eai_again"
	matchConnectionRefused     = "connection refused"
	matchNetworkUnreachable    = "network is unreachable"
	matchBrokenPipe            = "broken pipe"
	matchIOTimeout             = "i/o timeout"
	matchBadGateway502         = "502 bad gateway"
	matchServiceUnavailable503 = "503 service unavailable"
	matchGatewayTimeout504     = "504 gateway timeout"
	matchTooManyRequests429    = "429 too many requests"
	matchUpstreamConnectError  = "upstream connect error"
)

// Алиасы stagefiles сохраняют identity ошибок для errors.As и классификации ретраев.
type IncompleteWorkError = stagefiles.IncompleteWorkError
type MissingArtifactError = stagefiles.MissingArtifactError

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
	// Типизированный usage-limit идёт ДО текстовых правил: узкое распознавание сделал executor, а широкие
	// подстроки ("rate limit", "limit") не должны ни пропустить его как транспортный сбой, ни принять за лимит
	// обычный 429/529.
	var limit *executor.UsageLimitError
	if errors.As(err, &limit) {
		return ClassUsageLimit
	}
	// VerifyExecError — типизированная проверка ДО скана по подстрокам ниже: свободный текст Reason не должен
	// пройти как транспортная ретраебельная ошибка (даже если Reason упоминает "rate limit" — тип побеждает).
	var verr *VerifyExecError
	if errors.As(err, &verr) {
		return ClassVerifyFailed
	}
	// Типизированные сентинелы (T1/T2) тоже идут ДО скана по подстрокам: процесс, который не стартовал,
	// детерминированно фатален независимо от текста; idle timeout — ограниченный бюджет, а не угаданный
	// по подстроке.
	if errors.Is(err, executor.ErrAgentStart) {
		return ClassFatal
	}
	if errors.Is(err, context.Canceled) {
		return ClassFatal
	}
	if errors.Is(err, executor.ErrIdleTimeout) {
		return ClassRetryableUnknown
	}
	msg := strings.ToLower(err.Error())
	for _, p := range []string{
		matchHitYourLimit, matchRateLimit, matchTooManyRequests,
		matchOverloaded, matchAtCapacity,
		matchHTTP500, matchStatus500, matchInternalServerError,
		matchAPIError529, matchAPIError502, matchAPIError503, matchAPIError504,
		matchConnectionReset, matchTLSHandshake, matchNoSuchHost, matchFetchFailed,
		matchSocketHangUp, matchUnexpectedEOF, matchETIMEDOUT, matchEAIAgain,
		matchConnectionRefused, matchNetworkUnreachable, matchBrokenPipe, matchIOTimeout,
		matchBadGateway502, matchServiceUnavailable503, matchGatewayTimeout504,
		matchTooManyRequests429, matchUpstreamConnectError,
	} {
		if strings.Contains(msg, p) {
			return ClassRetryable
		}
	}
	// D1: инверсия дефолта. Процесс агента СТАРТОВАЛ и упал с текстом, которого нет в allowlist выше —
	// allowlist по подстрокам принципиально не может перечислить все формулировки сетевых сбоев (они приходят
	// из Node/Go/curl/разных CLI и прокси). Симметрично уже существующей логике verify-пути
	// (verify.go:514-519, "любой ненулевой exit — транзиентный"): считаем неузнанный сбой transient-by-default,
	// но ограниченным бюджетом (ClassRetryableUnknown, MaxUnknownRetries), чтобы детерминированный сбой
	// (сломанный wrapper, отсутствующий бинарь в PATH) не жёг полный бюджет ретраев наравне с настоящим
	// транспортным сбоем.
	return ClassRetryableUnknown
}
