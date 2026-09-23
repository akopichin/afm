package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// StageView is the per-stage read model served by GET /api/status. It joins
// state.StageState (event-log-derived) with the flow's static config
// (Interactive/AutoApprove) and two filesystem-derived runtime flags
// (Autonomous/HasDialog), and precomputes the two dashboard visibility
// capabilities (ShowPlan/ShowDialog) that pkg/web/dashboard's App.tsx used to
// recompute client-side from the same four raw flags — one source of truth
// for "can this stage's plan/dialog panel be shown" instead of two
// (Go here, TypeScript there) that had to be kept in sync by hand.
type StageView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Status      state.StageStatus `json:"status"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Interactive bool              `json:"interactive"`
	Autonomous  bool              `json:"autonomous"`
	AutoApprove bool              `json:"auto_approve"`
	HasDialog   bool              `json:"has_dialog"`
	// IsScript — статический конфиг флоу (Stage.IsScript()): нужен фронту,
	// чтобы скрывать пункт "Pause" в кебаб-меню, пока скриптовая стадия
	// реально выполняется (mid-script graceful stop не поддержан).
	IsScript bool `json:"is_script"`
	// PausedFrom заполняется только когда Status == paused (см.
	// state.StageState.PausedFrom, которое, в отличие от этого поля,
	// остаётся непустым и после Continue) — панель паузы в дашборде решает
	// по нему, какой текст показать.
	PausedFrom state.StageStatus `json:"paused_from,omitempty"`
	// ShowPlan/ShowDialog — the two visibility capabilities pkg/web/dashboard's
	// App.tsx computed from Autonomous/Status/Interactive/HasDialog. See that
	// file's showPlan/showDialog comment (removed in the frontend task of this
	// same plan) for the original rationale this mirrors exactly.
	ShowPlan   bool `json:"show_plan"`
	ShowDialog bool `json:"show_dialog"`
	// PreNote — текст заметки, прикреплённой к стадии до её старта (prenote.md).
	// Даёт фронту и текст для префилла модалки редактирования, и сигнал для
	// индикатора 📝 «к стадии прикреплена заметка». Пусто, если заметки нет.
	PreNote string `json:"pre_note,omitempty"`
	// Buttons — подписи предопределённых кнопок кебаб-меню этой стадии
	// (статический конфиг флоу). Пусто, если кнопок нет. Фронт рисует по одному
	// пункту меню на подпись и POST'ит подпись в /button при клике.
	Buttons []string `json:"buttons,omitempty"`
	// Cost — снапшот стоимости/токенов этой стадии (accounting.CostBundle.
	// Stages[id]). nil, если у стадии нет ни одной записи в usage.jsonl или
	// accounting вообще не подключён к серверу (см. Server.accounting).
	Cost *accounting.CostView `json:"cost,omitempty"`
	// Verify — durable-индикатор последнего AI-verify прохода этой стадии
	// (Task 7 плана per-stage-feed-full-feed): раньше бейдж вычислялся на
	// клиенте из (капнутой) глобальной ленты событий (computeVerifyIndicators)
	// и на долгих ранах мог "состариться" из окна — тот же класс проблемы, что
	// и вся эта фича лечит. Теперь это поле сервер считает через
	// latestVerifyByStage — ОДНО необрезанное сканирование ВСЕГО notices.jsonl
	// на весь /api/status (а не на стадию), а не reconstructNotices (тот
	// капнут maxStageReplayEvents=200). nil, если у стадии ни разу не было
	// verify_started/verify_result.
	Verify *VerifyView `json:"verify,omitempty"`
}

// VerifyView — снимок текущего состояния последнего AI-verify прохода стадии,
// зеркалит семантику удалённого клиентского computeVerifyIndicators
// (feed-view-model.ts): verify_started переводит в "running", verify_result —
// в свой verdict (pass/needs_changes/inconclusive), иначе — "error". Новый
// verify_started ПОСЛЕ уже завершённого прохода снова переводит в "running"
// (текущее состояние, а не финальный исход последнего прохода — так же, как
// вело себя клиентское вычисление).
type VerifyView struct {
	Step    int    `json:"step"`
	Command string `json:"command"`
	// Phase ∈ {"running","pass","needs_changes","inconclusive","error"}.
	Phase string `json:"phase"`
}

// buildStageViews joins rs.Stages (event-log state) with the flow's static
// interactive/auto_approve config and two on-disk runtime flags
// (autonomous.flag presence, any <phase>.dialog.jsonl presence) into one
// slice ordered by topoOrder(rs.StageOrder, dependsOn) — a display-only
// reordering. rs.StageOrder itself (the authoritative declaration order used
// by state/scheduling) is never touched. Replaces handleStatus's previous
// five-parallel-map construction. stageCosts is the per-stage slice of a
// single accounting.CostBundle.Stages map (nil when accounting isn't wired
// up) — handleStatus calls CostSnapshot() once per request and passes the
// same bundle here and into the run-level statusResponse fields.
func buildStageViews(rs state.RunState, runDir string, stageInteractive, stageAutoApprove, stageIsScript map[string]bool, dependsOn map[string][]string, stageButtons map[string][]string, stageCosts map[string]*accounting.CostView) []StageView {
	order := topoOrder(rs.StageOrder, dependsOn)
	views := make([]StageView, 0, len(order))
	verifyByStage := latestVerifyByStage(runDir)
	for _, id := range order {
		st := rs.Stages[id]
		autonomous := stageIsAutonomous(runDir, id)
		hasDialog := stageHasDialog(runDir, id)
		interactive := stageInteractive[id]
		isScript := stageIsScript[id]
		// autonomous-стадии обычно вообще не имеют plan.md и никогда не доходят
		// до статусов, для которых нужна панель плана — кроме failed (retry) и
		// paused (Continue): обе требуют кнопки действия, которая живёт в
		// PlanPanel, а не в DialogChannel. Pending ещё не начиналась и не имеет
		// пользовательского артефакта для просмотра; script-стадии не имеют
		// plan.md вообще. Для failed/paused PlanPanel всё равно нужен ради Retry/
		// Continue, включая autonomous и script-треки.
		showPlan := (st.Status != state.StatusPending && !isScript && !autonomous) ||
			st.Status == state.StatusFailed || st.Status == state.StatusPaused
		// ShowDialog резервирует строку под DialogChannel в лейауте. Условие ДОЛЖНО
		// совпадать с внутренним гейтом hasContent самого DialogChannel.tsx
		// (entries>0 || awaiting_user_input || hasDialog) — иначе для стадии, где
		// панель зарезервирована, но рендерить нечего (interactive/autonomous БЕЗ
		// диалоговой истории и не в awaiting_user_input), DialogChannel возвращает
		// <></>, и строка остаётся пустой дырой во всю высоту центральной колонки
		// (баг «ломается при клике на стадию»: клик по завершённой autonomous-стадии
		// показывал пустой центр без even fallback «Nothing to show»). hasDialog
		// покрывает авто-ответы/историю, awaiting_user_input — живой вопрос ещё до
		// появления <phase>.dialog.jsonl (и гонку «status обогнал fetch entries»).
		// interactive/autonomous сами по себе панель БОЛЬШЕ не открывают —
		// раньше это и порождало пустую дыру.
		showDialog := hasDialog || st.Status == state.StatusAwaitingUserInput
		pausedFrom := state.StageStatus("")
		if st.Status == state.StatusPaused {
			pausedFrom = st.PausedFrom
		}

		views = append(views, StageView{
			ID:          id,
			Name:        rs.StageNames[id],
			Status:      st.Status,
			UpdatedAt:   st.UpdatedAt,
			Interactive: interactive,
			Autonomous:  autonomous,
			AutoApprove: stageAutoApprove[id],
			HasDialog:   hasDialog,
			IsScript:    isScript,
			PausedFrom:  pausedFrom,
			ShowPlan:    showPlan,
			ShowDialog:  showDialog,
			PreNote:     state.LoadPreNote(filepath.Join(runDir, id)),
			Buttons:     stageButtons[id],
			Cost:        stageCosts[id],
			Verify:      verifyByStage[id],
		})
	}
	return views
}

// topoOrder returns ids reordered so every stage renders after all of its
// depends_on — a display-only concern the dashboard's stage list uses instead
// of raw flow.yaml declaration order. Stages with no ordering constraint
// between them (independent stages, or several unblocked by the same
// dependency) keep their original relative order: this is a stable Kahn's
// algorithm, queue seeded and re-fed in declaration order, so e.g. stage1
// (depends_on: [stage2]) among stage2..stage5 (no deps) renders as
// stage2, stage3, stage4, stage5, stage1 — not spliced ahead of its
// unrelated siblings just because it was declared first.
//
// flow.ParseFile's detectCycles already guarantees ids/dependsOn form a
// well-formed acyclic graph referencing only known stage ids by the time this
// runs; the length check below is a defensive fallback (return ids as-is)
// for that invariant, not a real code path.
func topoOrder(ids []string, dependsOn map[string][]string) []string {
	index := make(map[string]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}

	indegree := make(map[string]int, len(ids))
	dependents := make(map[string][]string, len(ids))
	for _, id := range ids {
		for _, dep := range dependsOn[id] {
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
		if _, err := os.Stat(filepath.Join(runDir, stageID, string(p)+".dialog.jsonl")); err == nil {
			return true
		}
	}
	return false
}

// verifyNotice is the on-disk shape of one notices.jsonl line, decoded just
// enough to pick out verify_started/verify_result entries (mirrors
// stagefiles.AppendNotice's noticeEntry — "time"/"type"/"stage_id"/"data").
type verifyNotice struct {
	Time    time.Time      `json:"time"`
	Type    string         `json:"type"`
	StageID string         `json:"stage_id"`
	Data    map[string]any `json:"data"`
}

// latestVerifyByStage scans the WHOLE notices.jsonl ONCE and returns, per
// stage id, only its single latest verify_started/verify_result notice
// (O(distinct stages) memory, unbounded lookback) — a DEDICATED single-pass
// scan, deliberately not reconstructNotices, which is capped at
// maxStageReplayEvents=200 and would let the badge age out on a long run
// after 200+ later same-stage notices (the exact eviction class this whole
// feature exists to kill). Prior to this, buildStageViews called a per-stage
// scan (latestVerifyForStage) once per stage, making /api/status rescan the
// entire file O(stages) times per poll — this collapses it to one scan
// regardless of stage count.
//
// "Latest" (per stage) is selected by timestamp, with file order as the
// tie-break: stagefiles.AppendNotice stamps time.Now() right before writing,
// so concurrent writers can land slightly out of timestamp order in the
// file — picking max-by-timestamp (ties won by whichever is scanned later)
// keeps parity with reconstructEventHistory's own timestamp sort.
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

// verifyViewFromNotice reduces one selected verify_started/verify_result
// notice into the VerifyView the dashboard renders: verify_started ⇒
// "running"; verify_result ⇒ its verdict (pass/needs_changes/inconclusive),
// or "error" for anything else.
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
	case VerifyPhasePass, VerifyPhaseNeedsChanges, VerifyPhaseInconclusive:
		view.Phase = verdict
	default:
		view.Phase = VerifyPhaseError
	}
	return view
}

// VerifyView.Phase values — named constants (instead of raw string literals
// repeated between the implementation and its tests within this package,
// which golangci-lint's goconst would otherwise flag) for the same vocabulary
// as the deleted client VerifyIndicatorPhase.
const (
	VerifyPhaseRunning      = "running"
	VerifyPhasePass         = "pass"
	VerifyPhaseNeedsChanges = "needs_changes"
	VerifyPhaseInconclusive = "inconclusive"
	VerifyPhaseError        = "error"
)

// noticeIntField/noticeStringField defensively read a notice's data map —
// json.Unmarshal decodes numbers as float64, so an int field must be
// converted, and a missing/wrong-typed key must not panic.
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
