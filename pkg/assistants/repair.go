package assistants

import (
	"bytes"
	"strings"

	"github.com/kaptinlin/jsonrepair"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

// maxCandidateRepairBytes bounds how large the single selected candidate object may be before RepairResult
// hands it to jsonrepair.Repair. The vendored jsonrepair (recursive-descent) is quadratic in cost for several
// DIFFERENT malformed-JSON shapes it repairs — missing commas between quoted strings, missing commas between
// bare numbers/keywords, deeply nested brackets — and bounding each shape separately turned out to be
// whack-a-mole (three rounds of review, each finding a different shape the previous bound missed). What every
// one of those costs is ultimately a function of is the candidate's own byte length, so bounding THAT closes
// all of them — present and future variants alike — in one place. 16 KiB is far larger than any plausible real
// envelope (a result/delegate envelope's structural shell is a few hundred bytes even with a dozen findings;
// the realistic size driver is prose length, not structural shape) while keeping jsonrepair's measured worst
// case (~2.8s at 224 KiB on the densest adversarial shape found) scaled down to tens of milliseconds.
const maxCandidateRepairBytes = 16 * 1024

// RepairResult returns a syntactically repaired candidate envelope, and ok=true when it found exactly one
// plausible envelope object in raw and that candidate differs from raw. ok=false means "nothing worth
// re-decoding".
func RepairResult(raw []byte) (fixed []byte, ok bool) {
	if err := jsonstrict.EnforceMaxBytes(raw, MaxResultBytes); err != nil {
		return nil, false
	}

	trimmed := bytes.TrimSpace(raw)
	candidates := findObjectSpans(trimmed)
	// Exactly one balanced {...} span anywhere in the buffer is required — not just one that happens to carry
	// "kind". A genuine single-envelope answer, with or without surrounding prose, never leaves a SECOND
	// balanced {...} span lying around elsewhere in the text. A second span is an equally strong signal that
	// something was silently dropped whether or not it carries its own "kind": a model printing a literal
	// "before/after" pair (the Review Focus case this was originally written for) is one example, but so is a
	// FIRST span whose own boundary was found in the wrong place — an unescaped quote inside a string value
	// can re-sync quote parity exactly at a `}` that was never meant to close the envelope, leaving the real
	// remainder (often a finding object) to surface here as its own, separate top-level span. Trying to instead
	// recognize "suspicious" trailing text right after the chosen candidate was tried and abandoned: enumerating
	// which bytes "look like JSON continuing" (and which whitespace bytes must be skipped first) is exactly the
	// kind of incomplete-by-construction blacklist that kept finding new gaps. Requiring the WHOLE buffer to
	// contain only one balanced span sidesteps that enumeration entirely.
	if len(candidates) != 1 {
		return nil, false
	}
	candidate := candidates[0]
	if !containsKindKey(candidate.text) {
		return nil, false
	}
	if len(candidate.text) > maxCandidateRepairBytes {
		return nil, false
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
// within the scanned buffer. The offset is kept even though RepairResult no longer inspects trailing bytes
// directly, because it is cheap to carry and callers may still want it (e.g. for diagnostics).
type objectSpan struct {
	text []byte
	end  int
}

// findObjectSpans finds every balanced top-level {...} span in raw, tracking string literals and escapes so
// that braces inside a string value never affect span boundaries. The scanner treats only " as a string
// delimiter, never ' — an apostrophe in ordinary prose is far more common than a single-quoted envelope, and
// honouring it would mis-span the much more frequent input.
//
// String/escape tracking is scoped to EACH candidate object independently (reset at every depth 0->1
// transition), never carried across prose outside an object. Prose is not JSON — a single unescaped `"`
// anywhere in surrounding text (a dropped closing quote, an inch-mark, a quoted word) would otherwise flip
// quote parity for the rest of the buffer and silently swallow the real envelope's own braces as "inside a
// string", defeating detection entirely.
//
// This function does NOT try to detect an unescaped `"` INSIDE the candidate itself (an even number of stray
// quotes in one string field value, which can re-sync parity exactly at an unrelated `}` and produce a
// genuinely truncated-but-valid-looking span) — RepairResult's "exactly one span total" check catches that
// case instead: the content a truncation drops almost always contains its own balanced {...} (a finding
// object, a second concatenated value), which shows up here as a SECOND span.
//
// Only {}'s are tracked, never []'s — a `[`/`]` pair nested inside a candidate never changes which `{` an
// outer `}` matches (JSON brackets must already nest correctly within themselves for the candidate to parse
// at all), so tracking them would add complexity without changing a single span boundary.
func findObjectSpans(raw []byte) []objectSpan {
	var spans []objectSpan
	inString := false
	escaped := false
	depth := 0
	start := 0
	for i, b := range raw {
		if depth == 0 {
			if b == '{' {
				depth = 1
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
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				spans = append(spans, objectSpan{text: raw[start : i+1], end: i + 1})
			}
		default:
			// ordinary byte outside a string
		}
	}
	return spans
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
