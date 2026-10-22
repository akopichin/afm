package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// T6: o.stageDir абсолютизирует путь на границе с агентом (D4 — остальные ~46 внутренних
// filepath.Join(o.opts.RunDir, s.ID) намеренно не трогаем).

func TestStageDir_IsAbsoluteForRelativeRunDir(t *testing.T) {
	o := New(Options{RunDir: filepath.Join(".afm", "runs", "x"), Config: config.Default()})
	got := o.stageDir("s1")
	if !filepath.IsAbs(got) {
		t.Fatalf("stageDir(%q) = %q, want absolute path", "s1", got)
	}
	if base := filepath.Base(got); base != "s1" {
		t.Fatalf("filepath.Base(stageDir) = %q, want %q", base, "s1")
	}
}

func TestStageDir_AlreadyAbsoluteIsUnchanged(t *testing.T) {
	runDir := t.TempDir()
	o := New(Options{RunDir: runDir, Config: config.Default()})
	got := o.stageDir("s1")
	want := filepath.Join(runDir, "s1")
	if got != want {
		t.Fatalf("stageDir = %q, want %q", got, want)
	}
}

func TestStageDir_SpacesInPathPreserved(t *testing.T) {
	// Реальный путь: после Chdir filepath.Abs опирается на os.Getwd (на macOS /var -> /private/var).
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	relRunDir := filepath.Join("run with spaces", "nested dir")
	o := New(Options{RunDir: relRunDir, Config: config.Default()})
	got := o.stageDir("stage one")
	want := filepath.Join(base, relRunDir, "stage one")
	if got != want {
		t.Fatalf("stageDir = %q, want %q", got, want)
	}
}

// TestStageDir_RelativeJoinResolvesToSameDir закрепляет D4: внутренние call sites, которые остаются
// как есть (`filepath.Join(o.opts.RunDir, id)`), и новый o.stageDir(id) указывают на один и тот же
// каталог — при НЕИЗМЕННОМ CWD процесса (afm никогда не делает os.Chdir).
func TestStageDir_RelativeJoinResolvesToSameDir(t *testing.T) {
	relRunDir := filepath.Join(".afm", "runs", "y")
	o := New(Options{RunDir: relRunDir, Config: config.Default()})

	internalJoin := filepath.Join(relRunDir, "s1") // то, что делают 46 нетронутых call site'ов
	absInternalJoin, err := filepath.Abs(internalJoin)
	if err != nil {
		t.Fatal(err)
	}
	if got := o.stageDir("s1"); got != absInternalJoin {
		t.Fatalf("stageDir() = %q, want %q (same dir as internal filepath.Join under unchanged CWD)", got, absInternalJoin)
	}
}

// TestRunnerConfig_StageDirIsAbsolute проверяет обе точки runner_factory.go, откуда значение уходит в
// AFM_STAGE_DIR (interactive и non-interactive раннеры), при относительном Options.RunDir.
func TestRunnerConfig_StageDirIsAbsolute(t *testing.T) {
	base := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	for _, interactive := range []bool{false, true} {
		name := fmt.Sprintf("interactive=%v", interactive)
		t.Run(name, func(t *testing.T) {
			stageID := fmt.Sprintf("s-%v", interactive)
			relRunDir := filepath.Join(".afm", "runs", fmt.Sprintf("r-%v", interactive))

			store, err := state.Open(relRunDir, []string{stageID})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })

			// Interactive stages need their stage dir to exist before LoadOrCreateSession — otherwise
			// runnerFor falls back to the non-interactive runner (still exercises the T6 fix, but not the
			// interactive call site we're actually trying to pin down).
			if err := os.MkdirAll(filepath.Join(relRunDir, stageID), 0o755); err != nil {
				t.Fatal(err)
			}

			marker := filepath.Join(base, fmt.Sprintf("marker-%v.txt", interactive))
			scriptPath := filepath.Join(base, fmt.Sprintf("agent-%v.sh", interactive))
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$AFM_STAGE_DIR\" > %q\nprintf '{\"type\":\"result\",\"subtype\":\"success\"}\\n'\n", marker)
			if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}

			stage := flow.Stage{ID: stageID, Name: "Backend", Interactive: interactive, Command: scriptPath}
			cfg := config.Default()
			cfg.Executor.IdleTimeout = 5 * time.Second

			o := New(Options{
				RunDir: relRunDir,
				Stages: []flow.Stage{stage},
				Store:  store,
				Config: cfg,
			})

			runner := o.runnerFor(stage, phaseImplementation)
			logFile := filepath.Join(base, fmt.Sprintf("impl-%v.log", interactive))
			if err := runner.RunAgent(context.Background(), phaseImplementation, stage.Name, "do work", logFile); err != nil {
				t.Fatalf("RunAgent: %v", err)
			}

			got, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("AFM_STAGE_DIR not set: %v", err)
			}
			gotPath := string(got)
			if !filepath.IsAbs(gotPath) {
				t.Fatalf("AFM_STAGE_DIR = %q, want absolute path", gotPath)
			}
			wantSuffix := filepath.Join(relRunDir, stageID)
			if !strings.HasSuffix(gotPath, wantSuffix) {
				t.Fatalf("AFM_STAGE_DIR = %q, want suffix %q", gotPath, wantSuffix)
			}
		})
	}
}
