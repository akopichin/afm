package memorypipeline

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// touch создаёт пустой файл path (и родительские директории при необходимости).
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSourceInventory_ClassifiesAgentSessionFiles(t *testing.T) {
	dir := t.TempDir()
	agentFiles := []string{
		"planning.log", "planning-reprompt.log", "planning-revision.log", "planning.stderr.log",
		"implementation.log", "implementation-feedback.log", "implementation.stderr.log",
		"review.log", "review-feedback.log", "review.stderr.log",
		"autonomous.log", "autonomous-feedback.log", "autonomous.stderr.log",
		"planning.jsonl", "planning-reprompt.jsonl", "planning-revision.jsonl",
		"implementation.jsonl", "review.jsonl", "autonomous.jsonl",
		"plan.md", "plan.v1.md", "plan.v2.md", "plan.v10.md",
		"execution_summary.md",
		"planning.q1.question.json", "planning.q1.answer.json", "planning.dialog.jsonl",
	}
	for _, f := range agentFiles {
		touch(t, filepath.Join(dir, f))
	}

	inv, err := SourceInventory(dir)
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	if len(inv.AgentSessions) != len(agentFiles) {
		t.Fatalf("AgentSessions = %v, want %d entries for %v", inv.AgentSessions, len(agentFiles), agentFiles)
	}
	for _, f := range agentFiles {
		want := filepath.Join(dir, f)
		if !slices.Contains(inv.AgentSessions, want) {
			t.Errorf("AgentSessions missing %q; got %v", want, inv.AgentSessions)
		}
	}
	if len(inv.Supplemental) != 0 {
		t.Errorf("Supplemental = %v, want empty", inv.Supplemental)
	}
	if !inv.HasAgentSession() {
		t.Error("HasAgentSession() = false, want true")
	}
}

func TestSourceInventory_ClassifiesSupplementalFiles(t *testing.T) {
	dir := t.TempDir()
	supp := []string{"prenote.md", "feedback.md", "before.log", "after.log", "before.stderr.log", "after.stderr.log"}
	for _, f := range supp {
		touch(t, filepath.Join(dir, f))
	}

	inv, err := SourceInventory(dir)
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	if len(inv.AgentSessions) != 0 {
		t.Errorf("AgentSessions = %v, want empty", inv.AgentSessions)
	}
	if len(inv.Supplemental) != len(supp) {
		t.Fatalf("Supplemental = %v, want %d entries for %v", inv.Supplemental, len(supp), supp)
	}
	for _, f := range supp {
		want := filepath.Join(dir, f)
		if !slices.Contains(inv.Supplemental, want) {
			t.Errorf("Supplemental missing %q; got %v", want, inv.Supplemental)
		}
	}
}

func TestSourceInventory_ExcludesMemoryArtifactsAndDir(t *testing.T) {
	dir := t.TempDir()
	excluded := []string{
		"reflect_dataset.yaml", "reflect.log", "reflect.stderr.log",
		"aggregate.log", "prioritize.log", "update.log",
		"patterns.md", "prioritized.md", "high.md",
	}
	for _, f := range excluded {
		touch(t, filepath.Join(dir, f))
	}
	// А также вложенная директория memory-rebuild/ с файлами внутри —
	// сама директория (и всё под ней) должна быть пропущена целиком.
	touch(t, filepath.Join(dir, "memory-rebuild", "notes.md"))
	touch(t, filepath.Join(dir, "planning.log")) // единственный реальный источник

	inv, err := SourceInventory(dir)
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	want := []string{filepath.Join(dir, "planning.log")}
	if !slices.Equal(inv.AgentSessions, want) {
		t.Errorf("AgentSessions = %v, want %v", inv.AgentSessions, want)
	}
	if len(inv.Supplemental) != 0 {
		t.Errorf("Supplemental = %v, want empty", inv.Supplemental)
	}
}

// TestSourceInventory_ScriptOnlyStageHasNoAgentSession покрывает review #9:
// стадия только с before.log/after.log/script.log (скрипт-стадия без
// реального агента) не должна считаться имеющей агентскую сессию.
func TestSourceInventory_ScriptOnlyStageHasNoAgentSession(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"before.log", "after.log", "script.log"} {
		touch(t, filepath.Join(dir, f))
	}

	inv, err := SourceInventory(dir)
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	if inv.HasAgentSession() {
		t.Errorf("HasAgentSession() = true, want false (script-only stage): %v", inv.AgentSessions)
	}
	// script.log сам по себе не входит ни в одну из групп — это не источник
	// для reflect-агента.
	if slices.Contains(inv.All(), filepath.Join(dir, "script.log")) {
		t.Errorf("script.log must not be classified into either group: %v", inv.All())
	}
}

func TestSourceInventory_ResultsAbsoluteAndSorted(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"review.log", "autonomous.log", "plan.md", "execution_summary.md"} {
		touch(t, filepath.Join(dir, f))
	}
	for _, f := range []string{"feedback.md", "before.log"} {
		touch(t, filepath.Join(dir, f))
	}

	inv, err := SourceInventory(dir)
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	for _, p := range inv.All() {
		if !filepath.IsAbs(p) {
			t.Errorf("path %q is not absolute", p)
		}
	}
	if !slices.IsSorted(inv.AgentSessions) {
		t.Errorf("AgentSessions not sorted: %v", inv.AgentSessions)
	}
	if !slices.IsSorted(inv.Supplemental) {
		t.Errorf("Supplemental not sorted: %v", inv.Supplemental)
	}
	// All() группирует agent-session сначала, затем supplemental (обе группы
	// уже отсортированы внутри себя) — не единый глобальный сорт.
	all := inv.All()
	wantAll := append(append([]string{}, inv.AgentSessions...), inv.Supplemental...)
	if !slices.Equal(all, wantAll) {
		t.Errorf("All() = %v, want agent-sessions-first order %v", all, wantAll)
	}
}

func TestSourceInventory_SkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on windows")
	}
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "planning.log"))
	if err := os.Symlink(filepath.Join(dir, "planning.log"), filepath.Join(dir, "execution_summary.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	inv, err := SourceInventory(dir)
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	want := []string{filepath.Join(dir, "planning.log")}
	if !slices.Equal(inv.AgentSessions, want) {
		t.Errorf("AgentSessions = %v, want %v (symlink execution_summary.md must be skipped)", inv.AgentSessions, want)
	}
}

// TestSourceInventory_LstatErrorIsNotSwallowed покрывает review #9: путь
// строгий (без сети) — Lstat-ошибка на конкретной записи должна вернуться
// вызывающему коду, а не быть тихо пропущена. Директория с правами r--
// (без x) позволяет os.ReadDir перечислить имена, но блокирует Lstat по
// пути dir/name — воспроизводимо без гонок и без root.
func TestSourceInventory_LstatErrorIsNotSwallowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission bits don't apply on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "planning.log"))
	if err := os.Chmod(dir, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("restore chmod: %v", err)
		}
	}()

	_, err := SourceInventory(dir)
	if err == nil {
		t.Fatal("SourceInventory: want Lstat error, got nil")
	}
}

func TestSourceInventory_EmptyStageDirIsFine(t *testing.T) {
	dir := t.TempDir()
	inv, err := SourceInventory(dir)
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	if inv.HasAgentSession() || len(inv.All()) != 0 {
		t.Errorf("empty dir should yield empty inventory, got %+v", inv)
	}
}

func TestSourceInventory_MissingStageDirIsFine(t *testing.T) {
	inv, err := SourceInventory(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("SourceInventory: %v", err)
	}
	if inv.HasAgentSession() || len(inv.All()) != 0 {
		t.Errorf("missing dir should yield empty inventory, got %+v", inv)
	}
}
