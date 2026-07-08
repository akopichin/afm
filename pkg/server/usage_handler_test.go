package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
)

// stubAccountant — стаб/spy Accountant для тестов хендлера: фиксирует аргументы
// вызова Query и число вызовов, возвращает предзаготовленный ответ или ошибку.
// Удовлетворяет локальному интерфейсу Accountant структурно — как и конкретный
// *accounting.Accountant, что и проверяется контракт-тестом ниже.
type stubAccountant struct {
	calls  int
	metric string
	stage  string
	result []accounting.UsageAggregate
	err    error
}

func (s *stubAccountant) Query(metric string, stage string) ([]accounting.UsageAggregate, error) {
	s.calls++
	s.metric = metric
	s.stage = stage
	return s.result, s.err
}

// TestUsageHandler_HasContractSignature — контракт: UsageHandler принимает Accountant
// (локальный интерфейс, которому удовлетворяет *accounting.Accountant) и возвращает
// http.Handler. Должен компилироваться и быть вызываемым.
func TestUsageHandler_HasContractSignature(t *testing.T) {
	var acc Accountant = &stubAccountant{} // *accounting.Accountant подошёл бы так же
	var handler = UsageHandler(acc)
	if handler == nil {
		t.Fatal("UsageHandler returned nil handler")
	}
}

// TestUsageHandler_ReturnsAggregatesAsJSON — успешный путь: 200 + JSON-массив
// UsageAggregate, агрегаты передаются как есть, Query вызывается ровно один раз с
// разобранными query-параметрами.
func TestUsageHandler_ReturnsAggregatesAsJSON(t *testing.T) {
	want := []accounting.UsageAggregate{
		{StageID: "design", TimeBucket: "2026-07-07T10:00:00Z", Metric: "tokens", Value: 1200},
	}
	acc := &stubAccountant{result: want}
	handler := UsageHandler(acc)

	req := httptest.NewRequest(http.MethodGet, "/api/usage?metric=tokens&stage=design", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if ctype := w.Header().Get("Content-Type"); ctype != "application/json" {
		t.Fatalf("content-type: got %q, want application/json", ctype)
	}
	var got []accounting.UsageAggregate
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, w.Body.String())
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("body: got %+v, want %+v", got, want)
	}
	if acc.calls != 1 {
		t.Fatalf("Query call count: got %d, want 1", acc.calls)
	}
	if acc.metric != "tokens" || acc.stage != "design" {
		t.Fatalf("Query args: got metric=%q stage=%q, want tokens/design", acc.metric, acc.stage)
	}
}

// TestUsageHandler_RejectsInvalidMetric — неизвестная метрика → 400, Query не
// вызывается ни разу (валидация строго до запроса).
func TestUsageHandler_RejectsInvalidMetric(t *testing.T) {
	acc := &stubAccountant{}
	handler := UsageHandler(acc)

	req := httptest.NewRequest(http.MethodGet, "/api/usage?metric=dollars", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
	if acc.calls != 0 {
		t.Fatalf("Query must not be called on invalid metric, got %d calls", acc.calls)
	}
}

// TestUsageHandler_DefaultsStageToAllAndSurfacesQueryError — без параметров metric
// и stage дефолтит metric=tokens, stage="" (не суррогат вроде "*" или "all"), а ошибка
// Query пробрасывается как 500.
func TestUsageHandler_DefaultsStageToAllAndSurfacesQueryError(t *testing.T) {
	acc := &stubAccountant{err: errors.New("boom")}
	handler := UsageHandler(acc)

	req := httptest.NewRequest(http.MethodGet, "/api/usage", nil) // ни metric, ни stage
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
	if acc.calls != 1 {
		t.Fatalf("Query call count: got %d, want 1", acc.calls)
	}
	if acc.metric != "tokens" {
		t.Fatalf("default metric: got %q, want tokens", acc.metric)
	}
	if acc.stage != "" {
		t.Fatalf("default stage: got %q, want empty (all-stages sentinel, not %q)", acc.stage, acc.stage)
	}
}

// TestUsageHandler_EmptyResultIsJSONArrayNotError — metric=cost без настроенного
// pricing: пустой (не nil) результат кодируется как JSON-массив "[]", а не как null
// и не как ошибка. От этого зависит логика скрытия cost-тоггла на дашборде.
func TestUsageHandler_EmptyResultIsJSONArrayNotError(t *testing.T) {
	acc := &stubAccountant{result: []accounting.UsageAggregate{}}
	handler := UsageHandler(acc)

	req := httptest.NewRequest(http.MethodGet, "/api/usage?metric=cost", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (empty cost result is not an error)", w.Code)
	}
	if body := w.Body.String(); body != "[]\n" {
		t.Fatalf("body: got %q, want \"[]\\n\" (array, not null)", body)
	}
}
