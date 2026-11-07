package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm-engine/testkit/engine"
	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/pkg/server"
)

// Ростер дашборда называет бокового агента Assistant, а его исполнителя — отдельным полем, и в новом ране, и в
// ране, сохранённом до переименования (display_name там — команда). Повторный старт не переписывает сохранённую регистрацию.
func TestStartSideAgent_RosterNamesAssistantAndItsExecutor(t *testing.T) {
	cases := []struct {
		name, savedName, command, wantCommandName string
	}{
		{"new run", "", "codex-as-claude", "Codex"},
		{"run saved before the rename", "claude", "claude", "Claude Code"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			afmRoot := t.TempDir()
			paths := engine.Paths{
				AfmRoot: afmRoot,
				AfmDir:  filepath.Join(afmRoot, config.AfmDir),
				RunsDir: filepath.Join(afmRoot, config.AfmDir, "runs"),
			}
			const runName = "names"
			runID := runName + "-20260101-100000-aaaa"
			runDir := filepath.Join(paths.RunsDir, runID)
			f := &flow.Flow{Name: runName, Stages: []flow.Stage{{ID: "impl", Name: "impl", Agents: []flow.AgentType{flow.AgentAuto}}}}
			store, err := implstate.Open(runDir, []string{"impl"})
			if err != nil {
				t.Fatal(err)
			}
			if c.savedName != "" {
				seedSavedSideParticipant(t, store, runID, c.savedName)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(runDir, "events.jsonl")
			journal, _ := os.ReadFile(journalPath)

			cfg := config.Default()
			cfg.Client.Command = c.command
			run, err := engine.Open(context.Background(), engine.Spec{Flow: f, Config: cfg.Execution()}, engine.Options{
				Paths: paths, RunName: runName, Policies: engine.Policies{AssistantAvailable: true},
				Stdout: io.Discard, Stderr: io.Discard,
			})
			if err != nil {
				t.Fatalf("engine.Open: %v", err)
			}
			t.Cleanup(func() { _ = run.Close() })
			if run.Assistant() == nil {
				t.Fatal("Open did not create the Assistant")
			}
			read := run.Read()
			dashboard := httptest.NewServer(server.New(server.Config{
				RunDir: runDir, Store: read, Stages: registryStageProvider{reader: read}, Mesh: meshServerActions(run.Mesh()),
			}).Handler())
			t.Cleanup(dashboard.Close)

			type rosterEntry struct {
				ID          string `json:"id"`
				Alias       string `json:"alias"`
				DisplayName string `json:"display_name"`
				Command     string `json:"command"`
				CommandName string `json:"command_name"`
			}
			var roster struct {
				Participants []rosterEntry `json:"participants"`
			}
			decodeDashboardJSON(t, dashboard.URL+"/api/mesh/participants", &roster)
			found := false
			for _, p := range roster.Participants {
				if p.ID != "side:"+runID {
					continue
				}
				found = true
				if p.DisplayName != "Assistant" || p.Alias != "assistant" || p.Command != c.command || p.CommandName != c.wantCommandName {
					t.Errorf("roster entry = %+v, want the Assistant @assistant running %s (%s)", p, c.command, c.wantCommandName)
				}
			}
			if !found {
				t.Fatalf("roster %+v has no side:%s", roster.Participants, runID)
			}

			after, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if n := bytes.Count(after, []byte(`"op_kind":"mesh_register_side_agent"`)); n != 1 {
				t.Errorf("events.jsonl holds %d side registrations, want 1", n)
			}
			if c.savedName != "" && !bytes.HasPrefix(after, journal) {
				t.Errorf("starting over a saved registration rewrote its journal entry:\nbefore %s\nafter  %s", journal, after)
			}
		})
	}
}
