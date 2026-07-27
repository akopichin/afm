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

// TestDefaultExecFunc_ForwardsSignalToChild воспроизводит прод-баг: SIGTERM
// (или SIGINT) хостовому процессу afm в Docker-режиме не останавливал сам
// прогон — docker run (дочерний процесс) оставался сиротой и продолжал
// работать вместе с контейнером, потому что exec.Command не форвардит
// сигналы дочернему процессу автоматически, а до этой правки defaultExecFunc
// вообще не ловил сигналы (Go просто убивал сам процесс дефолтной
// диспозицией). Тест шлёт SIGTERM самому себе (симулируя Ctrl-C/kill
// хостовому процессу afm), пока defaultExecFunc блокируется в ожидании
// реального дочернего процесса, и проверяет, что сигнал форвардится в
// ребёнка (тот получает его и реагирует), а defaultExecFunc возвращается
// быстро — не висит до собственного тайм-аута ребёнка.
func TestDefaultExecFunc_ForwardsSignalToChild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "signaled")
	script := fmt.Sprintf(`trap 'touch %q; exit 42' TERM; sleep 30`, marker)

	resultErr := make(chan error, 1)
	go func() {
		resultErr <- defaultExecFunc("/bin/sh", []string{"sh", "-c", script}, os.Environ())
	}()

	// Даём defaultExecFunc время зарегистрировать signal.Notify и запустить
	// дочерний процесс, прежде чем слать сигнал самому текущему процессу
	// (симулирует Ctrl-C/kill хостовому afm).
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
		// 30с — щедрый запас: доставка сигналов дочерним процессам в
		// некоторых песочницах ощутимо задержана (секунды, не мс), хотя в
		// обычной среде форвардинг практически мгновенный.
		t.Fatal("defaultExecFunc did not return within 30s after SIGTERM — signal was not forwarded, child left running orphaned")
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("child process did not receive the forwarded signal (marker file missing): %v", err)
	}
}
