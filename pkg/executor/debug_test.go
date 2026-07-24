package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAgentInput_WritesBothFiles(t *testing.T) {
	run := t.TempDir()
	stage := filepath.Join(run, "brainstorm")
	if err := os.Mkdir(stage, 0755); err != nil {
		t.Fatal(err)
	}
	e := New(Config{Debug: true, Command: "claude", RunDir: run, StageID: "brainstorm", SessionID: "s1"})

	e.logAgentInput("planning", "HELLO PROMPT")
	e.logAgentInput("planning", "SECOND PROMPT")

	debugLog, err := os.ReadFile(filepath.Join(run, "debug.log"))
	if err != nil {
		t.Fatalf("debug.log: %v", err)
	}
	got := string(debugLog)
	for _, want := range []string{"stage=brainstorm", "phase=planning", "cmd=claude", "session=s1", "--- BEGIN PROMPT ---", "HELLO PROMPT", "SECOND PROMPT"} {
		if !strings.Contains(got, want) {
			t.Errorf("debug.log missing %q; got:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "BEGIN PROMPT"); n != 2 {
		t.Errorf("expected 2 appended entries, got %d", n)
	}

	perStage, err := os.ReadFile(filepath.Join(stage, "planning.prompt.log"))
	if err != nil {
		t.Fatalf("planning.prompt.log: %v", err)
	}
	if !strings.Contains(string(perStage), "HELLO PROMPT") {
		t.Errorf("per-stage log missing prompt; got:\n%s", string(perStage))
	}
}

func TestLogAgentInput_DisabledWritesNothing(t *testing.T) {
	run := t.TempDir()
	e := New(Config{Debug: false, Command: "claude", RunDir: run})
	e.logAgentInput("planning", "X")
	if _, err := os.Stat(filepath.Join(run, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log должен отсутствовать при Debug=false")
	}
}

func TestLogAgentInput_NoStageIDOnlyDebugLog(t *testing.T) {
	run := t.TempDir()
	e := New(Config{Debug: true, Command: "glm51", RunDir: run}) // StageID пуст (supervisor)
	e.logAgentInput("supervisor", "SUP PROMPT")
	if _, err := os.Stat(filepath.Join(run, "debug.log")); err != nil {
		t.Errorf("debug.log должен существовать: %v", err)
	}
}
