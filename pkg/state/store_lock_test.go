package state

import (
	"errors"
	"testing"
)

func TestOpen_SecondProcessGetsRunLocked(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"a"}

	s1, err := Open(dir, ids)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer s1.Close()

	_, err = Open(dir, ids)
	if !errors.Is(err, ErrRunLocked) {
		t.Fatalf("second Open: want ErrRunLocked, got %v", err)
	}
}

func TestOpen_LockReleasedOnClose(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"a"}

	s1, err := Open(dir, ids)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := Open(dir, ids)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	s2.Close()
}
