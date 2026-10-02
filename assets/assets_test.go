package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// The live-run skill must ship in the embedded bundle so the managed-browser
// capability does not depend on a user's personal ~/.claude/skills/live-run.
func TestSkillsFS_IncludesLiveRun(t *testing.T) {
	data, err := SkillsFS.ReadFile("claude/skills/live-run/SKILL.md")
	if err != nil {
		t.Fatalf("live-run skill must be embedded: %v", err)
	}
	if !strings.Contains(string(data), "afm_chrome") {
		t.Fatal("live-run skill must reference the afm_chrome MCP server")
	}
}
