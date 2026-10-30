package assistants

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kaptinlin/jsonrepair"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

// Bound the recursive syntax repairer independently of the larger valid-result limit.
const maxCandidateRepairBytes = 16 * 1024

// RepairResult returns a syntactically repaired candidate envelope, and ok=true when it found exactly one
// plausible envelope object in raw and that candidate differs from raw. ok=false means "nothing worth
// re-decoding".
func RepairResult(raw []byte) (fixed []byte, ok bool) {
	if err := jsonstrict.EnforceMaxBytes(raw, MaxResultBytes); err != nil {
		return nil, false
	}

	trimmed := bytes.TrimSpace(raw)
	candidate, ok := jsonstrict.SingleObject(trimmed)
	if !ok {
		return nil, false
	}

	if !containsKindKey(candidate) {
		return nil, false
	}
	if len(candidate) > maxCandidateRepairBytes {
		return nil, false
	}

	if err := jsonstrict.DecodeNoDuplicates(candidate); errors.Is(err, jsonstrict.ErrDuplicateKey) {
		return nil, false
	}
	if json.Valid(candidate) {
		return candidate, !bytes.Equal(candidate, trimmed)
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

// DecodeResultWithRepair retains the legacy API; RecoverResult also returns candidate bytes and provenance.
func DecodeResultWithRepair(raw []byte) (env Envelope, repaired bool, err error) {
	env, _, kind, err := RecoverResult(raw)
	return env, kind != "", err
}

// RecoverResult never modifies raw and returns a candidate only after strict validation.
func RecoverResult(raw []byte) (Envelope, []byte, jsonstrict.RepairKind, error) {
	env, original := DecodeResult(raw)
	if original == nil {
		if fixed, changed := jsonstrict.RepairRawNewlines(raw); changed {
			return env, fixed, jsonstrict.MechanicalRepair, nil
		}
		return env, raw, "", nil
	}
	if len(raw) > MaxResultBytes {
		return Envelope{}, nil, "", original
	}
	if fixed, changed := NormalizeResult(raw); changed {
		if env, err := DecodeResult(fixed); err == nil {
			return env, fixed, jsonstrict.SchemaNormalization, nil
		}
	}
	// Raw control characters can coexist with a known misplaced field in a large valid envelope.
	if fixed, changed := jsonstrict.RepairRawNewlines(raw); changed {
		if normalized, ok := NormalizeResult(fixed); ok {
			if env, err := DecodeResult(normalized); err == nil {
				return env, normalized, jsonstrict.SchemaNormalization, nil
			}
		}
	}
	fixed, ok := RepairResult(raw)
	if ok {
		if env, err := DecodeResult(fixed); err == nil {
			return env, fixed, jsonstrict.MechanicalRepair, nil
		}
		if normalized, changed := NormalizeResult(fixed); changed {
			if env, err := DecodeResult(normalized); err == nil {
				return env, normalized, jsonstrict.SchemaNormalization, nil
			}
		}
	}
	_, complete := jsonstrict.SingleObject(bytes.TrimSpace(raw))
	if !complete {
		original = jsonstrict.NewIssue(jsonstrict.RefusalIssue, original)
	}
	return Envelope{}, nil, "", original
}

// NormalizeResult moves only a recognized finding recommendation, preserving its association and text.
func NormalizeResult(raw []byte) ([]byte, bool) {
	if jsonstrict.EnforceMaxBytes(raw, MaxResultBytes) != nil || jsonstrict.DecodeNoDuplicates(raw) != nil {
		return nil, false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil, false
	}
	var kind Kind
	if json.Unmarshal(root["kind"], &kind) != nil || kind != KindResult {
		return nil, false
	}
	var findings []map[string]json.RawMessage
	if json.Unmarshal(root["findings"], &findings) != nil {
		return nil, false
	}
	changed := false
	for _, finding := range findings {
		suggestion, exists := finding["suggested_change"]
		if !exists {
			continue
		}
		var text textField
		if json.Unmarshal(suggestion, &text) != nil {
			return nil, false
		}
		var detail string
		if !bytes.HasPrefix(bytes.TrimSpace(finding["detail"]), []byte(`"`)) || json.Unmarshal(finding["detail"], &detail) != nil {
			return nil, false
		}
		finding["detail"], _ = json.Marshal(detail + "\n\nSuggested change:\n" + string(text))
		delete(finding, "suggested_change")
		changed = true
	}
	if !changed {
		return nil, false
	}
	root["findings"], _ = json.Marshal(findings)
	fixed, err := json.Marshal(root)
	return fixed, err == nil
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
