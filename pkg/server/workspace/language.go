package workspace

import (
	"path/filepath"
	"strings"
)

// Language ids the frontend expects. langPlain is the fallback for unknown
// extensions.
const (
	langGo    = "go"
	langYAML  = "yaml"
	langPlain = "plain"
)

// detectLanguage maps a file name's extension to the syntax-highlighting
// language id the frontend expects. Unknown extensions fall back to langPlain.
// This is the single definition of the mapping — Task 8's Read reuses it.
func detectLanguage(name string) string {
	// goga CODEMANIFEST files have no extension but hold YAML-structured
	// content. Match the exact base name (case-insensitive) so a near-miss
	// like CODEMANIFEST.bak still falls through to the extension switch below.
	if strings.EqualFold(filepath.Base(name), "CODEMANIFEST") {
		return langYAML
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return langGo
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	case ".yaml", ".yml":
		return langYAML
	case ".md", ".markdown":
		return "markdown"
	case ".sh", ".bash":
		return "bash"
	default:
		return langPlain
	}
}
