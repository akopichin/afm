package assistants_test

import (
	"strings"
	"testing"
	"time"

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
// appear — but only if that is genuinely the object's own close. Here it is NOT: real JSON (a finding object)
// still follows, and it surfaces as its OWN top-level span once the premature `}` closes the first one —
// tripping the "exactly one span in the whole buffer" requirement. Accepting the truncated prefix as-is would
// silently drop a blocker finding and report a false "pass" — repair must refuse rather than guess which `}`
// the model meant.
func TestRepairResult_StrayQuoteInsideStringTruncatesRefused(t *testing.T) {
	raw := []byte(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"the file uses "latin1" encoding"}` +
		`,"findings":[{"id":"f1","severity":"blocker","detail":"nil pointer on empty input, must not ship"}]}`)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false: a second balanced span left behind by truncation must refuse, not silently drop it")
	}
	_, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err == nil {
		t.Fatal("expected DecodeResultWithRepair to fail rather than silently report a truncated pass")
	}
	if repaired {
		t.Fatal("expected repaired == false")
	}
}

// TestRepairResult_BareQuoteNoCommaTruncationRefused is the same truncation failure mode as
// TestRepairResult_StrayQuoteInsideStringTruncatesRefused, but the dropped remainder is separated from the
// truncated prefix by a bare `"` (a missing comma before the next key) rather than a `,` — pinning that
// detection does not depend on enumerating which punctuation follows a truncated candidate. The finding
// object still surfaces as its own top-level span regardless of what (if anything) sits between it and the
// truncated prefix.
func TestRepairResult_BareQuoteNoCommaTruncationRefused(t *testing.T) {
	raw := []byte(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"the file uses "latin1" encoding"}` +
		`"findings":[{"id":"f1","severity":"blocker","detail":"nil pointer on empty input, must not ship"}]}`)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false: a second balanced span left behind by truncation must refuse, not silently drop it")
	}
	_, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err == nil {
		t.Fatal("expected DecodeResultWithRepair to fail rather than silently report a truncated pass")
	}
	if repaired {
		t.Fatal("expected repaired == false")
	}
}

// TestRepairResult_ConcatenatedObjectWithoutSeparatorRefused: two complete JSON objects glued together with NO
// separator at all (a plausible LLM failure mode — e.g. a retried/concatenated completion) is a second,
// independent way to leave a dangling finding object behind. Only the FIRST object carries "kind", so the
// older "two candidates must both carry kind" check would have missed this — "exactly one span, period" does
// not.
func TestRepairResult_ConcatenatedObjectWithoutSeparatorRefused(t *testing.T) {
	raw := []byte(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok"}` +
		`{"findings":[{"id":"f1","severity":"blocker","detail":"nil pointer, must not ship"}]}`)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false: a second concatenated object must refuse, not silently drop it")
	}
	_, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err == nil {
		t.Fatal("expected DecodeResultWithRepair to fail")
	}
	if repaired {
		t.Fatal("expected repaired == false")
	}
}

// TestRepairResult_ArrayWrappedSingleObjectRepaired pins a deliberate restored leniency: wrapping the single
// envelope object in an array (a plausible model mistake, not a truncation) leaves exactly ONE balanced {...}
// span — the "exactly one span" requirement does not react to brackets that are not braces, so this is still
// recovered, same as a markdown fence is stripped.
func TestRepairResult_ArrayWrappedSingleObjectRepaired(t *testing.T) {
	raw := []byte("[" + assistantstest.FixtureValidResult + "]")
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

// repairResultRefusedQuickly asserts RepairResult refuses raw, AND does so fast — proving refusal came from a
// cheap pre-check (the byte-size cap), never from actually calling the quadratic jsonrepair.Repair on the
// whole pathological candidate first and discarding the result afterward.
func repairResultRefusedQuickly(t *testing.T, raw []byte) {
	t.Helper()
	start := time.Now()
	_, ok := assistants.RepairResult(raw)
	elapsed := time.Since(start)
	if ok {
		t.Fatal("expected RepairResult ok == false")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("RepairResult took %v — refusal must be cheap (size-checked BEFORE jsonrepair.Repair runs), not a side effect of running it", elapsed)
	}
}

// TestRepairResult_OversizedCandidateRefused pins the one cost-bounding cap that replaced three rounds of
// increasingly specific (and each-round-bypassed) nesting/breadth heuristics: jsonrepair.Repair is quadratic
// in COST for several different malformed-JSON shapes it repairs, but every one of those costs is ultimately a
// function of the candidate's own byte length — so bounding that, rather than enumerating shapes, is what
// actually closes the vector. The trailing comma makes this candidate genuinely malformed (jsonrepair would
// normally strip it and succeed, same as TestRepairResult_SyntaxJunk) — proving refusal comes from the size
// cap, not from RepairResult's separate "nothing changed" short-circuit.
func TestRepairResult_OversizedCandidateRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"`)
	b.WriteString(strings.Repeat("x", 20_000))
	b.WriteString(`",}`)
	repairResultRefusedQuickly(t, []byte(b.String()))
}

// TestRepairResult_DeeplyNestedCandidateRefused is a REGRESSION test for the exact shape round 1/2 measured
// (~2.2s at ~260 KiB of {}-nesting): the byte-size cap bounds it too, since a candidate cannot nest this deep
// without accumulating bytes proportionally — there is no need for a separate depth counter once size itself
// is bounded.
func TestRepairResult_DeeplyNestedCandidateRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":`)
	const depth = 3500 // ~21 KB of {"a": nesting — comfortably over maxCandidateRepairBytes (16 KiB)
	for range depth {
		b.WriteString(`{"a":`)
	}
	b.WriteString(`"x",`)
	for range depth {
		b.WriteString(`}`)
	}
	b.WriteString(`}`)
	repairResultRefusedQuickly(t, []byte(b.String()))
}

// TestRepairResult_DeeplyArrayNestedCandidateRefused is the array-bracket variant of
// TestRepairResult_DeeplyNestedCandidateRefused — jsonrepair.Repair is just as quadratic on deeply-nested
// arrays, and the byte-size cap does not care which bracket type accumulated the bytes.
func TestRepairResult_DeeplyArrayNestedCandidateRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"x","evidence":`)
	const depth = 10_000 // ~20 KB of [[[...]]] nesting — comfortably over maxCandidateRepairBytes
	b.WriteString(strings.Repeat("[", depth))
	b.WriteString(`1,`)
	b.WriteString(strings.Repeat("]", depth))
	b.WriteString(`}`)
	repairResultRefusedQuickly(t, []byte(b.String()))
}

// TestRepairResult_ManyBareSiblingsCandidateRefused is a REGRESSION test for the specific security-reported
// bypass: jsonrepair's missing-separator repair rebuilds its whole accumulated output on every occurrence, so
// a FLAT candidate with thousands of BARE (unquoted, uncounted-by-string-starts) siblings — space-separated
// numbers, not even strings — is just as costly as deeply nested input (measured ~8.6s at ~262 KB, the densest
// shape found across all three review rounds). The byte-size cap closes this the same way it closes every
// other shape: by bounding what jsonrepair ever sees, regardless of what's inside it.
func TestRepairResult_ManyBareSiblingsCandidateRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"x","evidence":[`)
	const n = 10_000 // "1 " per sibling, ~20 KB total — comfortably over maxCandidateRepairBytes
	for i := range n {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString("1")
	}
	b.WriteString(`]}`)
	repairResultRefusedQuickly(t, []byte(b.String()))
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
	// Quoted "mykind" already fails containsKindKey's colon-adjacency check regardless of isIdentByte (the
	// character right after "mykind" is a quote, never ':') — this pins that shape, but does NOT by itself
	// exercise the identifier-boundary guard. See the BareForm variant below for that.
	raw := []byte(`{"schema_version":1,"mykind":"result","verdict":"pass","summary":"ok"}`)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false: \"mykind\" must not be mistaken for the \"kind\" key")
	}
}

// TestRepairResult_BareKindKeyDoesNotMatchLongerIdentifierBareForm actually exercises isIdentByte's
// identifier-boundary check: with an UNQUOTED key, the character right after "mykind" is ':' (same as a real
// "kind" key would have), so only the boundary check (not colon-adjacency) stands between this and a false
// match.
func TestRepairResult_BareKindKeyDoesNotMatchLongerIdentifierBareForm(t *testing.T) {
	raw := []byte(`{schema_version:1,mykind:"result",verdict:"pass",summary:"ok"}`)
	if _, ok := assistants.RepairResult(raw); ok {
		t.Fatal("expected RepairResult ok == false: bare \"mykind\" must not be mistaken for the bare \"kind\" key")
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
