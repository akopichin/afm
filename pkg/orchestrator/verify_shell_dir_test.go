package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// A shell verify step checks the code the agents changed, so it runs where they run: the effective project
// root (a worktree when the run uses one), not the directory afm itself was started in.
func TestRunVerification_ShellStepRunsInProjectRoot(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "go test ./..."}}},
	}
	for _, tc := range []struct {
		name    string
		rootDir string
		wantDir string
	}{
		{name: "worktree root", rootDir: "/work/.worktrees/feature", wantDir: "/work/.worktrees/feature"},
		{name: "no root_dir inherits afm cwd", rootDir: "", wantDir: "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := newVerifyTestOrchestrator(t, stage)
			o.opts.RootDir = tc.rootDir
			var gotDir string
			o.runVerifyShell = func(_ context.Context, dir, _, _ string) (string, error) {
				gotDir = dir
				return "ok", nil
			}

			if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
				t.Fatalf("RunVerification: %v", err)
			}
			if gotDir != tc.wantDir {
				t.Errorf("shell verify ran in %q, want %q", gotDir, tc.wantDir)
			}
		})
	}
}

// The same through the real shell runner: the step passes only where the project's marker file exists.
func TestRunVerification_RealShellStepSeesProjectRootFiles(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "test -f only-in-worktree.marker"}}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.opts.RootDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(o.opts.RootDir, "only-in-worktree.marker"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("shell verify must run in the project root and find the marker: %v", err)
	}
}

// Script stages and agents get their stage directory; a shell verify step checks the same stage's files, so it
// must see the absolute directory of the verified stage, not one inherited from afm's own environment.
func TestRunVerification_RealShellStepSeesAbsoluteStageDir(t *testing.T) {
	t.Setenv("AFM_STAGE_DIR", "/stale/inherited")
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: `test -f "$AFM_STAGE_DIR/stage.marker"`}}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)
	o.absRunDir = o.opts.RunDir
	o.opts.RootDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(stageDir, "stage.marker"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("shell verify must see AFM_STAGE_DIR of the verified stage: %v", err)
	}
}
