//go:build windows

package progress

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Lock acquires an exclusive blocking lock on Windows.
func (l *Lock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return fmt.Errorf("LockFileEx: %w", err)
	}
	l.f = f
	return nil
}

// TryLock attempts a non-blocking exclusive lock on Windows.
func (l *Lock) TryLock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		f.Close()
		return fmt.Errorf("lock busy: %w", err)
	}
	l.f = f
	return nil
}

// Unlock releases the lock on Windows.
func (l *Lock) Unlock() {
	if l.f != nil {
		ol := new(windows.Overlapped)
		windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol)
		l.f.Close()
		l.f = nil
	}
}
