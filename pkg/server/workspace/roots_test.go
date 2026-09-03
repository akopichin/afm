package workspace

import "testing"

func TestValidateRelPath(t *testing.T) {
	ok := []string{"", ".", "pkg/server/handlers.go", "a/b/c"}
	for _, p := range ok {
		if _, err := validateRelPath(p); err != nil {
			t.Errorf("%q should be valid: %v", p, err)
		}
	}
	bad := []string{"/abs", "../up", "a/../b", "a/../../b", "a\x00b", ".git", "a/.git/x", ".afm/config.yaml", "a/.afm"}
	for _, p := range bad {
		if _, err := validateRelPath(p); err == nil {
			t.Errorf("%q should be rejected", p)
		}
	}
}
