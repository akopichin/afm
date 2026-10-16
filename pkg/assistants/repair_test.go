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

func TestRepairResult_StrayQuoteInSurroundingProseDoesNotBreakDetection(t *testing.T) {
	// A single unescaped '"' anywhere before the envelope object must not desync a scanner that tracks string
	// state across the WHOLE buffer — it must not make the real object's braces look like they are "inside a
	// string" and get silently dropped.
	raw := []byte(`The model said "ok this is fine. ` + assistantstest.FixtureValidResult)
	env, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil {
		t.Fatalf("DecodeResultWithRepair: %v", err)
	}
	if !repaired {
		t.Fatal("expected repaired == true")
	}
	if env.Result == nil || env.Result.Verdict != assistants.VerdictPass {
		t.Fatalf("expected verdict pass, got %+v", env.Result)
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

// TestRepairResult_ExcessiveNestingRefused pins a defensive cap: jsonrepair.Repair (recursive-descent) is
// quadratic in nesting depth, so a candidate nested deeper than is ever plausible for a real envelope must be
// refused BEFORE it is handed to jsonrepair — never spend seconds of CPU on a pathological answer.
func TestRepairResult_ExcessiveNestingRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":`)
	const depth = 500
	for range depth {
		b.WriteString(`{"a":`)
	}
	b.WriteString(`"x"`)
	for range depth {
		b.WriteString(`}`)
	}
	b.WriteString(`}`)

	if _, ok := assistants.RepairResult([]byte(b.String())); ok {
		t.Fatal("expected RepairResult ok == false for a candidate nested far beyond any real envelope")
	}
}

func TestRepairResult_BareKindKey(t *testing.T) {
	raw := []byte("```json\n" + `{schema_version: 1, kind: result, verdict: "needs_changes", summary: "found a bug"}` + "\n```")
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

func TestRepairResult_BareKindKeyDoesNotMatchLongerIdentifier(t *testing.T) {
	// "mykind" must not be mistaken for the "kind" key — isIdentByte's boundary check.
	raw := []byte(`{"schema_version":1,"mykind":"result","verdict":"pass","summary":"ok"}`)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false: \"mykind\" must not be mistaken for the \"kind\" key")
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
