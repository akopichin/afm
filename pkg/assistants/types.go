// Package assistants — нейтральный словарь доменных типов для фичи
// collaboration (боковые помощники/сабагенты стадии): профили, задачи,
// результаты, инварианты дерева задач и политика глубины делегирования.
//
// Пакет сознательно не знает о хранилище и исполнении: ни pkg/state, ни
// pkg/orchestrator сюда не импортируются (см. TestNoForbiddenImports) — это
// общий словарь, на который оба смогут ссылаться, не создавая цикл
// state ↔ orchestrator. Ни одного вызова модели, ни одного HTTP-эндпоинта.
package assistants

import "time"

// TaskID — идентификатор задачи помощника, уникальный в пределах рана.
type TaskID string

// ProfileID — идентификатор профиля помощника (настроенного типа агента).
type ProfileID string

// Profile описывает настроенный тип помощника: какую команду запускать и с
// какими системными инструкциями/возможностями.
type Profile struct {
	ID    ProfileID
	Title string
	// Command — имя уже настроенной команды/recipe (claude, codex-as-claude,
	// docker.agents-рецепт и т.п.), а не новый бинарь/транспорт.
	Command      string
	Instructions string
	Capabilities Capabilities
	// ReadonlyEnforcement — какая read-only-гарантия действовала для этой команды в момент, когда профиль
	// был разрешён к использованию (T1.5's AvailabilityCheck). Пусто у "сырого" Profile, возвращаемого
	// Registry.Get — заполняется вызывающим (Attach, T3.4) из ProfileView.ReadonlyEnforcement ПЕРЕД тем как
	// профиль замораживается в Task.ProfileSnapshot, чтобы история задачи навсегда помнила гарантию,
	// действовавшую на момент прогона, а не ту, что действует сейчас (dev-plan.md Этап 1 T1.5).
	ReadonlyEnforcement ReadonlyEnforcement
}

// Capabilities — что разрешено помощнику этого профиля.
type Capabilities struct {
	Delegate bool // может создавать собственных детей
	Browser  bool // управляемый браузер; по умолчанию false
}

// Mode определяет, блокирует ли незакрытая задача завершение корневой стадии.
type Mode string

const (
	// ModeAdvisory — задача не блокирует завершение стадии; её результат —
	// рекомендация.
	ModeAdvisory Mode = "advisory"
	// ModeRequired — задача должна дойти до pass, прежде чем стадия сможет
	// завершиться (см. BlocksRootCompletion).
	ModeRequired Mode = "required"
)

// Valid reports whether m is a recognized Mode, or empty (meaning "use Attach's own default").
func (m Mode) Valid() bool {
	switch m {
	case "", ModeAdvisory, ModeRequired:
		return true
	default:
		return false
	}
}

// Trigger определяет, когда помощник приглашается высказаться.
type Trigger string

const (
	// TriggerOneShot — один ход и завершение.
	TriggerOneShot Trigger = "one_shot"
	// TriggerCheckpoints — помощник приглашается на контрольных точках родителя.
	TriggerCheckpoints Trigger = "checkpoints"
	// TriggerOnCompletion — помощник приглашается, когда порождающая его
	// работа завершена.
	TriggerOnCompletion Trigger = "on_completion"
)

// Valid reports whether t is a recognized Trigger, or empty (meaning "use Attach's own default").
func (t Trigger) Valid() bool {
	switch t {
	case "", TriggerOneShot, TriggerCheckpoints, TriggerOnCompletion:
		return true
	default:
		return false
	}
}

// TaskStatus — состояние задачи помощника в её собственном, отдельном от
// stage-FSM жизненном цикле.
type TaskStatus string

const (
	StatusWaitingParent   TaskStatus = "waiting_parent"
	StatusQueued          TaskStatus = "queued"
	StatusRunning         TaskStatus = "running"
	StatusWaitingChildren TaskStatus = "waiting_children"
	StatusWatching        TaskStatus = "watching"
	StatusPaused          TaskStatus = "paused"
	StatusCompleted       TaskStatus = "completed"
	StatusFailed          TaskStatus = "failed"
	StatusCancelled       TaskStatus = "cancelled"
)

// Terminal сообщает, что статус окончательный — задача больше не изменится.
func (s TaskStatus) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Verdict — вердикт помощника по завершении хода.
type Verdict string

const (
	VerdictPass         Verdict = "pass"
	VerdictNeedsChanges Verdict = "needs_changes"
	VerdictInconclusive Verdict = "inconclusive"
)

// ClosesGate сообщает, закрывает ли вердикт required-гейт. Закрывает только
// pass: needs_changes и inconclusive — обе «не готово», хоть и по разным
// причинам.
func (v Verdict) ClosesGate() bool {
	return v == VerdictPass
}

// Origin — кто инициировал создание задачи.
type Origin string

const (
	OriginUser      Origin = "user"
	OriginSideAgent Origin = "side_agent"
	OriginAssistant Origin = "assistant"
)

// Task — одна задача помощника в дереве задач стадии.
type Task struct {
	ID               TaskID
	RunID            string
	RootStageID      string
	ParentTaskID     *TaskID // nil для первого уровня (прямой потомок стадии)
	Depth            int     // вычисляет сервер через ComputeDepth; 1 для первого уровня
	PolicyRev        string  // DepthPolicy.Revision, под которой создана задача
	MaxDepthAtCreate int

	ProfileID ProfileID
	// ProfileSnapshot — неизменяемая копия профиля на момент создания задачи:
	// последующее редактирование профиля не должно задним числом менять уже
	// идущую/завершённую задачу.
	ProfileSnapshot Profile

	Title       string
	Instruction string
	Mode        Mode
	Trigger     Trigger

	Status       TaskStatus
	StatusReason string

	// *Generation — счётчики для оптимистичных проверок свежести (stale read
	// защита), по одному на каждый уровень дерева, на который может повлиять
	// конкурентное изменение.
	RootGeneration   uint64
	ParentGeneration uint64
	TaskGeneration   uint64

	CreatedBy Origin
	CreatedAt time.Time
	StartedAt time.Time
	// FinishedAt — нулевое время, пока задача не в терминальном статусе.
	FinishedAt time.Time
}

// ContextVersion идентифицирует снимок контекста задачи (инструкция + история
// хода), относительно которого посчитан ответ помощника — чтобы можно было
// обнаружить устаревший ответ (ответ на уже не актуальный контекст) до его
// принятия.
type ContextVersion string

// Finding — одно наблюдение помощника. Path/LineStart/LineEnd — указатели:
// находка может быть не привязана ни к файлу, ни к строке (например, общий
// архитектурный комментарий).
type Finding struct {
	ID        string  `json:"id"`
	Severity  string  `json:"severity"`
	Path      *string `json:"path,omitempty"`
	LineStart *int    `json:"line_start,omitempty"`
	LineEnd   *int    `json:"line_end,omitempty"`
	Detail    string  `json:"detail"`
}

// Result — декодированное содержимое хода помощника с kind:"result" (см.
// result.go и schema/assistant-result.schema.json). Теги json соответствуют
// envelope-полям схемы один в один; schema_version/kind сюда не входят — это
// служебные поля конверта, разбираемые отдельно в result.go.
type Result struct {
	Verdict         Verdict        `json:"verdict"`
	Summary         string         `json:"summary"`
	Findings        []Finding      `json:"findings,omitempty"`
	Evidence        []string       `json:"evidence,omitempty"`
	ContextVersion  ContextVersion `json:"context_version,omitempty"`
	SuggestedChange string         `json:"suggested_change,omitempty"`
	Limitations     string         `json:"limitations,omitempty"`
	// Repaired is set by the SERVER when the model's raw answer needed mechanical repair (RepairResult)
	// before it could be strictly decoded. Deliberately absent from resultEnvelope/
	// schema/assistant-result.schema.json — a model must never be able to declare itself repaired.
	Repaired bool `json:"repaired,omitempty"`
}

// DelegateRequest — один дочерний таск, который помощник с kind:"delegate"
// просит создать оркестратор. Полная форма (включая авторизацию/лимиты на
// делегирование) фиксируется на этапе 4a; здесь — минимальная форма, уже
// достаточная для декодирования ответа модели.
type DelegateRequest struct {
	ProfileID   ProfileID `json:"profile_id"`
	Title       string    `json:"title"`
	Instruction string    `json:"instruction"`
	Mode        Mode      `json:"mode"`
	Trigger     Trigger   `json:"trigger"`
}

// Continuation говорит оркестратору, как и когда вернуться к делегирующему
// помощнику после того, как запрошенные им дети завершатся.
type Continuation struct {
	// Trigger — обычно checkpoints или on_completion: когда именно будить
	// родителя.
	Trigger Trigger `json:"trigger"`
	// Note — необязательный контекст, который родитель увидит на следующем
	// ходу (например, что именно он ждёт от детей).
	Note string `json:"note,omitempty"`
}

// Proposal's full definition (type/status lifecycle/payload/etc.) lives in proposal.go (T6.4) — this file
// originally carried a narrow, uncalled placeholder of the same name; proposal.go supersedes it entirely.
