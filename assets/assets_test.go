package assets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Кастомный промпт из overrideDir используется, когда файл есть.
func TestReadPrompt_OverrideUsedWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.md"), []byte("CUSTOM PLAN"), 0644); err != nil {
		t.Fatal(err)
	}
	text, fromOverride, err := ReadPrompt("planning.md", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fromOverride {
		t.Fatal("want fromOverride=true")
	}
	if text != "CUSTOM PLAN" {
		t.Fatalf("want custom text, got %q", text)
	}
}

// Отсутствующий в overrideDir промпт (напр. autonomous.md) берётся из вкомпиленного
// дефолта — afm не должен падать, если кастомная папка неполная.
func TestReadPrompt_FallbackToEmbeddedWhenMissing(t *testing.T) {
	dir := t.TempDir() // autonomous.md здесь НЕТ
	text, fromOverride, err := ReadPrompt("autonomous.md", dir)
	if err != nil {
		t.Fatalf("fallback must not error: %v", err)
	}
	if fromOverride {
		t.Fatal("want fromOverride=false (embedded)")
	}
	if len(text) == 0 {
		t.Fatal("embedded autonomous.md must be non-empty")
	}
}

// Без overrideDir всегда вкомпиленный дефолт.
func TestReadPrompt_EmbeddedWhenNoOverrideDir(t *testing.T) {
	text, fromOverride, err := ReadPrompt("planning.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if fromOverride {
		t.Fatal("want fromOverride=false (embedded)")
	}
	if len(text) == 0 {
		t.Fatal("embedded planning.md must be non-empty")
	}
}

// The afm-live-run skill must ship in the embedded bundle so the managed-browser
// capability does not depend on a user's personal ~/.claude/skills/live-run.
func TestSkillsFS_IncludesLiveRun(t *testing.T) {
	data, err := SkillsFS.ReadFile("claude/skills/afm-live-run/SKILL.md")
	if err != nil {
		t.Fatalf("afm-live-run skill must be embedded: %v", err)
	}
	if !strings.Contains(string(data), "afm_chrome") {
		t.Fatal("afm-live-run skill must reference the afm_chrome MCP server")
	}
}

// Каждый встроенный скилл называется afm-* (не путается с личными скиллами
// пользователя), а frontmatter содержит name = имя папки и description —
// Codex требует оба поля.
func TestEmbeddedSkillsNamingAndFrontmatter(t *testing.T) {
	entries, err := SkillsFS.ReadDir("claude/skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded skills")
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "afm-") {
			t.Errorf("skill %q must be named afm-*", name)
		}
		data, err := SkillsFS.ReadFile("claude/skills/" + name + "/SKILL.md")
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		meta, err := parseFrontmatter(string(data))
		if err != nil {
			t.Errorf("skill %q: %v", name, err)
			continue
		}
		if meta.Name != name {
			t.Errorf("skill %q: frontmatter name = %q, want %q", name, meta.Name, name)
		}
		if strings.TrimSpace(meta.Description) == "" {
			t.Errorf("skill %q: frontmatter description must not be empty", name)
		}
	}
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseFrontmatter разбирает YAML между строкой "---" в самом начале файла и следующей строкой "---".
func parseFrontmatter(text string) (skillFrontmatter, error) {
	var meta skillFrontmatter
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if lines[0] != "---" {
		return meta, errors.New("file must start with a --- frontmatter line")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			err := yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &meta)
			return meta, err
		}
	}
	return meta, errors.New("frontmatter has no closing --- line")
}
