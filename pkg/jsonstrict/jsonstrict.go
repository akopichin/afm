// Package jsonstrict holds the TRANSPORT-only primitives shared by every strict decoder of a model's
// machine-readable JSON answer (pkg/orchestrator/verify's ModelResult and pkg/assistants' Result envelope):
// reject a markdown code fence, enforce a byte cap BEFORE parsing, reject unknown fields and trailing
// content, reject duplicate keys at any depth, and repair the one narrow raw-newline-inside-a-string
// mistake real models make. None of these know anything about a DOMAIN schema (verdict/findings vs
// kind/children) — that stays in each caller. This is NOT a general json-repair library: RepairRawNewlines
// fixes exactly one narrow mistake and nothing else (see its doc comment).
package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrMarkdownFence is wrapped by RejectFence's error.
var ErrMarkdownFence = errors.New("result is wrapped in a markdown code fence; expected bare JSON")

// ErrTooLarge is wrapped by EnforceMaxBytes's error.
var ErrTooLarge = errors.New("result exceeds byte limit")

// ErrTrailingContent is wrapped by DecodeDisallowUnknown's error when there is content after the document.
var ErrTrailingContent = errors.New("trailing content after JSON document")

// ErrDuplicateKey is wrapped by DecodeNoDuplicates's error.
var ErrDuplicateKey = errors.New("duplicate key in the same JSON object")

// EnforceMaxBytes rejects raw if it exceeds maxBytes. Callers MUST call this on the full, untrimmed input
// before any parsing — a cap checked after decoding has already paid the cost it exists to avoid.
func EnforceMaxBytes(raw []byte, maxBytes int) error {
	if len(raw) > maxBytes {
		return fmt.Errorf("%w: %d byte limit (got %d)", ErrTooLarge, maxBytes, len(raw))
	}
	return nil
}

// RejectFence rejects a document wrapped in a markdown code fence (```...```). Intentionally strict: the
// model is expected to emit bare JSON; RepairRawNewlines must never be allowed to strip a fence (that would
// silently accept exactly the sloppy format this rejects), so callers MUST call RejectFence before any
// repair attempt.
func RejectFence(trimmed []byte) error {
	if bytes.HasPrefix(trimmed, []byte("```")) {
		return ErrMarkdownFence
	}
	return nil
}

// DecodeDisallowUnknown parses exactly ONE JSON document into v, rejecting unknown fields and any content
// (including a second document) after it.
func DecodeDisallowUnknown(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrTrailingContent
	}
	return nil
}

// DecodeNoDuplicates walks raw token-by-token and rejects a key repeated within the same JSON object, at any
// depth of nesting.
func DecodeNoDuplicates(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	return checkDuplicateValue(dec)
}

// checkDuplicateValue parses one JSON value (object, array, or scalar), recursively checking for duplicate
// keys in every nested object.
func checkDuplicateValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar — nothing to check inside it
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			if _, dup := seen[key]; dup {
				return fmt.Errorf("%w: key %q appears more than once", ErrDuplicateKey, key)
			}
			seen[key] = struct{}{}
			if err := checkDuplicateValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing '}'
		return err
	case '[':
		for dec.More() {
			if err := checkDuplicateValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing ']'
		return err
	}
	return nil
}

// RepairRawNewlines escapes RAW control bytes found ONLY inside JSON string literals — the one narrow
// mistake reviewers/models make when they paste a multi-line code quote into a string field without
// escaping its newlines. Structural whitespace between tokens is untouched; an already-escaped \n (backslash
// + n) is untouched (never double-escaped). Returns the repaired bytes and whether anything changed.
func RepairRawNewlines(data []byte) ([]byte, bool) {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(data)+8)
	inString := false
	escaped := false
	changed := false
	for _, b := range data {
		if inString {
			if escaped {
				out = append(out, b)
				escaped = false
				continue
			}
			switch {
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			case b < 0x20:
				switch b {
				case '\n':
					out = append(out, '\\', 'n')
				case '\r':
					out = append(out, '\\', 'r')
				case '\t':
					out = append(out, '\\', 't')
				default:
					out = append(out, '\\', 'u', '0', '0', hexdigits[b>>4], hexdigits[b&0xf])
				}
				changed = true
				continue
			default:
				// ordinary string byte — falls through to the append below
			}
			out = append(out, b)
			continue
		}
		if b == '"' {
			inString = true
		}
		out = append(out, b)
	}
	if !changed {
		return data, false
	}
	return out, true
}
