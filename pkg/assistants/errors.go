package assistants

// ErrorCode — стабильный машинный код ошибки collaboration-API (§13.3). Строковая
// константа, а не int-enum: код уходит в HTTP-ответы и журнал, должен остаться
// читаемым и не смещаться при рефакторинге/добавлении новых кодов.
type ErrorCode string

const (
	// CodeFeatureDisabled — collaboration выключен конфигурацией (флоу/проект/глобально).
	CodeFeatureDisabled ErrorCode = "feature_disabled"
	// CodeCapabilityUnavailable — запрошенная возможность (например, Browser) недоступна
	// выбранному профилю/агенту.
	CodeCapabilityUnavailable ErrorCode = "capability_unavailable"
	// CodeStaleRevision — операция ссылается на устаревшую ревизию политики/контекста.
	CodeStaleRevision ErrorCode = "stale_revision"
	// CodeInvalidStatus — переход запрошен из статуса, из которого он невозможен.
	CodeInvalidStatus ErrorCode = "invalid_status"
	// CodeFlowPaused — операция недоступна, пока флоу/стадия на паузе.
	CodeFlowPaused ErrorCode = "flow_paused"
	// CodeRunFinalizing — ран уже заканчивается, новые задачи помощников не принимаются.
	CodeRunFinalizing ErrorCode = "run_finalizing"
	// CodeBudgetExceeded — исчерпан бюджет (стоимость/число задач/т.п.).
	CodeBudgetExceeded ErrorCode = "budget_exceeded"
	// CodeDelegationDepthExceeded — новая задача превысила разрешённую глубину делегирования.
	CodeDelegationDepthExceeded ErrorCode = "delegation_depth_exceeded"
	// CodeInvalidParent — указанный родитель не проходит инварианты дерева задач (см. tree.go).
	CodeInvalidParent ErrorCode = "invalid_parent"
	// CodeAncestorInactive — предок задачи уже не активен (завершён/отменён), создавать
	// от его имени потомков нельзя.
	CodeAncestorInactive ErrorCode = "ancestor_inactive"
	// CodeActorNotAllowed — операция требует конкретного происхождения actor'а (например, T7.6's waiver:
	// только человек, никогда результат самой модели).
	CodeActorNotAllowed ErrorCode = "actor_not_allowed"
	// CodeExecutorNotPermitted — Этап 10 (T10.2, "no self elevation"): помощник пытается делегировать ребёнку
	// профиль с МЕНЕЕ строгой read-only-гарантией, чем у него самого (см. ReadonlyEnforcement.WeakerThan).
	// Применяется только когда делегирует сам помощник (actor.Kind == assistant) — явное, уже подтверждённое
	// действие человека/бокового агента этим правилом не ограничено (та же оговорка, что у self-match выше).
	CodeExecutorNotPermitted ErrorCode = "executor_not_permitted"
	// CodeNotFound — запрос ссылается на то, чего нет: неизвестный тред, участник, сообщение.
	CodeNotFound ErrorCode = "not_found"
	// CodeInvalidRequest — запрос некорректен по форме: пустые/повторяющиеся адресаты, адресат не в треде,
	// неполное условие ожидания и т.п. Reason объясняет, что исправить.
	CodeInvalidRequest ErrorCode = "invalid_request"
)
