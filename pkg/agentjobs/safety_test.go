package agentjobs

import (
	"reflect"
	"strings"
	"testing"
)

// The journal must never grow a field that could carry argv, environment or user text.
func TestRecordFieldsCarryNoArgvEnvOrMessageText(t *testing.T) {
	forbidden := []string{"argv", "args", "command", "cmd", "env", "secret", "token", "prompt", "message", "text", "content"}
	for _, typ := range []reflect.Type{reflect.TypeOf(Record{}), reflect.TypeOf(JobMeta{}), reflect.TypeOf(Segment{}), reflect.TypeOf(Header{})} {
		for field := range typ.Fields() {
			name := strings.ToLower(field.Name + " " + field.Tag.Get("json"))
			for _, word := range forbidden {
				if strings.Contains(name, word) {
					t.Errorf("%s.%s looks like it can hold %q", typ.Name(), field.Name, word)
				}
			}
		}
	}
}
