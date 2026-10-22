package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/assets"
	"github.com/akopichin/afm/pkg/config"
)

// embeddedSkillNames — скиллы, вшитые в бинарник.
func embeddedSkillNames(t *testing.T) []string {
	t.Helper()
	entries, err := assets.SkillsFS.ReadDir("claude/skills")
	if err != nil || len(entries) == 0 {
		t.Fatalf("embedded skills: %v %v", entries, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// skillsHome подменяет домашнюю папку ($HOME, на Windows %USERPROFILE%) временной и сбрасывает
// CODEX_HOME, чтобы Codex-ссылки шли в ~/.codex.
func skillsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Fatalf("os.UserHomeDir() = %q, %v; want %q", got, err, home)
	}
	return home
}

func runInstallSkills(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newInstallSkillsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// assertSkillLinked проверяет: SKILL.md лежит в store, а папки скилла у Claude и Codex — ссылки на него.
func assertSkillLinked(t *testing.T, home, name string) {
	t.Helper()
	storeDir := filepath.Join(home, ".afm", "skills", name)
	if _, err := os.Stat(filepath.Join(storeDir, "SKILL.md")); err != nil {
		t.Errorf("в store нет %s/SKILL.md: %v", name, err)
		return
	}
	want, err := filepath.EvalSymlinks(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, agentDir := range []string{".claude", ".codex"} {
		link := filepath.Join(home, agentDir, "skills", name)
		info, err := os.Lstat(link)
		if err != nil {
			t.Errorf("%s: %v", link, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s должен быть симлинком, а это %v", link, info.Mode())
			continue
		}
		got, err := filepath.EvalSymlinks(link)
		if err != nil {
			t.Errorf("%s: ссылка битая: %v", link, err)
			continue
		}
		if got != want {
			t.Errorf("%s ведёт в %s, ожидался %s", link, got, want)
		}
	}
}

func TestInstallSkillsLinksAllSkills(t *testing.T) {
	home := skillsHome(t)
	out, err := runInstallSkills(t)
	if err != nil {
		t.Fatalf("install-skills: %v\n%s", err, out)
	}
	for _, name := range embeddedSkillNames(t) {
		assertSkillLinked(t, home, name)
	}
	header := "afm skills: " + filepath.Join(home, ".afm", "skills") + " -> " +
		filepath.Join(home, ".claude", "skills") + ", " + filepath.Join(home, ".codex", "skills") + "\n"
	if !strings.HasPrefix(out, header) {
		t.Errorf("вывод должен начинаться с %q, вывод:\n%s", header, out)
	}
	if !strings.Contains(out, "+ ") {
		t.Errorf("ожидались строки «+» об установке, вывод:\n%s", out)
	}

	out, err = runInstallSkills(t)
	if err != nil {
		t.Fatalf("install-skills: %v\n%s", err, out)
	}
	if want := header + "all skills up to date\n"; out != want {
		t.Errorf("повторный запуск: вывод %q, ожидался %q", out, want)
	}
}

func TestInstallSkillsRealDirKeptWithoutForce(t *testing.T) {
	home := skillsHome(t)
	realDir := filepath.Join(home, ".claude", "skills", "afm-run")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(realDir, "SKILL.md")
	if err := os.WriteFile(edited, []byte("my edits"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runInstallSkills(t)
	if err != nil {
		t.Fatalf("install-skills: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(edited); string(data) != "my edits" {
		t.Errorf("настоящая папка не должна меняться без --force, SKILL.md = %q", data)
	}
	if !strings.Contains(out, "afm install-skills --force") {
		t.Errorf("ожидалась подсказка про --force, вывод:\n%s", out)
	}

	out, err = runInstallSkills(t, "--force")
	if err != nil {
		t.Fatalf("install-skills --force: %v\n%s", err, out)
	}
	assertSkillLinked(t, home, "afm-run")
	backups, _ := filepath.Glob(filepath.Join(home, ".claude", ".afm-skills-backup-*", "afm-run", "SKILL.md"))
	if len(backups) != 1 {
		t.Fatalf("ожидался один backup папки afm-run, найдено %v", backups)
	}
	if data, _ := os.ReadFile(backups[0]); string(data) != "my edits" {
		t.Errorf("backup должен сохранить правки, SKILL.md = %q", data)
	}
	if !strings.Contains(out, filepath.Dir(filepath.Dir(backups[0]))) {
		t.Errorf("путь backup должен быть напечатан, вывод:\n%s", out)
	}
}

// Ошибка sync печатается одной строкой без справки по команде и завершает процесс с кодом 1.
func TestInstallSkillsErrorWithoutUsage(t *testing.T) {
	home := skillsHome(t)
	if err := os.WriteFile(filepath.Join(home, ".afm"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runInstallSkills(t)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 || !exitErr.Silent {
		t.Fatalf("want silent ExitError code 1, got %v", err)
	}
	if !strings.Contains(out, "Error: ") || !strings.Contains(out, "must be a real directory") {
		t.Errorf("error must be printed, output:\n%s", out)
	}
	if strings.Contains(out, "Usage:") {
		t.Errorf("usage must not be printed, output:\n%s", out)
	}
}

// onHost делает процесс «хостом»: ни AFM_IN_DOCKER, ни marker-файла контейнера.
func onHost(t *testing.T) {
	t.Helper()
	t.Setenv("AFM_IN_DOCKER", "")
	t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return false }))
}

func TestSyncSkillsForRunOnHost(t *testing.T) {
	home := skillsHome(t)
	onHost(t)
	realDir := filepath.Join(home, ".claude", "skills", "afm-check")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	syncSkillsForRun(home, &out)

	assertSkillLinked(t, home, "afm-run")
	want := "warning: afm skills: 1 warning(s), run \"afm install-skills\" for details\n"
	if out.String() != want {
		t.Errorf("при run печатается одна строка-сводка %q, вывод:\n%s", want, out.String())
	}
}

func TestSyncSkillsForRunInContainerDoesNothing(t *testing.T) {
	home := skillsHome(t)
	t.Setenv("AFM_IN_DOCKER", "1")

	var out bytes.Buffer
	syncSkillsForRun(home, &out)

	if _, err := os.Lstat(filepath.Join(home, ".afm")); !os.IsNotExist(err) {
		t.Errorf("в контейнере store не должен создаваться, Lstat ~/.afm: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("в контейнере ничего не печатается, вывод:\n%s", out.String())
	}
}

func TestSyncSkillsForRunReportsSyncError(t *testing.T) {
	home := skillsHome(t)
	onHost(t)
	if err := os.MkdirAll(filepath.Join(home, ".afm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".afm", "skills")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	syncSkillsForRun(home, &out)

	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "warning: afm skills: ") {
		t.Errorf("ошибка Sync должна печататься одной строкой-предупреждением, вывод:\n%s", out.String())
	}
}

// Несколько ошибок Sync печатаются одной строкой через «; ».
func TestSyncSkillsForRunJoinsErrorsInOneLine(t *testing.T) {
	home := skillsHome(t)
	onHost(t)
	for _, agentHome := range []string{".claude", ".codex"} {
		if err := os.WriteFile(filepath.Join(home, agentHome), []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	syncSkillsForRun(home, &out)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "warning: afm skills: ") || !strings.Contains(lines[0], "; ") {
		t.Errorf("обе ошибки должны печататься одной строкой через «; », вывод:\n%s", out.String())
	}
}
