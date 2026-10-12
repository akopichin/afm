package workspace

import "testing"

func TestBuildMarker(t *testing.T) {
	cases := map[string]string{
		"/workspace/a.go":          `[AFM file: "/workspace/a.go"]`,
		`/workspace/weird"name.go`: `[AFM file: "/workspace/weird\"name.go"]`,
		`/workspace/back\slash.go`: `[AFM file: "/workspace/back\\slash.go"]`,
		"/workspace/new\nline.go":  `[AFM file: "/workspace/new\nline.go"]`,
	}
	for abs, want := range cases {
		if got := buildMarker(abs); got != want {
			t.Errorf("buildMarker(%q) = %q, want %q", abs, got, want)
		}
	}
}
