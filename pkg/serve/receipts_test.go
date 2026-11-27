package serve

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

var baseParams = LaunchParams{Project: "app", Pipeline: "dev", Mode: "dashboard", MCPAllow: []string{"read", "runs"}}

func TestLaunchIDIsStableHexOfRequestID(t *testing.T) {
	// sha256("abc")
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := LaunchID("abc"); got != want {
		t.Errorf("LaunchID = %s", got)
	}
}

func TestParamsHash(t *testing.T) {
	same := baseParams
	same.MCPAllow = []string{"runs", "read"}
	if ParamsHash(same) != ParamsHash(baseParams) {
		t.Error("MCPAllow order must not change the hash")
	}
	for name, mutate := range map[string]func(*LaunchParams){
		"project":  func(p *LaunchParams) { p.Project = "other" },
		"mode":     func(p *LaunchParams) { p.Mode = "core" },
		"worktree": func(p *LaunchParams) { p.Worktree = "w" },
		"mcp":      func(p *LaunchParams) { p.MCPAllow = []string{"read"} },
		"task":     func(p *LaunchParams) { p.TaskSHA256 = TaskDigest("do it") },
	} {
		p := baseParams
		mutate(&p)
		if ParamsHash(p) == ParamsHash(baseParams) {
			t.Errorf("%s must change the hash", name)
		}
	}
	if TaskDigest("a") == TaskDigest("b") || TaskDigest("") != "" {
		t.Error("TaskDigest must separate tasks and leave an empty task empty")
	}
}

func TestCreateReceiptIsIdempotent(t *testing.T) {
	h, _ := acquire(t)
	first, created, err := h.CreateReceipt("req-1", baseParams, LaunchIdentity{}, t0)
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	if first.State != StateAccepted || first.LaunchID != LaunchID("req-1") {
		t.Errorf("receipt = %+v", first)
	}
	again, created, err := h.CreateReceipt("req-1", baseParams, LaunchIdentity{}, t0.Add(time.Hour))
	if err != nil || created {
		t.Fatalf("repeat: created=%v err=%v", created, err)
	}
	if again.LaunchID != first.LaunchID || !again.CreatedAt.Equal(t0) {
		t.Errorf("repeat must return the original receipt, got %+v", again)
	}
}

func TestCreateReceiptConflictOnDifferentParams(t *testing.T) {
	h, _ := acquire(t)
	if _, _, err := h.CreateReceipt("req-1", baseParams, LaunchIdentity{}, t0); err != nil {
		t.Fatal(err)
	}
	other := baseParams
	other.Mode = "core"
	got, created, err := h.CreateReceipt("req-1", other, LaunchIdentity{}, t0)
	if !errors.Is(err, ErrRequestConflict) || created {
		t.Fatalf("created=%v err=%v, want ErrRequestConflict", created, err)
	}
	if got.Params.Mode != "dashboard" {
		t.Errorf("conflict must expose the stored receipt, got %+v", got)
	}
}

func TestCreateReceiptSurvivesCrashBeforeLink(t *testing.T) {
	h, dir := acquire(t)
	launches := filepath.Join(dir, launchesDirName)
	// A crash between writing the temp file and linking leaves only a partial temp file behind.
	stray := filepath.Join(launches, "."+LaunchID("req-1")+".tmp-crash")
	if err := os.WriteFile(stray, []byte(`{"launch_id": "tru`), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, err := h.Receipts(); err != nil || len(list) != 0 {
		t.Fatalf("stray temp must not be a receipt: %v, %v", list, err)
	}
	if _, created, err := h.CreateReceipt("req-1", baseParams, LaunchIdentity{}, t0); err != nil || !created {
		t.Fatalf("create after crash: created=%v err=%v", created, err)
	}
	h.Close()
	h2, err := AcquireHost(dir, 1, t0)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if _, err := os.Stat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp must be removed on acquire: %v", err)
	}
}

func TestUpdateReceiptPersistsTransition(t *testing.T) {
	h, dir := acquire(t)
	r, _, err := h.CreateReceipt("req-1", baseParams, LaunchIdentity{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	code := 3
	later := t0.Add(time.Minute)
	if _, err := h.UpdateReceipt(r.LaunchID, later, func(r *Receipt) {
		r.State, r.PID, r.ExitCode = StateExited, 99, &code
	}); err != nil {
		t.Fatal(err)
	}
	h.Close()
	h2, err := AcquireHost(dir, 1, t0)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	got, err := h2.Receipt(r.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateExited || got.PID != 99 || *got.ExitCode != 3 || !got.UpdatedAt.Equal(later) || !got.CreatedAt.Equal(t0) {
		t.Errorf("receipt = %+v", got)
	}
}

func TestUpdateReceiptUnknownLaunch(t *testing.T) {
	h, _ := acquire(t)
	_, err := h.UpdateReceipt(LaunchID("nope"), t0, func(*Receipt) {})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want ErrNotExist", err)
	}
}

func TestReceiptsListedOldestFirstWithPrivateMode(t *testing.T) {
	h, dir := acquire(t)
	for i, id := range []string{"b", "a", "c"} {
		if _, _, err := h.CreateReceipt(id, baseParams, LaunchIdentity{}, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := h.Receipts()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.RequestID)
	}
	if !slices.Equal(ids, []string{"b", "a", "c"}) {
		t.Errorf("order = %v", ids)
	}
	info, err := os.Stat(filepath.Join(dir, launchesDirName, LaunchID("b")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("receipt mode = %o, want 600", got)
	}
}

func TestConcurrentCreateYieldsOneWinner(t *testing.T) {
	h, _ := acquire(t)
	const n = 8
	results := make(chan bool, n)
	for range n {
		go func() {
			_, created, err := h.CreateReceipt("same", baseParams, LaunchIdentity{}, t0)
			results <- err == nil && created
		}()
	}
	wins := 0
	for range n {
		if <-results {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("winners = %d, want 1", wins)
	}
}

func TestReceiptOutcomeIsBoundToTheObservedRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		receipt Receipt
		want    string
	}{
		{"older receipt", Receipt{State: StateExited, ProcessGone: true}, ""},
		{"lost before identity known", Receipt{State: StateLost}, outcomeUnknown},
		{"start rejected", Receipt{State: StateFailed}, outcomeLaunchFailed},
		{"failed readiness with live child", Receipt{State: StateFailed, PID: 17}, ""},
		{"earlier repeat cycle", Receipt{OutcomeRunID: "old", Outcome: "finished"}, ""},
		{"same cycle", Receipt{OutcomeRunID: "current", Outcome: "interrupted"}, "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.receipt.outcomeFor("current"); got != tc.want {
				t.Errorf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}
