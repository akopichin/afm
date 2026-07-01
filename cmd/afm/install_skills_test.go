package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSkillsCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("installSkills: %v", err)
	}
	for _, name := range []string{"afm", "afm-check", "afm-init", "afm-retry", "afm-review"} {
		p := filepath.Join(dir, name, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("ожидался файл %s: %v", p, err)
		}
	}
}

func TestInstallSkillsSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("первая установка: %v", err)
	}
	target := filepath.Join(dir, "afm", "SKILL.md")
	if err := os.WriteFile(target, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("повторная установка: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "modified" {
		t.Error("файл не должен был перезаписаться без --force")
	}
}

func TestInstallSkillsForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("первая установка: %v", err)
	}
	target := filepath.Join(dir, "afm", "SKILL.md")
	original, _ := os.ReadFile(target)
	if err := os.WriteFile(target, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installSkills(dir, true); err != nil {
		t.Fatalf("force установка: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) == "modified" {
		t.Error("файл должен был перезаписаться при --force")
	}
	if len(data) == 0 || string(data) != string(original) {
		t.Errorf("содержимое после --force не совпадает с embedded: got %q", string(data))
	}
}

func TestResolveSkillsDirOverride(t *testing.T) {
	got, err := resolveSkillsDir("/custom/path")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/path" {
		t.Errorf("ожидался /custom/path, получили %s", got)
	}
}

func TestResolveSkillsDirDefault(t *testing.T) {
	got, err := resolveSkillsDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Error("resolveSkillsDir(\"\") вернул пустую строку")
	}
	// Должен содержать .claude/skills
	if filepath.Base(got) != "skills" {
		t.Errorf("ожидался путь .../.claude/skills, получили %s", got)
	}
}

// TestNewInstallSkillsCmdWiring защищает от lint-ругани unused/staticcheck:
// до Task 3 команда не регистрируется в root, поэтому конструктор
// newInstallSkillsCmd обязан использоваться хотя бы в тесте. Заодно
// проверяем, что флаги --skills-dir/--force зарегистрированы, а RunE
// действительно устанавливает скиллы при вызове Execute.
func TestNewInstallSkillsCmdWiring(t *testing.T) {
	cmd := newInstallSkillsCmd()
	if cmd.Use != "install-skills" {
		t.Errorf("Use = %q, want %q", cmd.Use, "install-skills")
	}
	for _, f := range []string{"skills-dir", "force"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("флаг --%s не зарегистрирован", f)
		}
	}

	dir := t.TempDir()
	cmd.SetArgs([]string{"--skills-dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "afm", "SKILL.md")); err != nil {
		t.Errorf("execute должен был установить скиллы (afm/SKILL.md): %v", err)
	}
}
