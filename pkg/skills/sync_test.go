package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/akopichin/afm/assets"
)

// testNames — скиллы тестового набора skillSet, по алфавиту.
var testNames = []string{"afm-check", "afm-init", "afm-run"}

// skillSet — embedded-набор версии version (V1/V2/V3).
func skillSet(version string) fstest.MapFS {
	set := fstest.MapFS{}
	for _, name := range testNames {
		set[name+"/SKILL.md"] = &fstest.MapFile{Data: []byte(name + " " + version), Mode: 0o644}
	}
	return set
}

// setupHome подменяет $HOME временной папкой и очищает унаследованный CODEX_HOME.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	return home
}

func mustSync(t *testing.T, home string, force bool, src fs.FS) Report {
	t.Helper()
	report, err := syncFS(home, force, src)
	if err != nil {
		t.Fatalf("syncFS: %v", err)
	}
	return report
}

func injectRename(t *testing.T, fn func(oldpath, newpath string) error) {
	t.Helper()
	orig := rename
	rename = fn
	t.Cleanup(func() { rename = orig })
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func assertLink(t *testing.T, entry, want string) {
	t.Helper()
	got, err := os.Readlink(entry)
	if err != nil {
		t.Fatalf("readlink %s: %v", entry, err)
	}
	if want = filepath.FromSlash(want); got != want {
		t.Fatalf("link %s = %q, want %q", entry, got, want)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s must not exist (err=%v)", path, err)
	}
}

func assertRealDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s must be a real dir, mode %v", path, info.Mode())
	}
}

// assertStandardLinks: в обеих папках агентов ссылки ../../.afm/skills/<name> читают версию version.
func assertStandardLinks(t *testing.T, home, version string) {
	t.Helper()
	for _, agentDir := range []string{filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".codex", "skills")} {
		for _, name := range testNames {
			entry := filepath.Join(agentDir, name)
			assertLink(t, entry, "../../.afm/skills/"+name)
			if got := readFile(t, filepath.Join(entry, "SKILL.md")); got != name+" "+version {
				t.Fatalf("%s reads %q, want %q", entry, got, name+" "+version)
			}
		}
	}
}

// listTree — все пути под root (без следования симлинкам), относительно root.
func listTree(t *testing.T, root string) map[string]bool {
	t.Helper()
	paths := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		paths[rel] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func containsLine(lines []string, sub string) bool {
	for _, line := range lines {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

func TestSyncRejectsBadHome(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	setupHome(t)
	for _, home := range []string{"", "relative"} {
		if _, err := Sync(home, false); err == nil {
			t.Fatalf("Sync(%q) must fail", home)
		}
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("cwd must stay empty, got %v", entries)
	}
}

// Чистый $HOME, настоящий embedded-набор.
func TestSyncCleanHome(t *testing.T) {
	home := setupHome(t)
	report, err := Sync(home, false)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(assets.SkillsFS, "claude/skills")
	if err != nil || len(entries) == 0 {
		t.Fatalf("embedded skills: %v %v", entries, err)
	}
	for _, e := range entries {
		name := e.Name()
		want, _ := assets.SkillsFS.ReadFile("claude/skills/" + name + "/SKILL.md")
		storeFile := filepath.Join(home, ".afm", "skills", name, "SKILL.md")
		if got := readFile(t, storeFile); got != string(want) {
			t.Fatalf("store %s differs", name)
		}
		assertReadableByAll(t, storeFile)
		assertDirMode755(t, filepath.Dir(storeFile))
		for _, agent := range []string{".claude", ".codex"} {
			entry := filepath.Join(home, agent, "skills", name)
			assertLink(t, entry, "../../.afm/skills/"+name)
			if got := readFile(t, filepath.Join(entry, "SKILL.md")); got != string(want) {
				t.Fatalf("%s reads wrong content", entry)
			}
		}
	}
	if len(report.Installed) == 0 {
		t.Fatal("report must list installed skills")
	}
}

func TestSyncCodexHome(t *testing.T) {
	home := setupHome(t)
	codexHome := filepath.Join(home, "cx")
	t.Setenv("CODEX_HOME", codexHome)
	mustSync(t, home, false, skillSet("V1"))
	for _, name := range testNames {
		assertLink(t, filepath.Join(codexHome, "skills", name), "../../.afm/skills/"+name)
		if got := readFile(t, filepath.Join(codexHome, "skills", name, "SKILL.md")); got != name+" V1" {
			t.Fatalf("codex %s reads %q", name, got)
		}
	}
	assertAbsent(t, filepath.Join(home, ".codex"))
}

// Относительный CODEX_HOME — ошибка для Codex, в cwd ничего не создаётся, Claude-ссылки ставятся.
func TestSyncRelativeCodexHome(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	home := setupHome(t)
	t.Setenv("CODEX_HOME", "relative-codex")

	_, err := syncFS(home, false, skillSet("V1"))
	if err == nil || !strings.Contains(err.Error(), "CODEX_HOME must be an absolute path") {
		t.Fatalf("CODEX_HOME error expected, got %v", err)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("cwd must stay empty, got %v", entries)
	}
	for _, name := range testNames {
		assertLink(t, filepath.Join(home, ".claude", "skills", name), "../../.afm/skills/"+name)
	}
}

// Повторный sync ничего не переписывает.
func TestSyncIdempotent(t *testing.T) {
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	var paths []string
	for _, name := range testNames {
		paths = append(paths,
			filepath.Join(home, ".afm", "skills", name, "SKILL.md"),
			filepath.Join(home, ".claude", "skills", name),
			filepath.Join(home, ".codex", "skills", name))
	}
	before := map[string]os.FileInfo{}
	for _, p := range paths {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = info
	}
	report := mustSync(t, home, false, skillSet("V1"))
	for _, p := range paths {
		after, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before[p], after) || !before[p].ModTime().Equal(after.ModTime()) {
			t.Fatalf("%s was rewritten", p)
		}
	}
	if len(report.Installed) != 0 || len(report.Warnings) != 0 {
		t.Fatalf("second sync must report nothing, got %+v", report)
	}
}

func TestSyncUpdatesStore(t *testing.T) {
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	mustSync(t, home, false, skillSet("V2"))
	assertStandardLinks(t, home, "V2")
	assertReadableByAll(t, filepath.Join(home, ".afm", "skills", "afm-run", "SKILL.md"))
}

// Файл store с актуальным содержимым, но правами 0600 (прежний сбой chmod) получает 0644.
func TestSyncRepairsStoreFilePermissions(t *testing.T) {
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	storeFile := filepath.Join(home, ".afm", "skills", "afm-run", "SKILL.md")
	if err := os.Chmod(storeFile, 0o600); err != nil {
		t.Fatal(err)
	}
	mustSync(t, home, false, skillSet("V1"))
	assertReadableByAll(t, storeFile)
}

// assertReadableByAll: файл store читают агенты других пользователей (в том числе в контейнере).
func assertReadableByAll(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // права Unix на Windows не моделируются
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("%s mode %v, want 0644", path, info.Mode().Perm())
	}
}

// assertDirMode755: папку скилла в store открывают агенты других пользователей.
func assertDirMode755(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // права Unix на Windows не моделируются
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("%s mode %v, want 0755", dir, info.Mode().Perm())
	}
}

// ~/.afm или ~/.afm/skills — симлинк.
func TestSyncRejectsSymlinkedStoreRoots(t *testing.T) {
	for _, rel := range []string{".afm", filepath.Join(".afm", "skills")} {
		t.Run(rel, func(t *testing.T) {
			home := setupHome(t)
			outside := t.TempDir()
			mustSymlink(t, outside, filepath.Join(home, rel))
			before := listTree(t, home)
			if _, err := syncFS(home, false, skillSet("V1")); err == nil {
				t.Fatal("must fail")
			}
			if after := listTree(t, home); len(after) != len(before) {
				t.Fatalf("home changed: %v", after)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatalf("outside dir changed: %v", entries)
			}
		})
	}
}

// Симлинки внутри store заменяются настоящими записями, внешние файлы не меняются.
func TestSyncReplacesSymlinksInsideStore(t *testing.T) {
	t.Run("skill dir", func(t *testing.T) {
		home := setupHome(t)
		outside := t.TempDir()
		writeFile(t, filepath.Join(outside, "SKILL.md"), "external")
		mustSymlink(t, outside, filepath.Join(home, ".afm", "skills", "afm-run"))
		mustSync(t, home, false, skillSet("V1"))
		if got := readFile(t, filepath.Join(outside, "SKILL.md")); got != "external" {
			t.Fatalf("external file changed: %q", got)
		}
		assertRealDir(t, filepath.Join(home, ".afm", "skills", "afm-run"))
		assertStandardLinks(t, home, "V1")
	})
	t.Run("skill file", func(t *testing.T) {
		home := setupHome(t)
		outside := filepath.Join(t.TempDir(), "SKILL.md")
		writeFile(t, outside, "external")
		mustSymlink(t, outside, filepath.Join(home, ".afm", "skills", "afm-run", "SKILL.md"))
		mustSync(t, home, false, skillSet("V1"))
		if got := readFile(t, outside); got != "external" {
			t.Fatalf("external file changed: %q", got)
		}
		info, err := os.Lstat(filepath.Join(home, ".afm", "skills", "afm-run", "SKILL.md"))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("store file must be regular: %v %v", info, err)
		}
		assertStandardLinks(t, home, "V1")
	})
}

func TestSyncRejectsSymlinkedLock(t *testing.T) {
	home := setupHome(t)
	outside := filepath.Join(t.TempDir(), "lock")
	mustSymlink(t, outside, filepath.Join(home, ".afm", "skills.lock"))
	if _, err := syncFS(home, false, skillSet("V1")); err == nil {
		t.Fatal("must fail")
	}
	assertAbsent(t, outside)
	assertAbsent(t, filepath.Join(home, ".afm", "skills"))
	assertAbsent(t, filepath.Join(home, ".claude"))
}

func TestSyncRealDir(t *testing.T) {
	home := setupHome(t)
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	writeFile(t, filepath.Join(entry, "SKILL.md"), "old copy")
	writeFile(t, filepath.Join(entry, "notes.md"), "my notes")

	report := mustSync(t, home, false, skillSet("V1"))
	assertRealDir(t, entry)
	if got := readFile(t, filepath.Join(entry, "notes.md")); got != "my notes" {
		t.Fatalf("notes changed: %q", got)
	}
	if !containsLine(report.Warnings, "afm install-skills --force") {
		t.Fatalf("warning must contain the command, got %v", report.Warnings)
	}

	report = mustSync(t, home, true, skillSet("V1"))
	assertStandardLinks(t, home, "V1")
	if len(report.Backups) != 1 {
		t.Fatalf("one backup expected, got %v", report.Backups)
	}
	backup := report.Backups[0]
	if got := readFile(t, filepath.Join(backup, "notes.md")); got != "my notes" {
		t.Fatalf("backup notes: %q", got)
	}
	if got := readFile(t, filepath.Join(backup, "SKILL.md")); got != "old copy" {
		t.Fatalf("backup SKILL.md: %q", got)
	}
}

// symlinkedClaudeHome делает ~/.claude ссылкой на папку claude-real рядом и возвращает её путь.
func symlinkedClaudeHome(t *testing.T, home string) string {
	t.Helper()
	claudeReal := filepath.Join(home, "claude-real")
	if err := os.MkdirAll(filepath.Join(claudeReal, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, claudeReal, filepath.Join(home, ".claude"))
	return claudeReal
}

// Предупреждение называет путь через ~/.claude, а не раскрытую цель ссылки.
func TestSyncWarningShowsUserFacingPath(t *testing.T) {
	home := setupHome(t)
	claudeReal := symlinkedClaudeHome(t, home)
	writeFile(t, filepath.Join(claudeReal, "skills", "afm-run", "notes.md"), "my notes")

	report := mustSync(t, home, false, skillSet("V1"))
	if !containsLine(report.Warnings, filepath.Join(home, ".claude", "skills", "afm-run")+":") {
		t.Fatalf("warning must name the user-facing path, got %v", report.Warnings)
	}
	if containsLine(report.Warnings, "claude-real") {
		t.Fatalf("warning must not name the link target, got %v", report.Warnings)
	}
}

// Ошибка скилла называет путь через ~/.claude, а не раскрытую цель ссылки.
func TestSyncErrorShowsUserFacingPath(t *testing.T) {
	home := setupHome(t)
	symlinkedClaudeHome(t, home)
	orig := symlink
	symlink = func(string, string) error { return errors.New("injected") }
	t.Cleanup(func() { symlink = orig })

	_, err := syncFS(home, false, skillSet("V1"))
	if err == nil {
		t.Fatal("must fail")
	}
	if !strings.Contains(err.Error(), filepath.Join(home, ".claude", "skills", "afm-run")) {
		t.Fatalf("error must name the user-facing path: %v", err)
	}
	if strings.Contains(err.Error(), "claude-real") {
		t.Fatalf("error must not name the link target: %v", err)
	}
}

// Миграция legacy-папки, затем обновления V2 и V3 через ссылку.
func TestSyncLegacyMigration(t *testing.T) {
	home := setupHome(t)
	for _, name := range testNames {
		writeFile(t, filepath.Join(home, ".claude", "skills", name, "SKILL.md"), name+" V1")
	}
	mustSync(t, home, true, skillSet("V1"))
	assertStandardLinks(t, home, "V1")
	mustSync(t, home, false, skillSet("V2"))
	assertStandardLinks(t, home, "V2")
	mustSync(t, home, false, skillSet("V3"))
	assertStandardLinks(t, home, "V3")
}

// Обычный файл с именем скилла не трогается.
func TestSyncKeepsRegularFile(t *testing.T) {
	home := setupHome(t)
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	writeFile(t, entry, "file")
	for _, force := range []bool{false, true} {
		report := mustSync(t, home, force, skillSet("V1"))
		if got := readFile(t, entry); got != "file" {
			t.Fatalf("file changed: %q", got)
		}
		if !containsLine(report.Warnings, "afm-run") {
			t.Fatalf("warning expected, got %v", report.Warnings)
		}
	}
}

// Подмена записи ссылкой не удалась — папка возвращается из backup.
func TestReplaceWithLinkRestoresDir(t *testing.T) {
	home := setupHome(t)
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	writeFile(t, filepath.Join(entry, "notes.md"), "my notes")
	entryReal := realPath(t, entry)
	injectRename(t, func(oldpath, newpath string) error {
		if strings.HasPrefix(filepath.Base(oldpath), ".afm-tmp-") && newpath == entryReal {
			return errors.New("injected")
		}
		return os.Rename(oldpath, newpath)
	})
	if _, err := syncFS(home, true, skillSet("V1")); err == nil {
		t.Fatal("must fail")
	}
	assertRealDir(t, entry)
	if got := readFile(t, filepath.Join(entry, "notes.md")); got != "my notes" {
		t.Fatalf("notes: %q", got)
	}
	assertNoTemp(t, filepath.Join(home, ".claude", "skills"))
	if backups, _ := filepath.Glob(filepath.Join(home, ".claude", ".afm-skills-backup-*")); len(backups) != 0 {
		t.Fatalf("empty backup must be removed after restore: %v", backups)
	}
}

// realPath раскрывает симлинки (на macOS TempDir лежит под /var -> /private/var): sync работает с
// канонизированными путями, и инъекции сравнивают с ними.
func realPath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	if tmp, _ := filepath.Glob(filepath.Join(dir, ".afm-tmp-*")); len(tmp) != 0 {
		t.Fatalf("temp links left: %v", tmp)
	}
}

// Подмена записи ссылкой и откат не удались — backup цел, путь в ошибке.
func TestReplaceWithLinkKeepsBackupWhenRestoreFails(t *testing.T) {
	home := setupHome(t)
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	writeFile(t, filepath.Join(entry, "notes.md"), "my notes")
	entryReal := realPath(t, entry)
	injectRename(t, func(oldpath, newpath string) error {
		if newpath == entryReal {
			return errors.New("injected")
		}
		return os.Rename(oldpath, newpath)
	})
	_, err := syncFS(home, true, skillSet("V1"))
	if err == nil {
		t.Fatal("must fail")
	}
	backups, _ := filepath.Glob(filepath.Join(realPath(t, home), ".claude", ".afm-skills-backup-*"))
	if len(backups) != 1 {
		t.Fatalf("one backup expected: %v", backups)
	}
	if got := readFile(t, filepath.Join(backups[0], "afm-run", "notes.md")); got != "my notes" {
		t.Fatalf("backup notes: %q", got)
	}
	if !strings.Contains(err.Error(), backups[0]) {
		t.Fatalf("error must contain backup path %s: %v", backups[0], err)
	}
	assertNoTemp(t, filepath.Join(home, ".claude", "skills"))
}

// Ошибка rename в backup (EXDEV) — запись не тронута, остальные скиллы установлены.
func TestReplaceWithLinkBackupRenameFails(t *testing.T) {
	home := setupHome(t)
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	writeFile(t, filepath.Join(entry, "notes.md"), "my notes")
	entryReal := realPath(t, entry)
	injectRename(t, func(oldpath, newpath string) error {
		if oldpath == entryReal {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		return os.Rename(oldpath, newpath)
	})
	_, err := syncFS(home, true, skillSet("V1"))
	if err == nil || !strings.Contains(err.Error(), "afm-run") {
		t.Fatalf("error for afm-run expected: %v", err)
	}
	assertRealDir(t, entry)
	if got := readFile(t, filepath.Join(entry, "notes.md")); got != "my notes" {
		t.Fatalf("notes: %q", got)
	}
	assertNoTemp(t, filepath.Join(home, ".claude", "skills"))
	for _, name := range []string{"afm-check", "afm-init"} {
		assertLink(t, filepath.Join(home, ".claude", "skills", name), "../../.afm/skills/"+name)
	}
	assertLink(t, filepath.Join(home, ".codex", "skills", "afm-run"), "../../.afm/skills/afm-run")
	if backups, _ := filepath.Glob(filepath.Join(home, ".claude", ".afm-skills-backup-*")); len(backups) != 0 {
		t.Fatalf("empty backup must be removed: %v", backups)
	}
}

// Чужой симлинк не трогается.
func TestSyncKeepsForeignSymlink(t *testing.T) {
	home := setupHome(t)
	foreign := t.TempDir()
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	mustSymlink(t, foreign, entry)
	for _, force := range []bool{false, true} {
		report := mustSync(t, home, force, skillSet("V1"))
		assertLink(t, entry, foreign)
		if !containsLine(report.Warnings, "afm-run") {
			t.Fatalf("warning expected, got %v", report.Warnings)
		}
	}
}

// Битые/удалённые ссылки и удалённая папка Codex восстанавливаются.
func TestSyncRepairs(t *testing.T) {
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	if err := os.Remove(filepath.Join(home, ".claude", "skills", "afm-run")); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(home, ".claude", "skills", "afm-init")
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "../../.afm/skills/afm-gone", broken)
	if err := os.RemoveAll(filepath.Join(home, ".codex", "skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".afm", "skills", "afm-check")); err != nil {
		t.Fatal(err)
	}
	mustSync(t, home, false, skillSet("V1"))
	assertStandardLinks(t, home, "V1")
}

// Пересечение корней — ошибка, в $HOME ничего, кроме store; backup не создаётся.
func TestSyncRejectsOverlappingRoots(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{"CODEX_HOME is store", func(t *testing.T, home string) {
			t.Setenv("CODEX_HOME", filepath.Join(home, ".afm", "skills"))
		}},
		{"CODEX_HOME is store in other case", func(t *testing.T, home string) {
			if err := os.MkdirAll(filepath.Join(home, ".afm"), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(home, ".AFM")); err != nil {
				t.Skip("case-sensitive filesystem")
			}
			t.Setenv("CODEX_HOME", filepath.Join(home, ".AFM", "skills"))
		}},
		{"CODEX_HOME is .afm", func(t *testing.T, home string) {
			t.Setenv("CODEX_HOME", filepath.Join(home, ".afm"))
		}},
		{"CODEX_HOME inside claude root", func(t *testing.T, home string) {
			t.Setenv("CODEX_HOME", filepath.Join(home, ".claude", "skills", "x"))
		}},
		{"missing CODEX_HOME inside store", func(t *testing.T, home string) {
			t.Setenv("CODEX_HOME", filepath.Join(home, ".afm", "skills", "missing", "deeper"))
		}},
		{".claude links to .afm", func(t *testing.T, home string) {
			mustSymlink(t, filepath.Join(home, ".afm"), filepath.Join(home, ".claude"))
		}},
		{".claude/skills links into store", func(t *testing.T, home string) {
			mustSymlink(t, filepath.Join(home, ".afm", "skills", "afm-run"), filepath.Join(home, ".claude", "skills"))
		}},
	}
	for _, tc := range cases {
		for _, force := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				home := setupHome(t)
				writeFile(t, filepath.Join(home, ".codex", "skills", "afm-run", "notes.md"), "codex notes")
				tc.setup(t, home)
				before := listTree(t, home)
				if _, err := syncFS(home, force, skillSet("V1")); err == nil || !strings.Contains(err.Error(), "overlap") {
					t.Fatalf("overlap error expected, got %v", err)
				}
				for p := range listTree(t, home) {
					if !before[p] && p != ".afm" && !strings.HasPrefix(p, ".afm"+string(filepath.Separator)) {
						t.Fatalf("created outside store: %s", p)
					}
					if strings.Contains(p, ".afm-skills-backup-") {
						t.Fatalf("backup created: %s", p)
					}
				}
			})
		}
	}
}

// Корень агента содержит $HOME со store: force не уносит $HOME в backup.
func TestSyncRejectsAgentRootContainingStore(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "skills", "afm-run")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", root)
	sentinel := filepath.Join(home, "sentinel.txt")
	writeFile(t, sentinel, "keep")

	if _, err := syncFS(home, true, skillSet("V1")); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error expected, got %v", err)
	}
	if got := readFile(t, sentinel); got != "keep" {
		t.Fatalf("sentinel: %q", got)
	}
	if backups, _ := filepath.Glob(filepath.Join(root, ".afm-skills-backup-*")); len(backups) != 0 {
		t.Fatalf("nothing must be moved to backup: %v", backups)
	}
}

// На ФС, где NFC- и NFD-имена — одна папка (APFS), CODEX_HOME в другой нормализации Unicode,
// указывающий на .afm, — пересечение: force ничего не переносит.
func TestSyncRejectsOverlapDifferingByUnicodeNormalization(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "caf\u00e9")     // NFC
	homeNFD := filepath.Join(base, "cafe\u0301") // NFD
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	nfc, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	nfd, err := os.Stat(homeNFD)
	if err != nil || !os.SameFile(nfc, nfd) {
		t.Skip("filesystem distinguishes NFC and NFD names")
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(homeNFD, ".afm"))
	writeFile(t, filepath.Join(home, ".afm", "skills", "afm-run", "notes.md"), "keep")

	if _, err := syncFS(home, true, skillSet("V1")); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error expected, got %v", err)
	}
	if got := readFile(t, filepath.Join(home, ".afm", "skills", "afm-run", "notes.md")); got != "keep" {
		t.Fatalf("notes: %q", got)
	}
	if backups, _ := filepath.Glob(filepath.Join(home, ".afm", ".afm-skills-backup-*")); len(backups) != 0 {
		t.Fatalf("nothing must be moved to backup: %v", backups)
	}
}

// fsAliases сообщает, считает ли ФС под dir имена a и b одной папкой (APFS: регистр, NFC/NFD, ſ/s).
func fsAliases(t *testing.T, dir, a, b string) bool {
	t.Helper()
	probe := filepath.Join(dir, "alias-probe")
	if err := os.MkdirAll(filepath.Join(probe, a), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(probe) }()
	ia, errA := os.Stat(filepath.Join(probe, a))
	ib, errB := os.Stat(filepath.Join(probe, b))
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// Корни, которые ещё не созданы и совпадают только на уровне ФС (строки разные), ловит физическая
// проверка после создания папок: ни одной ссылки, store не превращается в цикл.
func TestSyncRejectsMissingRootsAliasedByFilesystem(t *testing.T) {
	for _, alias := range []string{"\u017fkills", "SKILLS"} { // ſkills, другой регистр
		t.Run(alias, func(t *testing.T) {
			home := setupHome(t)
			if !fsAliases(t, t.TempDir(), "skills", alias) {
				t.Skipf("filesystem distinguishes skills and %s", alias)
			}
			t.Setenv("CODEX_HOME", filepath.Join(home, ".claude", alias, "afm-run"))

			_, err := syncFS(home, true, skillSet("V1"))
			if err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("overlap error expected, got %v", err)
			}
			if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", "afm-run", "skills")); err == nil {
				// папку Codex создали внутри ссылки Claude — значит, ссылки появились раньше проверки
				t.Fatal("agent dirs must be checked before any link is created")
			}
			if links, _ := filepath.Glob(filepath.Join(home, ".afm", "skills", "*", "*")); len(links) != len(testNames) {
				t.Fatalf("store must contain only SKILL.md files, got %v", links)
			}
		})
	}
}

// Исправная абсолютная ссылка на store, записанная в другом регистре, на регистронезависимой ФС —
// это та же папка store: ссылка afm и приводится к относительной.
func TestSyncNormalizesLinkToStoreInOtherCase(t *testing.T) {
	home := setupHome(t)
	if !fsAliases(t, t.TempDir(), ".afm", ".AFM") {
		t.Skip("case-sensitive filesystem")
	}
	if _, err := syncFS(home, false, skillSet("V1")); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".AFM", "skills", "afm-run"), entry); err != nil {
		t.Fatal(err)
	}
	if _, err := syncFS(home, false, skillSet("V1")); err != nil {
		t.Fatal(err)
	}
	assertLink(t, entry, "../../.afm/skills/afm-run")
}

// Ссылка на путь, отличающийся от store только регистром, — чужая (сравнение с учётом регистра).
func TestPointsIntoIsCaseSensitive(t *testing.T) {
	dir := realPath(t, t.TempDir())
	store := filepath.Join(dir, ".afm", "skills")
	if !pointsInto(dir, filepath.Join(store, "afm-run"), store) {
		t.Fatal("link into store must be recognized")
	}
	if pointsInto(dir, filepath.Join(dir, ".AFM", "skills", "afm-run"), store) {
		t.Fatal("link differing from store only by case must not be recognized")
	}
}

// На регистрозависимой ФС ссылка на соседнюю .AFM/skills/afm-run не трогается.
func TestSyncKeepsSymlinkToOtherCaseDir(t *testing.T) {
	home := setupHome(t)
	other := filepath.Join(home, ".AFM", "skills", "afm-run")
	writeFile(t, filepath.Join(other, "SKILL.md"), "other")
	if _, err := os.Stat(filepath.Join(home, ".afm")); err == nil {
		t.Skip("case-insensitive filesystem")
	}
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	mustSymlink(t, other, entry)
	for _, force := range []bool{false, true} {
		report := mustSync(t, home, force, skillSet("V1"))
		assertLink(t, entry, other)
		if !containsLine(report.Warnings, "not an afm link") {
			t.Fatalf("warning expected, got %v", report.Warnings)
		}
	}
}

func TestWithin(t *testing.T) {
	type withinCase struct {
		p, root string
		want    bool
	}
	cases := []withinCase{
		{"/h/.afm", "/h/.afm", true},
		{"/h/.afm/skills", "/h/.afm", true},
		{"/h/.AFM/skills", "/h/.afm", true},
		{"/h/.afm/skills", "/h/.AFM", true},
		{"/h/.afm2", "/h/.afm", false},
		{"/h", "/h/.afm", false},
		{"/h/.claude/skills", "/h/.afm", false},
		{"/h/.afm", "/", true},
		{"/", "/", true},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases,
			withinCase{`C:\h\.afm`, `C:\`, true},
			withinCase{`D:\h\.afm`, `C:\`, false},
		)
	}
	for _, tc := range cases {
		p, root := filepath.FromSlash(tc.p), filepath.FromSlash(tc.root)
		if got := within(p, root); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", p, root, got, tc.want)
		}
	}
}

// Соседние и отсутствующие папки — не пересечение.
func TestSyncAcceptsDisjointRoots(t *testing.T) {
	t.Run("standard layout exists", func(t *testing.T) {
		home := setupHome(t)
		for _, dir := range []string{".claude/skills", ".codex/skills", ".afm/skills"} {
			if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		mustSync(t, home, false, skillSet("V1"))
		assertStandardLinks(t, home, "V1")
	})
	t.Run("CODEX_HOME next to HOME", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		t.Setenv("HOME", home)
		codexHome := filepath.Join(root, "codex")
		t.Setenv("CODEX_HOME", codexHome)
		mustSync(t, home, false, skillSet("V1"))
		assertLink(t, filepath.Join(codexHome, "skills", "afm-run"), "../../home/.afm/skills/afm-run")
		if got := readFile(t, filepath.Join(codexHome, "skills", "afm-run", "SKILL.md")); got != "afm-run V1" {
			t.Fatalf("codex reads %q", got)
		}
	})
}

// Чужой симлинк вида .afm-skills-backup-* рядом с папкой агента не используется как backup.
func TestSyncBackupIsNewDir(t *testing.T) {
	home := setupHome(t)
	skillsDir := filepath.Join(home, ".claude", "skills")
	writeFile(t, filepath.Join(skillsDir, "afm-run", "notes.md"), "my notes")
	trap := filepath.Join(home, ".claude", ".afm-skills-backup-trap")
	mustSymlink(t, skillsDir, trap)

	report := mustSync(t, home, true, skillSet("V1"))
	if len(report.Backups) != 1 {
		t.Fatalf("one backup expected: %v", report.Backups)
	}
	backupDir := filepath.Dir(report.Backups[0])
	if filepath.Base(backupDir) == filepath.Base(trap) {
		t.Fatalf("backup reused existing entry %s", trap)
	}
	assertRealDir(t, backupDir)
	if got := readFile(t, filepath.Join(report.Backups[0], "notes.md")); got != "my notes" {
		t.Fatalf("backup notes: %q", got)
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			t.Fatalf("only skill links expected in discovery dir, got %s", e.Name())
		}
	}
}

// Исправная абсолютная afm-ссылка заменяется относительной.
func TestSyncNormalizesAbsoluteLink(t *testing.T) {
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, filepath.Join(home, ".afm", "skills", "afm-run"), entry)
	report := mustSync(t, home, false, skillSet("V1"))
	assertStandardLinks(t, home, "V1")
	if !containsLine(report.Installed, "afm-run") {
		t.Fatalf("replacement must be reported: %v", report.Installed)
	}
}

// Afm-ссылка удаляется до rename (Windows не переименовывает поверх ссылки на каталог).
func TestReplaceWithLinkRemovesOldLinkFirst(t *testing.T) {
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	entry := filepath.Join(home, ".claude", "skills", "afm-run")
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, filepath.Join(home, ".afm", "skills", "afm-run"), entry)
	injectRename(t, func(oldpath, newpath string) error {
		if _, err := os.Lstat(newpath); err == nil {
			return errors.New("rename over existing entry")
		}
		return os.Rename(oldpath, newpath)
	})
	mustSync(t, home, false, skillSet("V1"))
	assertLink(t, entry, "../../.afm/skills/afm-run")
}

// Прежние имена скиллов afm — обычные чужие записи: не трогаются и не упоминаются даже при force.
func TestSyncIgnoresOldNames(t *testing.T) {
	home := setupHome(t)
	skillsDir := filepath.Join(home, ".claude", "skills")
	writeFile(t, filepath.Join(skillsDir, "live-run", "SKILL.md"), "live")
	writeFile(t, filepath.Join(skillsDir, "mesh-driven-development", "SKILL.md"), "mesh")
	writeFile(t, filepath.Join(home, ".codex", "skills", "afm", "SKILL.md"), "afm")
	foreign := t.TempDir()
	mustSymlink(t, foreign, filepath.Join(skillsDir, "afm"))

	report := mustSync(t, home, true, skillSet("V1"))
	assertRealDir(t, filepath.Join(skillsDir, "live-run"))
	assertRealDir(t, filepath.Join(skillsDir, "mesh-driven-development"))
	assertRealDir(t, filepath.Join(home, ".codex", "skills", "afm"))
	assertLink(t, filepath.Join(skillsDir, "afm"), foreign)
	if len(report.Warnings) != 0 || len(report.Backups) != 0 {
		t.Fatalf("old names must be ignored silently: %+v", report)
	}
}

// Дерево $HOME, перенесённое в другой корень (как cp -a), резолвит ссылки в новый store.
func TestSyncLinksArePortable(t *testing.T) {
	home := setupHome(t)
	mustSync(t, home, false, skillSet("V1"))
	newHome := filepath.Join(t.TempDir(), "home", "afm")
	copyTree(t, filepath.Join(home, ".afm"), filepath.Join(newHome, ".afm"))
	copyTree(t, filepath.Join(home, ".claude"), filepath.Join(newHome, ".claude"))
	// .codex/skills копируется из другого места, как entrypoint в контейнере.
	elsewhere := filepath.Join(t.TempDir(), "host-codex")
	copyTree(t, filepath.Join(home, ".codex"), elsewhere)
	copyTree(t, elsewhere, filepath.Join(newHome, ".codex"))
	writeFile(t, filepath.Join(newHome, ".afm", "skills", "afm-run", "SKILL.md"), "moved")
	for _, agent := range []string{".claude", ".codex"} {
		if got := readFile(t, filepath.Join(newHome, agent, "skills", "afm-run", "SKILL.md")); got != "moved" {
			t.Fatalf("%s reads %q, want new store", agent, got)
		}
	}
}

// copyTree копирует дерево, сохраняя симлинки как есть (Readlink + Symlink).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target) //nolint:gosec // тестовое дерево в TempDir
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		default:
			data, err := os.ReadFile(p) //nolint:gosec // тестовое дерево в TempDir
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o644)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Симлинк не создаётся — ошибка для агента, никакой копии.
func TestSyncSymlinkFailure(t *testing.T) {
	home := setupHome(t)
	orig := symlink
	symlink = func(string, string) error { return errors.New("injected: symlinks not permitted") }
	t.Cleanup(func() { symlink = orig })
	if _, err := syncFS(home, false, skillSet("V1")); err == nil {
		t.Fatal("must fail")
	}
	for _, agent := range []string{".claude", ".codex"} {
		for _, name := range testNames {
			assertAbsent(t, filepath.Join(home, agent, "skills", name))
		}
		assertNoTemp(t, filepath.Join(home, agent, "skills"))
	}
	if got := readFile(t, filepath.Join(home, ".afm", "skills", "afm-run", "SKILL.md")); got != "afm-run V1" {
		t.Fatalf("store must be written: %q", got)
	}
}

// Два конкурентных sync.
func TestSyncConcurrent(t *testing.T) {
	home := setupHome(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = syncFS(home, false, skillSet("V1"))
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertStandardLinks(t, home, "V1")
}
