package executor

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestRunAgent_InterruptSendsSIGINTNotKill(t *testing.T) {
	dir := t.TempDir()
	// Скрипт: ловит SIGINT, пишет маркер и завершается с кодом 0. Polling for an observable "trap is
	// installed" side effect removes the race entirely, regardless of machine load.
	script := "trap 'touch " + dir + "/signaled; exit 0' INT\n" +
		"touch " + dir + "/ready\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"working\"}]}}'\n" +
		"while :; do sleep 0.1; done\n"
	scriptPath := dir + "/agent.sh"
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}

	interruptCh := make(chan struct{}, 1)
	e := New(Config{
		Command:     scriptPath,
		IdleTimeout: 10 * time.Second,
		InterruptCh: interruptCh,
	})

	done := make(chan error, 1)
	go func() {
		done <- e.RunAgent(context.Background(), "test", "Stage", "prompt", dir+"/run.log")
	}()

	// Wait for the trap to actually be installed (see comment above) instead
	// of a fixed sleep, before sending the interrupt.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(dir + "/ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("script did not reach trap installation within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	interruptCh <- struct{}{}

	select {
	case err := <-done:
		if !errors.Is(err, ErrUserInterrupted) {
			t.Errorf("RunAgent error = %v, want errors.Is(err, ErrUserInterrupted)", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunAgent did not return within 20s of interrupt signal")
	}

	if _, err := os.Stat(dir + "/signaled"); err != nil {
		t.Errorf("script did not receive SIGINT (marker file missing): %v", err)
	}
}

func TestRunAgent_PreFiredInterruptNeverStartsProcess(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/started"
	scriptPath := dir + "/agent.sh"
	script := "#!/bin/bash\ntouch " + marker + "\necho '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	interruptCh := make(chan struct{}, 1)
	interruptCh <- struct{}{} // сигнал уже пришёл ДО запуска

	e := New(Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
		InterruptCh: interruptCh,
	})

	err := e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	if !errors.Is(err, ErrUserInterrupted) {
		t.Fatalf("RunAgent error = %v, want errors.Is(err, ErrUserInterrupted)", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("process must never have started — marker file exists")
	}
}

// TestRunAgent_PreCancelledCtxNeverStartsProcess — зеркало предыдущего теста
// для отмены родительского ctx (полная отмена рана), а не сигнала паузы.
func TestRunAgent_PreCancelledCtxNeverStartsProcess(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/started"
	scriptPath := dir + "/agent.sh"
	script := "#!/bin/bash\ntouch " + marker + "\necho '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // уже отменён до вызова

	e := New(Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	err := e.RunAgent(ctx, "test", "s1", "prompt", dir+"/run.log")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAgent error = %v, want errors.Is(err, context.Canceled)", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("process must never have started — marker file exists")
	}
}
