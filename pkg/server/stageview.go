package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// StageView joins event-log state, StageConfig and on-disk artifacts for
// GET /api/status. ShowPlan and ShowDialog define dashboard panel visibility.
type StageView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Status      state.StageStatus `json:"status"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Interactive bool              `json:"interactive"`
	Autonomous  bool              `json:"autonomous"`
	AutoApprove bool              `json:"auto_approve"`
	HasDialog   bool              `json:"has_dialog"`
	// IsScript — статический конфиг флоу: нужен фронту, чтобы скрывать пункт "Pause" в кебаб-меню, пока
	// скриптовая стадия реально выполняется.
	IsScript bool `json:"is_script"`
	// PausedFrom заполняется только когда Status == paused — панель паузы в дашборде решает по нему, какой
	// текст показать.
	PausedFrom state.StageStatus `json:"paused_from,omitempty"`
	ShowPlan   bool              `json:"show_plan"`
	ShowDialog bool              `json:"show_dialog"`
	// PreNote — текст заметки, прикреплённой к стадии до её старта. Даёт фронту и текст для префилла модалки
	// редактирования, и сигнал для индикатора 📝 «к стадии прикреплена заметка».
	PreNote string `json:"pre_note,omitempty"`
	// Buttons — подписи предопределённых кнопок кебаб-меню этой стадии. Пусто, если кнопок нет.
	Buttons []string `json:"buttons,omitempty"`
	// Cost — снапшот стоимости/токенов этой стадии (accounting.CostBundle. Stages[id]).
	Cost *accounting.CostView `json:"cost,omitempty"`
	// Verify — последний durable verify-индикатор из полного notices.jsonl; nil, если verify-событий не было.
	Verify *VerifyView `json:"verify,omitempty"`
	// AwaitingAssistantsBlocked (Этап 7, T7.9) — только при Status == awaiting_assistants: true, если текущий
	// блокирующий required-гейт требует решения человека (needs_changes/failed/cancelled/held помощник), а не
	// просто ждёт, пока помощник доработает (state.AssistantsState.NeedsHumanDecision, T7.7's же
	// классификация). Фронт рисует needs_changes как actionable attention только когда это true.
	AwaitingAssistantsBlocked bool `json:"awaiting_assistants_blocked,omitempty"`
}

// VerifyView отражает последний verify_started как running, verify_result как verdict либо error; новый
// проход снова даёт running.
type VerifyView struct {
	Step    int    `json:"step"`
	Command string `json:"command"`
	// Phase ∈ {"running","pass","needs_changes","inconclusive","error"}.
	Phase string `json:"phase"`
}

// buildStageViews joins event-log state, static configuration and on-disk artifacts. Ordering is
// display-only: rs.StageOrder remains unchanged.
func buildStageViews(rs state.RunState, runDir string, stages map[string]StageConfig, stageCosts map[string]*accounting.CostView) []StageView {
	order := topoOrder(rs.StageOrder, stages)
	views := make([]StageView, 0, len(order))
	verifyByStage := latestVerifyByStage(runDir)
	for _, id := range order {
		st := rs.Stages[id]
		autonomous := stageIsAutonomous(runDir, id)
		hasDialog := stageHasDialog(runDir, id)
		cfg := stages[id]
		isScript := cfg.IsScript
		// PlanPanel нужен failed/paused стадиям ради Retry/Continue, включая autonomous и script без plan.md.
		showPlan := (st.Status != state.StatusPending && !isScript && !autonomous) ||
			st.Status == state.StatusFailed || st.Status == state.StatusPaused
		// ShowDialog резервирует строку под DialogChannel в лейауте. interactive/autonomous сами по себе панель
		// БОЛЬШЕ не открывают — раньше это и порождало пустую дыру.
		showDialog := hasDialog || st.Status == state.StatusAwaitingUserInput
		pausedFrom := state.StageStatus("")
		if st.Status == state.StatusPaused {
			pausedFrom = st.PausedFrom
		}
		awaitingAssistantsBlocked := st.Status == state.StatusAwaitingAssistants && rs.Assistants.NeedsHumanDecision(id)

		views = append(views, StageView{
			ID:                        id,
			Name:                      rs.StageNames[id],
			Status:                    st.Status,
			UpdatedAt:                 st.UpdatedAt,
			Interactive:               cfg.Interactive,
			Autonomous:                autonomous,
			AutoApprove:               cfg.AutoApprove,
			HasDialog:                 hasDialog,
			IsScript:                  isScript,
			PausedFrom:                pausedFrom,
			ShowPlan:                  showPlan,
			ShowDialog:                showDialog,
			PreNote:                   state.LoadPreNote(filepath.Join(runDir, id)),
			Buttons:                   cfg.Buttons,
			Cost:                      stageCosts[id],
			Verify:                    verifyByStage[id],
			AwaitingAssistantsBlocked: awaitingAssistantsBlocked,
		})
	}
	return views
}

// topoOrder returns ids reordered so every stage renders after all of its depends_on — a display-only
// concern the dashboard's stage list uses instead of raw flow.yaml declaration order.
func topoOrder(ids []string, stages map[string]StageConfig) []string {
	index := make(map[string]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}

	indegree := make(map[string]int, len(ids))
	dependents := make(map[string][]string, len(ids))
	for _, id := range ids {
		for _, dep := range stages[id].DependsOn {
			if _, ok := index[dep]; !ok {
				continue
			}
			indegree[id]++
			dependents[dep] = append(dependents[dep], id)
		}
	}

	queue := make([]string, 0, len(ids))
	for _, id := range ids {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}

	result := make([]string, 0, len(ids))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, id)
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(result) != len(ids) {
		return ids
	}
	return result
}

func stageIsAutonomous(runDir, stageID string) bool {
	_, err := os.Stat(filepath.Join(runDir, stageID, "autonomous.flag"))
	return err == nil
}

func stageHasDialog(runDir, stageID string) bool {
	for _, p := range flow.Phases() {
		if _, err := os.Stat(mcp.DialogPath(filepath.Join(runDir, stageID), string(p))); err == nil {
			return true
		}
	}
	return false
}

// verifyNotice is the on-disk shape of one notices.jsonl line, decoded just enough to pick out
// verify_started/verify_result entries.
type verifyNotice struct {
	Time    time.Time      `json:"time"`
	Type    string         `json:"type"`
	StageID string         `json:"stage_id"`
	Data    map[string]any `json:"data"`
}

// latestVerifyByStage scans all notices once, keeping one verify notice per stage by timestamp, then file
// order on ties.
func latestVerifyByStage(runDir string) map[string]*VerifyView {
	f, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()

	best := make(map[string]*verifyNotice)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var n verifyNotice
		if json.Unmarshal(sc.Bytes(), &n) != nil {
			continue
		}
		if n.Type != string(bus.EventVerifyStarted) && n.Type != string(bus.EventVerifyResult) {
			continue
		}
		if cur := best[n.StageID]; cur == nil || !n.Time.Before(cur.Time) {
			entry := n
			best[n.StageID] = &entry
		}
	}

	views := make(map[string]*VerifyView, len(best))
	for stageID, notice := range best {
		views[stageID] = verifyViewFromNotice(notice)
	}
	return views
}

// verifyViewFromNotice maps verify_started to running and verify_result to its verdict or error.
func verifyViewFromNotice(n *verifyNotice) *VerifyView {
	view := &VerifyView{
		Step:    noticeIntField(n.Data, "step"),
		Command: noticeStringField(n.Data, "command"),
	}
	if n.Type == string(bus.EventVerifyStarted) {
		view.Phase = VerifyPhaseRunning
		return view
	}
	switch verdict := noticeStringField(n.Data, "verdict"); verdict {
	case VerifyPhasePass, VerifyPhaseNeedsChanges, VerifyPhaseInconclusive, VerifyPhaseAcceptedStalemate:
		view.Phase = verdict
	default:
		view.Phase = VerifyPhaseError
	}
	return view
}

// VerifyView.Phase values — named constants for the same vocabulary as the deleted client
// VerifyIndicatorPhase.
const (
	VerifyPhaseRunning = "running"
	VerifyPhasePass    = "pass"
	// VerifyPhaseAcceptedStalemate — терминальный ПРИНЯТЫЙ исход, синтезированный afm при срабатывании
	// review_patience. Отличается от pass (это не чистое прохождение) и НЕ error — стадия принята.
	VerifyPhaseAcceptedStalemate = "accepted_stalemate"
	VerifyPhaseNeedsChanges      = "needs_changes"
	VerifyPhaseInconclusive      = "inconclusive"
	VerifyPhaseError             = "error"
)

// noticeIntField/noticeStringField читают JSON-поля без panic; числовые значения после Unmarshal имеют тип
// float64.
func noticeIntField(data map[string]any, key string) int {
	if n, ok := data[key].(float64); ok {
		return int(n)
	}
	return 0
}

func noticeStringField(data map[string]any, key string) string {
	if s, ok := data[key].(string); ok {
		return s
	}
	return ""
}
