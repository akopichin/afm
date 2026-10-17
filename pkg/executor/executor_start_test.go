package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRun_StartFailure_IsSentinel — несуществующий бинарь не должен уходить в ретрай как
// транзиентный сбой: cmd.Start() падает детерминированно (бинарь не найден), и Run обязан
// обернуть это в ErrAgentStart, различимый через errors.Is, с именем команды в тексте.
func TestRun_StartFailure_IsSentinel(t *testing.T) {
	dir := t.TempDir()

	e := New(Config{Command: "afm-no-such-binary-xyz", IdleTimeout: 5 * time.Second})
	err := e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrAgentStart) {
		t.Fatalf("RunAgent error = %v, want errors.Is(err, ErrAgentStart)", err)
	}
	if !strings.Contains(err.Error(), "afm-no-such-binary-xyz") {
		t.Fatalf("error does not contain command name: %v", err)
	}
}

// TestRun_StartedThenExitNonZero_IsNotStartFailure — процесс, который успешно стартовал и
// позже вышел с ненулевым кодом, это НЕ сбой запуска: ErrAgentStart тут не должен
// примешиваться, иначе ретраиваемый транспортный сбой схлопнулся бы с детерминированным
// "бинарь не найден".
func TestRun_StartedThenExitNonZero_IsNotStartFailure(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", "exit 3\n")

	e := New(Config{Command: script, IdleTimeout: 5 * time.Second})
	err := e.RunAgent(context.Background(), "test", "s1", "prompt", dir+"/run.log")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrAgentStart) {
		t.Fatalf("RunAgent error unexpectedly wrapped as ErrAgentStart: %v", err)
	}
}
