package docker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDefaultExecFunc_ForwardsSignalToChild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "signaled")
	script := fmt.Sprintf(`trap 'touch %q; exit 42' TERM; sleep 30`, marker)

	resultErr := make(chan error, 1)
	go func() {
		resultErr <- defaultExecFunc("/bin/sh", []string{"sh", "-c", script}, os.Environ())
	}()

	// Даём defaultExecFunc время зарегистрировать signal.Notify и запустить дочерний процесс, прежде чем
	// слать сигнал самому текущему процессу (симулирует Ctrl-C/kill хостовому afm).
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	select {
	case err := <-resultErr:
		var exitErr *SubprocessExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected *SubprocessExitError, got %v (%T)", err, err)
		}
		if exitErr.Code != 42 {
			t.Errorf("exit code = %d, want 42 (child's trap handler exit code)", exitErr.Code)
		}
	case <-time.After(30 * time.Second):
		// 30с — щедрый запас: доставка сигналов дочерним процессам в некоторых песочницах ощутимо задержана
		// (секунды, не мс), хотя в обычной среде форвардинг практически мгновенный.
		t.Fatal("defaultExecFunc did not return within 30s after SIGTERM — signal was not forwarded, child left running orphaned")
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("child process did not receive the forwarded signal (marker file missing): %v", err)
	}
}
