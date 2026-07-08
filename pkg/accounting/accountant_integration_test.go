package accounting_test

import (
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/state"
)

// TestAccountantQueryCombinesAllThreeSources — единственный в пакете
// кросс-сущностный интеграционный тест: прогоняет полную оркестрацию
// Accountant.Query (LoadUsageRecords + LoadStageWindows + ReadResultUsage +
// Aggregate) против файлового ран-каталога и фейкового Store с реальной
// передачей аргументов. Изолированный модульный тест ни одной сущности не
// способен покрыть эту склейку.
//
// Ключевое проверяемое поведение (только здесь, end-to-end): стадия, у которой
// есть атрибутированная прокси-запись, НЕ должна дополнительно получать
// result-usage фолбэк — Value должно быть 1200 (только прокси-запись), а не
// 1200+2400 (прокси + фолбэк). Это правило «без двойного счёта», которое Aggregate
// применяет на уровне строк, но здесь проверяется насквозь через Query.
func TestAccountantQueryCombinesAllThreeSources(t *testing.T) {
	runDir := t.TempDir()

	// usage.jsonl: одна прокси-запись стадии design в 10:01 (внутри окна
	// выполнения 10:00–10:10), 1000 входных + 200 выходных = 1200 токенов.
	writeFile(t, runDir, "usage.jsonl",
		`{"timestamp":"2026-07-07T10:01:00Z","model":"claude-sonnet-5","inputTokens":1000,"outputTokens":200,"cacheCreationTokens":0,"cacheReadTokens":0,"requestBytes":2048,"responseBytes":4096}
`)

	// design/implementation.jsonl: терминальное result-событие для завершённой
	// фазы (2000 входных + 400 выходных = 2400 токенов). Так как у design уже
	// есть прокси-запись, этот фолбэк должен быть проигнорирован агрегатором.
	writeFile(t, filepath.Join(runDir, "design"), "implementation.jsonl",
		`{"type":"result","total_cost_usd":0.5,"usage":{"input_tokens":2000,"output_tokens":400,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"session_id":"sess-impl","duration_ms":1000}
`)

	// Store: design pending→running@10:00 → running→done@10:10 (одно окно
	// [10:00,10:10)); каталог стадий — только design (по нему Query ищет
	// <runDir>/design/{planning,implementation,review}.jsonl).
	store := fakeStore{
		history: []state.Transition{
			{Seq: 1, Time: mustTime(t, "2026-07-07T10:00:00Z"), StageID: "design", From: state.StatusPending, To: state.StatusRunning},
			{Seq: 2, Time: mustTime(t, "2026-07-07T10:10:00Z"), StageID: "design", From: state.StatusRunning, To: state.StatusDone},
		},
		snapshot: state.RunState{StageOrder: []string{"design"}},
	}

	acc := accounting.NewAccountant(runDir, store, config.PricingConfig{}, 5)
	aggs, err := acc.Query("tokens", "")
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}

	wantLen := 1
	if len(aggs) != wantLen {
		t.Fatalf("len(aggregates) = %d, want %d (no fallback double-count): %+v", len(aggs), wantLen, aggs)
	}

	want := accounting.UsageAggregate{
		StageID:    "design",
		TimeBucket: "2026-07-07T10:00:00Z",
		Metric:     "tokens",
		Value:      1200,
	}
	if aggs[0] != want {
		t.Errorf("aggregates[0] = %+v, want %+v (proxy record only — ResultUsage fallback must NOT be summed)",
			aggs[0], want)
	}
}
