package executor

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestRunAgent_InterruptSendsSIGINTNotKill проверяет, что сигнал на
// InterruptCh приводит к SIGINT (агент сам грамотно завершает текущую
// атомарную операцию и выходит), а не к жёсткому Kill — и что RunAgent
// возвращает ErrUserInterrupted, отличимый от обычной ошибки завершения.
func TestRunAgent_InterruptSendsSIGINTNotKill(t *testing.T) {
	dir := t.TempDir()
	// Скрипт: ловит SIGINT, пишет маркер и завершается с кодом 0 (а не
	// оставляет процесс висеть, что случилось бы при отсутствии обработки).
	// A loop of short sleeps, not a bare "sleep 30": bash defers running an
	// INT trap until the current *foreground* external command completes,
	// so a single long "sleep 30" would swallow the signal for the full
	// 30s (confirmed empirically on both bash 3.2/macOS and bash 5/Linux).
	// Backgrounding it ("sleep 30 &" + "wait $!") reacts to the trap
	// immediately, but leaves the orphaned "sleep 30" holding stdout open
	// after bash exits — run()'s reader goroutine correctly waits for a
	// true pipe EOF (all fds closed) before returning, so it would then
	// hang until that orphan exits on its own ~30s later. A loop of short
	// sleeps bounds trap-deferral to one short sleep (~100ms) without ever
	// leaving an orphan holding the pipe.
	// "ready" marker is touched right after the trap is installed, so the
	// test can poll for it instead of guessing a fixed sleep duration: a
	// fixed sleep (300ms as originally sketched, even 1s) is inherently
	// racy under system load — under a full `make test` run (many package
	// binaries competing for CPU), bash's own startup can occasionally
	// exceed even a 1s budget, so the signal arrives before the trap is
	// registered and bash dies via SIGINT's default action instead of
	// running the trap (confirmed by direct instrumentation: cmd.Wait()
	// returns "signal: interrupt" with zero stdout emitted). Polling for
	// an observable "trap is installed" side effect removes the race
	// entirely, regardless of machine load.
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
