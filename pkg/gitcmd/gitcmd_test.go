package gitcmd

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSanitizeEnv_StripsGitVars(t *testing.T) {
	in := []string{
		"GIT_DIR=/x",
		"GIT_COMMON_DIR=/y",
		"GIT_INDEX_FILE=/z",
		"PATH=/usr/bin",
		"HOME=/h",
		"GITHUB_TOKEN=t",
	}
	want := []string{
		"PATH=/usr/bin",
		"HOME=/h",
		"GITHUB_TOKEN=t",
	}
	got := SanitizeEnv(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SanitizeEnv(%v) = %v, want %v", in, got, want)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func TestRun_SucceedsInRepo(t *testing.T) {
	dir := initRepo(t)
	stdout, ok, err := Run(context.Background(), dir, "rev-parse", "--git-common-dir")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ok {
		t.Fatal("Run: ok = false, want true")
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("Run: empty stdout")
	}
}

func TestRun_NonZeroExitIsNotAnError(t *testing.T) {
	dir := initRepo(t)
	_, ok, err := Run(context.Background(), dir, "rev-parse", "--verify", "refs/heads/nope")
	if err != nil {
		t.Fatalf("Run: err = %v, want nil", err)
	}
	if ok {
		t.Fatal("Run: ok = true, want false")
	}
}

func TestRun_IgnoresAmbientGitEnv(t *testing.T) {
	dir := initRepo(t)
	t.Setenv("GIT_DIR", "/nonexistent")
	_, ok, err := Run(context.Background(), dir, "rev-parse", "--git-common-dir")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ok {
		t.Fatal("Run: ok = false, want true (ambient GIT_DIR must be ignored)")
	}
}

func TestRun_RespectsContextCancellation(t *testing.T) {
	dir := initRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = Run(ctx, dir, "rev-parse", "--git-common-dir")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return promptly after context cancellation")
	}
	if err == nil {
		t.Fatal("Run: err = nil, want non-nil for cancelled context")
	}
}
