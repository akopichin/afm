package workspace

import "testing"

// TestDetectLanguage runs on every platform (pure function, no filesystem) —
// unlike list_test.go's Linux-only end-to-end test, this gives real RED/GREEN
// evidence on a non-Linux dev host.
func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"main.go":     "go",
		"app.ts":      "typescript",
		"App.tsx":     "typescript",
		"index.js":    "javascript",
		"App.jsx":     "javascript",
		"esm.mjs":     "javascript",
		"legacy.cjs":  "javascript",
		"script.py":   "python",
		"types.pyi":   "python",
		"README.md":   "plain",
		"noext":       "plain",
		"Alpha.GO":    "go", // case-insensitive extension
		"archive.TAR": "plain",
	}
	for name, want := range cases {
		if got := detectLanguage(name); got != want {
			t.Errorf("detectLanguage(%q) = %q, want %q", name, got, want)
		}
	}
}
