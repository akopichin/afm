package workspace

import (
	"path"
	"strings"
)

// Hidden service subtrees, invisible everywhere under a root: rejected by
// validateRelPath, and filtered out of directory listings in list.go.
const (
	hiddenGitDir = ".git"
	hiddenAfmDir = ".afm"
)

// validateRelPath cleans and validates a path relative to a root: it rejects
// absolute paths, `..` escapes, NUL bytes, and hidden service subtrees
// (`.git`, `.afm`) anywhere in the path. An empty path or "." both mean "the
// root itself" and normalize to ".".
func validateRelPath(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", ErrInvalidRootOrPath
	}
	if p == "" || p == "." {
		return ".", nil
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", ErrInvalidRootOrPath
	}
	// Reject any ".." segment in the RAW path before cleaning: path.Clean
	// would silently resolve "a/../b" to "b", masking a traversal attempt
	// instead of rejecting it.
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", ErrInvalidRootOrPath
		}
	}
	clean := path.Clean(p)
	for _, seg := range strings.Split(clean, "/") {
		if seg == hiddenGitDir || seg == hiddenAfmDir {
			return "", ErrNotFound // hidden service subtrees
		}
	}
	return clean, nil
}
