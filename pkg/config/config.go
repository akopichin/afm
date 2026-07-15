package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// envFlag reports whether an environment variable is truthy ("1" or "true").
func envFlag(name string) bool {
	v := os.Getenv(name)
	return v == "1" || v == "true"
}

// ClientConfig configures the AI client command.
type ClientConfig struct {
	Command    string   `yaml:"command"`
	ExtraArgs  []string `yaml:"extra_args"`
	ClaudeBare *bool    `yaml:"claude_bare"` // nil → --bare OFF (default, нужны skills); true → --bare on
}

// IsClaudeBare сообщает, добавлять ли --bare в генерируемые claude-врапперы.
// По умолчанию (nil) → false: --bare ВЫКЛЮЧЕН, т.к. он skip'ает skills
// auto-discovery — Skill-tool перестаёт резолвить goga-* skills (агент имитирует
// их сам). claude_bare: true включает --bare (body ~4 KB вместо ~127 KB, ниже
// нагрузка на шлюз/z.ai и шанс 529) — имеет смысл для stages БЕЗ skills.
func (c ClientConfig) IsClaudeBare() bool {
	return c.ClaudeBare != nil && *c.ClaudeBare
}

// ExecutorConfig controls agent execution parameters.
type ExecutorConfig struct {
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	MaxParallel int           `yaml:"max_parallel"`
}

// ServerConfig configures the web dashboard server.
type ServerConfig struct {
	Port        *int  `yaml:"port"`
	OpenBrowser *bool `yaml:"open_browser"`
}

// IsOpenBrowser returns OpenBrowser value (defaults to false).
func (s ServerConfig) IsOpenBrowser() bool {
	if s.OpenBrowser == nil {
		return false
	}
	return *s.OpenBrowser
}

// GetPort returns Port value (defaults to 9876).
func (s ServerConfig) GetPort() int {
	if s.Port == nil {
		return 9876
	}
	return *s.Port
}

// AgentRecipe describes how to generate a claude-compatible wrapper for a custom
// agent command (e.g. glm51) inside Docker, so the host binary is not mounted.
// See docs/superpowers/specs/2026-07-14-docker-autoshim-design.md.

// Допустимые значения AgentRecipe.Type.
const (
	recipeTypeClaude = "claude"
	recipeTypeOpenAI = "openai"
	recipeTypeCursor = "cursor" // Cursor Cloud Agents API (async run-based, не chat completions)
)

type AgentRecipe struct {
	Type         string     `yaml:"type"`          // "" | "claude" = claude (default); "openai" = OpenAI-compatible; "cursor" = Cursor Cloud Agents API
	Model        string     `yaml:"model"`         // required → ANTHROPIC_DEFAULT_*_MODEL (claude) / OPENAI_MODEL (openai) / CURSOR_MODEL (cursor)
	URL          string     `yaml:"url"`           // optional (claude); required (openai, cursor) — agent gateway
	SystemPrompt string     `yaml:"system_prompt"` // optional; "file:<path>" → --append-system-prompt-file content
	Auth         RecipeAuth `yaml:"auth"`          // required
}

// RecipeAuth describes where afm reads the secret on the host (From) and which
// claude auth env var the generated wrapper exports (To).
type RecipeAuth struct {
	From string `yaml:"from"` // "env:VAR" | "file:<path>"
	To   string `yaml:"to"`   // "env:<VAR>"; VAR ∈ ClaudeAuthEnvVars
}

// EnvVarName strips the "env:" prefix from Auth.To.
func (a RecipeAuth) EnvVarName() string { return strings.TrimPrefix(a.To, "env:") }

// ClaudeAuthEnvVars — env vars through which claude accepts tokens in a Linux
// container (macOS Keychain is unavailable there).
var ClaudeAuthEnvVars = []string{
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
}

func isClaudeAuthEnvVar(name string) bool {
	for _, v := range ClaudeAuthEnvVars {
		if v == name {
			return true
		}
	}
	return false
}

// Validate returns an error if the recipe is malformed.
func (r AgentRecipe) Validate() error {
	// type — allow-list; неизвестное значение (напр. опечатка "openapi") молча
	// трактовалось бы как claude, что ведёт к некорректной генерации обёртки.
	switch r.Type {
	case "", recipeTypeClaude, recipeTypeOpenAI, recipeTypeCursor:
	default:
		return fmt.Errorf("recipe: type must be \"\", \"claude\", \"openai\", or \"cursor\"; got %q", r.Type)
	}
	if r.Model == "" {
		return errors.New("recipe: model is required")
	}
	if !strings.HasPrefix(r.Auth.To, "env:") {
		return errors.New("recipe: auth.to must be an env: reference (e.g. env:OPENAI_API_KEY)")
	}
	// openai и cursor — внешние шлюзы: url обязателен, auth.to не ограничен ClaudeAuthEnvVars
	// (используют свои env vars: OPENAI_API_KEY, CURSOR_API_KEY и т.д.).
	if r.Type == recipeTypeOpenAI || r.Type == recipeTypeCursor {
		if r.URL == "" {
			return fmt.Errorf("recipe: url is required for type: %s", r.Type)
		}
		return nil
	}
	// claude (type == "" или "claude"): auth.to ограничен ClaudeAuthEnvVars
	if !isClaudeAuthEnvVar(r.Auth.EnvVarName()) {
		return fmt.Errorf("recipe: auth.to env var %q is not one of %v", r.Auth.EnvVarName(), ClaudeAuthEnvVars)
	}
	return nil
}

// DockerConfig configures Docker-mode self-re-exec.
type DockerConfig struct {
	Enabled *bool  `yaml:"enabled"` // nil = смотрим AFM_USE_DOCKER
	Image   string `yaml:"image"`
	// AutoShim включает генерацию claude-совместимых обёрток для recipe-агентов
	// внутри контейнера, чтобы не монтировать хостовый бинарник :ro.
	// nil = выключено (монтирование :ro как раньше).
	AutoShim *bool `yaml:"autoShim"`
	// Agents — recipe-агенты: имя команды → рецепт генерации обёртки.
	Agents map[string]AgentRecipe `yaml:"agents"`
	// SecretsFile — опц. путь к файлу с секретами; по умолчанию global ~/.afm
	// + project .afm.
	SecretsFile string `yaml:"secrets_file"`
	// ExtraMounts — дополнительные хост-пути (можно с ~), которые пробрасываются
	// в контейнер по тому же пути только для чтения. Нужно кастомным агентам,
	// хранящим токены/конфиги вне ~/.claude (напр. ~/.ai-free).
	ExtraMounts []string `yaml:"extra_mounts"`
}

// IsAutoShim reports whether wrapper auto-generation is enabled.
func (d DockerConfig) IsAutoShim() bool { return d.AutoShim != nil && *d.AutoShim }

// ValidateAgents validates every recipe. Called only when autoShim is enabled.
func (d DockerConfig) ValidateAgents() error {
	for name, r := range d.Agents {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("docker.agents.%s: %w", name, err)
		}
	}
	return nil
}

// IsDockerEnabled returns true if Docker mode should be used.
// AFM_IN_DOCKER=1 always returns false (already inside container).
func (d DockerConfig) IsDockerEnabled() bool {
	if os.Getenv("AFM_IN_DOCKER") == "1" {
		return false
	}
	if d.Enabled != nil {
		return *d.Enabled
	}
	return envFlag("AFM_USE_DOCKER")
}

// GetImage returns the Docker image to use, preferring AFM_DOCKER_IMAGE env var.
func (d DockerConfig) GetImage() string {
	if img := os.Getenv("AFM_DOCKER_IMAGE"); img != "" {
		return img
	}
	if d.Image != "" {
		return d.Image
	}
	return "akopichin/afm:latest"
}

// Config is the merged configuration for afm.
type Config struct {
	Client     ClientConfig   `yaml:"client"`
	Executor   ExecutorConfig `yaml:"executor"`
	Server     ServerConfig   `yaml:"server"`
	Docker     DockerConfig   `yaml:"docker"`
	PromptsDir string         `yaml:"prompts_dir"`
	Theme      string         `yaml:"theme"`
}

// Default returns the built-in default configuration.
func Default() Config {
	openBrowser := false
	port := 9876
	return Config{
		Client:   ClientConfig{Command: "claude"},
		Executor: ExecutorConfig{IdleTimeout: 30 * time.Minute, MaxParallel: 0},
		Server:   ServerConfig{Port: &port, OpenBrowser: &openBrowser},
	}
}

// Dashboard theme names returned by EffectiveTheme.
const (
	themeGoga      = "goga"
	themeNovacorps = "novacorps"
)

// EffectiveTheme returns the normalized dashboard theme name.
// "goga" activates the goga theme; any other value (incl. empty/"novacorps")
// falls back to the default "novacorps". Unknown values log a warning to stderr.
func (c Config) EffectiveTheme() string {
	t := strings.ToLower(strings.TrimSpace(c.Theme))
	if t == themeGoga {
		return themeGoga
	}
	if c.Theme != "" && t != themeNovacorps {
		fmt.Fprintf(os.Stderr, "warning: unknown theme %q, using %s\n", c.Theme, themeNovacorps)
	}
	return themeNovacorps
}

// LoadFrom loads and merges config from explicit global and project dirs.
// Missing files are silently ignored; defaults apply.
func LoadFrom(globalDir, projectDir string) (Config, error) {
	cfg := Default()
	if err := mergeFile(&cfg, filepath.Join(globalDir, "config.yaml")); err != nil {
		return cfg, err
	}
	if err := mergeFile(&cfg, filepath.Join(projectDir, "config.yaml")); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func mergeFile(dst *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var overlay Config
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if overlay.Client.Command != "" {
		dst.Client.Command = overlay.Client.Command
	}
	if overlay.Client.ExtraArgs != nil {
		dst.Client.ExtraArgs = overlay.Client.ExtraArgs
	}
	if overlay.Client.ClaudeBare != nil {
		dst.Client.ClaudeBare = overlay.Client.ClaudeBare
	}
	if overlay.Executor.IdleTimeout != 0 {
		dst.Executor.IdleTimeout = overlay.Executor.IdleTimeout
	}
	if overlay.Executor.MaxParallel != 0 {
		dst.Executor.MaxParallel = overlay.Executor.MaxParallel
	}
	if overlay.PromptsDir != "" {
		dst.PromptsDir = overlay.PromptsDir
	}
	if overlay.Theme != "" {
		dst.Theme = overlay.Theme
	}
	if overlay.Server.Port != nil {
		dst.Server.Port = overlay.Server.Port
	}
	if overlay.Server.OpenBrowser != nil {
		dst.Server.OpenBrowser = overlay.Server.OpenBrowser
	}
	if overlay.Docker.Enabled != nil {
		dst.Docker.Enabled = overlay.Docker.Enabled
	}
	if overlay.Docker.Image != "" {
		dst.Docker.Image = overlay.Docker.Image
	}
	if overlay.Docker.ExtraMounts != nil {
		dst.Docker.ExtraMounts = overlay.Docker.ExtraMounts
	}
	if overlay.Docker.AutoShim != nil {
		dst.Docker.AutoShim = overlay.Docker.AutoShim
	}
	if overlay.Docker.SecretsFile != "" {
		dst.Docker.SecretsFile = overlay.Docker.SecretsFile
	}
	if overlay.Docker.Agents != nil {
		if dst.Docker.Agents == nil {
			dst.Docker.Agents = map[string]AgentRecipe{}
		}
		for k, v := range overlay.Docker.Agents {
			dst.Docker.Agents[k] = v // per-key overlay: проектный слой дополняет/переопределяет глобальный
		}
	}
	return nil
}
