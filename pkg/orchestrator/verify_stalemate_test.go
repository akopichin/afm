package orchestrator

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestWorkingTreeFingerprint_OversizedDiffNotOk — tracked-файл, перезаписанный
// содержимым сверх maxGitOutputBytes, даёт git diff --binary больше капа →
// ok=false (fail-safe, тот же консервативный исход, что и для untracked-капа;
// защита от OOM-буферизации большого бинарного diff). Содержимое —
// несжимаемое (random), иначе zlib внутри git diff --binary схлопнул бы его
// обратно под кап.
func TestWorkingTreeFingerprint_OversizedDiffNotOk(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	big := make([]byte, maxGitOutputBytes+1)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), big, 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := fp(t, dir); ok {
		t.Error("expected ok==false for an oversized tracked diff")
	}
}

// newGitRepoWithCommit creates a temp git repo with one committed tracked file
// and returns its path. Reuses the package-local runGit test helper
// (reflection_test.go), setting a committer identity so commit does not fail.
func newGitRepoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func fp(t *testing.T, dir string) (string, bool) {
	t.Helper()
	return workingTreeFingerprint(dir)
}

// TestWorkingTreeFingerprint_StableWhenUnchanged — два последовательных вызова на
// неизменном дереве дают один и тот же хэш.
func TestWorkingTreeFingerprint_StableWhenUnchanged(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	a, okA := fp(t, dir)
	b, okB := fp(t, dir)
	if !okA || !okB {
		t.Fatalf("expected ok, got %v/%v", okA, okB)
	}
	if a != b {
		t.Errorf("fingerprint changed on unchanged tree: %s vs %s", a, b)
	}
}

// TestWorkingTreeFingerprint_TrackedEditDiffers — правка отслеживаемого файла
// меняет хэш.
func TestWorkingTreeFingerprint_TrackedEditDiffers(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	before, _ := fp(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	after, ok := fp(t, dir)
	if !ok {
		t.Fatal("expected ok")
	}
	if before == after {
		t.Error("fingerprint unchanged after editing a tracked file")
	}
}

// TestWorkingTreeFingerprint_UntrackedContentEditDiffers — правка СОДЕРЖИМОГО
// untracked-файла на месте (имя не меняется) меняет хэш. Это ловит ловушку
// path-only (git status --porcelain): plan-стадия редактирует untracked
// ./dev-plan.md на месте — path-only отпечаток выглядел бы одинаково.
func TestWorkingTreeFingerprint_UntrackedContentEditDiffers(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	untracked := filepath.Join(dir, "dev-plan.md")
	if err := os.WriteFile(untracked, []byte("round 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := fp(t, dir)
	if err := os.WriteFile(untracked, []byte("round 2 — more text\n"), 0644); err != nil {
		t.Fatal(err)
	}
	after, ok := fp(t, dir)
	if !ok {
		t.Fatal("expected ok")
	}
	if before == after {
		t.Error("fingerprint unchanged after editing untracked file content in place")
	}
}

// TestWorkingTreeFingerprint_StagedUnstagedCancellationDiffers — staged и
// unstaged изменения, взаимно компенсирующие друг друга так, что worktree ==
// HEAD, всё равно дают хэш, отличный от чистого дерева: индекс грязный.
// Единый `git diff HEAD` схлопнул бы их и ложно сигналил "без изменений".
func TestWorkingTreeFingerprint_StagedUnstagedCancellationDiffers(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	clean, _ := fp(t, dir)

	path := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(path, []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt") // staged: v1 → v2
	if err := os.WriteFile(path, []byte("v1\n"), 0644); err != nil {
		t.Fatal(err) // unstaged: v2 → v1; worktree content back to HEAD, index dirty
	}

	dirty, ok := fp(t, dir)
	if !ok {
		t.Fatal("expected ok")
	}
	if dirty == clean {
		t.Error("fingerprint of a dirty index equals clean tree (staged/unstaged cancellation not captured)")
	}
}

// TestWorkingTreeFingerprint_CommittedChangeDiffers — коммит между снимками
// меняет HEAD, значит и хэш (HEAD входит в отпечаток). Без этого реально
// закоммиченное изменение между двумя чистыми снимками выглядело бы неизменным.
func TestWorkingTreeFingerprint_CommittedChangeDiffers(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	before, _ := fp(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-q", "-m", "second")
	after, ok := fp(t, dir)
	if !ok {
		t.Fatal("expected ok")
	}
	if before == after {
		t.Error("fingerprint unchanged after a commit (HEAD not part of fingerprint)")
	}
}

// TestWorkingTreeFingerprint_AfmChurnExcluded — запись в НЕ-игнорируемый .afm/
// между снимками НЕ меняет хэш: afm переписывает свой state-tree каждый раунд,
// иначе stalemate не сработал бы никогда.
func TestWorkingTreeFingerprint_AfmChurnExcluded(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	if err := os.MkdirAll(filepath.Join(dir, ".afm", "runs", "r1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".afm", "runs", "r1", "events.jsonl"), []byte("round1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before, ok := fp(t, dir)
	if !ok {
		t.Fatal("expected ok")
	}
	if err := os.WriteFile(filepath.Join(dir, ".afm", "runs", "r1", "events.jsonl"), []byte("round1\nround2\nround3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	after, ok := fp(t, dir)
	if !ok {
		t.Fatal("expected ok")
	}
	if before != after {
		t.Error("fingerprint changed after .afm/ churn (should be excluded)")
	}
}

// TestWorkingTreeFingerprint_NonGitDir — каталог вне git-дерева → ok==false
// (patience молча выключается для стадии).
func TestWorkingTreeFingerprint_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	if _, ok := fp(t, dir); ok {
		t.Error("expected ok==false for a non-git directory")
	}
}

// TestWorkingTreeFingerprint_OversizedUntracked — untracked-файл больше лимита →
// ok==false: не можем доказать "без изменений", не рискуем ложным stalemate.
func TestWorkingTreeFingerprint_OversizedUntracked(t *testing.T) {
	dir := newGitRepoWithCommit(t)
	big := make([]byte, maxUntrackedFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "huge.bin"), big, 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := fp(t, dir); ok {
		t.Error("expected ok==false for an oversized untracked file")
	}
}
