package assistants

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// Models write the free-text fields of an answer either as one string or as a list of lines (limitations,
// evidence and summary have all come back in both shapes). Rejecting a whole review for the shape of its prose
// throws away a correct answer, so these fields accept both and store the field's own type. Anything else —
// a number, an object, null, a list holding a non-string — is still a protocol error.

// textField is a string field that also accepts a list of strings, joined with newlines.
type textField string

func (f *textField) UnmarshalJSON(data []byte) error {
	lines, err := decodeStringOrList(data, reflect.TypeFor[string]())
	if err != nil {
		return err
	}
	*f = textField(strings.Join(lines, "\n"))
	return nil
}

// textListField is a list-of-strings field that also accepts a single string.
type textListField []string

func (f *textListField) UnmarshalJSON(data []byte) error {
	lines, err := decodeStringOrList(data, reflect.TypeFor[[]string]())
	if err != nil {
		return err
	}
	*f = lines
	return nil
}

// decodeStringOrList decodes a JSON string (as one line) or an array of strings. Anything else — null
// included, alone or in the array, as the published schema has it — is a *json.UnmarshalTypeError against
// field; the decoder adds the field's name to it, so the protocol error still says which field was wrong.
func decodeStringOrList(data []byte, field reflect.Type) ([]string, error) {
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
