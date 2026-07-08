package accounting

import (
	"time"

	"github.com/akopichin/afm/pkg/proxy"
	"github.com/akopichin/afm/pkg/state"
)

// Store — пакетный интерфейс только для чтения к *state.Store: всё, что нужно
// аккаунтингу для атрибуции и агрегации — это история переходов и снапшот стадий.
// Определён локально (а не импорт *state.Store как указателя), чтобы сигнатуры
// LoadStageWindows/NewAccountant оставались pointer-free по DSL-правилам Go, при
// этом реальный *state.Store удовлетворяет интерфейсу структурно.
type Store interface {
	History() ([]state.Transition, error)
	Snapshot() state.RunState
}

// StageWindow описывает временное окно одного выполнения стейджа: от перехода в
// running до терминального перехода (done/failed). End пуст, если стейдж ещё
// выполняется или ран был прерван до терминала.
type StageWindow struct {
	StageID string
	Start   string // RFC3339 — время перехода в running.
	End     string // RFC3339 — время терминального перехода; "" = ещё выполняется.
}

// isTerminalTransition сообщает, ведёт ли переход стейдж в терминальный статус
// (done или failed) — именно такие переходы закрывают окно выполнения.
func isTerminalTransition(t state.Transition) bool {
	return t.To == state.StatusDone || t.To == state.StatusFailed
}

// LoadStageWindows строит окна выполнения стейджей из истории переходов store.
//
// Опирается на гарантированный порядок History (возрастание Time) — не
// пересортировывает. Окно открывается в момент ухода стадии из pending в любой
// активный статус (planning/running/awaiting_* — не только running: планировочные
// и интерактивные стейджи до running не доходят, но токены потребляют в своём
// окне); следующий терминальный переход (done/failed) той же стадии закрывает его
// своим временем. Стейдж без последующего терминала получает End="". Повторный
// запуск стадии после явной неудачи (failed→pending→planning) даёт отдельное окно.
//
// Ошибка History пробрасывается без сокрытия.
func LoadStageWindows(store Store) ([]StageWindow, error) {
	history, err := store.History()
	if err != nil {
		return nil, err
	}

	// Не-nil пустой срез на успехе (nil зарезервирован для ошибки) — согласовано с
	// state.Store.History() и LoadUsageRecords: range по нему безопасен в любом случае.
	windows := make([]StageWindow, 0)
	// pendingStart хранит время открытия окна для стейджа, находящегося в выполнении.
	// FSM гарантирует не более одного такого окна на стадию одновременно.
	pendingStart := map[string]string{}

	for _, t := range history {
		switch {
		case t.From == state.StatusPending && !isTerminalTransition(t):
			// Уход из pending в активный статус — старт выполнения стадии. Открывает
			// окно независимо от того, в какой именно активный статус ушёл стейдж.
			pendingStart[t.StageID] = t.Time.Format(time.RFC3339)
		case isTerminalTransition(t):
			// Терминальный переход закрывает открытое окно, если оно есть.
			start, ok := pendingStart[t.StageID]
			if !ok {
				continue
			}
			windows = append(windows, StageWindow{
				StageID: t.StageID,
				Start:   start,
				End:     t.Time.Format(time.RFC3339),
			})
			delete(pendingStart, t.StageID)
		default:
			// Прочие переходы (planning/awaiting_approval/...) окна не формируют.
		}
	}

	// Окна без терминального перехода — стейдж ещё выполняется или ран прерван.
	// Проходим по ним в стабильном порядке (по тому, как Start появились в истории)
	// через ещё один проход, чтобы порядок окон был детерминированным.
	for _, t := range history {
		start, ok := pendingStart[t.StageID]
		if !ok {
			continue
		}
		windows = append(windows, StageWindow{
			StageID: t.StageID,
			Start:   start,
			End:     "",
		})
		delete(pendingStart, t.StageID)
	}

	return windows, nil
}

// AttributeStage сопоставляет Timestamp записи с окнами стейджей и возвращает ID
// единственного окна, интервал [Start, End) которого содержит это время.
//
// End="" трактуется как открытый правый край (без верхней границы). Ровно одно
// совпадение → StageID этого окна, ok=true. Ноль или более одного совпадения
// (перекрывающиеся окна параллельных top-level стейджей) → ok=false —
// неоднозначность сознательно не разрешается.
func AttributeStage(record proxy.UsageRecord, windows []StageWindow) (string, bool) {
	const rfc3339 = "2006-01-02T15:04:05Z07:00"

	match := ""
	matches := 0
	for _, w := range windows {
		start, err := time.Parse(rfc3339, w.Start)
		if err != nil {
			// Некорректное окно не может содержать запись — пропускаем.
			continue
		}
		if record.Timestamp.Before(start) {
			// Запись раньше начала окна — не попадает (нижняя граница включительна).
			continue
		}
		if w.End != "" {
			end, err := time.Parse(rfc3339, w.End)
			if err != nil {
				continue
			}
			// Верхняя граница исключительна: [Start, End).
			if !record.Timestamp.Before(end) {
				continue
			}
		}
		match = w.StageID
		matches++
	}
	if matches != 1 {
		return "", false
	}
	return match, true
}
