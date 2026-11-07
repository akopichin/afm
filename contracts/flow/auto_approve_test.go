package flow

import "testing"

func TestParse_AutoApproveDefaultsFalse(t *testing.T) {
	f, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [planning, implementation]
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Stages[0].AutoApprove {
		t.Error("AutoApprove should default to false when auto_approve is absent from YAML")
	}
}

func TestParse_AutoApproveTrue(t *testing.T) {
	f, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [planning, implementation]
    auto_approve: true
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.Stages[0].AutoApprove {
		t.Error("AutoApprove should be true when auto_approve: true is set in YAML")
	}
}

func TestParse_AutoApproveOnAutoStage_NoErrorNoOp(t *testing.T) {
	// auto_approve is a documented no-op on agents:[auto] stages (they skip
	// the approval gate entirely) — parsing must not reject the combination.
	f, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto]
    auto_approve: true
`)
	if err != nil {
		t.Fatalf("auto_approve + agents:[auto] should parse without error: %v", err)
	}
	if !f.Stages[0].AutoApprove {
		t.Error("AutoApprove field should still be true even though it has no effect on an auto stage")
	}
}
