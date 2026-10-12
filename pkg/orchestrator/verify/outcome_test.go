package verify

import "testing"

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
