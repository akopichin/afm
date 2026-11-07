//go:build !windows

package skills

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Файл в store не заменяется (read-only папка скилла) — прежний файл цел, ошибка возвращена.
func TestSyncStoreWriteFailureKeepsOldFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	skillDir := filepath.Join(home, ".afm", "skills", "afm-run")
	if err := os.Chmod(skillDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(skillDir, 0o755) }) //nolint:errcheck,gosec // восстановление прав для очистки TempDir
	if _, err := syncFS(home, false, skillSet("V2")); err == nil {
		t.Fatal("must fail")
	}
	if got := readFile(t, filepath.Join(skillDir, "SKILL.md")); got != "afm-run V1" {
		t.Fatalf("old file must stay: %q", got)
	}
}

// FIFO в store первого скилла — ошибка для него без чтения, sync не зависает. Остальные скиллы
// записываются и получают ссылки, а скилл с ошибкой ссылку не получает.
func TestSyncFIFOInStore(t *testing.T) {
	home := setupHome(t)
	first, last := testNames[0], testNames[len(testNames)-1]
	fifo := filepath.Join(home, ".afm", "skills", first, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := syncFS(home, false, skillSet("V1"))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("must fail for FIFO")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sync hangs on FIFO")
	}
	if got := readFile(t, filepath.Join(home, ".afm", "skills", last, "SKILL.md")); got != last+" V1" {
		t.Fatalf("later skills must be written: %q", got)
	}
	for _, agentDir := range []string{".claude", ".codex"} {
		assertLink(t, filepath.Join(home, agentDir, "skills", last), "../../.afm/skills/"+last)
		assertAbsent(t, filepath.Join(home, agentDir, "skills", first))
	}
}

// Частичный sync оставляет смесь версий, следующий sync восстанавливает набор.
// Сбой первого скилла не мешает обновить следующие.
func TestSyncPartialFailureRecovers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	first, last := testNames[0], testNames[len(testNames)-1]
	firstDir := filepath.Join(home, ".afm", "skills", first)
	if err := os.Chmod(firstDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(firstDir, 0o755) }) //nolint:errcheck,gosec // восстановление прав для очистки TempDir
	if _, err := syncFS(home, false, skillSet("V2")); err == nil {
		t.Fatal("must fail")
	}
	if got := readFile(t, filepath.Join(firstDir, "SKILL.md")); got != first+" V1" {
		t.Fatalf("first skill must stay V1: %q", got)
	}
	if got := readFile(t, filepath.Join(home, ".afm", "skills", last, "SKILL.md")); got != last+" V2" {
		t.Fatalf("last skill must be V2: %q", got)
	}
	if err := os.Chmod(firstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustSync(t, home, false, skillSet("V2"))
	assertStandardLinks(t, home, "V2")
}
