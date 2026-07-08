package accounting_test

import (
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/proxy"
	"github.com/akopichin/afm/pkg/state"
)

// fakeStore реализует accounting.Store (структурно удовлетворяет *state.Store)
// только для тестов атрибуции: возвращает заданную историю/снапшот/ошибку.
type fakeStore struct {
	history  []state.Transition
	histErr  error
	snapshot state.RunState
}

func (f fakeStore) History() ([]state.Transition, error) { return f.history, f.histErr }
func (f fakeStore) Snapshot() state.RunState             { return f.snapshot }

// mustTime разбирает RFC3339-строку или падает в тесте.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("mustTime(%q): %v", s, err)
	}
	return tt
}

// TestAttributionSignature — контрактный тест: StageWindow имеет три строковых
// поля (StageID, Start, End), LoadStageWindows и AttributeStage компилируются с
// объявленными сигнатурами. Ожидается провал до создания attribution.go.
func TestAttributionSignature(t *testing.T) {
	w := accounting.StageWindow{
		StageID: "design",
		Start:   "2026-07-07T10:00:00Z",
		End:     "2026-07-07T10:05:00Z",
	}
	if w.StageID == "" || w.Start == "" {
		t.Fatalf("StageWindow fields not populated: %+v", w)
	}

	store := fakeStore{}
	windows, err := accounting.LoadStageWindows(store)
	if err != nil {
		t.Fatalf("LoadStageWindows: %v", err)
	}
	if windows == nil {
		t.Fatal("LoadStageWindows returned nil windows for empty history")
	}

	stageID, ok := accounting.AttributeStage(proxy.UsageRecord{}, []accounting.StageWindow{w})
	_ = stageID
	_ = ok
}

// TestLoadStageWindowsPairsRunningWithTerminal — design: pending→running@10:00,
// running→done@10:05 → одно окно [10:00,10:05].
func TestLoadStageWindowsPairsRunningWithTerminal(t *testing.T) {
	store := fakeStore{history: []state.Transition{
		{Seq: 1, Time: mustTime(t, "2026-07-07T10:00:00Z"), StageID: "design", From: state.StatusPending, To: state.StatusRunning},
		{Seq: 2, Time: mustTime(t, "2026-07-07T10:05:00Z"), StageID: "design", From: state.StatusRunning, To: state.StatusDone},
	}}

	windows, err := accounting.LoadStageWindows(store)
	if err != nil {
		t.Fatalf("LoadStageWindows: %v", err)
	}
	want := []accounting.StageWindow{{StageID: "design", Start: "2026-07-07T10:00:00Z", End: "2026-07-07T10:05:00Z"}}
	if len(windows) != 1 || windows[0] != want[0] {
		t.Errorf("windows = %+v, want %+v", windows, want)
	}
}

// TestLoadStageWindowsOpenEndedForStillRunningStage — единственный переход в
// running без терминального → End="".
func TestLoadStageWindowsOpenEndedForStillRunningStage(t *testing.T) {
	store := fakeStore{history: []state.Transition{
		{Seq: 1, Time: mustTime(t, "2026-07-07T10:00:00Z"), StageID: "design", From: state.StatusPending, To: state.StatusRunning},
	}}

	windows, err := accounting.LoadStageWindows(store)
	if err != nil {
		t.Fatalf("LoadStageWindows: %v", err)
	}
	if len(windows) != 1 {
		t.Fatalf("windows len = %d, want 1", len(windows))
	}
	if windows[0].End != "" {
		t.Errorf("End = %q, want empty (still running)", windows[0].End)
	}
}

// TestLoadStageWindowsOpensOnPlanningLifecycle — реальный жизненный цикл
// интерактивного planning-стейджа: pending→planning→awaiting_user_input→planning→done.
// Стейдж никогда не доходит до running, но обязан дать одно окно [10:00,10:09].
// Регрессия: ранее окно открывалось только на To==running → для интерактивных
// флоу окон не строилось, и /api/usage возвращал пустой массив.
func TestLoadStageWindowsOpensOnPlanningLifecycle(t *testing.T) {
	store := fakeStore{history: []state.Transition{
		{Seq: 1, Time: mustTime(t, "2026-07-07T10:00:00Z"), StageID: "init", From: state.StatusPending, To: state.StatusPlanning},
		{Seq: 2, Time: mustTime(t, "2026-07-07T10:03:00Z"), StageID: "init", From: state.StatusPlanning, To: state.StatusAwaitingUserInput},
		{Seq: 3, Time: mustTime(t, "2026-07-07T10:05:00Z"), StageID: "init", From: state.StatusAwaitingUserInput, To: state.StatusPlanning},
		{Seq: 4, Time: mustTime(t, "2026-07-07T10:09:00Z"), StageID: "init", From: state.StatusPlanning, To: state.StatusDone},
	}}

	windows, err := accounting.LoadStageWindows(store)
	if err != nil {
		t.Fatalf("LoadStageWindows: %v", err)
	}
	want := []accounting.StageWindow{{StageID: "init", Start: "2026-07-07T10:00:00Z", End: "2026-07-07T10:09:00Z"}}
	if len(windows) != 1 || windows[0] != want[0] {
		t.Errorf("windows = %+v, want %+v", windows, want)
	}
}

// TestLoadStageWindowsPropagatesHistoryError — ошибка History пробрасывается
// без сокрытия, windows == nil.
func TestLoadStageWindowsPropagatesHistoryError(t *testing.T) {
	store := fakeStore{histErr: errors.New("events.jsonl: corrupt")}

	windows, err := accounting.LoadStageWindows(store)
	if err == nil {
		t.Fatal("expected error from History, got nil")
	}
	if windows != nil {
		t.Errorf("windows = %+v, want nil on History error", windows)
	}
}

// TestAttributeStageMatchesSingleWindow — запись в 10:02 попадает в единственное
// окно [10:00,10:05) → design, ok=true.
func TestAttributeStageMatchesSingleWindow(t *testing.T) {
	windows := []accounting.StageWindow{
		{StageID: "design", Start: "2026-07-07T10:00:00Z", End: "2026-07-07T10:05:00Z"},
	}
	record := proxy.UsageRecord{Timestamp: mustTime(t, "2026-07-07T10:02:00Z")}

	stageID, ok := accounting.AttributeStage(record, windows)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if stageID != "design" {
		t.Errorf("stageID = %q, want design", stageID)
	}
}

// TestAttributeStageAmbiguousOverlapReturnsNotOk — два перекрывающихся окна a и b
// [10:00,10:10), запись в 10:05 попадает в оба → ok=false.
func TestAttributeStageAmbiguousOverlapReturnsNotOk(t *testing.T) {
	windows := []accounting.StageWindow{
		{StageID: "a", Start: "2026-07-07T10:00:00Z", End: "2026-07-07T10:10:00Z"},
		{StageID: "b", Start: "2026-07-07T10:00:00Z", End: "2026-07-07T10:10:00Z"},
	}
	record := proxy.UsageRecord{Timestamp: mustTime(t, "2026-07-07T10:05:00Z")}

	stageID, ok := accounting.AttributeStage(record, windows)
	if ok {
		t.Fatal("ok = true, want false (ambiguous overlap)")
	}
	if stageID != "" {
		t.Errorf("stageID = %q, want empty on ambiguity", stageID)
	}
}
