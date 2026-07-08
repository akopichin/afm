package accounting

import (
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/proxy"
)

// Метрики потребления, принимаемые Aggregate. Константы, а не строковые литералы:
// каждое значение встречается больше одного раза (проверка фолбэка + switch в
// rowValue), что ловит goconst.
const (
	metricTokens = "tokens"
	metricCost   = "cost"
	metricKB     = "kb"
)

// UsageAggregate — одна строка агрегированного потребления: сумма метрики Metric
// по стейджу StageID в временном бакете TimeBucket (RFC3339, начало бакета).
//
// JSON-теги в camelCase — по конвенции проекта (как usage.jsonl): именно эти имена
// полей читает фронтенд из ответа GET /api/usage.
type UsageAggregate struct {
	StageID    string  `json:"stageId"`
	TimeBucket string  `json:"timeBucket"`
	Metric     string  `json:"metric"`
	Value      float64 `json:"value"`
}

// aggRow — единая метрико-нейтральная строка, по которой Aggregate группирует. К
// ней сводятся и прокси-записи, и фолбэк result-usage строки (метрика tokens,
// когда у стадии нет прокси-записей в этом ране).
type aggRow struct {
	stageID             string
	timestamp           time.Time // у фолбэка — Start окна стейджа.
	model               string    // пуст для фолбэка (cost для него неприменим).
	inputTokens         int
	outputTokens        int
	cacheCreationTokens int
	cacheReadTokens     int
	requestBytes        int
	responseBytes       int
}

// Aggregate сводит прокси-записи records (атрибутированные по окнам windows) и
// фолбэк result-usage строки resultUsages в срез UsageAggregate по запрошенной
// метрике metric и фильтру stage (пусто = все стадии).
//
// Алгоритм:
//  1. Каждая запись атрибутируется через AttributeStage; неатрибутированные
//     (ok=false) отбрасываются.
//  2. Применяется фильтр stage (пусто = все стадии).
//  3. Для стадий без прокси-записей resultUsages подставляются как токеновый
//     фолбэк (только для metric=tokens — у ResultUsage нет Model, cost для него не
//     разрешим). Стадия с прокси-записью фолбэк НЕ получает (без двойного счёта).
//  4. Считается значение метрики по каждой строке: tokens = сумма четырёх
//     токеновых полей; cost = DeriveCost после успешного GetModelPricing (иначе
//     строка пропускается); kb = (RequestBytes+ResponseBytes)/1024.
//  5. TimeBucket — Timestamp, округлённый вниз до границы bucketMinutes-минут
//     (от начала часа); для фолбэка — Start окна стейджа.
//  6. Группировка по (StageID, TimeBucket), значения суммируются в Value.
//
// TotalCostUsd из ResultUsage никогда не попадает в Value: cost считается только
// через PricingConfig/DeriveCost.
func Aggregate(
	records []proxy.UsageRecord,
	windows []StageWindow,
	resultUsages []ResultUsage,
	pricing config.PricingConfig,
	metric string,
	stage string,
	bucketMinutes int,
) []UsageAggregate {
	rows := make([]aggRow, 0, len(records)+len(resultUsages))
	stagesWithProxy := map[string]struct{}{}

	// Шаги 1-2: атрибуция прокси-записей + фильтр по stage.
	for _, rec := range records {
		stageID, ok := AttributeStage(rec, windows)
		if !ok {
			continue
		}
		if stage != "" && stageID != stage {
			continue
		}
		stagesWithProxy[stageID] = struct{}{}
		rows = append(rows, aggRow{
			stageID:             stageID,
			timestamp:           rec.Timestamp,
			model:               rec.Model,
			inputTokens:         rec.InputTokens,
			outputTokens:        rec.OutputTokens,
			cacheCreationTokens: rec.CacheCreationTokens,
			cacheReadTokens:     rec.CacheReadTokens,
			requestBytes:        rec.RequestBytes,
			responseBytes:       rec.ResponseBytes,
		})
	}

	// Шаг 3: фолбэк resultUsages для стадий без прокси-записей (только tokens).
	if metric == metricTokens {
		for _, ru := range resultUsages {
			if _, ok := stagesWithProxy[ru.StageID]; ok {
				continue // у стадии есть прокси-запись — фолбэк не добавляем.
			}
			if stage != "" && ru.StageID != stage {
				continue
			}
			rows = append(rows, aggRow{
				stageID:             ru.StageID,
				timestamp:           fallbackTimestamp(ru.StageID, windows),
				inputTokens:         ru.InputTokens,
				outputTokens:        ru.OutputTokens,
				cacheCreationTokens: ru.CacheCreationTokens,
				cacheReadTokens:     ru.CacheReadTokens,
			})
		}
	}

	// Шаги 4-6: значение метрики, бакет, группировка-суммирование.
	groups := map[aggregateKey]float64{}
	for _, row := range rows {
		value, ok := rowValue(metric, row, pricing)
		if !ok {
			continue
		}
		key := aggregateKey{stageID: row.stageID, bucket: bucketStart(row.timestamp, bucketMinutes)}
		groups[key] += value
	}

	aggregates := make([]UsageAggregate, 0, len(groups))
	for key, value := range groups {
		aggregates = append(aggregates, UsageAggregate{
			StageID:    key.stageID,
			TimeBucket: key.bucket,
			Metric:     metric,
			Value:      value,
		})
	}
	// Детерминированный порядок вывода (для стабильного API и воспроизводимых тестов).
	slices.SortFunc(aggregates, func(a, b UsageAggregate) int {
		switch {
		case a.StageID < b.StageID:
			return -1
		case a.StageID > b.StageID:
			return 1
		}
		switch {
		case a.TimeBucket < b.TimeBucket:
			return -1
		case a.TimeBucket > b.TimeBucket:
			return 1
		}
		return 0
	})
	return aggregates
}

// aggregateKey — составной ключ группировки (стейдж × временной бакет).
type aggregateKey struct {
	stageID string
	bucket  string
}

// rowValue считает значение метрики для одной строки. cost с моделью без
// настроенной цены (ok=false из GetModelPricing) и неизвестная метрика возвращают
// ok=false — такая строка пропускается (не входит в агрегаты ни с нулём, ни с чем).
func rowValue(metric string, row aggRow, pricing config.PricingConfig) (float64, bool) {
	switch metric {
	case metricTokens:
		return float64(row.inputTokens + row.outputTokens + row.cacheCreationTokens + row.cacheReadTokens), true
	case metricCost:
		p, ok := pricing.GetModelPricing(row.model)
		if !ok {
			return 0, false
		}
		return DeriveCost(row.inputTokens, row.outputTokens, row.cacheCreationTokens+row.cacheReadTokens, p), true
	case metricKB:
		return float64(row.requestBytes+row.responseBytes) / 1024, true
	default:
		return 0, false
	}
}

// bucketStart округляет t вниз до границы bucketMinutes-минут (от начала часа) и
// форматирует результат как RFC3339. time.Time.Truncate отсчитывает от эпохи, что
// для bucketMinutes, делящих 60 (1,2,3,5,6,10,15,30…), совпадает с границей часа.
// bucketMinutes <= 0 трактуется как 5 (защита; обычно дефолт уже подставлен
// вызывающей стороной через AccountingConfig.GetBucketMinutes).
func bucketStart(t time.Time, bucketMinutes int) string {
	if bucketMinutes <= 0 {
		bucketMinutes = 5
	}
	return t.UTC().Truncate(time.Duration(bucketMinutes) * time.Minute).Format(time.RFC3339)
}

// fallbackTimestamp возвращает Start последнего окна стейджа stageID — фолбэк
// result-usage не имеет собственного Timestamp, и последнее (как правило
// единственное) выполнение стейджа реалистично отражает результат. Окна нет или
// Start не разбирается → нулевое время (пограничный случай; в тестах не возникает,
// поскольку наличие result-события подразумевает завершённое окно выполнения).
func fallbackTimestamp(stageID string, windows []StageWindow) time.Time {
	var last string
	for _, w := range windows {
		if w.StageID == stageID {
			last = w.Start
		}
	}
	if last == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return time.Time{}
	}
	return t
}
