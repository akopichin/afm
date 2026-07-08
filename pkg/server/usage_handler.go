package server

import (
	"encoding/json"
	"net/http"

	"github.com/akopichin/afm/pkg/accounting"
)

// Допустимые значения query-параметра metric. Константы, а не литералы: каждое
// значение встречается больше одного раза (дефолт + валидация), что ловит goconst.
// Совпадают по смыслу с metricTokens/metricCost/metricKB в pkg/accounting/aggregate.go,
// но это отдельный слой (HTTP-вход) — поэтому продублированы локально.
const (
	usageMetricTokens = "tokens"
	usageMetricCost   = "cost"
	usageMetricKB     = "kb"
)

// Accountant — зависимость хендлера /api/usage от слоя учёта потребления. Локальный
// интерфейс, а не конкретный *accounting.Accountant: позволяет тестировать хендлер со
// стабом/шпионом, а реальный *accounting.Accountant удовлетворяет ему структурно
// (Go duck typing). Совпадает по имени с типом в CODEMANIFEST-сигнатуре
// UsageHandler(accountant Accountant) и полем Config.Accountant.
type Accountant interface {
	Query(metric string, stage string) ([]accounting.UsageAggregate, error)
}

// UsageHandler строит HTTP-обработчик GET /api/usage?metric=tokens|cost|kb&stage=<id>.
//
// Algorithm:
//  1. metric из query (дефолт "tokens"), stage из query (дефолт "" = все стадии).
//  2. metric ∈ {tokens, cost, kb}; иначе — 400, accountant.Query НЕ вызывается.
//  3. aggregates, err := accountant.Query(metric, stage).
//  4. err != nil → 500; иначе → 200 и JSON-массив UsageAggregate.
//
// metric=cost без настроенного pricing возвращает пустой массив (200, не ошибка):
// Accountant.Query для этого случая возвращает пустой (не nil) срез без ошибки, и
// хендлер форвардит его как "[]" — на это опирается дашборд при скрытии cost-тоггла.
func UsageHandler(accountant Accountant) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metric := r.URL.Query().Get("metric")
		if metric == "" {
			metric = usageMetricTokens
		}
		stage := r.URL.Query().Get("stage") // пусто = все стадии

		// Валидация строго до Query: неизвестная метрика не доходит до Accountant.
		switch metric {
		case usageMetricTokens, usageMetricCost, usageMetricKB:
		default:
			http.Error(w, "invalid metric", http.StatusBadRequest)
			return
		}

		aggregates, err := accountant.Query(metric, stage)
		if err != nil {
			http.Error(w, "usage query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Гарантируем wire-контракт "200 []" даже если слой учёта вернёт nil: пустой
		// cost-ответ должен кодироваться массивом, а не null — иначе дашборд не отличит
		// «pricing не настроен» от ошибки.
		if aggregates == nil {
			aggregates = []accounting.UsageAggregate{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(aggregates)
	})
}
