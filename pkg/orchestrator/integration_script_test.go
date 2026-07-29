package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestIntegration_ScriptStage_HappyPath(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	marker := filepath.Join(rootDir, "script-ran.marker")

	stages := []flow.Stage{{
		ID:     "notify",
		Name:   "Notify",
		Script: "touch " + marker + "; echo done-output",
	}}

	ids := []string{"notify"}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("orch.Run: %v", err)
	}

	if st := orchestrator.StoreFromOrch(orch).Get("notify"); st != state.StatusDone {
		t.Fatalf("expected status done, got %s", st)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script side effect (marker file) missing: %v", err)
	}

	logData, err := os.ReadFile(filepath.Join(runDir, "notify", "script.log"))
	if err != nil {
		t.Fatalf("script.log missing: %v", err)
	}
	if !strings.Contains(string(logData), "done-output") {
		t.Errorf("script.log missing output: %q", string(logData))
	}
}
