package config_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
)

func loadLayers(t *testing.T, global, project string) (config.Config, error) {
	t.Helper()
	globalDir, projectDir := t.TempDir(), t.TempDir()
	if global != "" {
		writeYAML(t, globalDir, "config.yaml", global)
	}
	if project != "" {
		writeYAML(t, projectDir, "config.yaml", project)
	}
	return config.LoadFrom(globalDir, projectDir)
}

func TestCommandNames_MergesByKeyProjectWins(t *testing.T) {
	cfg, err := loadLayers(t,
		"command_names:\n  /opt/a.sh: Global A\n  /opt/shared.sh: Global Shared\n",
		"command_names:\n  /opt/shared.sh: Project Shared\n  /opt/b.sh: Project B\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	want := map[string]string{
		"/opt/a.sh":      "Global A",
		"/opt/shared.sh": "Project Shared",
		"/opt/b.sh":      "Project B",
	}
	if len(cfg.CommandNames) != len(want) {
		t.Fatalf("CommandNames = %v, want %v", cfg.CommandNames, want)
	}
	for k, v := range want {
		if cfg.CommandNames[k] != v {
			t.Errorf("CommandNames[%q] = %q, want %q", k, cfg.CommandNames[k], v)
		}
	}
}

func TestCommandNames_AbsentByDefault(t *testing.T) {
	cfg, err := loadLayers(t, "", "")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.CommandNames != nil {
		t.Errorf("CommandNames = %v, want nil", cfg.CommandNames)
	}
}

func TestCommandNames_TrimsValuesAndKeepsKeysExact(t *testing.T) {
	cfg, err := loadLayers(t, "", "command_names:\n  /opt/a.sh: \"  Spaced Name \"\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := cfg.CommandNames["/opt/a.sh"]; got != "Spaced Name" {
		t.Errorf("value = %q, want trimmed %q", got, "Spaced Name")
	}
}

func TestCommandNames_DuplicateNamesAllowed(t *testing.T) {
	cfg, err := loadLayers(t, "", "command_names:\n  /a/x.sh: Same\n  /b/x.sh: Same\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.CommandNames["/a/x.sh"] != "Same" || cfg.CommandNames["/b/x.sh"] != "Same" {
		t.Errorf("CommandNames = %v", cfg.CommandNames)
	}
}

func TestCommandNames_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"empty value", "command_names:\n  /a.sh: \"\"\n", "command_names./a.sh"},
		{"blank value", "command_names:\n  /a.sh: \"   \"\n", "command_names./a.sh"},
		{"null value", "command_names:\n  /a.sh: null\n", "command_names./a.sh"},
		{"bare value", "command_names:\n  /a.sh:\n", "command_names./a.sh"},
		{"number value", "command_names:\n  /a.sh: 123\n", "command_names./a.sh"},
		{"bool value", "command_names:\n  /a.sh: true\n", "command_names./a.sh"},
		{"map value", "command_names:\n  /a.sh:\n    x: y\n", "command_names./a.sh"},
		{"list value", "command_names:\n  /a.sh: [x]\n", "command_names./a.sh"},
		{"empty key", "command_names:\n  \"\": Name\n", "empty"},
		{"blank key", "command_names:\n  \"  \": Name\n", "empty"},
		{"null key", "command_names: {~: Main}\n", "empty"},
		{"explicit empty key", "command_names:\n  ?\n  : Main\n", "empty"},
		{"null key with bad value", "command_names: {null: 123}\n", "empty"},
		{"null section", "command_names:\n", "command_names"},
		{"list section", "command_names: [a]\n", "command_names"},
		{"string section", "command_names: Name\n", "command_names"},
	}
	for _, tt := range tests {
		for _, layer := range []string{"global", "project"} {
			t.Run(tt.name+"/"+layer, func(t *testing.T) {
				global, project := "", ""
				if layer == "global" {
					global = tt.body
				} else {
					project = tt.body
				}
				_, err := loadLayers(t, global, project)
				if err == nil {
					t.Fatal("LoadFrom accepted invalid command_names")
				}
				if !strings.Contains(err.Error(), layer) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q must name layer %q and %q", err, layer, tt.wantErr)
				}
			})
		}
	}
}

// Merge keys and aliases change which nodes yaml.v3 actually decodes, so validation must follow them.
func TestCommandNaming_MergeAndAliasValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"merged numeric label", "<<: {command_names: {claude: 123}}\n", "command_names.claude"},
		{"merged blank label", "<<: {command_names: {claude: '  '}}\n", "command_names.claude"},
		{"merged numeric prefix", "<<: {unknown_command_name: 123}\n", "unknown_command_name"},
		{"merged blank prefix", "<<: {unknown_command_name: '  '}\n", "unknown_command_name"},
		{"inner merged numeric label", "command_names:\n  <<: {claude: 123}\n", "command_names.claude"},
		{"inner merged blank key", "command_names:\n  <<: {'': Name}\n", "empty"},
		{"alias-key numeric prefix", "key: &key unknown_command_name\n*key: 123\n", "unknown_command_name"},
		{"alias-key bad section", "key: &key command_names\n*key: [a]\n", "command_names"},
		{"aliased numeric label", "n: &n 123\ncommand_names:\n  claude: *n\n", "command_names.claude"},
		{"merged list section", "<<: {command_names: [a]}\n", "command_names"},
		{"merged null key", "command_names:\n  <<: {~: Name}\n", "empty"},
		{"merged null key in sequence", "command_names:\n  <<: [{a: A}, {~: Name}]\n", "empty"},
		{"aliased null key", "k: &k ~\ncommand_names: {*k: Main}\n", "empty"},
	}
	for _, tt := range tests {
		for _, layer := range []string{"global", "project"} {
			t.Run(tt.name+"/"+layer, func(t *testing.T) {
				global, project := "", ""
				if layer == "global" {
					global = tt.body
				} else {
					project = tt.body
				}
				_, err := loadLayers(t, global, project)
				if err == nil {
					t.Fatal("LoadFrom accepted invalid command naming through merge or alias")
				}
				if !strings.Contains(err.Error(), layer) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q must name layer %q and %q", err, layer, tt.wantErr)
				}
			})
		}
	}
}

func TestCommandNaming_MergeAndAliasValid(t *testing.T) {
	body := "base: &base\n  claude: Main\n  /a.sh: A\nlabel: &label Shared\n" +
		"command_names:\n  <<: *base\n  /a.sh: Override\n  /b.sh: *label\n" +
		"unknown_command_name: *label\n"
	cfg, err := loadLayers(t, "", body)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	want := map[string]string{"claude": "Main", "/a.sh": "Override", "/b.sh": "Shared"}
	if len(cfg.CommandNames) != len(want) {
		t.Fatalf("CommandNames = %v, want %v", cfg.CommandNames, want)
	}
	for k, v := range want {
		if cfg.CommandNames[k] != v {
			t.Errorf("CommandNames[%q] = %q, want %q", k, cfg.CommandNames[k], v)
		}
	}
	if got := cfg.EffectiveUnknownCommandName(); got != "Shared" {
		t.Errorf("unknown prefix = %q, want Shared", got)
	}

	cfg, err = loadLayers(t, "", "<<: {command_names: {claude: Merged}, unknown_command_name: Top}\n")
	if err != nil {
		t.Fatalf("LoadFrom top-level merge: %v", err)
	}
	if cfg.CommandNames["claude"] != "Merged" || cfg.EffectiveUnknownCommandName() != "Top" {
		t.Errorf("top-level merge: names=%v prefix=%q", cfg.CommandNames, cfg.EffectiveUnknownCommandName())
	}
}

// Quoted spellings are literal commands, not YAML null or the merge key.
func TestCommandNames_QuotedNullAndMergeKeysAreLiteral(t *testing.T) {
	cfg, err := loadLayers(t, "", "command_names: {'null': Main, '~': Tilde, '<<': Merge}\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	want := map[string]string{"null": "Main", "~": "Tilde", "<<": "Merge"}
	if len(cfg.CommandNames) != len(want) {
		t.Fatalf("CommandNames = %v, want %v", cfg.CommandNames, want)
	}
	for k, v := range want {
		if cfg.CommandNames[k] != v {
			t.Errorf("CommandNames[%q] = %q, want %q", k, cfg.CommandNames[k], v)
		}
	}
}

func TestCommandNames_QuotedNumberIsString(t *testing.T) {
	cfg, err := loadLayers(t, "", "command_names:\n  /a.sh: \"123\"\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.CommandNames["/a.sh"] != "123" {
		t.Errorf("CommandNames = %v", cfg.CommandNames)
	}
}

func TestCommandNames_ProjectOverridesGlobalThroughLoadFrom(t *testing.T) {
	// The container re-reads the same mounted layers with this same loader, so host and container agree.
	cfg, err := loadLayers(t,
		"command_names:\n  /opt/x.sh: Global\n",
		"command_names:\n  /opt/x.sh: Project\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := config.ResolveCommandName(cfg.CommandNames, "/opt/x.sh"); got != "Project" {
		t.Errorf("resolved = %q, want Project", got)
	}
}

func TestUnknownCommandName_DefaultAndMerge(t *testing.T) {
	cfg, err := loadLayers(t, "", "")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.UnknownCommandName != "" {
		t.Errorf("raw field = %q, want empty", cfg.UnknownCommandName)
	}
	if got := cfg.EffectiveUnknownCommandName(); got != "Unknown" {
		t.Errorf("default = %q, want Unknown", got)
	}

	cfg, err = loadLayers(t, "unknown_command_name: Global\n", "")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := cfg.EffectiveUnknownCommandName(); got != "Global" {
		t.Errorf("global = %q, want Global", got)
	}

	cfg, err = loadLayers(t, "unknown_command_name: Global\n", "unknown_command_name: Project\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := cfg.EffectiveUnknownCommandName(); got != "Project" {
		t.Errorf("project = %q, want Project", got)
	}
}

func TestUnknownCommandName_Trims(t *testing.T) {
	cfg, err := loadLayers(t, "", "unknown_command_name: \"  test \"\n")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := cfg.EffectiveUnknownCommandName(); got != "test" {
		t.Errorf("got %q, want test", got)
	}
}

func TestUnknownCommandName_ValidationErrors(t *testing.T) {
	tests := []struct{ name, body string }{
		{"empty", "unknown_command_name: \"\"\n"},
		{"blank", "unknown_command_name: \"   \"\n"},
		{"null", "unknown_command_name: null\n"},
		{"bare key", "unknown_command_name:\n"},
		{"number", "unknown_command_name: 5\n"},
		{"bool", "unknown_command_name: true\n"},
		{"list", "unknown_command_name: [a]\n"},
		{"map", "unknown_command_name:\n  a: b\n"},
	}
	for _, tt := range tests {
		for _, layer := range []string{"global", "project"} {
			t.Run(tt.name+"/"+layer, func(t *testing.T) {
				global, project := "", ""
				if layer == "global" {
					global = tt.body
				} else {
					project = tt.body
				}
				_, err := loadLayers(t, global, project)
				if err == nil {
					t.Fatal("LoadFrom accepted invalid unknown_command_name")
				}
				if !strings.Contains(err.Error(), layer) || !strings.Contains(err.Error(), "unknown_command_name") {
					t.Errorf("error %q must name layer %q and the key", err, layer)
				}
			})
		}
	}
}

func TestResolveCommandName_BuiltIns(t *testing.T) {
	tests := []struct{ command, want string }{
		{"claude", "Claude Code"},
		{"codex-as-claude", "Codex"},
		{"cursor-agent-as-claude", "Cursor Agent"},
		{"openai-as-claude", "OpenAI"},
		{"openai-agent-as-claude", "OpenAI Agent"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := config.ResolveCommandName(nil, tt.command); got != tt.want {
				t.Errorf("bare = %q, want %q", got, tt.want)
			}
			if got := config.ResolveCommandName(nil, "/usr/local/bin/"+tt.command); got != tt.want {
				t.Errorf("with directory = %q, want %q", got, tt.want)
			}
			if got := config.ResolveCommandName(nil, "/opt/shims/"+tt.command+".sh"); got != tt.want {
				t.Errorf("with directory and .sh = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCommandName_OverridesBeatBuiltIns(t *testing.T) {
	overrides := map[string]string{
		"/opt/shims/codex-as-claude.sh": "Codex Reviewer",
		"claude":                        "Main Claude",
	}
	if got := config.ResolveCommandName(overrides, "/opt/shims/codex-as-claude.sh"); got != "Codex Reviewer" {
		t.Errorf("exact path override = %q", got)
	}
	if got := config.ResolveCommandName(overrides, "claude"); got != "Main Claude" {
		t.Errorf("bare override = %q", got)
	}
	// An override is keyed by the exact string: another path of the same binary keeps the built-in name.
	if got := config.ResolveCommandName(overrides, "/other/codex-as-claude.sh"); got != "Codex" {
		t.Errorf("other path = %q, want built-in Codex", got)
	}
}

func TestResolveCommandName_UnknownFallsBackToBasename(t *testing.T) {
	tests := []struct{ command, want string }{
		{"/opt/tools/my-agent.sh", "my-agent.sh"},
		{"/opt/tools/my-agent", "my-agent"},
		{"my-agent", "my-agent"},
		{"/opt/glm-as-claude", "glm-as-claude"},
	}
	for _, tt := range tests {
		if got := config.ResolveCommandName(nil, tt.command); got != tt.want {
			t.Errorf("ResolveCommandName(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestResolveCommandName_SharedBasenameDifferentOverrides(t *testing.T) {
	overrides := map[string]string{"/a/run.sh": "Alpha", "/b/run.sh": "Beta"}
	if got := config.ResolveCommandName(overrides, "/a/run.sh"); got != "Alpha" {
		t.Errorf("/a/run.sh = %q", got)
	}
	if got := config.ResolveCommandName(overrides, "/b/run.sh"); got != "Beta" {
		t.Errorf("/b/run.sh = %q", got)
	}
	if got := config.ResolveCommandName(overrides, "/c/run.sh"); got != "run.sh" {
		t.Errorf("/c/run.sh = %q, want basename", got)
	}
}

func TestResolveCommandName_EmptyCommand(t *testing.T) {
	if got := config.ResolveCommandName(map[string]string{"": "X"}, ""); got != "" {
		t.Errorf("empty command = %q, want empty", got)
	}
}
