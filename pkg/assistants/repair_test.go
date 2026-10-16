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

// TestRepairResult_StrayQuoteInsideStringTruncatesRefused pins a dangerous failure mode: an unescaped quote
// INSIDE a string field value (e.g. an unescaped "latin1" in summary) can make an EVEN number of stray quotes
// re-sync the scanner's string-tracking exactly where the model's own closing `}` would also legitimately
// appear — but only if that is genuinely the object's own close. Here it is NOT: real JSON (",\"findings\":...")
// still follows. Accepting the truncated prefix as-is would silently drop a blocker finding and report a false
// "pass" — repair must refuse rather than guess which `}` the model meant.
func TestRepairResult_StrayQuoteInsideStringTruncatesRefused(t *testing.T) {
	raw := []byte(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"the file uses "latin1" encoding"}` +
		`,"findings":[{"id":"f1","severity":"blocker","detail":"nil pointer on empty input, must not ship"}]}`)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false: trailing JSON-like content after the candidate must refuse, not truncate")
	}
	_, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err == nil {
		t.Fatal("expected DecodeResultWithRepair to fail rather than silently report a truncated pass")
	}
	if repaired {
		t.Fatal("expected repaired == false")
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
// refused BEFORE it is handed to jsonrepair — never spend seconds of CPU on a pathological answer. The
// trailing comma makes this candidate genuinely MALFORMED (jsonrepair would normally strip it and succeed,
// same as TestRepairResult_SyntaxJunk) — proving refusal comes from the depth guard itself, not from
// RepairResult's separate "nothing changed" short-circuit, which a syntactically-valid deep candidate would
// trip on its own regardless of the guard.
func TestRepairResult_ExcessiveNestingRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":`)
	const depth = 500
	for range depth {
		b.WriteString(`{"a":`)
	}
	b.WriteString(`"x",`)
	for range depth {
		b.WriteString(`}`)
	}
	b.WriteString(`}`)

	if _, ok := assistants.RepairResult([]byte(b.String())); ok {
		t.Fatal("expected RepairResult ok == false for a candidate nested far beyond any real envelope")
	}
}

// TestRepairResult_ExcessiveArrayNestingRefused pins that maxCandidateNestingDepth bounds [] nesting too, not
// just {} — jsonrepair.Repair is just as quadratic on deeply-nested arrays, so a cap that only counted braces
// would leave this vector completely unguarded. Malformed (trailing comma) for the same reason as
// TestRepairResult_ExcessiveNestingRefused above.
func TestRepairResult_ExcessiveArrayNestingRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"x","evidence":`)
	const depth = 500
	b.WriteString(strings.Repeat("[", depth))
	b.WriteString(`1,`)
	b.WriteString(strings.Repeat("]", depth))
	b.WriteString(`}`)

	if _, ok := assistants.RepairResult([]byte(b.String())); ok {
		t.Fatal("expected RepairResult ok == false for a candidate array-nested far beyond any real envelope")
	}
}

// TestRepairResult_ExcessiveElementCountRefused pins a SEPARATE cost vector from nesting depth: jsonrepair's
// missing-comma repair rebuilds its whole accumulated output on every occurrence, so a FLAT candidate with
// thousands of siblings (no nesting at all) is just as costly as a deeply nested one — measured ~2.8s for
// ~32,000 siblings, well within MaxResultBytes and nowhere near maxCandidateNestingDepth. Space-separated
// (not comma-separated) values are exactly the "missing comma" defect jsonrepair repairs.
func TestRepairResult_ExcessiveElementCountRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"x","evidence":[`)
	const n = 3000
	for i := range n {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(`"item"`)
	}
	b.WriteString(`]}`)

	if _, ok := assistants.RepairResult([]byte(b.String())); ok {
		t.Fatal("expected RepairResult ok == false for a candidate with far more siblings than any real envelope")
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
