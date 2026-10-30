package jsonstrict

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// StringOrList decodes a JSON string (as one line) or an array of strings. Anything else — null
// included, alone or in the array, as the published schema has it — is a *json.UnmarshalTypeError against
// field; the decoder adds the field's name to it, so the protocol error still says which field was wrong.
func StringOrList(data []byte, field reflect.Type) ([]string, error) {
	notText := &json.UnmarshalTypeError{Value: "a value other than a string or an array of strings", Type: field}
	trimmed := bytes.TrimSpace(data)
	switch {
	case bytes.HasPrefix(trimmed, []byte(`"`)):
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, err
		}
		return []string{s}, nil
	case bytes.HasPrefix(trimmed, []byte("[")):
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, notText
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			var line string
			if !bytes.HasPrefix(bytes.TrimSpace(item), []byte(`"`)) || json.Unmarshal(item, &line) != nil {
				return nil, notText
			}
			lines = append(lines, line)
		}
		return lines, nil
	default:
		return nil, notText
	}
}
