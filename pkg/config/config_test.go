package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/config"
)

func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

const defaultCommand = "claude"

func TestDefaultConfig(t *testing.T) {
	cfg := config.Default()
	if cfg.Client.Command != defaultCommand {
		t.Errorf("default command: got %q want %q", cfg.Client.Command, "claude")
	}
	if cfg.Executor.IdleTimeout != 30*time.Minute {
		t.Errorf("default idle timeout: got %v", cfg.Executor.IdleTimeout)
	}
	if cfg.Executor.MaxParallel != 0 {
		t.Errorf("default max_parallel: got %d", cfg.Executor.MaxParallel)
	}
	if cfg.Executor.TruncateOutput != 0 {
		t.Errorf("default truncate_output: got %d", cfg.Executor.TruncateOutput)
	}
}

func TestClientConfig_IsClaudeBare(t *testing.T) {
	// nil (поле отсутствует) → bare ВЫКЛЮЧЕН: skills нужны по умолчанию.
	if (config.ClientConfig{}).IsClaudeBare() {
		t.Error("nil ClaudeBare should default to false")
	}
	tb := true
	if !(config.ClientConfig{ClaudeBare: &tb}).IsClaudeBare() {
		t.Error("ClaudeBare=true should be true")
	}
	fb := false
	if (config.ClientConfig{ClaudeBare: &fb}).IsClaudeBare() {
		t.Error("ClaudeBare=false should be false")
	}
}

func TestLoadProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
client:
  command: claude
executor:
  idle_timeout: 10m
`)
	writeYAML(t, projectDir, "config.yaml", `
client:
  command: gemini
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Client.Command != "gemini" {
		t.Errorf("project should override command: got %q", cfg.Client.Command)
	}
	if cfg.Executor.IdleTimeout != 10*time.Minute {
		t.Errorf("global idle_timeout should carry over: got %v", cfg.Executor.IdleTimeout)
	}
}

func TestTruncateOutputCarriesFromGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
executor:
  truncate_output: 50
`)
	writeYAML(t, projectDir, "config.yaml", `
client:
  command: gemini
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Executor.TruncateOutput != 50 {
		t.Errorf("global truncate_output should carry over: got %d", cfg.Executor.TruncateOutput)
	}
}

func TestTruncateOutputProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
executor:
  truncate_output: 50
`)
	writeYAML(t, projectDir, "config.yaml", `
executor:
  truncate_output: 200
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Executor.TruncateOutput != 200 {
		t.Errorf("project truncate_output should override global: got %d", cfg.Executor.TruncateOutput)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	cfg, err := config.LoadFrom("/nonexistent", "/also/nonexistent")
	if err != nil {
		t.Fatalf("missing config files should not error: %v", err)
	}
	if cfg.Client.Command != defaultCommand {
		t.Errorf("should fall back to defaults: got %q", cfg.Client.Command)
	}
}

func TestServerConfigDefaults(t *testing.T) {
	cfg := config.Default()
	if cfg.Server.GetPort() != 9876 {
		t.Errorf("default port: got %d, want 9876", cfg.Server.GetPort())
	}
	if cfg.Server.IsOpenBrowser() {
		t.Error("default open_browser should be false")
	}
}

func TestServerConfig_IsOpenBrowser(t *testing.T) {
	var s config.ServerConfig
	if s.IsOpenBrowser() {
		t.Error("nil OpenBrowser should default to false")
	}
	tb := true
	s.OpenBrowser = &tb
	if !s.IsOpenBrowser() {
		t.Error("OpenBrowser=true should return true")
	}
	fb := false
	s.OpenBrowser = &fb
	if s.IsOpenBrowser() {
		t.Error("OpenBrowser=false should return false")
	}
}

func TestConfig_IsAutoRecover(t *testing.T) {
	var c config.Config
	if !c.IsAutoRecover() {
		t.Error("nil AutoRecover should default to true")
	}
	tb := true
	c.AutoRecover = &tb
	if !c.IsAutoRecover() {
		t.Error("AutoRecover=true should return true")
	}
	fb := false
	c.AutoRecover = &fb
	if c.IsAutoRecover() {
		t.Error("AutoRecover=false should return false")
	}
}

func TestDefaultConfig_AutoRecoverTrue(t *testing.T) {
	cfg := config.Default()
	if !cfg.IsAutoRecover() {
		t.Error("Default() should have auto_recover=true")
	}
}

func TestAutoRecoverMerge_ProjectDisablesGlobalDefault(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "auto_recover: false\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsAutoRecover() {
		t.Error("explicit auto_recover: false in project config should override the true default")
	}
}

func TestAutoRecoverMerge_AbsentKeepsDefault(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: goga\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsAutoRecover() {
		t.Error("config without auto_recover key should keep the true default")
	}
}

func TestServerConfigOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "server:\n  port: 8080\n  open_browser: false\n")

	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.GetPort() != 8080 {
		t.Errorf("port: got %d, want 8080", cfg.Server.GetPort())
	}
	if cfg.Server.IsOpenBrowser() {
		t.Error("open_browser should be false")
	}
}

func TestServerPortZeroDisablesServer(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "server:\n  port: 0\n")

	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.GetPort() != 0 {
		t.Errorf("port should be 0 when explicitly set: got %d", cfg.Server.GetPort())
	}
}

func TestDockerConfig_IsDockerEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	cases := []struct {
		name         string
		cfg          config.DockerConfig
		envUseDocker string
		envInDocker  string
		want         bool
	}{
		{"enabled=true", config.DockerConfig{Enabled: &trueVal}, "", "", true},
		{"enabled=false", config.DockerConfig{Enabled: &falseVal}, "", "", false},
		{"nil+env=1", config.DockerConfig{}, "1", "", true},
		{"nil+env=true", config.DockerConfig{}, "true", "", true},
		{"nil+env=", config.DockerConfig{}, "", "", false},
		{"in_docker overrides", config.DockerConfig{Enabled: &trueVal}, "", "1", false},
		{"explicit=true wins over AFM_IN_DOCKER=1 not", config.DockerConfig{Enabled: &trueVal}, "", "1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_USE_DOCKER", tc.envUseDocker)
			t.Setenv("AFM_IN_DOCKER", tc.envInDocker)
			if got := tc.cfg.IsDockerEnabled(); got != tc.want {
				t.Errorf("IsDockerEnabled()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestDockerConfig_GetImage(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "")
		cfg := config.DockerConfig{}
		if cfg.GetImage() != "akopichin/afm:latest" {
			t.Errorf("got %q, want akopichin/afm:latest", cfg.GetImage())
		}
	})
	t.Run("config override", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "")
		cfg := config.DockerConfig{Image: "myrepo/afm:v1"}
		if cfg.GetImage() != "myrepo/afm:v1" {
			t.Errorf("got %q", cfg.GetImage())
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "local/afm:dev")
		cfg := config.DockerConfig{Image: "myrepo/afm:v1"}
		if cfg.GetImage() != "local/afm:dev" {
			t.Errorf("got %q", cfg.GetImage())
		}
	})
}

func TestLoadFrom_DockerConfig(t *testing.T) {
	dir := t.TempDir()
	trueVal := true
	writeYAML(t, dir, "config.yaml", `
docker:
  enabled: true
  image: test/afm:dev
  extra_mounts:
    - ~/.ai-free
    - /etc/extra
`)
	cfg, err := config.LoadFrom(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Docker.Enabled == nil || *cfg.Docker.Enabled != trueVal {
		t.Errorf("Docker.Enabled: got %v, want &true", cfg.Docker.Enabled)
	}
	if cfg.Docker.Image != "test/afm:dev" {
		t.Errorf("Docker.Image: got %q", cfg.Docker.Image)
	}
	wantMounts := []string{"~/.ai-free", "/etc/extra"}
	if len(cfg.Docker.ExtraMounts) != len(wantMounts) {
		t.Fatalf("Docker.ExtraMounts: got %v, want %v", cfg.Docker.ExtraMounts, wantMounts)
	}
	for i, w := range wantMounts {
		if cfg.Docker.ExtraMounts[i].Path != w {
			t.Errorf("Docker.ExtraMounts[%d].Path: got %q, want %q", i, cfg.Docker.ExtraMounts[i].Path, w)
		}
	}
	_ = trueVal
}

func TestLoadFrom_FileBrowserEnabledMergesAcrossLayers(t *testing.T) {
	cases := []struct {
		name        string
		globalYAML  string
		projectYAML string
		want        bool
	}{
		{
			name: "global false, project unset -> disabled",
			globalYAML: `
docker:
  file_browser:
    enabled: false
`,
			projectYAML: ``,
			want:        false,
		},
		{
			name:       "global unset, project false -> disabled",
			globalYAML: ``,
			projectYAML: `
docker:
  file_browser:
    enabled: false
`,
			want: false,
		},
		{
			name: "global false, project true -> enabled (project overrides)",
			globalYAML: `
docker:
  file_browser:
    enabled: false
`,
			projectYAML: `
docker:
  file_browser:
    enabled: true
`,
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globalDir := t.TempDir()
			projectDir := t.TempDir()
			if tc.globalYAML != "" {
				writeYAML(t, globalDir, "config.yaml", tc.globalYAML)
			}
			if tc.projectYAML != "" {
				writeYAML(t, projectDir, "config.yaml", tc.projectYAML)
			}
			cfg, err := config.LoadFrom(globalDir, projectDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Docker.FileBrowser.IsEnabled(); got != tc.want {
				t.Errorf("FileBrowser.IsEnabled(): got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtraMounts_UnmarshalScalarAndObject(t *testing.T) {
	var dc config.DockerConfig
	yml := []byte(`
extra_mounts:
  - path: ../shared-contracts
    name: contracts
    browse: true
  - path: ~/.ai-free
    browse: false
  - ~/.legacy-agent
`)
	if err := yaml.Unmarshal(yml, &dc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := config.ExtraMounts{
		{Path: "../shared-contracts", Name: "contracts", Browse: true},
		{Path: "~/.ai-free", Browse: false},
		{Path: "~/.legacy-agent", Browse: false}, // legacy scalar → browse:false
	}
	if !reflect.DeepEqual(dc.ExtraMounts, want) {
		t.Fatalf("got %+v want %+v", dc.ExtraMounts, want)
	}
}

func TestFileBrowser_DefaultsDisabled(t *testing.T) {
	if (config.DockerFileBrowserConfig{}).IsEnabled() {
		t.Fatal("nil Enabled should default to disabled")
	}
	tr := true
	if !(config.DockerFileBrowserConfig{Enabled: &tr}).IsEnabled() {
		t.Fatal("explicit true must enable")
	}
}

func TestFileBrowser_EnvOverridesConfig(t *testing.T) {
	tr, f := true, false
	cases := []struct {
		name    string
		env     string // "" = не задавать
		enabled *bool
		want    bool
	}{
		{name: "env on overrides config off", env: "1", enabled: &f, want: true},
		{name: "env off overrides config on", env: "0", enabled: &tr, want: false},
		{name: "env true, config nil", env: "true", enabled: nil, want: true},
		{name: "env empty falls back to config", env: "", enabled: &tr, want: true},
		{name: "env unset, config off", env: "", enabled: &f, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("AFM_FILE_BROWSER", tc.env)
			}
			c := config.DockerFileBrowserConfig{Enabled: tc.enabled}
			if got := c.IsEnabled(); got != tc.want {
				t.Errorf("IsEnabled(): got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtraMounts_Validate(t *testing.T) {
	cases := []struct {
		name string
		in   config.ExtraMounts
		ok   bool
	}{
		{"ok", config.ExtraMounts{{Path: "a", Browse: true}, {Path: "b"}}, true},
		{"empty path", config.ExtraMounts{{Path: ""}}, false},
		{"browse empty path", config.ExtraMounts{{Path: "", Browse: true}}, false},
		{"dup", config.ExtraMounts{{Path: "a"}, {Path: "a", Browse: true}}, false},
	}
	for _, c := range cases {
		err := c.in.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: got err=%v want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestThemeMerge(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: goga\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "goga" {
		t.Errorf("theme: got %q, want %q", cfg.Theme, "goga")
	}
}

func TestThemeEmptyDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: \"\"\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "" {
		t.Errorf("empty theme should stay empty: got %q", cfg.Theme)
	}
}

func TestSkinDirMerge(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "skin_dir: /tmp/my-skin\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SkinDir != "/tmp/my-skin" {
		t.Errorf("skin_dir: got %q, want %q", cfg.SkinDir, "/tmp/my-skin")
	}
}

func TestSkinDirEmptyDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "skin_dir: \"\"\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SkinDir != "" {
		t.Errorf("empty skin_dir should stay empty: got %q", cfg.SkinDir)
	}
}

func TestEffectiveTheme(t *testing.T) {
	cases := []struct {
		name  string
		theme string
		want  string
	}{
		{"empty", "", "coffee"},
		{"goga", "goga", "goga"},
		{"goga-upper", "GOGA", "goga"},
		{"goga-spaced", "  goga  ", "goga"},
		{"novacorps", "novacorps", "novacorps"},
		{"novacorps-upper", "Novacorps", "novacorps"},
		{"coffee", "coffee", "coffee"},
		{"unknown", "dark", "coffee"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Theme: tc.theme}
			if got := cfg.EffectiveTheme(); got != tc.want {
				t.Errorf("EffectiveTheme(%q)=%q, want %q", tc.theme, got, tc.want)
			}
		})
	}
}

func TestDockerAutoShim_ParseAndValidate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := `
docker:
  autoShim: true
  secrets_file: ~/.afm/secrets.env
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      system_prompt: "file:~/.ai-free/claude-glm/system-prompt.md"
      auth:
        from: "file:~/.ai-free/claude-glm/token"
        to: "env:ANTHROPIC_AUTH_TOKEN"
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFrom(dir, dir) // global=project=dir → один файл
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !cfg.Docker.IsAutoShim() {
		t.Error("IsAutoShim: expected true")
	}
	r := cfg.Docker.Agents["glm51"]
	if r.Model != "glm-5.1" {
		t.Errorf("Model: got %q", r.Model)
	}
	if r.Auth.EnvVarName() != "ANTHROPIC_AUTH_TOKEN" {
		t.Errorf("EnvVarName: got %q", r.Auth.EnvVarName())
	}
	if cfg.Docker.SecretsFile != "~/.afm/secrets.env" {
		t.Errorf("SecretsFile: got %q", cfg.Docker.SecretsFile)
	}
	if err := cfg.Docker.ValidateAgents(); err != nil {
		t.Errorf("ValidateAgents: %v", err)
	}
}

func TestDockerAutoShim_ValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string
	}{
		{"missing model", config.AgentRecipe{Auth: config.RecipeAuth{To: "env:ANTHROPIC_AUTH_TOKEN"}}, "model is required"},
		{"auth.to not env", config.AgentRecipe{Model: "m", Auth: config.RecipeAuth{To: "ANTHROPIC_AUTH_TOKEN"}}, "must be an env:"},
		{"auth.to not in list", config.AgentRecipe{Model: "m", Auth: config.RecipeAuth{To: "env:RANDOM"}}, "not one of"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.recipe.Validate()
			if err == nil || !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("Validate(): got %v, want substring %q", err, c.errSub)
			}
		})
	}
}

func TestAgentRecipe_OpenAIType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name: "valid openai recipe",
			recipe: config.AgentRecipe{
				Type:  "openai",
				Model: "claude-sonnet-4-5",
				URL:   "https://api2.cursor.sh/v1",
				Auth:  config.RecipeAuth{From: "env:CURSOR_TOKEN", To: "env:OPENAI_API_KEY"},
			},
		},
		{
			name: "openai: missing model",
			recipe: config.AgentRecipe{
				Type: "openai",
				URL:  "https://api2.cursor.sh/v1",
				Auth: config.RecipeAuth{From: "env:CURSOR_TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "model is required",
		},
		{
			name: "openai: missing url",
			recipe: config.AgentRecipe{
				Type:  "openai",
				Model: "gpt-4o",
				Auth:  config.RecipeAuth{From: "env:OPENAI_KEY", To: "env:OPENAI_API_KEY"},
			},
			errSub: "url is required",
		},
		{
			name: "openai: auth.to not env:",
			recipe: config.AgentRecipe{
				Type:  "openai",
				Model: "gpt-4o",
				URL:   "https://api.openai.com/v1",
				Auth:  config.RecipeAuth{From: "env:KEY", To: "OPENAI_API_KEY"},
			},
			errSub: "must be an env:",
		},
		{
			name: "openai: any env: var allowed (not restricted to ClaudeAuthEnvVars)",
			recipe: config.AgentRecipe{
				Type:  "openai",
				Model: "gpt-4o",
				URL:   "https://api.openai.com/v1",
				Auth:  config.RecipeAuth{From: "env:MY_CUSTOM_KEY", To: "env:MY_TARGET_KEY"},
			},
			// env: любой — ошибки нет
		},
		{
			name: "claude type (empty): OPENAI_API_KEY still rejected",
			recipe: config.AgentRecipe{
				Model: "glm-5.1",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "not one of",
		},
		{
			name: "unknown type (typo openapi) rejected by allow-list",
			recipe: config.AgentRecipe{
				Type:  "openapi", // распространённая опечатка для "openai"
				Model: "gpt-4o",
				URL:   "https://api.openai.com/v1",
				Auth:  config.RecipeAuth{From: "env:KEY", To: "env:OPENAI_API_KEY"},
			},
			errSub: "type must be",
		},
		{
			name: "explicit claude type allowed",
			recipe: config.AgentRecipe{
				Type:  "claude",
				Model: "glm-5.1",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:ANTHROPIC_API_KEY"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.recipe.Validate()
			if c.errSub == "" {
				if err != nil {
					t.Errorf("Validate(): unexpected error: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), c.errSub) {
					t.Errorf("Validate(): got %v, want substring %q", err, c.errSub)
				}
			}
		})
	}
}

func TestAgentRecipe_OpenAIAgentType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name: "valid openai-agent recipe",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				URL:   "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:  config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
			},
		},
		{
			name: "valid openai-agent recipe with max_turns",
			recipe: config.AgentRecipe{
				Type:     "openai-agent",
				Model:    "qwen3-max",
				URL:      "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:     config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
				MaxTurns: 10,
			},
		},
		{
			name: "openai-agent: missing model",
			recipe: config.AgentRecipe{
				Type: "openai-agent",
				URL:  "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth: config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "model is required",
		},
		{
			name: "openai-agent: missing url",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				Auth:  config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "url is required",
		},
		{
			name: "openai-agent: auth.to not env:",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				URL:   "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:  config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "OPENAI_API_KEY"},
			},
			errSub: "must be an env:",
		},
		{
			name: "openai-agent: any env: var allowed (not restricted to ClaudeAuthEnvVars)",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				URL:   "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:  config.RecipeAuth{From: "env:MY_CUSTOM_KEY", To: "env:MY_TARGET_KEY"},
			},
			// env: любой — ошибки нет
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.recipe.Validate()
			if c.errSub == "" {
				if err != nil {
					t.Errorf("Validate(): unexpected error: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), c.errSub) {
					t.Errorf("Validate(): got %v, want substring %q", err, c.errSub)
				}
			}
		})
	}
}

func TestAgentRecipe_CursorType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name: "valid cursor recipe",
			recipe: config.AgentRecipe{
				Type:  "cursor",
				Model: "auto",
				URL:   "https://api.cursor.com/v1",
				Auth:  config.RecipeAuth{From: "file:~/.ai-free/claude-glm/token-cursor", To: "env:CURSOR_API_KEY"},
			},
		},
		{
			name: "cursor: missing model",
			recipe: config.AgentRecipe{
				Type: "cursor",
				URL:  "https://api.cursor.com/v1",
				Auth: config.RecipeAuth{From: "file:t", To: "env:CURSOR_API_KEY"},
			},
			errSub: "model is required",
		},
		{
			name: "cursor: missing url",
			recipe: config.AgentRecipe{
				Type:  "cursor",
				Model: "auto",
				Auth:  config.RecipeAuth{From: "file:t", To: "env:CURSOR_API_KEY"},
			},
			errSub: "url is required",
		},
		{
			name: "cursor: any env: var allowed (not restricted to ClaudeAuthEnvVars)",
			recipe: config.AgentRecipe{
				Type:  "cursor",
				Model: "auto",
				URL:   "https://api.cursor.com/v1",
				Auth:  config.RecipeAuth{From: "env:MY_KEY", To: "env:MY_CURSOR_KEY"},
			},
			// env: любой — ошибки нет
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.recipe.Validate()
			if c.errSub == "" {
				if err != nil {
					t.Errorf("Validate(): unexpected error: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), c.errSub) {
					t.Errorf("Validate(): got %v, want substring %q", err, c.errSub)
				}
			}
		})
	}
}

func TestAgentRecipe_CodexType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name:   "valid codex recipe, no auth, no model",
			recipe: config.AgentRecipe{Type: "codex"},
		},
		{
			name:   "valid codex recipe with model",
			recipe: config.AgentRecipe{Type: "codex", Model: "gpt-5.1-codex"},
		},
		{
			name:   "codex: auth optional but validated if present",
			recipe: config.AgentRecipe{Type: "codex", Auth: config.RecipeAuth{From: "env:OPENAI_API_KEY", To: "env:OPENAI_API_KEY"}},
		},
		{
			name:   "codex: auth.to must be env: if present",
			recipe: config.AgentRecipe{Type: "codex", Auth: config.RecipeAuth{From: "env:X", To: "OPENAI_API_KEY"}},
			errSub: "auth.to must be an env: reference",
		},
		{
			name:   "codex: url not required",
			recipe: config.AgentRecipe{Type: "codex", Model: "gpt-5.1-codex"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.recipe.Validate()
			if c.errSub == "" {
				if err != nil {
					t.Errorf("Validate(): unexpected error: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), c.errSub) {
					t.Errorf("Validate(): got %v, want substring %q", err, c.errSub)
				}
			}
		})
	}
}

func TestAgentRecipe_ClaudeType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name: "empty Type behaves as claude",
			recipe: config.AgentRecipe{
				Type:  "",
				Model: "claude-sonnet-4-5",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:CLAUDE_CODE_OAUTH_TOKEN"},
			},
		},
		{
			name: "explicit ClaudeCommand as Type behaves the same as empty",
			recipe: config.AgentRecipe{
				Type:  config.ClaudeCommand,
				Model: "claude-sonnet-4-5",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:CLAUDE_CODE_OAUTH_TOKEN"},
			},
		},
		{
			name: "claude: auth.to restricted to ClaudeAuthEnvVars (unlike openai/cursor)",
			recipe: config.AgentRecipe{
				Type:  config.ClaudeCommand,
				Model: "claude-sonnet-4-5",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:SOME_OTHER_VAR"},
			},
			errSub: "is not one of",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.recipe.Validate()
			if tc.errSub == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("Validate() = %v, want error containing %q", err, tc.errSub)
			}
		})
	}
}

func TestDockerAutoShim_ParseOpenAIType(t *testing.T) {
	dir := t.TempDir()
	cfgYAML := `
docker:
  autoShim: true
  agents:
    cursor:
      type: openai
      model: claude-sonnet-4-5
      url: https://api2.cursor.sh/v1
      auth:
        from: "env:CURSOR_TOKEN"
        to: "env:OPENAI_API_KEY"
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFrom(dir, dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	r := cfg.Docker.Agents["cursor"]
	if r.Type != "openai" {
		t.Errorf("Type: got %q, want %q", r.Type, "openai")
	}
	if r.Auth.EnvVarName() != "OPENAI_API_KEY" {
		t.Errorf("EnvVarName: got %q", r.Auth.EnvVarName())
	}
	if err := cfg.Docker.ValidateAgents(); err != nil {
		t.Errorf("ValidateAgents: %v", err)
	}
}

func TestDockerAutoShim_MergeLayers(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	_ = os.WriteFile(filepath.Join(global, "config.yaml"), []byte("docker:\n  agents:\n    glm51:\n      model: glm-5.1\n      auth: {to: \"env:ANTHROPIC_AUTH_TOKEN\"}\n"), 0644)
	_ = os.WriteFile(filepath.Join(project, "config.yaml"), []byte("docker:\n  agents:\n    glm52:\n      model: glm-5.2\n      auth: {to: \"env:ANTHROPIC_AUTH_TOKEN\"}\n"), 0644)
	cfg, err := config.LoadFrom(global, project)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if _, ok := cfg.Docker.Agents["glm51"]; !ok {
		t.Error("merge: glm51 from global layer missing")
	}
	if _, ok := cfg.Docker.Agents["glm52"]; !ok {
		t.Error("merge: glm52 from project layer missing")
	}
	if len(cfg.Docker.Agents) != 2 {
		t.Errorf("merge: expected 2 agents, got %d", len(cfg.Docker.Agents))
	}
}
