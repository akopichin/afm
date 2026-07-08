package accounting_test

import (
	"math"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/proxy"
)

// TestAggregateSignature — контрактный тест: Aggregate существует с объявленной
// сигнатурой Aggregate([]proxy.UsageRecord, []StageWindow, []ResultUsage,
// config.PricingConfig, string, string, int) []UsageAggregate, а UsageAggregate
// имеет четыре объявленных поля. Ожидается провал до создания aggregate.go:
// пустые входы должны давать не-nil пустой срез (nil зарезервирован для ошибки).
func TestAggregateSignature(t *testing.T) {
	aggs := accounting.Aggregate(nil, nil, nil, config.PricingConfig{}, "tokens", "", 5)
	if aggs == nil {
		t.Fatal("Aggregate returned nil for empty inputs; want non-nil empty slice")
	}

	ua := accounting.UsageAggregate{
		StageID:    "design",
		TimeBucket: "2026-07-07T10:00:00Z",
		Metric:     "tokens",
		Value:      1,
	}
	if ua.StageID == "" || ua.TimeBucket == "" || ua.Metric == "" || ua.Value != 1 {
		t.Fatalf("UsageAggregate fields not populated: %+v", ua)
	}
}

// TestAggregateCostMetricUsesDeriveCost — Applied Fix: ветка cost реально зовёт
// DeriveCost, а не оставляет Value пустым. pricing={"claude-sonnet-5":{3.0,15.0,
// 0.3}}, окно design [10:00,) (открытое), запись в 10:01 с InputTokens=1000,
// OutputTokens=200 → Value=0.006 (inputCost=0.003 + outputCost=0.003).
func TestAggregateCostMetricUsesDeriveCost(t *testing.T) {
	pricing := config.PricingConfig{Models: map[string]config.ModelPricing{
		"claude-sonnet-5": {InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3},
	}}
	windows := []accounting.StageWindow{{StageID: "design", Start: "2026-07-07T10:00:00Z", End: ""}}
	records := []proxy.UsageRecord{{
		Timestamp:    mustTime(t, "2026-07-07T10:01:00Z"),
		Model:        "claude-sonnet-5",
		InputTokens:  1000,
		OutputTokens: 200,
	}}

	aggs := accounting.Aggregate(records, windows, nil, pricing, "cost", "", 5)
	if len(aggs) != 1 {
		t.Fatalf("len(aggregates) = %d, want 1", len(aggs))
	}
	want := accounting.UsageAggregate{
		StageID:    "design",
		TimeBucket: "2026-07-07T10:00:00Z",
		Metric:     "cost",
		Value:      0.006,
	}
	got := aggs[0]
	if got.StageID != want.StageID || got.TimeBucket != want.TimeBucket || got.Metric != want.Metric {
		t.Errorf("aggregate = %+v, want stage/bucket/metric %+v", got, want)
	}
	if math.Abs(got.Value-want.Value) > 1e-9 {
		t.Errorf("Value = %v, want %v (DeriveCost must drive the cost branch)", got.Value, want.Value)
	}
}

// TestAggregateCostMetricSkipsRecordsWithUnknownModelPricing — pricing настроен
// только под другую модель; запись с непрайсовой моделью пропускается целиком, а не
// попадает в агрегаты с нулевой стоимостью.
func TestAggregateCostMetricSkipsRecordsWithUnknownModelPricing(t *testing.T) {
	pricing := config.PricingConfig{Models: map[string]config.ModelPricing{
		"other-model": {InputPerMtok: 1, OutputPerMtok: 1, CachePerMtok: 1},
	}}
	windows := []accounting.StageWindow{{StageID: "design", Start: "2026-07-07T10:00:00Z", End: ""}}
	records := []proxy.UsageRecord{{
		Timestamp:   mustTime(t, "2026-07-07T10:01:00Z"),
		Model:       "claude-sonnet-5",
		InputTokens: 1000,
	}}

	aggs := accounting.Aggregate(records, windows, nil, pricing, "cost", "", 5)
	if len(aggs) != 0 {
		t.Errorf("aggregates = %+v, want empty (unpriced model must be skipped, not zero-rated)", aggs)
	}
}

// TestAggregateDoesNotDoubleCountProxyAndFallback — стадия с прокси-записью НЕ
// получает additionally result-usage фолбэк: Value = только прокси-токены
// (1200), а не 1200+2600. Это ключевое правило шага 3, проверяемое здесь на
// уровне самого Aggregate (а не только в интеграционном тесте Task 10).
func TestAggregateDoesNotDoubleCountProxyAndFallback(t *testing.T) {
	windows := []accounting.StageWindow{{StageID: "design", Start: "2026-07-07T10:00:00Z", End: ""}}
	records := []proxy.UsageRecord{{
		Timestamp:    mustTime(t, "2026-07-07T10:01:00Z"),
		Model:        "claude-sonnet-5",
		InputTokens:  1000,
		OutputTokens: 200,
	}}
	resultUsages := []accounting.ResultUsage{{
		StageID:      "design",
		InputTokens:  2000,
		OutputTokens: 400,
	}}

	aggs := accounting.Aggregate(records, windows, resultUsages, config.PricingConfig{}, "tokens", "", 5)
	if len(aggs) != 1 {
		t.Fatalf("len(aggregates) = %d, want 1", len(aggs))
	}
	const want = 1200.0
	if math.Abs(aggs[0].Value-want) > 1e-9 {
		t.Errorf("Value = %v, want %v (proxy record present → no fallback double-count)", aggs[0].Value, want)
	}
}

// TestAggregateTokensAndKbMetrics — компаньоны к cost-тесту, покрывают оставшиеся
// две метрики: tokens = сумма четырёх токеновых полей (две записи в одном бакете
// одновременно проверяют группировку-суммирование шага 6); kb =
// (RequestBytes+ResponseBytes)/1024.
func TestAggregateTokensAndKbMetrics(t *testing.T) {
	windows := []accounting.StageWindow{{StageID: "design", Start: "2026-07-07T10:00:00Z", End: ""}}
	records := []proxy.UsageRecord{
		{
			Timestamp:           mustTime(t, "2026-07-07T10:01:00Z"),
			Model:               "claude-sonnet-5",
			InputTokens:         1000,
			OutputTokens:        200,
			CacheCreationTokens: 50,
			CacheReadTokens:     30,
			RequestBytes:        2048,
			ResponseBytes:       6144,
		},
		{
			Timestamp:   mustTime(t, "2026-07-07T10:03:00Z"),
			Model:       "claude-sonnet-5",
			InputTokens: 100,
		},
	}

	// tokens = (1000+200+50+30) + (100) = 1380, оба в бакете 10:00 → одна строка.
	tokAggs := accounting.Aggregate(records, windows, nil, config.PricingConfig{}, "tokens", "", 5)
	if len(tokAggs) != 1 {
		t.Fatalf("tokens: len(aggregates) = %d, want 1 (same-bucket grouping)", len(tokAggs))
	}
	if math.Abs(tokAggs[0].Value-1380) > 1e-9 {
		t.Errorf("tokens Value = %v, want 1380", tokAggs[0].Value)
	}
	if tokAggs[0].TimeBucket != "2026-07-07T10:00:00Z" {
		t.Errorf("tokens TimeBucket = %q, want 2026-07-07T10:00:00Z", tokAggs[0].TimeBucket)
	}

	// kb = (2048 + 6144) / 1024 = 8 (вторая запись не вносит байт).
	kbAggs := accounting.Aggregate(records, windows, nil, config.PricingConfig{}, "kb", "", 5)
	if len(kbAggs) != 1 {
		t.Fatalf("kb: len(aggregates) = %d, want 1", len(kbAggs))
	}
	if math.Abs(kbAggs[0].Value-8) > 1e-9 {
		t.Errorf("kb Value = %v, want 8", kbAggs[0].Value)
	}
}

// TestAggregateTokensFallbackForStageWithoutProxyRecords — стадия без прокси-записей
// (прокси неактивен в ране) берёт токены из result-usage как фолбэк. TimeBucket —
// это Start окна стейджа (у фолбэка нет собственного Timestamp). Положительная
// ветка шага 3, не покрытая тестом на двойной счёт.
func TestAggregateTokensFallbackForStageWithoutProxyRecords(t *testing.T) {
	windows := []accounting.StageWindow{{StageID: "build", Start: "2026-07-07T11:00:00Z", End: "2026-07-07T11:10:00Z"}}
	resultUsages := []accounting.ResultUsage{{
		StageID:      "build",
		InputTokens:  2000,
		OutputTokens: 400,
	}}

	aggs := accounting.Aggregate(nil, windows, resultUsages, config.PricingConfig{}, "tokens", "", 5)
	if len(aggs) != 1 {
		t.Fatalf("len(aggregates) = %d, want 1 (fallback applied for proxy-less stage)", len(aggs))
	}
	want := accounting.UsageAggregate{
		StageID:    "build",
		TimeBucket: "2026-07-07T11:00:00Z",
		Metric:     "tokens",
		Value:      2400,
	}
	got := aggs[0]
	if got.StageID != want.StageID || got.TimeBucket != want.TimeBucket || got.Metric != want.Metric {
		t.Errorf("aggregate = %+v, want %+v", got, want)
	}
	if math.Abs(got.Value-want.Value) > 1e-9 {
		t.Errorf("Value = %v, want %v (fallback must substitute result-usage tokens)", got.Value, want.Value)
	}
}

// TestAggregateStageFilter — непустой stage оставляет только записи этой стадии;
// записи другой стадии отбрасываются.
func TestAggregateStageFilter(t *testing.T) {
	// Два непересекающихся окна — иначе обе записи попали бы в оба окна и были бы
	// отброшены AttributeStage как неоднозначные (ok=false).
	windows := []accounting.StageWindow{
		{StageID: "design", Start: "2026-07-07T10:00:00Z", End: "2026-07-07T10:05:00Z"},
		{StageID: "build", Start: "2026-07-07T10:05:00Z", End: "2026-07-07T10:10:00Z"},
	}
	records := []proxy.UsageRecord{
		{Timestamp: mustTime(t, "2026-07-07T10:01:00Z"), Model: "m", InputTokens: 100}, // design
		{Timestamp: mustTime(t, "2026-07-07T10:06:00Z"), Model: "m", InputTokens: 200}, // build
	}

	aggs := accounting.Aggregate(records, windows, nil, config.PricingConfig{}, "tokens", "build", 5)
	if len(aggs) != 1 {
		t.Fatalf("len(aggregates) = %d, want 1 (stage filter)", len(aggs))
	}
	if aggs[0].StageID != "build" {
		t.Errorf("StageID = %q, want build (filtered)", aggs[0].StageID)
	}
	if math.Abs(aggs[0].Value-200) > 1e-9 {
		t.Errorf("Value = %v, want 200 (only the build record)", aggs[0].Value)
	}
}
