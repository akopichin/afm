package accounting

import (
	"path/filepath"

	"github.com/akopichin/afm/pkg/config"
)

// stagePhases — фазы стейджа, для которых executor пишет терминальное result-событие
// в <runDir>/<stageID>/<phase>.jsonl. Совпадает с константами phasePlanning/
// phaseImplementation/phaseReview в pkg/orchestrator, но это файловая конвенция, а не
// Go-импорт — поэтому дублируется как локальный список имён файлов.
var stagePhases = []string{"planning", "implementation", "review"}

// Accountant — фасад запроса потребления для одного рана. Конструируется один раз и
// держит runDir, store, pricing и bucketMinutes как внутреннее состояние (без
// экспортируемых свойств — аналогично Proxy/Executor). Query пересчитывает агрегаты
// по требованию из usage.jsonl + истории переходов + <phase>.jsonl; без кэша.
type Accountant struct {
	runDir        string
	store         Store
	pricing       config.PricingConfig
	bucketMinutes int
}

// NewAccountant строит фасад для рана. Вызывающая сторона уже разрешила дефолт
// bucketMinutes (обычно через config.AccountingConfig.GetBucketMinutes) — здесь 5 не
// подставляется: переданное значение напрямую уходит в каждый вызов Aggregate.
func NewAccountant(runDir string, store Store, pricing config.PricingConfig, bucketMinutes int) *Accountant {
	return &Accountant{
		runDir:        runDir,
		store:         store,
		pricing:       pricing,
		bucketMinutes: bucketMinutes,
	}
}

// Query возвращает агрегаты потребления по метрике metric (tokens|cost|kb) и фильтру
// stage (пусто = все стадии).
//
// Алгоритм:
//  1. LoadUsageRecords из <runDir>/usage.jsonl (отсутствие файла → пусто, не ошибка:
//     прокси мог не запускаться в этом ране).
//  2. LoadStageWindows(store) — окна выполнения стейджей из истории переходов.
//  3. Для каждого stageID из store.Snapshot().StageOrder — ReadResultUsage по его
//     <runDir>/<stageID>/<phase>.jsonl (planning/implementation/review): авторитетные
//     токены как фолбэк для стадий без прокси-записей.
//  4. Aggregate(records, windows, resultUsages, pricing, metric, stage, bucketMinutes).
//
// metric=cost при пустом pricing.Models → пустой список (не ошибка: такая стадия
// просто не попадает в cost-агрегаты, т.к. ResultUsage без Model не разрешает
// GetModelPricing). err != nil только при жёстком сбое LoadUsageRecords (нечитаемый
// usage.jsonl) или LoadStageWindows (ошибка store.History). Store/events.jsonl не
// мутируется — используются только читающие History/Snapshot.
func (a *Accountant) Query(metric string, stage string) ([]UsageAggregate, error) {
	records, err := LoadUsageRecords(filepath.Join(a.runDir, "usage.jsonl"))
	if err != nil {
		return nil, err
	}

	windows, err := LoadStageWindows(a.store)
	if err != nil {
		// Ранний возврат: Aggregate не вызывается (он по контракту всегда вернул бы
		// не-nil срез, поэтому nil-результат здесь доказателен — см. тест).
		return nil, err
	}

	// Шаг 3: result-события из phase.jsonl каждого стейджа. Порядок = StageOrder —
	// детерминированный, но итоговые агрегаты Aggregate всё равно сортирует сам.
	resultUsages := make([]ResultUsage, 0)
	for _, stageID := range a.store.Snapshot().StageOrder {
		stageDir := filepath.Join(a.runDir, stageID)
		for _, phase := range stagePhases {
			usage, ok := ReadResultUsage(filepath.Join(stageDir, phase+".jsonl"))
			if !ok {
				continue // фаза не дошла до терминала или файла нет — не ошибка.
			}
			usage.StageID = stageID // ID известен по каталогу, не по содержимому файла.
			resultUsages = append(resultUsages, usage)
		}
	}

	return Aggregate(records, windows, resultUsages, a.pricing, metric, stage, a.bucketMinutes), nil
}
