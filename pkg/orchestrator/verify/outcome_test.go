package verify

import "testing"

// TestRunOutcome_ZeroValue проверяет, что нулевое значение RunOutcome
// однозначно читается как "ничего не решено": все булевы флаги false,
// ошибки/результат отсутствуют. Поведения пока нет — только форма типа.
func TestRunOutcome_ZeroValue(t *testing.T) {
	var o RunOutcome
	if o.ProcessOK || o.TimedOut || o.Interrupted {
		t.Fatalf("zero-value RunOutcome should have all flags false, got %+v", o)
	}
	if o.ProtocolErr != nil {
		t.Fatalf("zero-value RunOutcome.ProtocolErr should be nil, got %v", o.ProtocolErr)
	}
	if o.Result != nil {
		t.Fatalf("zero-value RunOutcome.Result should be nil, got %+v", o.Result)
	}
}
