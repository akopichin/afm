package assistants

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeContext_Valid(t *testing.T) {
	for _, tt := range []struct {
		in   RuntimeContext
		want bool
	}{
		{RuntimeContextIsolated, true},
		{RuntimeContextWrapper, true},
		{"", false},
		{"Wrapper", false},
		{"host", false},
	} {
		if got := tt.in.Valid(); got != tt.want {
			t.Errorf("%q.Valid() = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseRuntimeContextNode(t *testing.T) {
	ok := map[string]RuntimeContext{"wrapper": RuntimeContextWrapper, "isolated": RuntimeContextIsolated}
	for in, want := range ok {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte("v: "+in), &doc); err != nil {
			t.Fatal(err)
		}
		got, err := ParseRuntimeContextNode("f", doc.Content[0].Content[1])
		if err != nil || got != want {
			t.Errorf("%s: got (%q, %v), want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`""`, "null", "~", "1", "true", "{a: b}", "[wrapper]", "solo"} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte("v: "+bad), &doc); err != nil {
			t.Fatal(err)
		}
		_, err := ParseRuntimeContextNode("collaboration.runtime_context", doc.Content[0].Content[1])
		if err == nil || !strings.Contains(err.Error(), "collaboration.runtime_context") {
			t.Errorf("%s: err = %v, want error naming the field", bad, err)
		}
	}
}

func TestParseRuntimeContextNode_Alias(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("a: &x wrapper\nb: *x\n"), &doc); err != nil {
		t.Fatal(err)
	}
	got, err := ParseRuntimeContextNode("f", doc.Content[0].Content[3])
	if err != nil || got != RuntimeContextWrapper {
		t.Fatalf("got (%q, %v), want wrapper", got, err)
	}
}

func TestResolveRuntimeContext_Priority(t *testing.T) {
	w, i := RuntimeContextWrapper, RuntimeContextIsolated
	for _, tt := range []struct {
		name            string
		profile, collab *RuntimeContext
		want            RuntimeContext
	}{
		{"default", nil, nil, RuntimeContextIsolated},
		{"collaboration", nil, &w, RuntimeContextWrapper},
		{"profile over collaboration", &i, &w, RuntimeContextIsolated},
		{"profile wrapper over isolated collaboration", &w, &i, RuntimeContextWrapper},
	} {
		if got := ResolveRuntimeContext(tt.profile, tt.collab); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}
