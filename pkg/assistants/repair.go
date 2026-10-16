package assistants

import (
	"bytes"
	"strings"

	"github.com/kaptinlin/jsonrepair"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

// maxCandidateNestingDepth bounds how deeply nested a candidate's brackets — BOTH {}'s and []'s, counted
// together — may be before RepairResult gives up on it. The vendored jsonrepair.Repair (recursive-descent) is
// quadratic in nesting DEPTH for both object and array repair paths, so the two bracket kinds must share one
// bound: a cap on {} alone would leave a deeply array-nested candidate (e.g. a value like [[[[...]]]]) just as
// costly and completely unguarded. 200 is ~40x deeper than any plausible envelope (findings[]/evidence[] are
// flat arrays of flat objects/strings, so real nesting tops out around depth 3-4) while keeping jsonrepair's
// worst case well under a millisecond — far below the several seconds a pathologically deep candidate would
// otherwise cost inside the task's own completion goroutine.
const maxCandidateNestingDepth = 200

// maxCandidateElements bounds how many structural tokens — commas, bracket opens, and string starts, at any
// nesting level — a candidate may contain before RepairResult gives up on it. This is a SEPARATE axis from
// nesting depth: jsonrepair.Repair's "insert a missing separator" repair rebuilds its entire accumulated
// output buffer on every occurrence (measured: ~32,000 comma-separated siblings in a 224 KiB array — well
// within MaxResultBytes and nowhere near maxCandidateNestingDepth — took ~2.8s), so a FLAT candidate with
// thousands of siblings is just as costly as a deeply nested one and needs its own bound. Counting ONLY commas
// would miss the exact defect this guards against — siblings jsonrepair itself had to insert a separator
// between (e.g. space- or newline-separated string values with no comma at all) — so string starts and
// bracket opens are counted too, each counted once per sibling regardless of whether a separator between them
// was present. 2000 is well beyond any plausible envelope's field/finding count while keeping jsonrepair's
// worst case under ~20ms (measured).
const maxCandidateElements = 2000

// RepairResult returns a syntactically repaired candidate envelope, and ok=true when it found exactly one
// plausible envelope object in raw and that candidate differs from raw. ok=false means "nothing worth
// re-decoding".
func RepairResult(raw []byte) (fixed []byte, ok bool) {
	if err := jsonstrict.EnforceMaxBytes(raw, MaxResultBytes); err != nil {
		return nil, false
	}

	trimmed := bytes.TrimSpace(raw)
	candidates, tooComplex := findObjectSpans(trimmed)
	if tooComplex {
		return nil, false
	}
	var candidate objectSpan
	found := 0
	for _, span := range candidates {
		if containsKindKey(span.text) {
			found++
			candidate = span
		}
	}
	if found != 1 {
		return nil, false
	}
	// A quote left unescaped INSIDE a string value (not just in surrounding prose, which the reset-per-
	// candidate string tracking above already handles) can re-sync the scanner's quote parity exactly where a
	// `}` that was never meant to close the envelope happens to sit — producing a candidate that is valid JSON
	// on its own but is really only a TRUNCATED PREFIX of the model's actual answer, with everything after
	// (possibly a blocker finding) silently dropped. The real envelope never has anything JSON-structural
	// immediately after its own closing brace — only prose (the whole point of this feature) or nothing. If
	// what immediately follows instead looks like a continuation of JSON structure, refuse rather than guess
	// which `}` the model meant.
	if rest := bytes.TrimLeft(trimmed[candidate.end:], " \t\r\n"); len(rest) > 0 {
		switch rest[0] {
		case ',', ':', ']', '}':
			return nil, false
		}
	}

	repaired, err := jsonrepair.Repair(string(candidate.text))
	if err != nil {
		fixed = candidate.text
	} else {
		fixed = []byte(repaired)
	}
	if bytes.Equal(bytes.TrimSpace(fixed), trimmed) {
		return nil, false
	}
	return fixed, true
}

// DecodeResultWithRepair is DecodeResult plus one mechanical repair attempt. repaired reports whether the
// accepted envelope came from the repaired bytes. On failure it returns the ORIGINAL strict decode error.
func DecodeResultWithRepair(raw []byte) (env Envelope, repaired bool, err error) {
	env, err = DecodeResult(raw)
	if err == nil {
		return env, false, nil
	}
	original := err

	fixed, ok := RepairResult(raw)
	if !ok {
		return Envelope{}, false, original
	}
	env, err = DecodeResult(fixed)
	if err != nil {
		return Envelope{}, false, original
	}
	return env, true, nil
}

// objectSpan is one candidate {...} span found by findObjectSpans, together with its end offset (exclusive)
// within the scanned buffer — RepairResult needs the offset to inspect what, if anything, immediately follows
// the candidate's own closing brace.
type objectSpan struct {
	text []byte
	end  int
}

// findObjectSpans finds every balanced top-level {...} span in raw, tracking string literals and escapes so
// that braces inside a string value never affect span boundaries. The scanner treats only " as a string
// delimiter, never ' — an apostrophe in ordinary prose is far more common than a single-quoted envelope, and
// honouring it would mis-span the much more frequent input. tooComplex reports whether any candidate crossed
// maxCandidateNestingDepth OR maxCandidateElements (nesting and breadth are independent cost vectors into the
// same quadratic jsonrepair.Repair call — see each constant's own doc comment): on tooComplex,
// findObjectSpans has ALREADY discarded everything it found so far (returns nil, true) — there is nothing
// left for the caller to discard, it only needs to treat this as outright refusal, never a zero-candidate
// miss.
//
// depth counts BOTH {}'s and []'s together, not just braces: a deeply array-nested value (e.g. a field
// holding [[[[...]]]]) costs jsonrepair just as much as deeply nested objects, so the same bound must cover
// both bracket kinds sharing one counter — a `{`/`[` always closes with a `}`/`]` in well-formed JSON, so
// depth still correctly returns to 0 exactly when the span's own opening `{` finds its match, regardless of
// which bracket kinds were nested inside it. elements counts every comma, bracket-open, and string-start seen
// while depth>0 — a flat proxy for "how many siblings", reset at the same depth 0->1 transition as depth
// itself. Commas alone are not enough: the exact defect being guarded against is often a MISSING separator
// (jsonrepair inserts one), so counting only commas would undercount precisely the pathological case.
//
// String/escape tracking is scoped to EACH candidate object independently (reset at every depth 0->1
// transition), never carried across prose outside an object. Prose is not JSON — a single unescaped `"`
// anywhere in surrounding text (a dropped closing quote, an inch-mark, a quoted word) would otherwise flip
// quote parity for the rest of the buffer and silently swallow the real envelope's own braces as "inside a
// string", defeating detection entirely. An unescaped `"` INSIDE the candidate itself (an even number of stray
// quotes in one string field value) is a separate, more dangerous case this function does NOT try to detect —
// it can re-sync parity exactly at an unrelated `}`, producing a valid-looking but truncated span; RepairResult
// catches that case instead, by checking what immediately follows the chosen candidate.
func findObjectSpans(raw []byte) (spans []objectSpan, tooComplex bool) {
	inString := false
	escaped := false
	depth := 0
	elements := 0
	start := 0
	for i, b := range raw {
		if depth == 0 {
			if b == '{' {
				depth = 1
				elements = 0
				start = i
				inString = false
				escaped = false
			}
			continue
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			default:
				// ordinary string byte
			}
			continue
		}
		switch b {
		case '"':
			inString = true
			elements++
			if elements > maxCandidateElements {
				return nil, true
			}
		case '{', '[':
			depth++
			elements++
			if depth > maxCandidateNestingDepth || elements > maxCandidateElements {
				return nil, true
			}
		case '}':
			depth--
			if depth == 0 {
				spans = append(spans, objectSpan{text: raw[start : i+1], end: i + 1})
			}
		case ']':
			depth--
		case ',':
			elements++
			if elements > maxCandidateElements {
				return nil, true
			}
		default:
			// ordinary byte outside a string
		}
	}
	return spans, false
}

// containsKindKey reports whether span contains the envelope discriminator "kind" as an object key, accepting
// all three spellings jsonrepair can normalise: "kind", 'kind' or bare kind, each followed by optional
// whitespace and ':'. Over-matching is safe: a false second candidate only triggers the refusal the task
// already gets today, and the strict decoder remains the final authority.
func containsKindKey(span []byte) bool {
	s := string(span)
	for _, form := range []string{`"kind"`, `'kind'`} {
		if idx := strings.Index(s, form); idx >= 0 {
			rest := strings.TrimLeft(s[idx+len(form):], " \t\r\n")
			if strings.HasPrefix(rest, ":") {
				return true
			}
		}
	}
	return containsBareKindKey(s)
}

// containsBareKindKey looks for an unquoted "kind" key (e.g. {kind: "result"}), which jsonrepair also
// normalises. It requires a non-identifier boundary before "kind" (so it does not match inside a longer
// identifier like "mykind") and a ':' (optionally preceded by whitespace) after it.
func containsBareKindKey(s string) bool {
	const key = "kind"
	for idx := 0; ; {
		pos := strings.Index(s[idx:], key)
		if pos < 0 {
			return false
		}
		pos += idx
		before := byte(0)
		if pos > 0 {
			before = s[pos-1]
		}
		after := pos + len(key)
		if !isIdentByte(before) {
			rest := strings.TrimLeft(s[after:], " \t\r\n")
			if strings.HasPrefix(rest, ":") {
				return true
			}
		}
		idx = pos + len(key)
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
