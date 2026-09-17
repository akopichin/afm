package workspace

import "testing"

// TestDetectLanguage runs on every platform (pure function, no filesystem) —
// unlike list_test.go's Linux-only end-to-end test, this gives real RED/GREEN
// evidence on a non-Linux dev host.
func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"main.go":        "go",
		"app.ts":         "typescript",
		"App.tsx":        "typescript",
		"index.js":       "javascript",
		"App.jsx":        "javascript",
		"esm.mjs":        "javascript",
		"legacy.cjs":     "javascript",
		"script.py":      "python",
		"types.pyi":      "python",
		"flow.yaml":      "yaml",
		"config.yml":     "yaml",
		"README.md":      "markdown",
		"NOTES.markdown": "markdown",
		"build.sh":       "bash",
		"lib.bash":       "bash",
		"noext":          "plain",
		"Alpha.GO":       "go",   // case-insensitive extension
		"Config.YML":     "yaml", // case-insensitive extension
		"archive.TAR":    "plain",
		// goga CODEMANIFEST files: no extension, YAML-structured content.
		"CODEMANIFEST":                     "yaml",  // exact base name
		"codemanifest":                     "yaml",  // case-insensitive
		"internal/core/model/CODEMANIFEST": "yaml",  // nested path → base name
		"CODEMANIFEST.bak":                 "plain", // near-miss: not exact base
		"CODEMANIFEST.yaml":                "yaml",  // covered by .yaml case
	}
	for name, want := range cases {
		if got := detectLanguage(name); got != want {
			t.Errorf("detectLanguage(%q) = %q, want %q", name, got, want)
		}
	}
}
