package budget

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// scanObject parses data as exactly one JSON object and passes every member to visit in order, repeats
// included: encoding/json would silently keep only the last one.
func scanObject(data []byte, visit func(key string, raw json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := tok.(string) // Token reports a non-string object key as a syntax error
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if err := visit(key, raw); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// decodeObject returns the members of a budget DTO. Keys must be spelled exactly as allowed and appear once,
// without null: encoding/json would fold another spelling onto an already checked field.
func decodeObject(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	err := scanObject(data, func(key string, raw json.RawMessage) error {
		if !slices.Contains(allowed, key) {
			return fmt.Errorf("unknown field %q (want %s)", key, strings.Join(allowed, ", "))
		}
		if _, dup := obj[key]; dup {
			return fmt.Errorf("duplicate field %q", key)
		}
		if isJSONNull(raw) {
			return fmt.Errorf("%s must not be null", key)
		}
		obj[key] = raw
		return nil
	})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// RejectNullField checks the budget member of a container that encoding/json decodes, matching key in any
// letter case as it does: the member may appear once and not as null, which a *Spec reads as no budget.
func RejectNullField(data []byte, key string) error {
	if isJSONNull(data) {
		return nil // encoding/json leaves a container untouched by null
	}
	seen := false
	return scanObject(data, func(name string, raw json.RawMessage) error {
		if !strings.EqualFold(name, key) {
			return nil // other members keep the container's own decoding rules
		}
		if seen {
			return fmt.Errorf("%s is given more than once", key)
		}
		seen = true
		if isJSONNull(raw) {
			return fmt.Errorf("%s must not be null: give tokens and/or money, or omit the key", key)
		}
		return nil
	})
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// strictUnmarshal is json.Unmarshal that rejects unknown fields and trailing data.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}
