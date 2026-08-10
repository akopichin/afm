package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAllStatuses_MatchesConsts(t *testing.T) {
	want := []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusAwaitingUserInput,
		StatusDone, StatusFailed, StatusHookFailed,
	}
	got := AllStatuses()
	if len(got) != len(want) {
		t.Fatalf("AllStatuses() has %d entries, want %d: %v", len(got), len(want), got)
	}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("AllStatuses()[%d] = %q, want %q", i, got[i], s)
		}
	}
}

func TestAllDone(t *testing.T) {
	s := NewRunState([]string{"a", "b"})
	if s.AllDone() {
		t.Error("should not be done initially")
	}
	s.SetStageStatus("a", StatusDone)
	s.SetStageStatus("b", StatusDone)
	if !s.AllDone() {
		t.Error("should be done when all stages done")
	}
}

func TestSaveFeedback(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	os.MkdirAll(stageDir, 0755)

	err := SaveFeedback(stageDir, "Добавь Redis")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if !strings.Contains(string(data), "Добавь Redis") {
		t.Errorf("feedback not saved: %q", string(data))
	}

	// Second feedback — appended
	err = SaveFeedback(stageDir, "Ещё TTL")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	content := string(data)
	if !strings.Contains(content, "Добавь Redis") || !strings.Contains(content, "Ещё TTL") {
		t.Errorf("second feedback not appended: %q", content)
	}
	if !strings.Contains(content, "revision 2") {
		t.Errorf("missing revision separator: %q", content)
	}
}

func TestAwaitingUserInputStatus(t *testing.T) {
	s := NewRunState([]string{"a"})
	s.SetStageStatus("a", StatusAwaitingUserInput)
	if s.Stages["a"].Status != StatusAwaitingUserInput {
		t.Errorf("expected awaiting_user_input, got %q", s.Stages["a"].Status)
	}
	if s.AllDone() {
		t.Error("awaiting_user_input must not count as done")
	}
}

func TestFindLatestRunDir(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs")

	// run-старый
	old := filepath.Join(base, "myflow-20260101-100000")
	os.MkdirAll(old, 0755)

	// run-новый (алфавитно позже)
	newer := filepath.Join(base, "myflow-20260101-120000")
	os.MkdirAll(newer, 0755)

	got, err := FindLatestRunDir(base, "myflow")
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Errorf("got %q, want %q", got, newer)
	}
}

func TestFindLatestRunDir_NotFound(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs")
	os.MkdirAll(base, 0755)

	_, err := FindLatestRunDir(base, "noflow")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVersionPlan(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	os.MkdirAll(stageDir, 0755)
	planFile := filepath.Join(stageDir, "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan v1"), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := VersionPlan(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("version: got %d, want 1", n)
	}

	// plan.md renamed to plan.v1.md
	if _, err := os.Stat(filepath.Join(stageDir, "plan.v1.md")); err != nil {
		t.Error("plan.v1.md should exist")
	}
	// plan.md no longer exists
	if _, err := os.Stat(planFile); !os.IsNotExist(err) {
		t.Error("plan.md should be removed after versioning")
	}
}

func TestLatestPlanVersion(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		dir := t.TempDir()
		v, content, err := LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 0 || content != "" {
			t.Errorf("got (%d, %q), want (0, \"\")", v, content)
		}
	})

	t.Run("single version", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "plan.v1.md"), []byte("v1 content"), 0644); err != nil {
			t.Fatal(err)
		}
		v, content, err := LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 1 || content != "v1 content" {
			t.Errorf("got (%d, %q), want (1, \"v1 content\")", v, content)
		}
	})

	t.Run("multiple versions with a gap picks the max", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "plan.v1.md"), []byte("v1 content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.v3.md"), []byte("v3 content"), 0644); err != nil {
			t.Fatal(err)
		}
		v, content, err := LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 3 || content != "v3 content" {
			t.Errorf("got (%d, %q), want (3, \"v3 content\")", v, content)
		}
	})

	t.Run("garbage names are ignored", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"plan.vX.md", "plan.v1.txt", "plan.md", "plan.v.md"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("should not count"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		v, content, err := LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 0 || content != "" {
			t.Errorf("got (%d, %q), want (0, \"\") — garbage names must not be counted as versions", v, content)
		}
	})
}

func TestIsIdle_QuestionStatusAlwaysIdle(t *testing.T) {
	stages := map[string]StageState{
		"a": {Status: StatusRunning},
		"b": {Status: StatusAwaitingApproval},
	}
	if !isIdle(stages) {
		t.Error("want idle=true when any stage is awaiting_approval, regardless of another running stage")
	}
}

func TestIsIdle_FailedAloneIsIdle(t *testing.T) {
	stages := map[string]StageState{
		"a": {Status: StatusFailed},
	}
	if !isIdle(stages) {
		t.Error("want idle=true when a stage is failed and nothing else is active")
	}
}

func TestIsIdle_FailedWhileAnotherRunningIsNotIdle(t *testing.T) {
	// Регрессия из use-idle-time.ts: каскадно упавшая downstream-стадия не
	// должна копить Idle, пока другой агент реально работает.
	stages := map[string]StageState{
		"a": {Status: StatusRunning},
		"b": {Status: StatusFailed},
	}
	if isIdle(stages) {
		t.Error("want idle=false when a stage is failed but another is running")
	}
}

func TestIsIdle_RetryingAloneIsNotIdle(t *testing.T) {
	// retrying — пассивный бэкофф-таймер, не «активная работа», но и не Idle
	// сам по себе (это отдельная метрика, см. BackoffOpenSince).
	stages := map[string]StageState{
		"a": {Status: StatusRetrying},
	}
	if isIdle(stages) {
		t.Error("want idle=false for a lone retrying stage")
	}
}

func TestMaxUpdatedAt_ReturnsLatest(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC)
	stages := map[string]StageState{
		"a": {UpdatedAt: t1},
		"b": {UpdatedAt: t2},
	}
	if got := maxUpdatedAt(stages); !got.Equal(t2) {
		t.Errorf("maxUpdatedAt = %v, want %v", got, t2)
	}
}

func TestAccountIdleAndBackoff_AccumulatesIdleGap(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusAwaitingUserInput, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusRunning, t1)
	rs.SetStageStatusAt("a", StatusRunning, t1)

	if rs.IdleAccumulatedMs != 5000 {
		t.Errorf("IdleAccumulatedMs = %d, want 5000", rs.IdleAccumulatedMs)
	}
}

func TestAccountIdleAndBackoff_NoAccumulationWhenNotIdle(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRunning, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusDone, t1)
	rs.SetStageStatusAt("a", StatusDone, t1)

	if rs.IdleAccumulatedMs != 0 {
		t.Errorf("IdleAccumulatedMs = %d, want 0", rs.IdleAccumulatedMs)
	}
}

func TestAccountIdleAndBackoff_ClosesBackoffEpisode(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(3 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRetrying, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusRunning, t1)
	rs.SetStageStatusAt("a", StatusRunning, t1)

	if rs.BackoffAccumulatedMs != 3000 {
		t.Errorf("BackoffAccumulatedMs = %d, want 3000", rs.BackoffAccumulatedMs)
	}
}

func TestAccountIdleAndBackoff_SumsParallelBackoffEpisodes(t *testing.T) {
	// Два параллельных ретрая суммируются, не мёржатся (осознанное упрощение,
	// см. use-status-duration.ts) — сумма может быть чуть больше wall-clock.
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	tCloseA := t0.Add(2 * time.Second)
	tCloseB := t0.Add(4 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRetrying, UpdatedAt: t0},
		"b": {Status: StatusRetrying, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusRunning, tCloseA)
	rs.SetStageStatusAt("a", StatusRunning, tCloseA)
	accountIdleAndBackoff(rs, "b", StatusRunning, tCloseB)
	rs.SetStageStatusAt("b", StatusRunning, tCloseB)

	if rs.BackoffAccumulatedMs != 6000 {
		t.Errorf("BackoffAccumulatedMs = %d, want 6000 (2000+4000)", rs.BackoffAccumulatedMs)
	}
}

func TestRunState_IdleSince_NilWhenNotIdle(t *testing.T) {
	rs := &RunState{Stages: map[string]StageState{"a": {Status: StatusRunning}}}
	if got := rs.IdleSince(); got != nil {
		t.Errorf("IdleSince() = %v, want nil", got)
	}
}

func TestRunState_IdleSince_MatchesLatestUpdatedAtWhenIdle(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusFailed, UpdatedAt: t1},
	}}
	got := rs.IdleSince()
	if got == nil || !got.Equal(t1) {
		t.Errorf("IdleSince() = %v, want %v", got, t1)
	}
}

func TestRunState_BackoffOpenSince_OneEntryPerRetryingStage(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 10, 0, 1, 0, time.UTC)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRetrying, UpdatedAt: t1},
		"b": {Status: StatusDone, UpdatedAt: t2},
		"c": {Status: StatusRetrying, UpdatedAt: t2},
	}}
	got := rs.BackoffOpenSince()
	if len(got) != 2 {
		t.Fatalf("BackoffOpenSince() len = %d, want 2", len(got))
	}
}
