package accounting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/akopichin/afm/pkg/proxy"
)

// ResultUsage — токены/стоимость/длительность терминального result-события из
// <phase>.jsonl стейджа. Служит фолбэк-источником токенов для стадий, у которых
// в этом рану нет записей прокси (usage.jsonl): метрика tokens тогда берётся из
// этого события, а не из реального захвата прокси.
//
// StageID проставляется вызывающей стороной (Accountant) — он определяется
// каталогом стейджа, а не содержимым файла, поэтому ReadResultUsage его не
// заполняет.
//
// Поле Model намеренно отсутствует: без него метрику cost для фолбэк-строк
// разрешить нельзя (GetModelPricing неприменим) — такие стадии в cost-агрегаты
// не попадают.
//
// TotalCostUsd хранится только для самопроверки (кросс-чек) и никогда не
// суммируется в UsageAggregate.Value: cost считается единственно через
// PricingConfig/DeriveCost.
type ResultUsage struct {
	StageID             string
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	TotalCostUsd        float64
	DurationMs          int
	SessionID           string
}

// LoadUsageRecords читает usage.jsonl по пути path, возвращая по одной
// proxy.UsageRecord на строку.
//
// Отсутствие файла — не ошибка (прокси мог не запускаться в этом рану):
// возвращается пустой срез и nil. Испорченная/обрезанная строка (процесс убит
// посередине записи) пропускается, а не роняет чтение — по тому же принципу,
// что pkg/state.Open для повреждённого хвоста events.jsonl. Прочие подлинные
// ошибки чтения существующего файла (например, отказ прав доступа)
// пробрасываются как err.
func LoadUsageRecords(path string) ([]proxy.UsageRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Не-nil пустой срез (nil зарезервирован для ошибки) — range по нему
			// безопасен; согласовано с LoadStageWindows и store.History().
			return make([]proxy.UsageRecord, 0), nil
		}
		return nil, fmt.Errorf("read usage log: %w", err)
	}

	records := make([]proxy.UsageRecord, 0)
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec proxy.UsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// Обрезанная/повреждённая строка — пропускаем, не роняя всё чтение.
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// resultUsage — вложенный объект usage в result-событии claude (snake_case
// токены). Вынесен в именованный тип, чтобы resultEvent не содержал анонимных
// структур (правило revive nested-structs).
type resultUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

// resultEvent — промежуточная структура для разбора строки type=="result" из
// <phase>.jsonl: имена полей соответствуют наблюдаемому формату claude
// (snake_case, usage вложен). Лишние поля строки игнорируются JSON-декодером.
type resultEvent struct {
	Type         string      `json:"type"`
	TotalCostUsd float64     `json:"total_cost_usd"`
	Usage        resultUsage `json:"usage"`
	SessionID    string      `json:"session_id"`
	DurationMs   int         `json:"duration_ms"`
}

// ReadResultUsage находит последнее result-событие в jsonl-файле стейджа
// (jsonlPath) и возвращает его токены/стоимость/длительность.
//
// Файл отсутствует/нечитаем или result-событий нет → ResultUsage{}, false (не
// ошибка — фаза просто не дошла до терминала). Иначе возвращается заполненная
// ResultUsage (поле StageID пусто — его проставляет вызывающая сторона) и true.
//
// ok=false означает именно «не удалось прочитать/найти», а не «успех с нулевой
// метрикой»: реальное событие с обнулённой usage (is_error:true) корректно даёт
// ok=true — ok связан только с разборчивостью, а не со значениями полей.
func ReadResultUsage(jsonlPath string) (ResultUsage, bool) {
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return ResultUsage{}, false
	}

	var last resultEvent
	found := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev resultEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Неразборчивая строка (assistant/user/обрезанный хвост) — пропускаем.
			continue
		}
		if ev.Type != "result" {
			continue
		}
		last = ev
		found = true
	}
	if !found {
		return ResultUsage{}, false
	}

	return ResultUsage{
		InputTokens:         last.Usage.InputTokens,
		OutputTokens:        last.Usage.OutputTokens,
		CacheCreationTokens: last.Usage.CacheCreationTokens,
		CacheReadTokens:     last.Usage.CacheReadTokens,
		TotalCostUsd:        last.TotalCostUsd,
		DurationMs:          last.DurationMs,
		SessionID:           last.SessionID,
	}, true
}
