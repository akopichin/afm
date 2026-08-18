package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/state"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. check.go/list.go print via fmt.Printf directly
// (not cmd.OutOrStdout()), so capturing the real os.Stdout is required.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// writeEventsLog writes a sequence of transitions to <runDir>/events.jsonl,
// the authoritative source state.LoadRunState (and thus `afm check`) reads
// from — mirroring makeRunState but allowing several transitions per stage.
func writeEventsLog(t *testing.T, runDir string, transitions []state.Transition) {
	t.Helper()
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, tr := range transitions {
		data, err := json.Marshal(tr)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckReadsStatusFromLogNotSnapshot verifies that `afm check` derives
// stage status from events.jsonl (via state.LoadRunState), not from the
// cached state.json snapshot. A couple of transitions are seeded in the log
// (pending -> running -> done), while a deliberately stale state.json claims
// the stage is "failed" — check must report the log's truth ("done"), never
// the stale snapshot's "failed".
func TestCheckReadsStatusFromLogNotSnapshot(t *testing.T) {
	chdirTemp(t)

	runDir := filepath.Join(".afm", "runs", "flow-20260101-120000")
	writeEventsLog(t, runDir, []state.Transition{
		{Seq: 1, Time: time.Now(), StageID: cmdInit, From: state.StatusPending, To: state.StatusRunning, Event: "start"},
		{Seq: 2, Time: time.Now(), StageID: cmdInit, From: state.StatusRunning, To: state.StatusDone, Event: "finish"},
	})

	staleSnapshot := state.RunState{Stages: map[string]state.StageState{
		cmdInit: {Status: state.StatusFailed, UpdatedAt: time.Now()},
	}}
	data, err := json.Marshal(staleSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmd := newCheckCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	if !strings.Contains(out, cmdInit) {
		t.Errorf("output should mention stage %q, got:\n%s", cmdInit, out)
	}
	if !strings.Contains(out, string(state.StatusDone)) {
		t.Errorf("output should show status %q from the log, got:\n%s", state.StatusDone, out)
	}
	if strings.Contains(out, string(state.StatusFailed)) {
		t.Errorf("output should NOT reflect the stale state.json snapshot (%q), got:\n%s", state.StatusFailed, out)
	}
}

// TestCheckDoesNotBlockOnActiveRunLock is the load-bearing guarantee from
// CLAUDE.md: `afm check` is read-only and must NOT take the run's flock, so
// it keeps working while a live `afm run` (or any process holding state.Open)
// has the lock. We hold the lock ourselves via state.Open for the whole test
// and assert check still succeeds and reports the correct status.
func TestCheckDoesNotBlockOnActiveRunLock(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusRunning)

	store, err := state.Open(runDir, []string{cmdInit})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	out := captureStdout(t, func() {
		cmd := newCheckCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check must not block or fail while the run is locked: %v", err)
		}
	})

	if !strings.Contains(out, string(state.StatusRunning)) {
		t.Errorf("expected status %q in output, got:\n%s", state.StatusRunning, out)
	}
}

// TestCheckNoRuns verifies the error path when no run directories exist.
func TestCheckNoRuns(t *testing.T) {
	chdirTemp(t)

	if err := os.MkdirAll(filepath.Join(".afm", "runs"), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := newCheckCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no runs exist")
	}
}

func TestLastLogAction_OnlyPlanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.log"), []byte("line one\nplanning last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "planning last line" {
		t.Errorf("got %q, want %q", got, "planning last line")
	}
}

func TestLastLogAction_ImplementationBeatsPlanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.log"), []byte("planning last line"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "implementation.log"), []byte("implementation last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "implementation last line" {
		t.Errorf("got %q, want %q — later phase must win", got, "implementation last line")
	}
}

func TestLastLogAction_ReviewOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "review.log"), []byte("review last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "review last line" {
		t.Errorf("got %q, want %q — review.log was not covered before this fix", got, "review last line")
	}
}

func TestStatusColor_PausedGetsItsOwnColor(t *testing.T) {
	// paused needs a human decision (Continue), same class as
	// awaiting_approval — it must not fall into the same colorGray bucket as
	// genuinely inert statuses like pending/ready.
	if got := statusColor(state.StatusPaused); got == colorGray {
		t.Error("statusColor(paused) = colorGray, want a distinct attention color")
	}
}
