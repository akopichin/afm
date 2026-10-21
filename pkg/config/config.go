package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

// envFlag reports whether an environment variable is truthy ("1" or "true").
func envFlag(name string) bool {
	v, _ := envBool(name)
	return v
}

// envBool читает булев env-флаг, отличая "не задан" от "задан пустым".
// set=false, если переменной нет или она пустая; иначе value = 1/true.
func envBool(name string) (value, set bool) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return false, false
	}
	return v == "1" || v == "true", true
}

// ClientConfig configures the AI client command.
type ClientConfig struct {
	Command    string   `yaml:"command"`
	ExtraArgs  []string `yaml:"extra_args"`
	ClaudeBare *bool    `yaml:"claude_bare"` // nil → --bare OFF (default, нужны skills); true → --bare on
	// Reasoning задаёт уровень рассуждения (minimal..max) для дефолтного claude-клиента; afm клампит его в
	// claude --effort. Пусто → не передаётся. Работает на host и в Docker (default claude exec'ается напрямую).
	Reasoning string `yaml:"reasoning"`
}

// IsClaudeBare сообщает, добавлять ли --bare в генерируемые claude-врапперы. По умолчанию (nil) → false:
// --bare ВЫКЛЮЧЕН, т.к. он skip'ает skills auto-discovery (Skill-tool перестаёт резолвить goga-* skills).
// claude_bare: true включает --bare (body ~4 KB вместо ~127 KB, ниже нагрузка на шлюз) — для stages без skills.
func (c ClientConfig) IsClaudeBare() bool {
	return c.ClaudeBare != nil && *c.ClaudeBare
}

// ExecutorConfig controls agent execution parameters.
type ExecutorConfig struct {
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	MaxParallel    int           `yaml:"max_parallel"`
	TruncateOutput int           `yaml:"truncate_output"`
}

// defaultVerifyMaxFailures — сколько отклонений verify допускает по умолчанию: N = число retry-коррекций
// автора. 1 сохраняет прежнее поведение (ровно одна коррекция, затем провал).
const defaultVerifyMaxFailures = 1

// defaultVerifyReviewPatience — дефолт review_patience: 0 = ВЫКЛЮЧЕНО.
const defaultVerifyReviewPatience = 0

// defaultVerifyFreshCorrection — дефолт fresh_correction: true = автор получает свежую
// сессию (без --resume) на verify-коррекции.
const defaultVerifyFreshCorrection = true

// VerifyConfig — глобальные настройки verify-гейта.
type VerifyConfig struct {
	// MaxFailures — сколько отклонений verify допускать до провала стадии. Указатель, чтобы отличить «не
	// задано» (nil → дефолт 1) от явного значения, включая 0.
	MaxFailures *int `yaml:"max_failures"`
	// ReviewPatience — сколько ПОДРЯД идущих verify-отклонений, НЕ изменивших рабочее дерево, допустить,
	// прежде чем принять стадию как завершённую.
	ReviewPatience *int `yaml:"review_patience"`
	// FreshCorrection — выдавать ли автору ФРЕШ-сессию на коррекции после verify-отклонения, вместо
	// продолжения прежней (резюмированный автор склонен защищать свой прежний подход вместо правки по
	// фидбэку). Указатель, чтобы отличить «не задано» (nil → true) от явного false.
	FreshCorrection *bool `yaml:"fresh_correction"`
}

// ResolvedMaxFailures возвращает бюджет отклонений verify: значение из конфига,
// если задано, иначе дефолт 1.
func (c VerifyConfig) ResolvedMaxFailures() int {
	if c.MaxFailures != nil {
		return *c.MaxFailures
	}
	return defaultVerifyMaxFailures
}

// ResolvedReviewPatience возвращает бюджет stalemate: значение из конфига, если
// задано, иначе дефолт 0 (выключено).
func (c VerifyConfig) ResolvedReviewPatience() int {
	if c.ReviewPatience != nil {
		return *c.ReviewPatience
	}
	return defaultVerifyReviewPatience
}

// ResolvedFreshCorrection возвращает, давать ли автору фреш-сессию на verify-коррекции:
// значение из конфига, если задано, иначе дефолт true.
func (c VerifyConfig) ResolvedFreshCorrection() bool {
	if c.FreshCorrection != nil {
		return *c.FreshCorrection
	}
	return defaultVerifyFreshCorrection
}

// ServerConfig configures the web dashboard server.
type ServerConfig struct {
	Port        *int  `yaml:"port"`
	OpenBrowser *bool `yaml:"open_browser"`
}

// AccountingConfig controls the cost/usage accounting DISPLAY surfaces.
type AccountingConfig struct {
	Enabled *bool `yaml:"enabled"` // nil = отображение включено по умолчанию; env AFM_ACCOUNTING имеет приоритет
	// ShowMoney управляет показом денежных величин. nil/false = скрыты, показываем только токены; true =
	// показываем и деньги.
	ShowMoney *bool `yaml:"show_money"`
}

// IsEnabled сообщает, показывать ли accounting. Только явный enabled: false (или AFM_ACCOUNTING=0) прячет
// отображение; сбор usage.jsonl при этом продолжается.
func (c AccountingConfig) IsEnabled() bool {
	if v, set := envBool("AFM_ACCOUNTING"); set {
		return v
	}
	return c.Enabled == nil || *c.Enabled
}

// ShowMoneyEnabled сообщает, показывать ли денежные величины. Влияет только на отображение (dashboard,
// `afm check`/`report`) — сбор стоимости в usage.jsonl продолжается независимо.
func (c AccountingConfig) ShowMoneyEnabled() bool {
	if v, set := envBool("AFM_ACCOUNTING_SHOW_MONEY"); set {
		return v
	}
	return c.ShowMoney != nil && *c.ShowMoney
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

// AgentRecipe describes how to generate a claude-compatible wrapper for a custom agent command inside
// Docker, so the host binary is not mounted.

// ClaudeCommand — каноническое имя CLI-агента по умолчанию.
const ClaudeCommand = "claude"

// RecipeTypeOpenAI/RecipeTypeCursor — остальные допустимые значения AgentRecipe.Type/WrapperSpec.Type.
const (
	RecipeTypeOpenAI      = "openai"
	RecipeTypeOpenAIAgent = "openai-agent" // OpenAI-совместимый function calling + реальный tool-loop (см. scripts/openai-agent-as-claude.sh)
	RecipeTypeCursor      = "cursor"       // Cursor Cloud Agents API (async run-based, не chat completions)
	RecipeTypeCodex       = "codex"        // codex CLI через codex-as-claude (см. scripts/codex-as-claude.sh)
)

// AfmDir — имя служебного каталога afm: и глобального (~/.afm), и per-project.
const AfmDir = ".afm"

type AgentRecipe struct {
	Type         string     `yaml:"type"`          // "" | "claude" = claude (default); "openai" = OpenAI-compatible; "openai-agent" = OpenAI-compatible + real tool-loop; "cursor" = Cursor Cloud Agents API
	Model        string     `yaml:"model"`         // claude: --model + DEFAULT_* aliases; openai/openai-agent: OPENAI_MODEL; cursor: CURSOR_MODEL; codex: CODEX_MODEL
	URL          string     `yaml:"url"`           // optional (claude); required (openai, openai-agent, cursor) — agent gateway
	SystemPrompt string     `yaml:"system_prompt"` // optional; "file:<path>" → --append-system-prompt-file content
	Auth         RecipeAuth `yaml:"auth"`          // required
	MaxTurns     int        `yaml:"max_turns"`     // openai-agent only: max tool-call iterations per stage invocation; 0 → script default (40)
	// Reasoning задаёт уровень рассуждения (minimal..max) для этого рецепта; afm клампит его в механизм семьи
	// (codex model_reasoning_effort / claude --effort / openai reasoning_effort). Docker-only: врапперы
	// генерируются только в контейнере. Пусто → не передаётся; cursor игнорирует (нет API).
	Reasoning string `yaml:"reasoning"`
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

// reservedRecipeAuthVars — execution-control переменные генерируемых врапперов, которые НИКОГДА не должны
// быть целью auth.to ни у одного рецепта.
var reservedRecipeAuthVars = []string{
	"CODEX_VERIFY",
}

// isReservedAuthVarName запрещает auth.to перезаписывать AFM-транспорт и управляющие флаги; сравнение
// регистронезависимое.
func isReservedAuthVarName(name string) bool {
	up := strings.ToUpper(name)
	if strings.HasPrefix(up, "AFM_") {
		return true
	}
	for _, v := range reservedRecipeAuthVars {
		if up == v {
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
	case "", ClaudeCommand, RecipeTypeOpenAI, RecipeTypeOpenAIAgent, RecipeTypeCursor, RecipeTypeCodex:
	default:
		return fmt.Errorf("recipe: type must be \"\", \"claude\", \"openai\", \"openai-agent\", \"cursor\", or \"codex\"; got %q", r.Type)
	}
	// codex: model опционален; auth опционален — авторизация идёт через смонтированную ~/.codex, а не через
	// AFM_SECRET_<CMD>.
	if r.Type == RecipeTypeCodex {
		if r.Auth == (RecipeAuth{}) {
			return nil
		}
		if !strings.HasPrefix(r.Auth.To, "env:") {
			return errors.New("recipe: auth.to must be an env: reference (e.g. env:OPENAI_API_KEY)")
		}
		if isReservedAuthVarName(r.Auth.EnvVarName()) {
			return fmt.Errorf("recipe: auth.to env var %q is reserved for afm control/transport, cannot be used for auth", r.Auth.EnvVarName())
		}
		return nil
	}
	if r.Model == "" {
		return errors.New("recipe: model is required")
	}
	if !strings.HasPrefix(r.Auth.To, "env:") {
		return errors.New("recipe: auth.to must be an env: reference (e.g. env:OPENAI_API_KEY)")
	}
	if isReservedAuthVarName(r.Auth.EnvVarName()) {
		return fmt.Errorf("recipe: auth.to env var %q is reserved for afm control/transport, cannot be used for auth", r.Auth.EnvVarName())
	}
	// openai, openai-agent и cursor — внешние шлюзы: url обязателен, auth.to не
	// ограничен ClaudeAuthEnvVars (используют свои env vars: OPENAI_API_KEY, CURSOR_API_KEY и т.д.).
	if r.Type == RecipeTypeOpenAI || r.Type == RecipeTypeOpenAIAgent || r.Type == RecipeTypeCursor {
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
	// AutoShim включает генерацию claude-совместимых обёрток для recipe-агентов внутри контейнера, чтобы не
	// монтировать хостовый бинарник :ro. nil = выключено (монтирование :ro как раньше).
	AutoShim *bool `yaml:"autoShim"`
	// Agents — recipe-агенты: имя команды → рецепт генерации обёртки.
	Agents map[string]AgentRecipe `yaml:"agents"`
	// SecretsFile — опц. путь к файлу с секретами; по умолчанию global ~/.afm
	// + project .afm.
	SecretsFile string `yaml:"secrets_file"`
	// ExtraMounts — дополнительные хост-пути, которые пробрасываются в контейнер по тому же пути только для
	// чтения.
	ExtraMounts ExtraMounts `yaml:"extra_mounts"`
	// FileBrowser настраивает встроенный файловый браузер проекта в dashboard.
	FileBrowser DockerFileBrowserConfig `yaml:"file_browser"`
}

// DockerFileBrowserConfig настраивает встроенный файловый браузер проекта.
type DockerFileBrowserConfig struct {
	Enabled *bool `yaml:"enabled"` // nil = включено по умолчанию; env AFM_FILE_BROWSER имеет приоритет
}

// SideAgentConfig gates the dashboard side-agent. nil Enabled = ON by default;
// env AFM_SIDEAGENT wins over this field.
type SideAgentConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled сообщает, включён ли боковой агент дашборда. Приоритет: env AFM_SIDEAGENT > config > по
// умолчанию ВКЛЮЧЕНО. Зеркалит DockerFileBrowserConfig.IsEnabled.
func (c SideAgentConfig) IsEnabled() bool {
	if v, set := envBool("AFM_SIDEAGENT"); set {
		return v
	}
	return c.Enabled == nil || *c.Enabled
}

// WorktreesConfig gates per-flow git worktrees. nil/absent Enabled = OFF by
// default — the inverse of SideAgentConfig's default (nil/true = ON there).
// env AFM_WORKTREES wins over this field; flow.yaml worktrees overrides both.
type WorktreesConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled сообщает, включены ли git worktrees на флоу. Приоритет: env
// AFM_WORKTREES > config > по умолчанию ВЫКЛЮЧЕНО. Только явный enabled: true
// (или AFM_WORKTREES=1) включает их.
func (c WorktreesConfig) IsEnabled() bool {
	if v, set := envBool("AFM_WORKTREES"); set {
		return v
	}
	return c.Enabled != nil && *c.Enabled
}

// IsEnabled сообщает, включён ли файловый браузер. Приоритет: env AFM_FILE_BROWSER > config > по умолчанию
// ВКЛЮЧЕНО. Только явный enabled: false (или AFM_FILE_BROWSER=0) выключает его.
func (c DockerFileBrowserConfig) IsEnabled() bool {
	if v, set := envBool("AFM_FILE_BROWSER"); set {
		return v
	}
	return c.Enabled == nil || *c.Enabled
}

// ExtraMount — один дополнительный хост-путь, монтируемый в контейнер. Path может начинаться с ~.
type ExtraMount struct {
	Path   string `yaml:"path"`
	Name   string `yaml:"name,omitempty"`
	Browse bool   `yaml:"browse,omitempty"`
}

// ExtraMounts — список доп. хост-путей. Каждый элемент YAML-списка может быть либо строкой (legacy-форма
// "~/path", Browse:false), либо мапой {path, name, browse}.
type ExtraMounts []ExtraMount

// UnmarshalYAML принимает как строковые (legacy), так и объектные элементы
// списка extra_mounts.
func (m *ExtraMounts) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return errors.New("extra_mounts must be a list")
	}
	out := make(ExtraMounts, 0, len(value.Content))
	for _, item := range value.Content {
		switch item.Kind {
		case yaml.ScalarNode: // legacy "~/path" → browse:false
			out = append(out, ExtraMount{Path: item.Value})
		case yaml.MappingNode:
			var em ExtraMount
			if err := item.Decode(&em); err != nil {
				return err
			}
			out = append(out, em)
		default:
			return errors.New("extra_mounts item must be a string or a mapping")
		}
	}
	*m = out
	return nil
}

// Validate rejects malformed extra_mounts entries: empty path, browse:true
// with an empty path, or two entries pointing at the same normalized path.
func (m ExtraMounts) Validate() error {
	seen := map[string]bool{}
	for i, em := range m {
		if strings.TrimSpace(em.Path) == "" {
			return fmt.Errorf("extra_mounts[%d]: path is empty", i)
		}
		key := filepath.Clean(em.Path)
		if seen[key] {
			return fmt.Errorf("extra_mounts[%d]: duplicate path %q", i, em.Path)
		}
		seen[key] = true
	}
	return nil
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

// containerMarkerPresent reports whether a conventional container marker file exists on disk: /.dockerenv
// or /run/.containerenv.
var containerMarkerPresent = defaultContainerMarkerPresent

// containerMarkerPaths are the well-known marker files a container runtime leaves behind: /.dockerenv,
// /run/.containerenv.
var containerMarkerPaths = []string{"/.dockerenv", "/run/.containerenv"}

func defaultContainerMarkerPresent() bool {
	// Any existing marker path counts — file or directory. That's an accepted limit of marker-based detection
	// (см. дизайн: только Docker+Podman маркеры).
	for _, p := range containerMarkerPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// SwapContainerMarkerPresent replaces the container-marker detector used by InContainer and returns a
// function that restores the previous one.
func SwapContainerMarkerPresent(fn func() bool) (restore func()) {
	prev := containerMarkerPresent
	containerMarkerPresent = fn
	return func() { containerMarkerPresent = prev }
}

// InContainer detects managed or foreign containers. Use ReExecedIntoContainer to gate afm transport
// consumers.
func InContainer() bool {
	if ReExecedIntoContainer() {
		return true
	}
	return containerMarkerPresent()
}

// ReExecedIntoContainer reports whether THIS process is afm's own Docker re-exec — i.e. docker.ReExec
// launched it and set AFM_IN_DOCKER=1 together with the transport env.
func ReExecedIntoContainer() bool {
	return os.Getenv("AFM_IN_DOCKER") == "1"
}

// IsDockerEnabled returns true if Docker mode should be used.
func (d DockerConfig) IsDockerEnabled() bool {
	if InContainer() {
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
	SkinDir    string         `yaml:"skin_dir"`
	// AutoRecover controls whether failed stages are automatically reset to pending when a run
	// starts/resumes. nil/true = enabled (default); explicit false disables.
	AutoRecover *bool `yaml:"auto_recover"`
	// Pricing overrides/extends the built-in accounting rate table (list-price
	// USD per 1M tokens, an estimate — not a bank charge). Empty = builtins only.
	Pricing accounting.PricingConfig `yaml:"pricing"`
	// Accounting toggles the cost/usage DISPLAY. Collection into usage.jsonl is unaffected.
	Accounting AccountingConfig `yaml:"accounting"`
	// Hooks — lifecycle-хуки глобального и проектного слоёв. mergeFile мёржит их keyed по id: проектный слой
	// заменяет одноимённый глобальный хук, остальные складываются.
	Hooks []lifecyclehooks.Hook `yaml:"hooks,omitempty"`
	// Verify — глобальные настройки verify-гейта (число допустимых отклонений).
	// Per-stage override — flow.VerifySpec.MaxFailures.
	Verify VerifyConfig `yaml:"verify"`
	// Browser — managed Chrome DevTools browser capability (afm_chrome MCP).
	// nil/auto = offered on a supported host; per-stage opt-out via flow.Stage.Browser.
	Browser BrowserConfig `yaml:"browser"`
	// SideAgent gates the dashboard side-agent. nil/true = ON by default;
	// explicit false / AFM_SIDEAGENT=0 disables it. flow.yaml side_agent overrides.
	SideAgent SideAgentConfig `yaml:"side_agent,omitempty"`
	// Worktrees gates per-flow git worktrees. nil/false = OFF by default (the
	// inverse of SideAgent's default-ON); explicit true / AFM_WORKTREES=1
	// enables it. flow.yaml worktrees overrides both.
	Worktrees WorktreesConfig `yaml:"worktrees,omitempty"`
	// Collaboration gates the helper-agent ("collaboration") feature: side helpers/sub-tasks a stage agent
	// can delegate to. nil/true = ON by default; explicit false / AFM_COLLABORATION=0 disables it.
	Collaboration CollaborationConfig `yaml:"collaboration,omitempty"`
}

// Default returns the built-in default configuration.
func Default() Config {
	openBrowser := false
	port := 9876
	autoRecover := true
	return Config{
		Client:      ClientConfig{Command: ClaudeCommand},
		Executor:    ExecutorConfig{IdleTimeout: 30 * time.Minute, MaxParallel: 0},
		Server:      ServerConfig{Port: &port, OpenBrowser: &openBrowser},
		AutoRecover: &autoRecover,
	}
}

// IsAutoRecover reports whether failed stages should be auto-retried when a run starts or resumes.
// Defaults to true; only an explicit `auto_recover: false` disables it.
func (c Config) IsAutoRecover() bool {
	return c.AutoRecover == nil || *c.AutoRecover
}

// Dashboard theme names returned by EffectiveTheme.
const (
	ThemeGoga      = "goga"
	ThemeNovacorps = "novacorps"
	ThemeGraphite  = "graphite"
	// ThemeCoffee — устаревший алиас: нормализуется в graphite (совместимость
	// со старыми конфигами `theme: coffee`, встроенного coffee-скина больше нет).
	ThemeCoffee = "coffee"
)

// EffectiveTheme returns the normalized dashboard theme name. "goga"/"novacorps" activate those built-in
// skins; empty or unknown values fall back to the default "graphite".
func (c Config) EffectiveTheme() string {
	switch strings.ToLower(strings.TrimSpace(c.Theme)) {
	case ThemeGoga:
		return ThemeGoga
	case ThemeNovacorps:
		return ThemeNovacorps
	case ThemeCoffee:
		fmt.Fprintf(os.Stderr, "warning: theme %q is deprecated, using %s\n", ThemeCoffee, ThemeGraphite)
		return ThemeGraphite
	case ThemeGraphite, "":
		return ThemeGraphite
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown theme %q, using %s\n", c.Theme, ThemeGraphite)
		return ThemeGraphite
	}
}

// LoadFrom loads and merges config from explicit global and project dirs.
// Missing files are silently ignored; defaults apply.
func LoadFrom(globalDir, projectDir string) (Config, error) {
	cfg := Default()
	if err := mergeFile(&cfg, filepath.Join(globalDir, "config.yaml"), "global"); err != nil {
		return cfg, err
	}
	if err := mergeFile(&cfg, filepath.Join(projectDir, "config.yaml"), "project"); err != nil {
		return cfg, err
	}
	if err := cfg.Docker.ExtraMounts.Validate(); err != nil {
		return cfg, fmt.Errorf("docker.extra_mounts: %w", err)
	}
	if err := validatePricing(cfg.Pricing); err != nil {
		return cfg, err
	}
	if err := lifecyclehooks.ValidateLayer(cfg.Hooks, false); err != nil {
		return cfg, fmt.Errorf("hooks: %w", err)
	}
	if err := validateVerify(cfg.Verify); err != nil {
		return cfg, err
	}
	if err := cfg.ValidateBrowser(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// validateVerify отвергает отрицательный verify.max_failures. Клампа нет —
// ошибка конфигурации должна быть явной.
func validateVerify(v VerifyConfig) error {
	if v.MaxFailures != nil && *v.MaxFailures < 0 {
		return fmt.Errorf("verify.max_failures: must not be negative, got %d", *v.MaxFailures)
	}
	if v.ReviewPatience != nil && *v.ReviewPatience < 0 {
		return fmt.Errorf("verify.review_patience: must not be negative, got %d", *v.ReviewPatience)
	}
	return nil
}

// validatePricing rejects a PricingConfig containing any negative rate. Explicit 0 is a valid rate
// (disables that category).
func validatePricing(p accounting.PricingConfig) error {
	for model, rc := range p.Models {
		if err := validateRateCard(fmt.Sprintf("pricing.models.%s", model), rc); err != nil {
			return err
		}
	}
	for channel, models := range p.Channels {
		for model, rc := range models {
			if err := validateRateCard(fmt.Sprintf("pricing.channels.%s.%s", channel, model), rc); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateRateCard rejects a RateCard with any negative category rate.
func validateRateCard(label string, rc accounting.RateCard) error {
	fields := []struct {
		name string
		v    *accounting.Rate
	}{
		{"input", rc.Input},
		{"cache_read", rc.CacheRead},
		{"cache_write", rc.CacheWrite},
		{"cache_write_5m", rc.CacheWrite5m},
		{"cache_write_1h", rc.CacheWrite1h},
		{"output", rc.Output},
	}
	for _, f := range fields {
		if f.v != nil && *f.v < 0 {
			return fmt.Errorf("%s.%s: rate must not be negative, got %v", label, f.name, *f.v)
		}
	}
	return nil
}

func mergeFile(dst *Config, path string, layer string) error {
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
	if overlay.Client.Reasoning != "" {
		dst.Client.Reasoning = overlay.Client.Reasoning
	}
	if overlay.Executor.IdleTimeout != 0 {
		dst.Executor.IdleTimeout = overlay.Executor.IdleTimeout
	}
	if overlay.Executor.MaxParallel != 0 {
		dst.Executor.MaxParallel = overlay.Executor.MaxParallel
	}
	if overlay.Executor.TruncateOutput != 0 {
		dst.Executor.TruncateOutput = overlay.Executor.TruncateOutput
	}
	if overlay.PromptsDir != "" {
		dst.PromptsDir = overlay.PromptsDir
	}
	if overlay.Theme != "" {
		dst.Theme = overlay.Theme
	}
	if overlay.SkinDir != "" {
		dst.SkinDir = overlay.SkinDir
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
	if overlay.Docker.FileBrowser.Enabled != nil {
		dst.Docker.FileBrowser.Enabled = overlay.Docker.FileBrowser.Enabled
	}
	if overlay.SideAgent.Enabled != nil {
		dst.SideAgent.Enabled = overlay.SideAgent.Enabled
	}
	if overlay.Worktrees.Enabled != nil {
		dst.Worktrees.Enabled = overlay.Worktrees.Enabled
	}
	if overlay.Collaboration.Enabled != nil {
		dst.Collaboration.Enabled = overlay.Collaboration.Enabled
	}
	if overlay.Collaboration.MaxDepth != nil {
		if err := validateCollaborationMaxDepth(layer, overlay.Collaboration.MaxDepth); err != nil {
			return err
		}
		dst.Collaboration.MaxDepth = overlay.Collaboration.MaxDepth
		switch layer {
		case "global":
			dst.Collaboration.maxDepthGlobal = overlay.Collaboration.MaxDepth
		case "project":
			dst.Collaboration.maxDepthProject = overlay.Collaboration.MaxDepth
		default:
			// unreachable: LoadFrom only ever calls mergeFile with "global" or "project".
		}
	}
	if overlay.Collaboration.Profiles != nil {
		if dst.Collaboration.Profiles == nil {
			dst.Collaboration.Profiles = map[string]ProfileConfig{}
		}
		for id, p := range overlay.Collaboration.Profiles {
			dst.Collaboration.Profiles[id] = p // per-id overlay: later layer replaces the WHOLE profile definition
		}
	}
	if overlay.Collaboration.Limits.MaxTasks != nil {
		if err := validateCollaborationMaxTasks(layer, overlay.Collaboration.Limits.MaxTasks); err != nil {
			return err
		}
		dst.Collaboration.Limits.MaxTasks = overlay.Collaboration.Limits.MaxTasks
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
	if overlay.AutoRecover != nil {
		dst.AutoRecover = overlay.AutoRecover
	}
	if overlay.Accounting.Enabled != nil {
		dst.Accounting.Enabled = overlay.Accounting.Enabled
	}
	if overlay.Accounting.ShowMoney != nil {
		dst.Accounting.ShowMoney = overlay.Accounting.ShowMoney
	}
	if overlay.Pricing.Models != nil {
		if dst.Pricing.Models == nil {
			dst.Pricing.Models = map[string]accounting.RateCard{}
		}
		for model, rc := range overlay.Pricing.Models {
			dst.Pricing.Models[model] = rc // per-model overlay: проектный слой переопределяет глобальный целиком для этой модели
		}
	}
	if overlay.Pricing.Channels != nil {
		if dst.Pricing.Channels == nil {
			dst.Pricing.Channels = map[string]map[string]accounting.RateCard{}
		}
		for channel, models := range overlay.Pricing.Channels {
			if dst.Pricing.Channels[channel] == nil {
				dst.Pricing.Channels[channel] = map[string]accounting.RateCard{}
			}
			for model, rc := range models {
				dst.Pricing.Channels[channel][model] = rc
			}
		}
	}
	if overlay.Verify.MaxFailures != nil {
		dst.Verify.MaxFailures = overlay.Verify.MaxFailures
	}
	if overlay.Verify.ReviewPatience != nil {
		dst.Verify.ReviewPatience = overlay.Verify.ReviewPatience
	}
	if overlay.Verify.FreshCorrection != nil {
		dst.Verify.FreshCorrection = overlay.Verify.FreshCorrection
	}
	if overlay.Browser.Enabled != nil {
		dst.Browser.Enabled = overlay.Browser.Enabled
	}
	if overlay.Browser.Headless != nil {
		dst.Browser.Headless = overlay.Browser.Headless
	}
	if overlay.Browser.AppPort != 0 {
		dst.Browser.AppPort = overlay.Browser.AppPort
	}
	if overlay.Browser.ExecutablePath != "" {
		dst.Browser.ExecutablePath = overlay.Browser.ExecutablePath
	}
	if overlay.Hooks != nil {
		// Слой overlay валидируется ДО keyed-merge: иначе дубль id внутри
		// одного файла молча «схлопнется» заменой до ValidateLayer в LoadFrom.
		if err := lifecyclehooks.ValidateLayer(overlay.Hooks, false); err != nil {
			return fmt.Errorf("hooks: %w", err)
		}
		if dst.Hooks == nil {
			dst.Hooks = make([]lifecyclehooks.Hook, 0, len(overlay.Hooks))
		}
		for _, h := range overlay.Hooks {
			replaced := false
			for i := range dst.Hooks {
				if dst.Hooks[i].ID == h.ID {
					dst.Hooks[i] = h // более специфичный слой заменяет по id
					replaced = true
					break
				}
			}
			if !replaced {
				dst.Hooks = append(dst.Hooks, h)
			}
		}
	}
	return nil
}
