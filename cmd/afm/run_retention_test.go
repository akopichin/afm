package main

// Очистка ранов repeat (S3.T2): удаляются только успешные раны, созданные этим процессом, сверх keep_runs.
// Решение принимается по журналу events.jsonl, а не по памяти процесса.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

// seedRun пишет в base run dir id: стадия s в статусе stage и, если end != "", терминальная запись рана.
func seedRun(t *testing.T, base, id string, stage state.StageStatus, end state.RunStatus) {
	t.Helper()
	store, err := state.Open(filepath.Join(base, id), []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	if stage != state.StatusPending {
		if err := store.Apply(&state.Transition{StageID: "s", From: state.StatusPending, To: stage, Event: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if end != "" {
		if err := store.EndRun(end); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func seedSuccessful(t *testing.T, base string, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("rp-20260101-10000%d-aaaa", i)
		seedRun(t, base, ids[i], state.StatusDone, state.RunStatusFinished)
	}
	return ids
}

// sameRuns сравнивает наборы run ID: имена ранов одной секунды сортируются по случайному суффиксу.
func sameRuns(got, want []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want)))
}

func existingRuns(t *testing.T, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.Name())
	}
	return ids
}

func TestPruneRuns_KeepsLastSuccessful(t *testing.T) {
	for _, tt := range []struct {
		keep        int
		wantRemoved int
	}{{keep: 2, wantRemoved: 2}, {keep: 1, wantRemoved: 3}, {keep: 4, wantRemoved: 0}, {keep: 10, wantRemoved: 0}, {keep: 0, wantRemoved: 0}} {
		t.Run(fmt.Sprintf("keep=%d", tt.keep), func(t *testing.T) {
			base := t.TempDir()
			ids := seedSuccessful(t, base, 4)

			removed, err := pruneRuns(base, ids, tt.keep)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(removed, ids[:tt.wantRemoved]) {
				t.Errorf("removed = %v, want the oldest %d: %v", removed, tt.wantRemoved, ids[:tt.wantRemoved])
			}
			if got := existingRuns(t, base); !slices.Equal(got, ids[tt.wantRemoved:]) {
				t.Errorf("runs left = %v, want %v", got, ids[tt.wantRemoved:])
			}
		})
	}
}

func TestPruneRuns_OnlyOwnSuccessfulRuns(t *testing.T) {
	base := t.TempDir()
	own := seedSuccessful(t, base, 3)
	foreign := "rp-20250101-100000-bbbb" // успешный, но создан не этим процессом
	seedRun(t, base, foreign, state.StatusDone, state.RunStatusFinished)
	failed := "rp-20260101-100100-cccc"
	seedRun(t, base, failed, state.StatusFailed, state.RunStatusFailed)
	interrupted := "rp-20260101-100200-dddd"
	seedRun(t, base, interrupted, state.StatusRunning, state.RunStatusInterrupted)
	running := "rp-20260101-100300-eeee" // нет run_finished: ран ещё идёт
	seedRun(t, base, running, state.StatusDone, "")
	doneButFailedStage := "rp-20260101-100400-ffff" // run_finished, но стадия не done
	seedRun(t, base, doneButFailedStage, state.StatusFailed, state.RunStatusFinished)
	created := append(slices.Clone(own), failed, interrupted, running, doneButFailedStage)
	slices.Sort(created)

	removed, err := pruneRuns(base, created, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, own[:2]) {
		t.Fatalf("removed = %v, want the two oldest own successful runs %v", removed, own[:2])
	}
	for _, id := range []string{own[2], foreign, failed, interrupted, running, doneButFailedStage} {
		if _, err := os.Stat(filepath.Join(base, id)); err != nil {
			t.Errorf("run %s must survive: %v", id, err)
		}
	}
}

func TestPruneRuns_DoubtfulJournalIsNotRemoved(t *testing.T) {
	base := t.TempDir()
	ids := seedSuccessful(t, base, 3)
	corrupt := "rp-20260101-100900-0000"
	if err := os.MkdirAll(filepath.Join(base, corrupt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, corrupt, "events.jsonl"), []byte("NOT JSON\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := "rp-20260101-100800-1111" // каталог без журнала
	if err := os.MkdirAll(filepath.Join(base, missing), 0o755); err != nil {
		t.Fatal(err)
	}
	created := append(slices.Clone(ids), corrupt, missing)
	slices.Sort(created)

	removed, err := pruneRuns(base, created, 1)
	if err == nil {
		t.Fatal("unreadable journals must be reported")
	}
	for _, id := range []string{corrupt, missing} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q must name run %s", err, id)
		}
		if _, statErr := os.Stat(filepath.Join(base, id)); statErr != nil {
			t.Errorf("run %s with a doubtful journal was removed: %v", id, statErr)
		}
	}
	if !slices.Equal(removed, ids[:2]) {
		t.Errorf("removed = %v, want readable old runs %v still pruned", removed, ids[:2])
	}
}

func TestPruneRuns_RejectsPathLikeIDs(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "runs")
	seedRun(t, root, "victim", state.StatusDone, state.RunStatusFinished) // рядом с runs/, не внутри
	ids := seedSuccessful(t, base, 1)

	removed, err := pruneRuns(base, []string{"../victim", ids[0]}, 1)
	if err == nil || len(removed) != 0 {
		t.Fatalf("path-like id: removed=%v err=%v, want an error and nothing removed", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "victim")); err != nil {
		t.Errorf("a directory outside runs/ was touched: %v", err)
	}
}

// retentionFlow — repeat с keep_runs и счётчиком run dir'ов, которые видит стадия в момент старта.
func (fx *cycleFixture) retentionFlow(repeat string) string {
	script := fmt.Sprintf("echo tick >> %s\nls %s | wc -l | tr -d ' ' >> %s", fx.tickLog, filepath.Join(fx.root, ".afm", "runs"), filepath.Join(fx.root, "dirs.txt"))
	return fx.flowYAML(flowSpec{repeat: repeat, script: script})
}

func (fx *cycleFixture) dirsSeenByStage() string {
	data, err := os.ReadFile(filepath.Join(fx.root, "dirs.txt"))
	if err != nil {
		fx.t.Fatal(err)
	}
	return strings.ReplaceAll(strings.TrimSpace(string(data)), "\n", ",")
}

func TestRepeatLoop_RetentionKeepsTwoLastRuns(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.retentionFlow("{max_cycles: 3, keep_runs: 2}"))
	project := fx.write("project-file.txt", "keep me")

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	ids := fx.startRunIDs()
	if len(ids) != 3 {
		t.Fatalf("run IDs = %v, want 3", ids)
	}
	if got := fx.runDirs(); !sameRuns(got, ids[1:]) {
		t.Errorf("run dirs after exit = %v, want the last two %v", got, ids[1:])
	}
	if _, err := os.Stat(project); err != nil {
		t.Errorf("project file must survive: %v", err)
	}
	if got := fx.dirsSeenByStage(); got != "1,2,3" {
		t.Errorf("run dirs seen at each stage start = %s, want 1,2,3 (keep 2 has nothing to remove before the third run)", got)
	}
}

func TestRepeatLoop_RetentionRemovesOldRunAfterNextRunExists(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.retentionFlow("{max_cycles: 3, keep_runs: 1}"))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	ids := fx.startRunIDs()
	if got := fx.runDirs(); !sameRuns(got, ids[2:]) {
		t.Errorf("run dirs after exit = %v, want only the last %v", got, ids[2:])
	}
	// Третий ран уже создан, а первый удалён до старта его стадии: 1 (r1), 2 (r1,r2), 2 (r2,r3).
	if got := fx.dirsSeenByStage(); got != "1,2,2" {
		t.Errorf("run dirs seen at each stage start = %s, want 1,2,2", got)
	}
}

func TestRepeatLoop_RetentionKeepRunsZeroKeepsAll(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.retentionFlow("{max_cycles: 3, keep_runs: 0}"))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}
	if got := len(fx.runDirs()); got != 3 {
		t.Fatalf("run dirs = %d, want 3: keep_runs 0 deletes nothing", got)
	}
}

func TestRepeatLoop_RetentionLeavesForeignAndFailedRuns(t *testing.T) {
	fx := newCycleFixture(t)
	base := filepath.Join(fx.root, ".afm", "runs")
	foreign := "rp-20250101-100000-bbbb"
	failed := "rp-20250101-100100-cccc"
	seedRun(t, base, foreign, state.StatusDone, state.RunStatusFinished)
	seedRun(t, base, failed, state.StatusFailed, state.RunStatusFailed)
	fx.forceNewRun = true
	fx.write("flow.yaml", fx.retentionFlow("{max_cycles: 3, keep_runs: 1}"))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	ids := fx.startRunIDs()
	want := []string{foreign, failed, ids[2]}
	if got := fx.runDirs(); !sameRuns(got, want) {
		t.Errorf("run dirs = %v, want foreign and failed runs kept plus the last own run: %v", got, want)
	}
}

func TestRepeatLoop_RetentionNeverRemovesResumedRun(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3, keep_runs: 1}", script: fx.resumableScript()}))
	interrupted := fx.interruptFirstRun()

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	ids := fx.startRunIDs()
	if len(ids) != 4 || ids[1] != interrupted {
		t.Fatalf("run IDs = %v, want the interrupted run resumed first and two new runs", ids)
	}
	if got, want := fx.runDirs(), []string{interrupted, ids[3]}; !sameRuns(got, want) {
		t.Errorf("run dirs = %v, want the resumed run kept plus the last new run: %v", got, want)
	}
}

func TestRepeatLoop_RetentionNotAppliedWhenNextRunIsNotCreated(t *testing.T) {
	fx := newCycleFixture(t)
	script := fmt.Sprintf("echo tick >> %[1]s\n[ $(wc -l < %[1]s) -lt 2 ] || cp %[2]s %[3]s", fx.tickLog, filepath.Join(fx.root, "next.yaml"), fx.flowPath)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{keep_runs: 1}", script: script}))
	fx.write("next.yaml", "name: [broken\n")

	err := fx.runWithTimeout(testCycleConfig())
	if err == nil || !strings.Contains(err.Error(), "cycle 3 not started") {
		t.Fatalf("error = %v, want the reload failure of cycle 3", err)
	}
	if got := len(fx.runDirs()); got != 2 {
		t.Fatalf("run dirs = %d, want 2: nothing is pruned until the next run exists", got)
	}
}

func TestRepeatLoop_RetentionSkippedAfterFailedCycle(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.failingSecondStage("{keep_runs: 1}"))

	if err := fx.runWithTimeout(testCycleConfig()); err == nil {
		t.Fatal("failed cycle must end with an error")
	}
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want the failed run kept", got)
	}
}

func TestRepeatLoop_NoRepeatNoRetention(t *testing.T) {
	fx := newCycleFixture(t)
	base := filepath.Join(fx.root, ".afm", "runs")
	seedRun(t, base, "rp-20250101-100000-bbbb", state.StatusDone, state.RunStatusFinished)
	fx.forceNewRun = true
	fx.write("flow.yaml", fx.flowYAML(flowSpec{}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}
	if got := len(fx.runDirs()); got != 2 {
		t.Fatalf("run dirs = %d, want 2: a single pass never prunes", got)
	}
}

func TestPruneRuns_ErrorIsVisibleNotFatal(t *testing.T) {
	base := t.TempDir()
	ids := seedSuccessful(t, base, 3)
	if err := os.Remove(filepath.Join(base, ids[0], "events.jsonl")); err != nil {
		t.Fatal(err)
	}

	_, err := pruneRuns(base, ids, 1)
	if err == nil || !strings.Contains(err.Error(), ids[0]) {
		t.Fatalf("error = %v, want one naming %s", err, ids[0])
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v must wrap the cause", err)
	}
}

func TestRepeatLoop_RetentionDoesNotWarnAboutRunsItAlreadyPruned(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.retentionFlow("{max_cycles: 3, keep_runs: 1}"))

	stderr := captureStream(t, &os.Stderr, func() {
		if err := fx.runWithTimeout(testCycleConfig()); err != nil {
			t.Error(err)
		}
	})

	if stderr != "" {
		t.Fatalf("stderr = %q, want no warnings: the pruned run was removed on purpose", stderr)
	}
	if got := fx.created; len(got) != 1 {
		t.Errorf("runs still tracked after exit = %v, want only the last one", got)
	}
}

func TestRepeatLoop_RetentionPrunesWhenReloadStopsRepeat(t *testing.T) {
	fx := newCycleFixture(t)
	script := fmt.Sprintf("echo tick >> %[1]s\n[ $(wc -l < %[1]s) -lt 2 ] || cp %[2]s %[3]s", fx.tickLog, filepath.Join(fx.root, "next.yaml"), fx.flowPath)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{keep_runs: 1}", script: script}))
	// Второй цикл подменяет flow.yaml: repeat выключен, повторение заканчивается через stopReason.
	fx.write("next.yaml", fx.flowYAML(flowSpec{repeat: "false"}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	ids := fx.startRunIDs()
	if len(ids) != 2 {
		t.Fatalf("run IDs = %v, want 2 cycles", ids)
	}
	if got := fx.runDirs(); !sameRuns(got, ids[1:]) {
		t.Fatalf("run dirs after exit = %v, want only the last run %v", got, ids[1:])
	}
}
