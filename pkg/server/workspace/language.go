package workspace

import (
	"path/filepath"
	"strings"
)

// detectLanguage maps a file name's extension to the syntax-highlighting
// language id the frontend expects. Unknown extensions fall back to "plain".
// This is the single definition of the mapping — Task 8's Read reuses it.
func detectLanguage(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	default:
		return "plain"
	}
}
