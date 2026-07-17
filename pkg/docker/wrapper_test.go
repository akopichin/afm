package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvName(t *testing.T) {
	cases := map[string]string{
		"glm51":              "GLM51",
		"deepseek-v4":        "DEEPSEEK_V4",
		"ai-free.claude-glm": "AI_FREE_CLAUDE_GLM",
		"":                   "",
	}
	for in, want := range cases {
		if got := envName(in); got != want {
			t.Errorf("envName(%q) = %q, want %q", in, got, want)
		}
	}
}

// stubClaudeOnPATH кладёт fake-claude в temp-dir и prepend'ит его к PATH.
// Возвращает абсолютный путь к fake-claude.
func stubClaudeOnPATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho fake\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return bin
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// cleanup удаляет temp-директорию врапперов, игнорируя ошибку (errcheck-friendly).
func cleanup(dir string) {
	_ = os.RemoveAll(dir)
}

func TestCreateWrappers_AgentTemplate(t *testing.T) {
	realClaude := stubClaudeOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{
		Command:      "glm51",
		AuthTo:       "ANTHROPIC_AUTH_TOKEN",
		BaseURL:      "http://127.0.0.1:39217",
		Model:        "glm-5.1",
		HasSysPrompt: true,
	}})
	if err != nil {
		t.Fatalf("CreateWrappers: %v", err)
	}
	defer cleanup(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "glm51"))
	s := string(script)
	wantSubstrings := []string{
		"#!/bin/sh",
		`export ANTHROPIC_AUTH_TOKEN="$AFM_SECRET_GLM51"`,
		"unset AFM_SECRET_GLM51",
		`export ANTHROPIC_BASE_URL="http://127.0.0.1:39217"`,
		`export ANTHROPIC_DEFAULT_HAIKU_MODEL="glm-5.1"`,
		`export ANTHROPIC_DEFAULT_SONNET_MODEL="glm-5.1"`,
		`export ANTHROPIC_DEFAULT_OPUS_MODEL="glm-5.1"`,
		`"$AFM_SYSPROMPT_GLM51"`, // sysprompt guard
		"--append-system-prompt-file",
		`*) set -- "$@" --output-format stream-json ;; esac`, // stream-json dedup guard
		`*"--output-format stream-json"*|*"--output-format=stream-json"*) set -- "$@" --include-partial-messages ;; esac`, // partial only with stream-json (не ломает supervisor json-вызов)
		"exec " + realClaude + ` "$@"`,
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("agent wrapper missing %q\n--- script ---\n%s", w, s)
		}
	}
	// Регресс: supervisor-ный вызов (--output-format json) НЕ должен получать
	// --include-partial-messages — claude требует под него stream-json.
	// Скрипт не содержит безусловного добавления partial.
	if strings.Contains(s, "\nset -- \"$@\" --include-partial-messages\n") {
		t.Errorf("wrapper must not unconditionally add --include-partial-messages (breaks json calls)\n--- script ---\n%s", s)
	}
}

func TestCreateWrappers_NoSysPromptOmitsBlock(t *testing.T) {
	stubClaudeOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{Command: "glm52", AuthTo: "ANTHROPIC_AUTH_TOKEN", BaseURL: "https://x", Model: "glm-5.2"}})
	if err != nil {
		t.Fatalf("CreateWrappers: %v", err)
	}
	defer cleanup(dir)
	s := string(mustRead(t, filepath.Join(dir, "glm52")))
	if strings.Contains(s, "AFM_SYSPROMPT") {
		t.Errorf("sysprompt block should be absent without HasSysPrompt:\n%s", s)
	}
}

func TestCreateWrappers_ClaudeShimTemplate(t *testing.T) {
	realClaude := stubClaudeOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{Command: "claude", BaseURL: "http://127.0.0.1:9999"}})
	if err != nil {
		t.Fatalf("CreateWrappers: %v", err)
	}
	defer cleanup(dir)
	s := string(mustRead(t, filepath.Join(dir, "claude")))
	if !strings.Contains(s, `export ANTHROPIC_BASE_URL="http://127.0.0.1:9999"`) {
		t.Errorf("claude-shim should set BASE_URL:\n%s", s)
	}
	if strings.Contains(s, "ANTHROPIC_DEFAULT") || strings.Contains(s, "AFM_SECRET") {
		t.Errorf("claude-shim must not emit agent-only lines:\n%s", s)
	}
	if !strings.Contains(s, "exec "+realClaude+` "$@"`) {
		t.Errorf("claude-shim should exec real claude:\n%s", s)
	}
}

func TestCreateWrappers_NoClaudeHardError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // claude нет в PATH
	_, err := CreateWrappers([]WrapperSpec{{Command: "glm51", Model: "m"}})
	if err == nil {
		t.Fatal("expected error when claude not in PATH")
	}
}

func TestCreateWrappers_EmptySpecsNoop(t *testing.T) {
	dir, err := CreateWrappers(nil)
	if err != nil || dir != "" {
		t.Errorf("empty specs: got (%q,%v), want (\"\",nil)", dir, err)
	}
}

func TestCreateWrappers_OpenAITemplate(t *testing.T) {
	// openai-тип не требует claude в PATH
	t.Setenv("PATH", t.TempDir()) // claude нет в PATH

	dir, err := CreateWrappers([]WrapperSpec{{
		Type:    WrapperTypeOpenAI,
		Command: "cursor",
		AuthTo:  "OPENAI_API_KEY",
		BaseURL: "https://api2.cursor.sh/v1",
		Model:   "claude-sonnet-4-5",
	}})
	if err != nil {
		t.Fatalf("CreateWrappers (openai): %v", err)
	}
	defer cleanup(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "cursor"))
	s := string(script)

	wantSubstrings := []string{
		"#!/bin/sh",
		`export OPENAI_API_KEY="$AFM_SECRET_CURSOR"`,
		"unset AFM_SECRET_CURSOR",
		`export OPENAI_BASE_URL="https://api2.cursor.sh/v1"`,
		`export OPENAI_MODEL="claude-sonnet-4-5"`,
		`exec /usr/local/bin/openai-as-claude "$@"`,
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("openai wrapper missing %q\n--- script ---\n%s", w, s)
		}
	}
	// не должно быть claude-специфичных строк
	if strings.Contains(s, "ANTHROPIC_DEFAULT") || strings.Contains(s, "ANTHROPIC_BASE_URL") {
		t.Errorf("openai wrapper must not contain ANTHROPIC_ vars:\n%s", s)
	}
}

func TestCreateWrappers_OpenAINoClaudeRequired(t *testing.T) {
	// только openai specs — claude не нужен в PATH
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir) // нет claude, нет docker, ничего

	_, err := CreateWrappers([]WrapperSpec{
		{Type: WrapperTypeOpenAI, Command: "cursor", Model: "m", BaseURL: "http://x", AuthTo: "OPENAI_API_KEY"},
	})
	if err != nil {
		t.Errorf("openai-only wrappers must not fail when claude absent: %v", err)
	}
}

func TestCreateWrappers_MixedTypes_RequiresClaude(t *testing.T) {
	// смесь openai + claude без claude в PATH → ошибка
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, err := CreateWrappers([]WrapperSpec{
		{Type: WrapperTypeOpenAI, Command: "cursor", Model: "m", BaseURL: "http://x", AuthTo: "OPENAI_API_KEY"},
		{Command: "glm51", Model: "glm-5.1", AuthTo: "ANTHROPIC_AUTH_TOKEN"}, // claude-тип
	})
	if err == nil {
		t.Error("mixed openai+claude without claude in PATH: expected error")
	}
}

func TestCreateWrappers_CursorTemplate(t *testing.T) {
	// cursor-тип не требует claude в PATH
	t.Setenv("PATH", t.TempDir()) // claude нет в PATH

	dir, err := CreateWrappers([]WrapperSpec{{
		Type:    WrapperTypeCursor,
		Command: "cursor",
		AuthTo:  "CURSOR_API_KEY",
		BaseURL: "https://api.cursor.com/v1",
		Model:   "auto",
	}})
	if err != nil {
		t.Fatalf("CreateWrappers (cursor): %v", err)
	}
	defer cleanup(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "cursor"))
	s := string(script)

	wantSubstrings := []string{
		"#!/bin/sh",
		`export CURSOR_API_KEY="$AFM_SECRET_CURSOR"`,
		"unset AFM_SECRET_CURSOR",
		`export CURSOR_BASE_URL="https://api.cursor.com/v1"`,
		`export CURSOR_MODEL="auto"`,
		"exec /usr/local/bin/cursor-as-claude \"$@\"",
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("cursor wrapper missing %q\n--- script ---\n%s", w, s)
		}
	}
	// не должно быть claude/openai-специфичных строк
	for _, bad := range []string{"ANTHROPIC_DEFAULT", "ANTHROPIC_BASE_URL", "OPENAI_", "openai-as-claude"} {
		if strings.Contains(s, bad) {
			t.Errorf("cursor wrapper must not contain %q:\n%s", bad, s)
		}
	}
}

func TestCreateWrappers_CursorNoClaudeRequired(t *testing.T) {
	// только cursor specs — claude не нужен в PATH
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir) // нет claude, нет docker, ничего

	_, err := CreateWrappers([]WrapperSpec{
		{Type: WrapperTypeCursor, Command: "cursor", Model: "auto", BaseURL: "http://x", AuthTo: "CURSOR_API_KEY"},
	})
	if err != nil {
		t.Errorf("cursor-only wrappers must not fail when claude absent: %v", err)
	}
}

func TestCreateWrappers_BareFlag(t *testing.T) {
	stubClaudeOnPATH(t)

	// Bare=true → claude-враппер prepends --bare.
	dir, err := CreateWrappers([]WrapperSpec{{Command: "glm52", AuthTo: "ANTHROPIC_AUTH_TOKEN", BaseURL: "https://x", Model: "glm-5.2", Bare: true}})
	if err != nil {
		t.Fatalf("CreateWrappers (bare): %v", err)
	}
	defer cleanup(dir)
	s := string(mustRead(t, filepath.Join(dir, "glm52")))
	if !strings.Contains(s, `set -- "$@" --bare`) {
		t.Errorf("bare wrapper should prepend --bare:\n%s", s)
	}

	// Bare=false → --bare в скрипте нет.
	dir2, err := CreateWrappers([]WrapperSpec{{Command: "glm53", AuthTo: "ANTHROPIC_AUTH_TOKEN", BaseURL: "https://x", Model: "glm-5.2", Bare: false}})
	if err != nil {
		t.Fatalf("CreateWrappers (non-bare): %v", err)
	}
	defer cleanup(dir2)
	s2 := string(mustRead(t, filepath.Join(dir2, "glm53")))
	if strings.Contains(s2, "--bare") {
		t.Errorf("non-bare wrapper must not contain --bare:\n%s", s2)
	}
}
