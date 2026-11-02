package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// dialogPhaseEnvScript writes the agent's AFM_DIALOG_PHASE to envFile as "set:<value>" or "unset",
// so a present-but-empty variable is not mistaken for a missing one.
func dialogPhaseEnvScript(t *testing.T, envFile, answer string) string {
	t.Helper()
	script := "#!/bin/bash\n" +
		`if [ "${AFM_DIALOG_PHASE+x}" = x ]; then printf 'set:%s' "$AFM_DIALOG_PHASE"; else printf unset; fi > ` + envFile + "\n"
	if answer != "" {
		script += emitAssistantTextScript(t, answer) + "\n"
	}
	script += resultSuccessLine() + "\n"
	scriptPath := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

func readDialogPhaseEnv(t *testing.T, envFile string) string {
	t.Helper()
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func TestRunAgent_DialogPhaseReachesStageAuthor(t *testing.T) {
	t.Setenv("AFM_DIALOG_PHASE", "review")
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")

	ex := executor.New(executor.Config{
		Command:     dialogPhaseEnvScript(t, envFile, ""),
		IdleTimeout: 5 * time.Second,
		StageDir:    "/tmp/test-stage-dir",
		DialogPhase: "planning",
	})
	if err := ex.RunAgent(context.Background(), "planning", "s1", "do work", filepath.Join(dir, "planning.log")); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if got := readDialogPhaseEnv(t, envFile); got != "set:planning" {
		t.Errorf("AFM_DIALOG_PHASE = %q, want the configured phase (set:planning), not the inherited one", got)
	}
}

func TestRunAgent_StripsInheritedDialogPhase(t *testing.T) {
	t.Setenv("AFM_DIALOG_PHASE", "planning")
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")

	// A run that is not a stage author (memory pipeline, hooks) must not ask questions in the parent's name.
	ex := executor.New(executor.Config{Command: dialogPhaseEnvScript(t, envFile, ""), IdleTimeout: 5 * time.Second})
	if err := ex.RunAgent(context.Background(), "implementation", "s1", "do work", filepath.Join(dir, "impl.log")); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if got := readDialogPhaseEnv(t, envFile); got != "unset" {
		t.Errorf("AFM_DIALOG_PHASE leaked from the parent env: %q, want unset", got)
	}
}

func TestRunVerifyAgent_NoDialogPhase(t *testing.T) {
	t.Setenv("AFM_DIALOG_PHASE", "planning")
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")

	ex := executor.New(executor.Config{
		Command:     dialogPhaseEnvScript(t, envFile, validNeedsChangesJSON),
		IdleTimeout: 5 * time.Second,
		StageDir:    "/leaked/stage/dir",
		DialogPhase: "implementation",
	})
	if _, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", filepath.Join(dir, "verify.log"), filepath.Join(dir, "result.json")); err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	if got := readDialogPhaseEnv(t, envFile); got != "unset" {
		t.Errorf("verifier got AFM_DIALOG_PHASE %q, want unset", got)
	}
}

func TestRunHelperAgent_NoDialogPhase(t *testing.T) {
	t.Setenv("AFM_DIALOG_PHASE", "planning")
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")

	ex := executor.New(executor.Config{
		Command:     dialogPhaseEnvScript(t, envFile, helperValidResultJSON),
		IdleTimeout: 5 * time.Second,
		StageDir:    "/leaked/stage/dir",
		DialogPhase: "implementation",
	})
	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", filepath.Join(dir, "helper.log"), filepath.Join(dir, "result.json")); err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}

	if got := readDialogPhaseEnv(t, envFile); got != "unset" {
		t.Errorf("helper got AFM_DIALOG_PHASE %q, want unset", got)
	}
}
