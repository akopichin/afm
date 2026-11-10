package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

func TestClaudeStageExtraArgsKeepProtocol(t *testing.T) {
	for _, mode := range []string{"non-interactive", "interactive", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args.txt")
			body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nprintf '{\"type\":\"result\",\"subtype\":\"success\"}\\n'\n", argsFile)
			if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			stage := flow.Stage{ID: "input", Interactive: mode != "non-interactive"}
			runDir := filepath.Join(dir, "run")
			store, err := state.Open(runDir, []string{stage.ID})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			cfg := config.Default()
			cfg.Client.ExtraArgs = []string{"--model", "sonnet", "--fallback-model", "sonnet"}
			o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: cfg})
			stageDir := filepath.Join(runDir, stage.ID)
			if mode == "fallback" {
				err = os.RemoveAll(stageDir)
			} else {
				err = os.MkdirAll(stageDir, 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := o.runnerFor(stage, phaseAutonomous).RunAgent(context.Background(), phaseAutonomous, stage.ID, "do work", filepath.Join(dir, "agent.log")); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			want := append(executor.DefaultClaudeArgs(), cfg.Client.ExtraArgs...)
			if mode == "interactive" {
				if len(got) != len(want)+2 || got[len(want)] != "--session-id" {
					t.Fatalf("argv = %q, want %q followed by --session-id <id>", got, want)
				}
				got = got[:len(want)]
			}
			if !slices.Equal(got, want) {
				t.Fatalf("Claude argv = %q, want %q", got, want)
			}
		})
	}
}
