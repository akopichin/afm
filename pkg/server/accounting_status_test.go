package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// stubCostProvider — минимальная тестовая реализация accounting.CostProvider:
// CostSnapshot просто возвращает заранее заданный bundle, без реального
// Store/Ledger (см. бриф: "Use a tiny in-test stub ... do NOT spin a real
// Store unless trivial").
type stubCostProvider struct {
	bundle accounting.CostBundle
}

func (p stubCostProvider) CostSnapshot() accounting.CostBundle { return p.bundle }

// newTestServerWithAccounting — как setupTestServerWithWS, но с явно заданным
// Config.Accounting (nil-провайдер тестируется через обычный setupTestServer).
func newTestServerWithAccounting(t *testing.T, provider accounting.CostProvider) *Server {
	t.Helper()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		RunDir:     runDir,
		Store:      store,
		UIBus:      bus.NewUIBus(),
		Actions:    fakeStageActions{},
		Accounting: provider,
	})
	return srv
}

func decodeStatusMap(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode status as map: %v", err)
	}
	return m
}

// TestHandleStatus_AccountingFieldsOmittedWhenProviderNil — сервер без
// подключённого accounting (Config.Accounting оставлен нулевым, как в
// setupTestServer) не должен вообще упоминать accounting-поля в JSON —
// это "не поддерживается", отличное от подключённого, но нерабочего
// провайдера (StaticUnavailable).
func TestHandleStatus_AccountingFieldsOmittedWhenProviderNil(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	m := decodeStatusMap(t, w)
	for _, key := range []string{"run_cost", "run_overhead_cost", "coverage_issues", "accounting"} {
		if _, ok := m[key]; ok {
			t.Errorf("key %q should be omitted when accounting provider is nil, got %s", key, m[key])
		}
	}
}

// TestHandleStatus_AccountingFieldsPresentWhenProviderSet — с подключённым
// провайдером все четыре поля появляются в JSON и несут значения bundle.
func TestHandleStatus_AccountingFieldsPresentWhenProviderSet(t *testing.T) {
	bundle := accounting.CostBundle{
		Health:   accounting.HealthOK,
		HasData:  true,
		Run:      &accounting.CostView{DisplayCost: "$1.23", Coverage: "full"},
		Overhead: &accounting.CostView{DisplayCost: "$0.05", Coverage: "full"},
		Issues: []accounting.CoverageIssue{
			{Kind: accounting.IssueKindUnpriced, Attribution: accounting.Attribution{Kind: accounting.AttrStage, StageID: testStageID}, Count: 1},
		},
	}
	srv := newTestServerWithAccounting(t, stubCostProvider{bundle: bundle})

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RunCost == nil || resp.RunCost.DisplayCost != "$1.23" {
		t.Errorf("RunCost = %+v, want DisplayCost=$1.23", resp.RunCost)
	}
	if resp.RunOverheadCost == nil || resp.RunOverheadCost.DisplayCost != "$0.05" {
		t.Errorf("RunOverheadCost = %+v, want DisplayCost=$0.05", resp.RunOverheadCost)
	}
	if len(resp.CoverageIssues) != 1 || resp.CoverageIssues[0].Kind != accounting.IssueKindUnpriced {
		t.Errorf("CoverageIssues = %+v, want one unpriced issue", resp.CoverageIssues)
	}
	if resp.Accounting == nil || resp.Accounting.Health != string(accounting.HealthOK) || !resp.Accounting.HasData {
		t.Errorf("Accounting = %+v, want {health:ok has_data:true}", resp.Accounting)
	}
}

// TestHandleStatus_ShowMoney — флаг Server.showMoney протаскивается в
// accounting.show_money ответа /api/status. По умолчанию (не задан) — false,
// поэтому дашборд прячет деньги; явный true — показывает.
func TestHandleStatus_ShowMoney(t *testing.T) {
	bundle := accounting.CostBundle{Health: accounting.HealthOK, HasData: true}

	t.Run("default false", func(t *testing.T) {
		srv := newTestServerWithAccounting(t, stubCostProvider{bundle: bundle})
		req := httptest.NewRequest("GET", "/api/status", nil)
		w := httptest.NewRecorder()
		srv.handleStatus(w, req)
		var resp statusResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Accounting == nil || resp.Accounting.ShowMoney {
			t.Errorf("Accounting = %+v, want show_money=false by default", resp.Accounting)
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		runDir := t.TempDir()
		store, err := state.Open(runDir, []string{testStageID})
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { store.Close() })
		srv := New(Config{
			RunDir:     runDir,
			Store:      store,
			UIBus:      bus.NewUIBus(),
			Actions:    fakeStageActions{},
			Accounting: stubCostProvider{bundle: bundle},
			ShowMoney:  true,
		})
		req := httptest.NewRequest("GET", "/api/status", nil)
		w := httptest.NewRecorder()
		srv.handleStatus(w, req)
		var resp statusResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Accounting == nil || !resp.Accounting.ShowMoney {
			t.Errorf("Accounting = %+v, want show_money=true", resp.Accounting)
		}
	})
}

// TestHandleStatus_StaticUnavailableProvider — accounting.StaticUnavailable()
// (открытие Store'а провалилось на хосте) отдаёт health:"unavailable",
// has_data:false — но, в отличие от nil-провайдера, поле accounting
// ПРИСУТСТВУЕТ в JSON (nil vs static-unavailable — разные вещи, см. бриф).
func TestHandleStatus_StaticUnavailableProvider(t *testing.T) {
	srv := newTestServerWithAccounting(t, accounting.StaticUnavailable())

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	m := decodeStatusMap(t, w)
	if _, ok := m["accounting"]; !ok {
		t.Fatal("accounting key should be present for a static-unavailable provider")
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Accounting == nil || resp.Accounting.Health != string(accounting.HealthUnavailable) || resp.Accounting.HasData {
		t.Errorf("Accounting = %+v, want {health:unavailable has_data:false}", resp.Accounting)
	}
	if resp.RunCost != nil || resp.RunOverheadCost != nil || resp.CoverageIssues != nil {
		t.Errorf("static-unavailable bundle has no Run/Overhead/Issues, got RunCost=%+v RunOverheadCost=%+v CoverageIssues=%+v",
			resp.RunCost, resp.RunOverheadCost, resp.CoverageIssues)
	}
}

// TestHandleStatus_HealthOrthogonalToCoverage — регрессия на смешивание двух
// независимых осей: Health описывает сам Store (можно ли ему верить), а
// Coverage — насколько ПОЛНО оценена конкретная группа записей. Здоровый
// провайдер с одной неоценённой (unpriced) записью — совершенно
// легитимное одновременное состояние: health:"ok" И coverage:"none". Сервер
// не должен ничего домысливать — просто пробрасывать bundle как есть.
func TestHandleStatus_HealthOrthogonalToCoverage(t *testing.T) {
	bundle := accounting.CostBundle{
		Health:  accounting.HealthOK,
		HasData: true,
		Run:     &accounting.CostView{DisplayCost: "$0.00", Coverage: "none"},
	}
	srv := newTestServerWithAccounting(t, stubCostProvider{bundle: bundle})

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Accounting == nil || resp.Accounting.Health != "ok" || !resp.Accounting.HasData {
		t.Errorf("Accounting = %+v, want {health:ok has_data:true}", resp.Accounting)
	}
	if resp.RunCost == nil || resp.RunCost.Coverage != "none" {
		t.Errorf("RunCost.Coverage = %+v, want %q", resp.RunCost, "none")
	}
}

// TestHandleStatus_CoverageIssuesAttributionJSON фиксирует JSON-литерал
// Attribution для всех трёх Kind — контракт с фронтом, который на них
// переключается (stage_id присутствует только для attribution:stage).
func TestHandleStatus_CoverageIssuesAttributionJSON(t *testing.T) {
	bundle := accounting.CostBundle{
		Health:  accounting.HealthOK,
		HasData: true,
		Issues: []accounting.CoverageIssue{
			{Kind: accounting.IssueKindUnpriced, Attribution: accounting.Attribution{Kind: accounting.AttrStage, StageID: testStageID}, Count: 1},
			{Kind: accounting.IssueKindUnmetered, Attribution: accounting.Attribution{Kind: accounting.AttrRunOverhead}, Count: 2},
			{Kind: accounting.IssueKindUnpriced, Attribution: accounting.Attribution{Kind: accounting.AttrUnknown}, Count: 3},
		},
	}
	srv := newTestServerWithAccounting(t, stubCostProvider{bundle: bundle})

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	m := decodeStatusMap(t, w)
	var issues []json.RawMessage
	if err := json.Unmarshal(m["coverage_issues"], &issues); err != nil {
		t.Fatalf("decode coverage_issues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("coverage_issues len = %d, want 3", len(issues))
	}

	wantAttrs := []string{
		`"attribution":{"kind":"stage","stage_id":"` + testStageID + `"}`,
		`"attribution":{"kind":"run_overhead"}`,
		`"attribution":{"kind":"unknown"}`,
	}
	for i, want := range wantAttrs {
		if got := string(issues[i]); !strings.Contains(got, want) {
			t.Errorf("issue[%d] = %s, want substring %s", i, got, want)
		}
	}
}
