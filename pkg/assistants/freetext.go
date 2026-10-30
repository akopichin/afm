package assistants

import (
	"reflect"
	"strings"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

// Models write the free-text fields of an answer either as one string or as a list of lines (limitations,
// evidence and summary have all come back in both shapes). Rejecting a whole review for the shape of its prose
// throws away a correct answer, so these fields accept both and store the field's own type. Anything else —
// a number, an object, null, a list holding a non-string — is still a protocol error.

// textField is a string field that also accepts a list of strings, joined with newlines.
type textField string

func (f *textField) UnmarshalJSON(data []byte) error {
	lines, err := jsonstrict.StringOrList(data, reflect.TypeFor[string]())
	if err != nil {
		return err
	}
	*f = textField(strings.Join(lines, "\n"))
	return nil
}

// textListField is a list-of-strings field that also accepts a single string.
type textListField []string

func (f *textListField) UnmarshalJSON(data []byte) error {
	lines, err := jsonstrict.StringOrList(data, reflect.TypeFor[[]string]())
	if err != nil {
		return err
	}
	*f = lines
	return nil
}
