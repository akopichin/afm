package assistants_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/assistants/assistantstest"
)

func TestRepairResult_FencedAnswer(t *testing.T) {
	raw := []byte("```json\n" + assistantstest.FixtureValidResult + "\n```")
	env, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil {
		t.Fatalf("DecodeResultWithRepair: %v", err)
	}
	if !repaired {
		t.Fatal("expected repaired == true")
	}
	if env.Kind != assistants.KindResult {
		t.Fatalf("expected KindResult, got %q", env.Kind)
	}
}

func TestRepairResult_ProseAroundSingleObject(t *testing.T) {
	raw := []byte("Проверил код.\n" + assistantstest.FixtureNeedsChangesResult + "\nНадеюсь помог.")
	env, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil {
		t.Fatalf("DecodeResultWithRepair: %v", err)
	}
	if !repaired {
		t.Fatal("expected repaired == true")
	}
	if env.Result == nil || env.Result.Verdict != assistants.VerdictNeedsChanges {
		t.Fatalf("expected verdict needs_changes, got %+v", env.Result)
	}
}

func TestRepairResult_TwoCandidateObjectsRefused(t *testing.T) {
	raw := []byte(assistantstest.FixtureValidResult + "\nи ещё\n" + assistantstest.FixtureNeedsChangesResult)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false for two candidate objects")
	}
	_, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err == nil {
		t.Fatal("expected DecodeResultWithRepair to fail")
	}
	if repaired {
		t.Fatal("expected repaired == false")
	}
}

func TestRepairResult_SyntaxJunk(t *testing.T) {
	raw := []byte(`{
		"schema_version": 1,
		"kind": "result",
		"verdict": "pass",
		"summary": "ok",
	}`)
	_, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil {
		t.Fatalf("DecodeResultWithRepair: %v", err)
	}
	if !repaired {
		t.Fatal("expected repaired == true")
	}
}

func TestRepairResult_SingleQuotedEnvelope(t *testing.T) {
	raw := []byte(`{'schema_version':1,'kind':'result','verdict':'needs_changes','summary':'found a bug'}`)
	env, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil {
		t.Fatalf("DecodeResultWithRepair: %v", err)
	}
	if !repaired {
		t.Fatal("expected repaired == true")
	}
	if env.Result == nil || env.Result.Verdict != assistants.VerdictNeedsChanges {
		t.Fatalf("expected verdict preserved, got %+v", env.Result)
	}
}

func TestRepairResult_PureProseRefused(t *testing.T) {
	raw := []byte("Проверил. Всё плохо, вот пять замечаний...")
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false for pure prose")
	}
	_, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err == nil {
		t.Fatal("expected DecodeResultWithRepair to fail")
	}
	if repaired {
		t.Fatal("expected repaired == false")
	}
}

func TestRepairResult_OversizeRefusedBeforeRepair(t *testing.T) {
	padding := strings.Repeat(" ", assistants.MaxResultBytes+1024)
	raw := []byte("```json\n" + assistantstest.FixtureValidResult + padding + "\n```")
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false when input exceeds MaxResultBytes")
	}
}

func TestRepairResult_BraceInsideStringNotCounted(t *testing.T) {
	raw := []byte("```json\n" + `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "pass",
		"summary": "uses a { brace } inside the string"
	}` + "\n```")
	env, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil {
		t.Fatalf("DecodeResultWithRepair: %v", err)
	}
	if !repaired {
		t.Fatal("expected repaired == true")
	}
	if env.Result == nil || env.Result.Summary != "uses a { brace } inside the string" {
		t.Fatalf("expected summary preserved verbatim, got %+v", env.Result)
	}
}

func TestDecodeResultWithRepair_ValidAnswerNotRepaired(t *testing.T) {
	env, repaired, err := assistants.DecodeResultWithRepair([]byte(assistantstest.FixtureValidResult))
	if err != nil {
		t.Fatalf("DecodeResultWithRepair: %v", err)
	}
	if repaired {
		t.Fatal("expected repaired == false for an already-valid answer")
	}
	if env.Kind != assistants.KindResult {
		t.Fatalf("expected KindResult, got %q", env.Kind)
	}
}

func TestDecodeResult_StillRejectsFence(t *testing.T) {
	raw := []byte("```json\n" + assistantstest.FixtureValidResult + "\n```")
	if _, err := assistants.DecodeResult(raw); err == nil {
		t.Fatal("expected strict DecodeResult to still reject a fenced answer")
	}
}
