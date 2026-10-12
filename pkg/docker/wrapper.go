package docker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/config"
)

// envName sanitizes a command name into an uppercase env-var suffix: only [A-Z0-9_] allowed, everything
// else → '_'. Used for AFM_SECRET_<NAME> and AFM_SYSPROMPT_<NAME> transient env vars.
func envName(cmd string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(cmd) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// WrapperSpec describes one wrapper script to generate in the wrapper-dir. Model == "" with Type ""
// selects the claude proxy-shim (BASE_URL only, no model vars).
type WrapperSpec struct {
	Type         string // "" | "claude" = claude template; "openai" = openai-compatible; "openai-agent" = openai-compatible tool-loop; "cursor" = Cursor Cloud Agents
	Command      string
	AuthTo       string // auth env var name ("" for claude proxy-shim)
	BaseURL      string // baked gateway URL; "" → omit
	Model        string // model string; "" → claude proxy-shim for claude type
	HasSysPrompt bool   // emit sysprompt block (claude type only)
	Bare         bool   // prepend --bare to claude exec (skip CLAUDE.md/hooks/skills auto-context)
	MaxTurns     int    // openai-agent only: OPENAI_AGENT_MAX_TURNS; 0 → omit (script default)
	Reasoning    string // configured reasoning level (raw, may be non-canonical); clamped to the family mechanism and emitted per type; "" → omit
}

// CreateWrappers creates a temp dir with one executable script per spec, named after spec.Command, and
// returns its path. Caller must defer os.RemoveAll(dir).
func CreateWrappers(specs []WrapperSpec) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	// LookPath claude только если есть хотя бы один claude-тип (не openai, не openai-agent, не cursor,
	// не codex — все четыре используют собственные адаптеры, не вызывающие claude).
	var realClaude string
	for _, s := range specs {
		if s.Type != config.RecipeTypeOpenAI && s.Type != config.RecipeTypeOpenAIAgent && s.Type != config.RecipeTypeCursor && s.Type != config.RecipeTypeCodex {
			p, err := exec.LookPath(config.ClaudeCommand)
			if err != nil {
				return "", fmt.Errorf("claude not found in PATH (required for wrapper generation): %w", err)
			}
			realClaude = p
			break
		}
	}
	var realCodexBin string
	for _, s := range specs {
		if s.Type == config.RecipeTypeCodex {
			p, err := exec.LookPath("codex")
			if err != nil {
				return "", fmt.Errorf("codex not found in PATH (required for wrapper generation): %w", err)
			}
			realCodexBin = p
			break
		}
	}
	dir, err := os.MkdirTemp("", "fm-wrappers-*")
	if err != nil {
		return "", fmt.Errorf("create wrapper dir: %w", err)
	}
	for _, s := range specs {
		script, gErr := generateWrapper(s, realClaude, realCodexBin)
		if gErr != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("generate wrapper %q: %w", s.Command, gErr)
		}
		if wErr := os.WriteFile(filepath.Join(dir, s.Command), []byte(script), 0755); wErr != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("write wrapper %q: %w", s.Command, wErr)
		}
	}
	return dir, nil
}

// codexDefaultModel — codex model sentinel meaning "let codex/~/.codex/config.toml
// decide", same value ParseFile/config.AgentRecipe.Validate accept as a no-op.
const codexDefaultModel = "default"

// shellSingleQuote wraps s in POSIX single quotes so the value is forwarded to the generated wrapper's shell
// VERBATIM — no parameter expansion ($VAR), command substitution ($(...)/backticks), or word-splitting.
// An embedded single quote is emitted as the standard '\” sequence (close-quote, escaped quote, reopen).
// Used for reasoning values, which may be non-canonical (passed through as-is per the feature contract).
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func generateWrapper(s WrapperSpec, realClaude, realCodexBin string) (string, error) {
	if s.Command == "" {
		return "", errors.New("empty command")
	}
	name := envName(s.Command)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")

	if s.Type == config.RecipeTypeOpenAI {
		// openai-compatible: OPENAI_* vars + exec openai-as-claude.
		// realClaude не нужен — openai-as-claude не вызывает claude.
		if s.AuthTo != "" {
			fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
			fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
		}
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export OPENAI_BASE_URL=%q\n", s.BaseURL)
		}
		if s.Model != "" {
			fmt.Fprintf(&b, "export OPENAI_MODEL=%q\n", s.Model)
		}
		if v, _ := config.ResolveReasoning(config.MechanismOpenAI, s.Reasoning); v != "" {
			fmt.Fprintf(&b, "export OPENAI_REASONING_EFFORT=%s\n", shellSingleQuote(v))
		}
		b.WriteString("exec /usr/local/bin/openai-as-claude \"$@\"\n")
		return b.String(), nil
	}

	if s.Type == config.RecipeTypeOpenAIAgent {
		// openai-compatible tool-loop: OPENAI_* vars + exec openai-agent-as-claude.
		// realClaude не нужен — openai-agent-as-claude не вызывает claude.
		if s.AuthTo != "" {
			fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
			fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
		}
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export OPENAI_BASE_URL=%q\n", s.BaseURL)
		}
		if s.Model != "" {
			fmt.Fprintf(&b, "export OPENAI_MODEL=%q\n", s.Model)
		}
		if s.MaxTurns != 0 {
			fmt.Fprintf(&b, "export OPENAI_AGENT_MAX_TURNS=%q\n", strconv.Itoa(s.MaxTurns))
		}
		if v, _ := config.ResolveReasoning(config.MechanismOpenAI, s.Reasoning); v != "" {
			fmt.Fprintf(&b, "export OPENAI_REASONING_EFFORT=%s\n", shellSingleQuote(v))
		}
		b.WriteString("exec /usr/local/bin/openai-agent-as-claude \"$@\"\n")
		return b.String(), nil
	}

	if s.Type == config.RecipeTypeCursor {
		// cursor Cloud Agents API: CURSOR_* vars + exec cursor-as-claude.
		// realClaude не нужен — cursor-as-claude не вызывает claude.
		if s.AuthTo != "" {
			fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
			fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
		}
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export CURSOR_BASE_URL=%q\n", s.BaseURL)
		}
		if s.Model != "" {
			fmt.Fprintf(&b, "export CURSOR_MODEL=%q\n", s.Model)
		}
		b.WriteString("exec /usr/local/bin/cursor-as-claude \"$@\"\n")
		return b.String(), nil
	}

	if s.Type == config.RecipeTypeCodex {
		// codex: без API/URL/auth-token — авторизация идёт через смонтированный ~/.codex, не через этот враппер.
		// realClaude не нужен — codex-as-claude не вызывает claude.
		if s.AuthTo != "" {
			fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
			fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
		}
		// hasModel — ""/"default" означает "решает сам codex/~/.codex/config.toml",
		// поэтому CODEX_MODEL для них не выставляется.
		hasModel := s.Model != "" && s.Model != codexDefaultModel
		// CODEX_BIN — abs-путь к реальному codex CLI, резолвлен ДО того как wrapper-dir попал в PATH — иначе
		// codex-as-claude поймал бы сам этот враппер и ушёл в бесконечную рекурсию.
		fmt.Fprintf(&b, "export CODEX_BIN=%q\n", realCodexBin)
		if hasModel {
			fmt.Fprintf(&b, "export CODEX_MODEL=%q\n", s.Model)
		}
		if v, _ := config.ResolveReasoning(config.MechanismCodex, s.Reasoning); v != "" {
			fmt.Fprintf(&b, "export CODEX_REASONING=%s\n", shellSingleQuote(v))
		}
		b.WriteString("exec /usr/local/bin/codex-as-claude \"$@\"\n")
		return b.String(), nil
	}

	if s.Model == "" {
		// claude proxy-shim: BASE_URL only.
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%q\n", s.BaseURL)
		}
		if v, _ := config.ResolveReasoning(config.MechanismClaude, s.Reasoning); v != "" {
			fmt.Fprintf(&b, "set -- \"$@\" --effort %s\n", shellSingleQuote(v))
		}
		fmt.Fprintf(&b, "exec %s \"$@\"\n", realClaude)
		return b.String(), nil
	}
	// claude agent template
	if s.AuthTo != "" {
		fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
		fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
	}
	if s.BaseURL != "" {
		fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%q\n", s.BaseURL)
	}
	fmt.Fprintf(&b, "export ANTHROPIC_DEFAULT_HAIKU_MODEL=%q\n", s.Model)
	fmt.Fprintf(&b, "export ANTHROPIC_DEFAULT_SONNET_MODEL=%q\n", s.Model)
	fmt.Fprintf(&b, "export ANTHROPIC_DEFAULT_OPUS_MODEL=%q\n", s.Model)
	if s.HasSysPrompt {
		fmt.Fprintf(&b, "if [ -n \"$AFM_SYSPROMPT_%s\" ]; then\n", name)
		fmt.Fprintf(&b, "  _sp=$(mktemp); printf '%%s' \"$AFM_SYSPROMPT_%s\" > \"$_sp\"; chmod 600 \"$_sp\"; unset AFM_SYSPROMPT_%s\n", name, name)
		b.WriteString("  set -- \"$@\" --append-system-prompt-file \"$_sp\"\n")
		b.WriteString("fi\n")
	}
	// --bare: minimal mode claude Code — пропускает CLAUDE.md auto-discovery, SessionStart-хуки,
	// skills-listing, auto-memory. Управляется config client.claude_bare (default on).
	if s.Bare {
		b.WriteString("set -- \"$@\" --bare\n")
	}
	if v, _ := config.ResolveReasoning(config.MechanismClaude, s.Reasoning); v != "" {
		fmt.Fprintf(&b, "set -- \"$@\" --effort %s\n", shellSingleQuote(v))
	}
	// Стриминг обязателен для всех claude-обёрток. --output-format добавляем только при отсутствии
	// (interactive уже получает его через executor ExtraArgs), чтобы не дублировать флаг.
	b.WriteString("case \" $* \" in *\"--output-format\"*) : ;; *) set -- \"$@\" --output-format stream-json ;; esac\n")
	b.WriteString("case \" $* \" in *\"--output-format stream-json\"*|*\"--output-format=stream-json\"*) set -- \"$@\" --include-partial-messages ;; esac\n")
	fmt.Fprintf(&b, "exec %s \"$@\"\n", realClaude)
	return b.String(), nil
}
