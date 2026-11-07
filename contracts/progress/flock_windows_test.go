//go:build windows

package progress

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryLock_Windows_BusyReturnsSentinel(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".lock")
	a, _ := NewLock(p)
	if err := a.TryLock(); err != nil {
		t.Fatal(err)
	}
	defer a.Unlock()

	b, _ := NewLock(p)
	if err := b.TryLock(); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("want ErrLockBusy, got %v", err)
	}
}

func TestTryLock_Windows_OpenErrorNotBusy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing", ".lock")
	l, _ := NewLock(p)
	err := l.TryLock()
	if err == nil || errors.Is(err, ErrLockBusy) {
		t.Fatalf("open error must not be ErrLockBusy, got %v", err)
	}
}
