package docker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WrapperTypeOpenAI — WrapperSpec.Type value that selects the OpenAI-compatible
// template (OPENAI_* vars + exec openai-as-claude). Empty / "claude" selects the
// claude template.
const WrapperTypeOpenAI = "openai"

// WrapperTypeCursor — Cursor Cloud Agents API template (CURSOR_* vars + exec
// cursor-as-claude). Cursor не имеет синхронного /chat/completions — это
// асинхронный run-based API, поэтому использует свой адаптер, не claude.
const WrapperTypeCursor = "cursor"

// envName sanitizes a command name into an uppercase env-var suffix: only
// [A-Z0-9_] allowed, everything else → '_'. Used for AFM_SECRET_<NAME> and
// AFM_SYSPROMPT_<NAME> transient env vars.
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

// WrapperSpec describes one wrapper script to generate in the wrapper-dir.
// Type "" or "claude" selects the claude template (auth + ANTHROPIC_* vars + exec claude).
// Type "openai" selects the OpenAI-compatible template (OPENAI_* vars + exec openai-as-claude).
// Type "cursor" selects the Cursor Cloud Agents template (CURSOR_* vars + exec cursor-as-claude).
// Model == "" with Type "" selects the claude proxy-shim (BASE_URL only, no model vars).
type WrapperSpec struct {
	Type         string // "" | "claude" = claude template; "openai" = openai-compatible; "cursor" = Cursor Cloud Agents
	Command      string
	AuthTo       string // auth env var name ("" for claude proxy-shim)
	BaseURL      string // baked gateway URL; "" → omit
	Model        string // model string; "" → claude proxy-shim for claude type
	HasSysPrompt bool   // emit sysprompt block (claude type only)
	Bare         bool   // prepend --bare to claude exec (skip CLAUDE.md/hooks/skills auto-context)
}

// CreateWrappers creates a temp dir with one executable script per spec, named
// after spec.Command, and returns its path. realClaude is resolved once via
// exec.LookPath("claude") (absolute path → bypasses the wrapper-dir on PATH,
// avoiding recursion) — but only when at least one spec is NOT type "openai"
// (openai specs exec openai-as-claude, which never calls claude). Caller must
// defer os.RemoveAll(dir). Empty specs → ("", nil).
func CreateWrappers(specs []WrapperSpec) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	// LookPath claude только если есть хотя бы один claude-тип (не openai и не cursor —
	// оба используют собственные адаптеры, не вызывающие claude).
	var realClaude string
	for _, s := range specs {
		if s.Type != WrapperTypeOpenAI && s.Type != WrapperTypeCursor {
			p, err := exec.LookPath("claude")
			if err != nil {
				return "", fmt.Errorf("claude not found in PATH (required for wrapper generation): %w", err)
			}
			realClaude = p
			break
		}
	}
	dir, err := os.MkdirTemp("", "fm-wrappers-*")
	if err != nil {
		return "", fmt.Errorf("create wrapper dir: %w", err)
	}
	for _, s := range specs {
		script, gErr := generateWrapper(s, realClaude)
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

func generateWrapper(s WrapperSpec, realClaude string) (string, error) {
	if s.Command == "" {
		return "", errors.New("empty command")
	}
	name := envName(s.Command)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")

	if s.Type == WrapperTypeOpenAI {
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
		b.WriteString("exec /usr/local/bin/openai-as-claude \"$@\"\n")
		return b.String(), nil
	}

	if s.Type == WrapperTypeCursor {
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

	if s.Model == "" {
		// claude proxy-shim: BASE_URL only.
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%q\n", s.BaseURL)
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
	// --bare: minimal mode claude Code — пропускает CLAUDE.md auto-discovery,
	// SessionStart-хуки, skills-listing, auto-memory (~120 KB автоконтекста, который
	// агентам внутри flow не нужен). Меньший payload → меньше токенов и нагрузки на
	// шлюз (z.ai), ниже шанс 529. Управляется config client.claude_bare (default on).
	if s.Bare {
		b.WriteString("set -- \"$@\" --bare\n")
	}
	// Стриминг обязателен для всех claude-обёрток (прокси удалён — ZAI transform больше
	// не форсирует streaming). Без --output-format stream-json claude уходит в
	// non-streaming → z.ai 529 на overload + executor не парсит вывод; это покрывает и
	// non-interactive stages, которым executor не передаёт ExtraArgs. --output-format
	// добавляем только при отсутствии (interactive уже получает его через executor
	// ExtraArgs), чтобы не дублировать флаг.
	// --include-partial-messages добавляем ТОЛЬКО при output-format=stream-json: флаг
	// требует stream-json, и его безусловное добавление ломало supervisor-ный вызов
	// RunJSONQuery (--output-format json → claude: "requires --output-format=stream-json").
	// Для json-вызова (однократный LLM-запрос супервизора) partial не нужен и вреден.
	b.WriteString("case \" $* \" in *\"--output-format\"*) : ;; *) set -- \"$@\" --output-format stream-json ;; esac\n")
	b.WriteString("case \" $* \" in *\"--output-format stream-json\"*|*\"--output-format=stream-json\"*) set -- \"$@\" --include-partial-messages ;; esac\n")
	fmt.Fprintf(&b, "exec %s \"$@\"\n", realClaude)
	return b.String(), nil
}
