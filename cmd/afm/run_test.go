package main

import (
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

// TestBuildWrapperSpec проверяет, что buildWrapperSpec корректно пробрасывает
// поля recipe в WrapperSpec: Type, AuthTo, Model, HasSysPrompt и BaseURL
// (прямой recipe.URL — прокси удалён, host-match не нужен). Тест упражняет
// именно реальный хелпер buildWrapperSpec, а не собственный литерал.
func TestBuildWrapperSpec(t *testing.T) {
	openaiRecipe := config.AgentRecipe{
		Type:         "openai",
		Model:        "claude-sonnet-4-5",
		URL:          "https://api2.cursor.sh/v1",
		Auth:         config.RecipeAuth{From: "env:X", To: "env:OPENAI_API_KEY"},
		SystemPrompt: "file:prompts/cursor.md",
	}

	t.Run("openai fields wiring", func(t *testing.T) {
		spec := buildWrapperSpec("cursor", openaiRecipe, true)

		if spec.Type != "openai" {
			t.Errorf("Type: got %q, want %q", spec.Type, "openai")
		}
		if spec.Command != "cursor" {
			t.Errorf("Command: got %q, want %q", spec.Command, "cursor")
		}
		if spec.AuthTo != "OPENAI_API_KEY" {
			t.Errorf("AuthTo: got %q, want %q", spec.AuthTo, "OPENAI_API_KEY")
		}
		if spec.Model != "claude-sonnet-4-5" {
			t.Errorf("Model: got %q, want %q", spec.Model, "claude-sonnet-4-5")
		}
		if !spec.HasSysPrompt {
			t.Errorf("HasSysPrompt: got %t, want true (SystemPrompt is non-empty)", spec.HasSysPrompt)
		}
	})

	t.Run("HasSysPrompt false when SystemPrompt empty", func(t *testing.T) {
		noPrompt := openaiRecipe
		noPrompt.SystemPrompt = ""
		spec := buildWrapperSpec("cursor", noPrompt, true)
		if spec.HasSysPrompt {
			t.Errorf("HasSysPrompt: got %t, want false (SystemPrompt empty)", spec.HasSysPrompt)
		}
	})

	// BaseURL bake'ится из recipe.URL напрямую (прокси удалён).
	t.Run("BaseURL is recipe URL", func(t *testing.T) {
		spec := buildWrapperSpec("cursor", openaiRecipe, true)
		if spec.BaseURL != "https://api2.cursor.sh/v1" {
			t.Errorf("BaseURL: got %q, want recipe.URL", spec.BaseURL)
		}
	})

	t.Run("MaxTurns passed through", func(t *testing.T) {
		withMaxTurns := openaiRecipe
		withMaxTurns.MaxTurns = 15
		spec := buildWrapperSpec("idealab", withMaxTurns, true)
		if spec.MaxTurns != 15 {
			t.Errorf("MaxTurns: got %d, want 15", spec.MaxTurns)
		}
	})

	t.Run("MaxTurns zero when recipe doesn't set it", func(t *testing.T) {
		spec := buildWrapperSpec("idealab", openaiRecipe, true)
		if spec.MaxTurns != 0 {
			t.Errorf("MaxTurns: got %d, want 0", spec.MaxTurns)
		}
	})

	// Bare прокидывается в spec как есть (управляет --bare в claude-обёртке).
	t.Run("Bare passed through", func(t *testing.T) {
		if spec := buildWrapperSpec("glm51", openaiRecipe, true); !spec.Bare {
			t.Error("Bare: got false, want true")
		}
		if spec := buildWrapperSpec("glm51", openaiRecipe, false); spec.Bare {
			t.Error("Bare: got true, want false")
		}
	})

	// claude-рецепт (Type пустой) → Type == "" (claude template).
	t.Run("claude recipe empty type", func(t *testing.T) {
		claudeRecipe := config.AgentRecipe{
			Model: "claude-sonnet-4-5",
			URL:   "",
			Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:ANTHROPIC_API_KEY"},
		}
		spec := buildWrapperSpec("glm51", claudeRecipe, true)
		if spec.Type != "" {
			t.Errorf("Type: got %q, want %q (claude)", spec.Type, "")
		}
		if spec.AuthTo != "ANTHROPIC_API_KEY" {
			t.Errorf("AuthTo: got %q, want %q", spec.AuthTo, "ANTHROPIC_API_KEY")
		}
	})
}

func TestLoadPrompts_IncludesMemoryPrompts(t *testing.T) {
	p, err := loadPrompts("") // "" = embedded defaults
	if err != nil {
		t.Fatalf("loadPrompts: %v", err)
	}
	if p.Reflect == "" || p.Updater == "" || p.Compressor == "" {
		t.Fatalf("memory prompts empty: reflect=%d updater=%d compressor=%d",
			len(p.Reflect), len(p.Updater), len(p.Compressor))
	}
}
