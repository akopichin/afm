package workspace

import "testing"

// TestNew_NilRoots verifies the cross-platform baseline: no roots in → no roots
// out, no error, and Close succeeds. On non-Linux hosts (or on a kernel without
// openat2) this is also the shape of the graceful-degradation result.
func TestNew_NilRoots(t *testing.T) {
	fs, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if got := len(fs.Roots()); got != 0 {
		t.Errorf("no roots in → want 0 roots, got %d", got)
	}
	if err := fs.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestNew_EmptyRoots is the same, with an explicit empty slice.
func TestNew_EmptyRoots(t *testing.T) {
	fs, err := New([]Root{})
	if err != nil {
		t.Fatalf("New([]): %v", err)
	}
	if got := len(fs.Roots()); got != 0 {
		t.Errorf("want 0 roots, got %d", got)
	}
	_ = fs.Close()
}
