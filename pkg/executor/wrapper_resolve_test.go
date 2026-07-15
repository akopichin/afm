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

// TestRunAgentResolvesWrapperCommand verifies the executor resolves a generated
// wrapper command via WrapperDir (absolute path), not just the child env PATH.
// Regression: exec.Command("glm47") does LookPath against the PARENT process's
// PATH, which does NOT contain the wrapper-dir (WrapperDir is only added to
// the child's cmd.Env). Without resolving the command to WrapperDir/<cmd>
// first, a generated wrapper command fails with "executable file not found".
func TestRunAgentResolvesWrapperCommand(t *testing.T) {
	wrapDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	// "mywrap" lives ONLY in wrapDir — not on the test process PATH — so the fix
	// (resolve via WrapperDir) is the only way exec finds it.
	scriptPath := filepath.Join(wrapDir, "mywrap")
	script := "#!/bin/bash\necho ran > " + marker + "\necho '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(t.TempDir(), "impl.log")
	ex := executor.New(executor.Config{
		Command:     "mywrap", // bare name — not on PATH; must resolve via WrapperDir
		WrapperDir:  wrapDir,
		IdleTimeout: 5 * time.Second,
	})

	if err := ex.RunAgent(context.Background(), "implementation", "s1", "do work", logFile); err != nil {
		if strings.Contains(err.Error(), "not found") {
			t.Fatalf("wrapper command not resolved via WrapperDir (parent-PATH LookPath failed): %v", err)
		}
		t.Fatalf("RunAgent: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("wrapper did not run (marker file missing): %v", err)
	}
	if strings.TrimSpace(string(data)) != "ran" {
		t.Errorf("wrapper marker unexpected: %q", data)
	}
}
