package assistants

import (
	"bytes"
	"strings"

	"github.com/kaptinlin/jsonrepair"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

// maxCandidateNestingDepth bounds how deeply nested a candidate object's braces may be before RepairResult
// gives up on it. The vendored jsonrepair.Repair (recursive-descent) is quadratic in nesting depth — a
// legitimate envelope never nests more than a few levels (findings[]/evidence[] are flat arrays of flat
// objects/strings), so this cap is generous for any real answer while keeping jsonrepair's worst case
// negligible; it is what stands between a pathologically deep helper answer and several seconds of
// synchronous CPU inside the task's own completion goroutine.
const maxCandidateNestingDepth = 200

// RepairResult returns a syntactically repaired candidate envelope, and ok=true when it found exactly one
// plausible envelope object in raw and that candidate differs from raw. ok=false means "nothing worth
// re-decoding".
func RepairResult(raw []byte) (fixed []byte, ok bool) {
	if err := jsonstrict.EnforceMaxBytes(raw, MaxResultBytes); err != nil {
		return nil, false
	}

	trimmed := bytes.TrimSpace(raw)
	candidates, tooDeep := findObjectSpans(trimmed)
	if tooDeep {
		return nil, false
	}
	var candidate []byte
	found := 0
	for _, span := range candidates {
		if containsKindKey(span) {
			found++
			candidate = span
		}
	}
	if found != 1 {
		return nil, false
	}

	repaired, err := jsonrepair.Repair(string(candidate))
	if err != nil {
		fixed = candidate
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

// findObjectSpans finds every balanced top-level {...} span in raw, tracking string literals and escapes so
// that braces inside a string value never affect span boundaries. The scanner treats only " as a string
// delimiter, never ' — an apostrophe in ordinary prose is far more common than a single-quoted envelope, and
// honouring it would mis-span the much more frequent input. tooDeep reports whether any candidate's nesting
// exceeded maxCandidateNestingDepth — the caller must discard everything found rather than hand such a
// candidate to jsonrepair.
//
// String/escape tracking is scoped to EACH candidate object independently (reset at every depth 0->1
// transition), never carried across prose outside an object. Prose is not JSON — a single unescaped `"`
// anywhere in surrounding text (a dropped closing quote, an inch-mark, a quoted word) would otherwise flip
// quote parity for the rest of the buffer and silently swallow the real envelope's own braces as "inside a
// string", defeating detection entirely.
func findObjectSpans(raw []byte) (spans [][]byte, tooDeep bool) {
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
			if depth > maxCandidateNestingDepth {
				return nil, true
			}
		case '}':
			depth--
			if depth == 0 {
				spans = append(spans, raw[start:i+1])
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
