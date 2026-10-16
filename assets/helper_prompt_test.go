package assets_test

import (
	"regexp"
	"testing"

	"github.com/akopichin/afm/assets"
	"github.com/akopichin/afm/pkg/assistants"
)

var jsonFenceRe = regexp.MustCompile("(?s)```json\\s*\\n(.*?)\\n```")

// TestHelperPromptExamplesDecode pins that a ```json fence in these templates MUST be a complete, valid,
// decodable example — annotated or placeholder schemas belong in a ```text fence instead.
func TestHelperPromptExamplesDecode(t *testing.T) {
	for _, name := range []string{"helper-result.md", "helper-delegate.md"} {
		t.Run(name, func(t *testing.T) {
			text, _, err := assets.ReadPrompt(name, "")
			if err != nil {
				t.Fatalf("ReadPrompt(%q): %v", name, err)
			}
			matches := jsonFenceRe.FindAllStringSubmatch(text, -1)
			if len(matches) == 0 {
				t.Fatalf("%s: expected at least one ```json fence", name)
			}
			for _, m := range matches {
				if _, err := assistants.DecodeResult([]byte(m[1])); err != nil {
					t.Fatalf("%s: example fence does not decode: %v\n%s", name, err, m[1])
				}
			}
		})
	}
}
