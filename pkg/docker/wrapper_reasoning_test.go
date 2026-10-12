package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

func wrapperScript(t *testing.T, spec WrapperSpec) string {
	t.Helper()
	dir, err := CreateWrappers([]WrapperSpec{spec})
	if err != nil {
		t.Fatalf("CreateWrappers: %v", err)
	}
	t.Cleanup(func() { cleanup(dir) })
	b, err := os.ReadFile(filepath.Join(dir, spec.Command))
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	return string(b)
}

func TestWrapperReasoning_Codex(t *testing.T) {
	stubCodexOnPATH(t)
	s := wrapperScript(t, WrapperSpec{Type: config.RecipeTypeCodex, Command: "codex", Reasoning: "xhigh"})
	if !strings.Contains(s, `export CODEX_REASONING='xhigh'`) {
		t.Errorf("codex wrapper missing CODEX_REASONING:\n%s", s)
	}
}

func TestWrapperReasoning_CodexClamp(t *testing.T) {
	stubCodexOnPATH(t)
	// codex has no "max" — it clamps to "xhigh".
	s := wrapperScript(t, WrapperSpec{Type: config.RecipeTypeCodex, Command: "codex", Reasoning: "max"})
	if !strings.Contains(s, `export CODEX_REASONING='xhigh'`) {
		t.Errorf("codex wrapper should clamp max→xhigh:\n%s", s)
	}
	if strings.Contains(s, `CODEX_REASONING='max'`) {
		t.Errorf("codex wrapper must not forward unclamped max:\n%s", s)
	}
}

func TestWrapperReasoning_OpenAI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := wrapperScript(t, WrapperSpec{
		Type: config.RecipeTypeOpenAI, Command: "oai", AuthTo: "OPENAI_API_KEY",
		BaseURL: "http://x", Model: "m", Reasoning: "medium",
	})
	if !strings.Contains(s, `export OPENAI_REASONING_EFFORT='medium'`) {
		t.Errorf("openai wrapper missing OPENAI_REASONING_EFFORT:\n%s", s)
	}
}

func TestWrapperReasoning_OpenAIClamp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	// openai tops out at "high" — xhigh clamps to high.
	s := wrapperScript(t, WrapperSpec{
		Type: config.RecipeTypeOpenAI, Command: "oai", AuthTo: "OPENAI_API_KEY",
		BaseURL: "http://x", Model: "m", Reasoning: "xhigh",
	})
	if !strings.Contains(s, `export OPENAI_REASONING_EFFORT='high'`) {
		t.Errorf("openai wrapper should clamp xhigh→high:\n%s", s)
	}
}

func TestWrapperReasoning_OpenAIAgent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := wrapperScript(t, WrapperSpec{
		Type: config.RecipeTypeOpenAIAgent, Command: "oaia", AuthTo: "OPENAI_API_KEY",
		BaseURL: "http://x", Model: "m", Reasoning: "high",
	})
	if !strings.Contains(s, `export OPENAI_REASONING_EFFORT='high'`) {
		t.Errorf("openai-agent wrapper missing OPENAI_REASONING_EFFORT:\n%s", s)
	}
}

func TestWrapperReasoning_ClaudeAgent(t *testing.T) {
	stubClaudeOnPATH(t)
	s := wrapperScript(t, WrapperSpec{
		Type: "", Command: "glm", AuthTo: "ANTHROPIC_AUTH_TOKEN",
		BaseURL: "http://x", Model: "glm-5.1", Reasoning: "max",
	})
	if !strings.Contains(s, `--effort 'max'`) {
		t.Errorf("claude-agent wrapper missing --effort 'max':\n%s", s)
	}
}

func TestWrapperReasoning_ClaudeProxyShim(t *testing.T) {
	stubClaudeOnPATH(t)
	// proxy-shim: Model == "" — still honors reasoning via --effort.
	s := wrapperScript(t, WrapperSpec{Type: "", Command: "glm", BaseURL: "http://x", Reasoning: "high"})
	if !strings.Contains(s, `--effort 'high'`) {
		t.Errorf("claude proxy-shim wrapper missing --effort 'high':\n%s", s)
	}
}

// A non-canonical value is forwarded VERBATIM: single-quoted so a value with spaces cannot word-split into a
// stray positional arg, AND so shell metacharacters ($VAR, $(...), backticks) are NOT expanded/executed by
// the generated wrapper's shell. This is the feature contract ("forwarded verbatim with a warning").
func TestWrapperReasoning_NonCanonicalForwardedVerbatim(t *testing.T) {
	stubClaudeOnPATH(t)
	// Spaced value — must stay one token.
	s := wrapperScript(t, WrapperSpec{
		Type: "", Command: "glm", AuthTo: "ANTHROPIC_AUTH_TOKEN",
		BaseURL: "http://x", Model: "glm-5.1", Reasoning: "ultra high",
	})
	if !strings.Contains(s, `--effort 'ultra high'`) {
		t.Errorf("non-canonical reasoning must be single-quoted:\n%s", s)
	}

	// Expansion/command-substitution metacharacters — must appear literally inside single quotes, never in a
	// double-quoted context where the shell would expand them.
	for _, raw := range []string{"$HOME", "$(touch /pwned)", "`id`"} {
		s := wrapperScript(t, WrapperSpec{
			Type: "", Command: "glm", AuthTo: "ANTHROPIC_AUTH_TOKEN",
			BaseURL: "http://x", Model: "glm-5.1", Reasoning: raw,
		})
		if !strings.Contains(s, "--effort '"+raw+"'") {
			t.Errorf("reasoning %q must be forwarded verbatim in single quotes:\n%s", raw, s)
		}
		if strings.Contains(s, `--effort "`+raw+`"`) {
			t.Errorf("reasoning %q must NOT be double-quoted (shell would expand it):\n%s", raw, s)
		}
	}
}

// Codex/OpenAI export sites also forward verbatim (single-quoted), not double-quoted.
func TestWrapperReasoning_ExportsForwardedVerbatim(t *testing.T) {
	stubCodexOnPATH(t)
	s := wrapperScript(t, WrapperSpec{Type: config.RecipeTypeCodex, Command: "codex", Reasoning: "$HOME"})
	if !strings.Contains(s, `export CODEX_REASONING='$HOME'`) {
		t.Errorf("codex export must single-quote the raw value:\n%s", s)
	}
	if strings.Contains(s, `export CODEX_REASONING="$HOME"`) {
		t.Errorf("codex export must not double-quote (shell would expand):\n%s", s)
	}
}

// Embedded single quotes in a raw value are escaped as '\” so the wrapper stays valid shell.
func TestWrapperReasoning_EmbeddedSingleQuoteEscaped(t *testing.T) {
	stubCodexOnPATH(t)
	s := wrapperScript(t, WrapperSpec{Type: config.RecipeTypeCodex, Command: "codex", Reasoning: "a'b"})
	if !strings.Contains(s, `export CODEX_REASONING='a'\''b'`) {
		t.Errorf("embedded single quote must be escaped as '\\'':\n%s", s)
	}
}

func TestWrapperReasoning_CursorEmits_Nothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := wrapperScript(t, WrapperSpec{
		Type: config.RecipeTypeCursor, Command: "cur", AuthTo: "CURSOR_API_KEY",
		BaseURL: "http://x", Model: "m", Reasoning: "high",
	})
	if strings.Contains(s, "REASONING") || strings.Contains(s, "--effort") {
		t.Errorf("cursor wrapper must not emit reasoning:\n%s", s)
	}
}

func TestWrapperReasoning_EmptyIsRegressionClean(t *testing.T) {
	stubCodexOnPATH(t)
	s := wrapperScript(t, WrapperSpec{Type: config.RecipeTypeCodex, Command: "codex", Model: "m"})
	if strings.Contains(s, "REASONING") {
		t.Errorf("empty reasoning must emit nothing:\n%s", s)
	}
}
