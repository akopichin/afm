//go:build !windows

package progress

import (
	"fmt"
	"os"
	"syscall"
)

// Lock acquires an exclusive blocking flock.
func (l *Lock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return fmt.Errorf("flock: %w", err)
	}
	l.f = f
	return nil
}

// TryLock attempts a non-blocking exclusive flock.
func (l *Lock) TryLock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("lock busy: %w", err)
	}
	l.f = f
	return nil
}

// Unlock releases the flock and closes the file.
func (l *Lock) Unlock() {
	if l.f != nil {
		syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
		l.f = nil
	}
}
