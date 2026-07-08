package accounting_test

import (
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
)

// TestAccountantSignature — контрактный тест: NewAccountant(string, Store,
// config.PricingConfig, int) *Accountant и (*Accountant).Query(string, string)
// ([]UsageAggregate, error) компилируются и вызываются с этими сигнатурами. Store
// здесь — пакетный интерфейс accounting.Store; передаём fakeStore, который
// удовлетворяет ему структурно (внешний тест-пакет не может назвать сам тип Store).
// Пустой ран (нет usage.jsonl, пустая история) → не-nil пустой срез, nil err.
func TestAccountantSignature(t *testing.T) {
	acc := accounting.NewAccountant(t.TempDir(), fakeStore{}, config.PricingConfig{}, 5)

	aggs, err := acc.Query("tokens", "")
	if err != nil {
		t.Fatalf("Query on empty run returned unexpected error: %v", err)
	}
	if aggs == nil {
		t.Fatal("Query returned nil aggregates for empty run; want non-nil empty slice")
	}

	// Типы доступны как экспортируемые идентификаторы фасада.
	var _ = acc
	_ = accounting.UsageAggregate{}
}

// TestAccountantQueryPropagatesLoadStageWindowsError — жёсткий сбой store.History()
// (через LoadStageWindows) пробрасывается как err, причём Query возвращается ДО
// вызова Aggregate. Доказательство «Aggregate не вызывался»: Aggregate по контракту
// (см. TestAggregateSignature) всегда возвращает не-nil срез, поэтому nil-агрегаты
// означают, что мы ушли по пути раннего возврата, не дойдя до Aggregate.
func TestAccountantQueryPropagatesLoadStageWindowsError(t *testing.T) {
	store := fakeStore{histErr: errors.New("events.jsonl: corrupt")}
	acc := accounting.NewAccountant(t.TempDir(), store, config.PricingConfig{}, 5)

	aggs, err := acc.Query("tokens", "")
	if err == nil {
		t.Fatal("Query err = nil, want the propagated store.History() error")
	}
	if aggs != nil {
		t.Errorf("Query aggregates = %v, want nil (Aggregate must not be reached on History error)", aggs)
	}
}
