package executor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// writeScript — вспомогательная функция: пишет исполняемый bash-скрипт в dir/name.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := dir + "/" + name
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunAgent_StderrTailIsFoldedIntoError — транспортный сбой (CLI пишет диагноз в stderr и выходит
// ненулевым кодом) должен дойти до ошибки RunAgent, а не раствориться в голом "exit status 1".
func TestRunAgent_StderrTailIsFoldedIntoError(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", `echo "Error: connect ECONNRESET 10.0.0.1:443" >&2
exit 1
`)

	e := New(Config{Command: script, IdleTimeout: 5 * time.Second})
	err := e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ECONNRESET") {
		t.Fatalf("error does not contain stderr tail: %v", err)
	}
}

// TestRunAgent_StderrTailAbsent_ErrorUnchanged — агент, молча вышедший с ненулевым кодом (пустой
// stderr), не должен обрастать разделителем — обогащать нечем.
func TestRunAgent_StderrTailAbsent_ErrorUnchanged(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", "exit 1\n")

	e := New(Config{Command: script, IdleTimeout: 5 * time.Second})
	err := e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), "--- stderr ---") {
		t.Fatalf("error unexpectedly enriched with empty stderr: %v", err)
	}
}

// TestRunAgent_StderrTailBounded — хвост stderr ограничен (20 строк / 4КБ), даже если агент печатает
// тысячи строк.
func TestRunAgent_StderrTailBounded(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", `for i in $(seq 1 10000); do echo "line $i" >&2; done
exit 1
`)

	e := New(Config{Command: script, IdleTimeout: 10 * time.Second})
	err := e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(err.Error()) >= 16*1024 {
		t.Fatalf("error not bounded: %d bytes", len(err.Error()))
	}
}

// TestRunAgent_StderrTailPreservesSentinels — пользовательское прерывание не сбой: ErrUserInterrupted
// должен остаться различимым через errors.Is, а текст ошибки не обогащается хвостом stderr.
func TestRunAgent_StderrTailPreservesSentinels(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", `trap 'exit 0' INT
touch `+dir+`/ready
echo "noise" >&2
while :; do sleep 0.1; done
`)

	interruptCh := make(chan struct{}, 1)
	e := New(Config{
		Command:     script,
		IdleTimeout: 10 * time.Second,
		InterruptCh: interruptCh,
	})

	done := make(chan error, 1)
	go func() {
		done <- e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(dir + "/ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("script did not become ready within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	interruptCh <- struct{}{}

	var err error
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("RunAgent did not return within 20s of interrupt signal")
	}

	if !errors.Is(err, ErrUserInterrupted) {
		t.Fatalf("RunAgent error = %v, want errors.Is(err, ErrUserInterrupted)", err)
	}
	if strings.Contains(err.Error(), "--- stderr ---") {
		t.Fatalf("interrupt must not be enriched with stderr tail: %v", err)
	}
}

// TestRunAgent_StderrTailPreservesSentinels_ContextCanceled — отмена контекста (shutdown/рестарт рана) —
// не сбой агента, а внешняя остановка; её текст не должен обогащаться хвостом stderr, иначе он мог бы
// дойти до lifecycle-хука и утечь во внешний webhook (см. комментарий на withStderrTail). В отличие от
// TestRunAgent_PreCancelledCtxNeverStartsProcess (interrupt_test.go), здесь процесс УСПЕВАЕТ стартовать и
// что-то напечатать в stderr ДО отмены — именно этот случай раньше не был защищён и не был проверен тестом.
func TestRunAgent_StderrTailPreservesSentinels_ContextCanceled(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", `trap 'exit 0' TERM
touch `+dir+`/ready
echo "Error: connect ECONNRESET 10.0.0.1:443" >&2
while :; do sleep 0.1; done
`)

	ctx, cancel := context.WithCancel(context.Background())
	e := New(Config{Command: script, IdleTimeout: 10 * time.Second})

	done := make(chan error, 1)
	go func() {
		done <- e.RunAgent(ctx, "test", "s1", "prompt", dir+"/run.log")
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(dir + "/ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("script did not become ready within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Даём скрипту время дописать stderr до kill — ready-файл гарантирует только порядок строк внутри
	// самого скрипта, не то, что ОС уже сбросила байты в pipe к моменту cancel().
	time.Sleep(50 * time.Millisecond)
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("RunAgent did not return within 20s of context cancellation")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAgent error = %v, want errors.Is(err, context.Canceled)", err)
	}
	if strings.Contains(err.Error(), "--- stderr ---") || strings.Contains(err.Error(), "ECONNRESET") {
		t.Fatalf("cancelled context must not be enriched with stderr tail: %v", err)
	}
}

// TestRunAgent_IdleTimeoutSentinelSurvivesEnrichment — ErrIdleTimeout остаётся опознаваемым через
// errors.Is, даже когда ошибка дополнительно обогащена хвостом stderr.
func TestRunAgent_IdleTimeoutSentinelSurvivesEnrichment(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", `echo "noise" >&2
sleep 5
`)

	e := New(Config{Command: script, IdleTimeout: 50 * time.Millisecond})
	err := e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("RunAgent error = %v, want errors.Is(err, ErrIdleTimeout)", err)
	}
}

// TestRunAgent_StderrTailIsPerRun_NotWholeFile — ключевая регрессия D7: stderr-лог — append-only и
// общий для всех попыток стадии; вторая попытка не должна унаследовать диагноз первой из-за того, что
// хвост читается от начала файла, а не от смещения текущего запуска.
func TestRunAgent_StderrTailIsPerRun_NotWholeFile(t *testing.T) {
	dir := t.TempDir()
	logFile := dir + "/run.log"

	failingScript := writeScript(t, dir, "failing.sh", `echo "Error: connect ECONNRESET 10.0.0.1:443" >&2
exit 1
`)
	e1 := New(Config{Command: failingScript, IdleTimeout: 5 * time.Second})
	err1 := e1.RunAgent(context.Background(), "test", "s1", "prompt", logFile)
	if err1 == nil || !strings.Contains(err1.Error(), "ECONNRESET") {
		t.Fatalf("first attempt: expected ECONNRESET in error, got %v", err1)
	}

	silentScript := writeScript(t, dir, "silent.sh", "exit 1\n")
	e2 := New(Config{Command: silentScript, IdleTimeout: 5 * time.Second})
	err2 := e2.RunAgent(context.Background(), "test", "s1", "prompt", logFile)
	if err2 == nil {
		t.Fatal("second attempt: expected error, got nil")
	}
	if strings.Contains(err2.Error(), "ECONNRESET") {
		t.Fatalf("second attempt inherited first attempt's stderr tail: %v", err2)
	}
	if strings.Contains(err2.Error(), "--- stderr ---") {
		t.Fatalf("second attempt unexpectedly enriched: %v", err2)
	}

	stderrLog, readErr := os.ReadFile(dir + "/run.stderr.log")
	if readErr != nil {
		t.Fatalf("read stderr log: %v", readErr)
	}
	if !strings.Contains(string(stderrLog), "ECONNRESET") {
		t.Fatal("append-only stderr log lost the first attempt's line")
	}
}
