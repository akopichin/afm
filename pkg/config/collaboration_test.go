package config_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
)

// --- T1.1: tri-state enabled + merge ---------------------------------------------------------------------

func TestCollaboration_DefaultEnabled(t *testing.T) {
	if !(config.CollaborationConfig{}).IsEnabled() {
		t.Fatal("empty config should default to enabled")
	}
}

func TestCollaboration_ConfigFalse(t *testing.T) {
	f := false
	if (config.CollaborationConfig{Enabled: &f}).IsEnabled() {
		t.Fatal("explicit false must disable")
	}
}

func TestCollaboration_EnvZeroOverridesConfigTrue(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "0")
	tr := true
	if (config.CollaborationConfig{Enabled: &tr}).IsEnabled() {
		t.Fatal("AFM_COLLABORATION=0 must override config enabled:true")
	}
}

func TestCollaboration_EnvOneOverridesConfigFalse(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "1")
	f := false
	if !(config.CollaborationConfig{Enabled: &f}).IsEnabled() {
		t.Fatal("AFM_COLLABORATION=1 must override config enabled:false")
	}
}

func TestCollaboration_EmptyEnvDoesNotOverride(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "")
	f := false
	if (config.CollaborationConfig{Enabled: &f}).IsEnabled() {
		t.Fatal("AFM_COLLABORATION=\"\" must not override config enabled:false (envBool treats empty as unset)")
	}
}

func TestMergeFile_ProjectOverridesGlobalEnabled(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  enabled: false\n")
	writeYAML(t, p, "config.yaml", "collaboration:\n  enabled: true\n")
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Collaboration.IsEnabled() {
		t.Error("project enabled:true must override global enabled:false")
	}
}

func TestMergeFile_ProjectNilKeepsGlobal(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  enabled: false\n")
	writeYAML(t, p, "config.yaml", "theme: graphite\n") // project layer silent on collaboration
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.IsEnabled() {
		t.Error("project config absent of collaboration must not erase global enabled:false")
	}
}

// --- T1.2: collaboration.max_depth in global/project config -----------------------------------------------

func TestMaxDepth_UnsetIsNil(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: graphite\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.MaxDepth != nil {
		t.Errorf("MaxDepth = %v, want nil when the key is absent", *cfg.Collaboration.MaxDepth)
	}
}

func TestMaxDepth_ExplicitZeroPreservedThroughMerge(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  max_depth: 3\n")
	writeYAML(t, p, "config.yaml", "collaboration:\n  max_depth: 0\n")
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.MaxDepth == nil || *cfg.Collaboration.MaxDepth != 0 {
		t.Fatalf("MaxDepth = %v, want explicit 0 (project overrides global 3)", cfg.Collaboration.MaxDepth)
	}
}

func TestMaxDepth_NegativeRejectedAtLoad(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "collaboration:\n  max_depth: -1\n")
	_, err := config.LoadFrom("", dir)
	if err == nil {
		t.Fatal("expected LoadFrom to reject a negative collaboration.max_depth")
	}
	if !strings.Contains(err.Error(), "collaboration.max_depth") {
		t.Errorf("error = %q, want it to name collaboration.max_depth", err.Error())
	}
	if !strings.Contains(err.Error(), "project") {
		t.Errorf("error = %q, want it to name the project layer", err.Error())
	}
}

func TestMaxDepth_NegativeRejectedAtLoad_NamesGlobalLayer(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "collaboration:\n  max_depth: -1\n")
	_, err := config.LoadFrom(dir, "")
	if err == nil {
		t.Fatal("expected LoadFrom to reject a negative collaboration.max_depth")
	}
	if !strings.Contains(err.Error(), "global") {
		t.Errorf("error = %q, want it to name the global layer", err.Error())
	}
}

func TestMaxDepth_ProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  max_depth: 3\n")
	writeYAML(t, p, "config.yaml", "collaboration:\n  max_depth: 5\n")
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.MaxDepth == nil || *cfg.Collaboration.MaxDepth != 5 {
		t.Fatalf("MaxDepth = %v, want 5 (project overrides global)", cfg.Collaboration.MaxDepth)
	}
	global, project := cfg.Collaboration.MaxDepthLayers()
	if global == nil || *global != 3 {
		t.Errorf("MaxDepthLayers() global = %v, want 3", global)
	}
	if project == nil || *project != 5 {
		t.Errorf("MaxDepthLayers() project = %v, want 5", project)
	}
}

func TestDefault_DoesNotSetMaxDepth(t *testing.T) {
	if config.Default().Collaboration.MaxDepth != nil {
		t.Error("Default() must not set collaboration.max_depth — the numeric default lives in assistants.ResolveDepth")
	}
}

// --- collaboration.mode: layered, validated per layer ----------------------------------------------------

// loadModeLayers пишет по config.yaml в global и project (пустая строка = файла нет) и грузит конфиг.
func loadModeLayers(t *testing.T, global, project string) (config.Config, error) {
	t.Helper()
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	for _, d := range []string{g, p} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if global != "" {
		writeYAML(t, g, "config.yaml", global)
	}
	if project != "" {
		writeYAML(t, p, "config.yaml", project)
	}
	return config.LoadFrom(g, p)
}

func TestCollaborationMode_LayerMerge(t *testing.T) {
	tests := []struct {
		name            string
		global, project string
		want            string // "" = Mode не задан ни одним слоем
	}{
		{name: "absent everywhere", global: "collaboration:\n  enabled: true\n", project: "collaboration:\n  max_depth: 1\n"},
		{name: "no files"},
		{name: "global team", global: "collaboration:\n  mode: team\n", want: "team"},
		{name: "global assistants", global: "collaboration:\n  mode: assistants\n", want: "assistants"},
		{name: "project assistants", project: "collaboration:\n  mode: assistants\n", want: "assistants"},
		{name: "project team overrides global assistants",
			global: "collaboration:\n  mode: assistants\n", project: "collaboration:\n  mode: team\n", want: "team"},
		{name: "project assistants overrides global team",
			global: "collaboration:\n  mode: team\n", project: "collaboration:\n  mode: assistants\n", want: "assistants"},
		{name: "project omission inherits global",
			global: "collaboration:\n  mode: assistants\n", project: "collaboration:\n  max_depth: 2\n", want: "assistants"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadModeLayers(t, tt.global, tt.project)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if cfg.Collaboration.Mode != nil {
					t.Fatalf("Mode = %q, want unset (the default lives only at mode resolution)", *cfg.Collaboration.Mode)
				}
				return
			}
			if cfg.Collaboration.Mode == nil || string(*cfg.Collaboration.Mode) != tt.want {
				t.Fatalf("Mode = %v, want %q", cfg.Collaboration.Mode, tt.want)
			}
		})
	}
}

func TestCollaborationMode_DoesNotChangeIsEnabled(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "")
	cfg, err := loadModeLayers(t, "", "collaboration:\n  mode: assistants\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Collaboration.IsEnabled() {
		t.Error("mode must not switch collaboration off")
	}
	cfg, err = loadModeLayers(t, "", "collaboration:\n  enabled: false\n  mode: assistants\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.IsEnabled() {
		t.Error("enabled:false must stay off regardless of mode")
	}
}

func TestDefault_DoesNotSetCollaborationMode(t *testing.T) {
	if config.Default().Collaboration.Mode != nil {
		t.Fatal("Default() must not set collaboration.mode; the default lives only at mode resolution")
	}
}

// invalidModeValues — значения collaboration.mode, которые обязаны быть отвергнуты в любом слое.
var invalidModeValues = []struct{ name, line string }{
	{"empty string", `mode: ""`},
	{"explicit null", "mode: null"},
	{"tilde null", "mode: ~"},
	{"bare key", "mode:"},
	{"number", "mode: 1"},
	{"bool", "mode: true"},
	{"mapping", "mode: {a: b}"},
	{"unknown string", "mode: solo"},
}

func TestCollaborationMode_InvalidRejectedAtLoad(t *testing.T) {
	for _, v := range invalidModeValues {
		bad := "collaboration:\n  " + v.line + "\n"
		t.Run("global/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, bad, "")
			assertModeErr(t, err, "global")
		})
		t.Run("project/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, "", bad)
			assertModeErr(t, err, "project")
		})
	}
}

// TestCollaborationMode_InvalidLowerLayerRejectedEvenWhenOverridden: слой с мусором отвергается, даже если
// вышестоящий слой задаёт корректное значение.
func TestCollaborationMode_InvalidLowerLayerRejectedEvenWhenOverridden(t *testing.T) {
	valid := "collaboration:\n  mode: assistants\n"
	for _, v := range invalidModeValues {
		bad := "collaboration:\n  " + v.line + "\n"
		t.Run("global/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, bad, valid)
			assertModeErr(t, err, "global")
		})
	}
}

func assertModeErr(t *testing.T, err error, layer string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected LoadFrom to reject an invalid collaboration.mode")
	}
	if !strings.Contains(err.Error(), "collaboration.mode") {
		t.Errorf("error = %q, want it to name collaboration.mode", err)
	}
	// "parse <layer> config" can only come from mergeFile: the bare layer word
	// also appears in the t.TempDir() path derived from the subtest name.
	if want := "parse " + layer + " config"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}

// --- collaboration.mode через YAML merge key (<<) и алиасы -----------------------------------------------

// mergedMode оборачивает строки mode-значения в collaboration с merge key: `<<: *d`, где якорь &d объявлен
// отдельным верхнеуровневым ключом (форма «общий блок настроек»).
func mergedMode(line string) string {
	return "defaults: &d\n  " + line + "\ncollaboration:\n  <<: *d\n"
}

func TestCollaborationMode_MergeKey(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"alias merge", mergedMode("mode: assistants"), "assistants"},
		{"inline merge", "collaboration:\n  <<:\n    mode: team\n", "team"},
		{"sequence merge, first wins",
			"a: &a\n  mode: team\nb: &b\n  mode: assistants\ncollaboration:\n  <<: [*a, *b]\n", "team"},
		{"explicit key wins over merged",
			"defaults: &d\n  mode: 1\ncollaboration:\n  mode: assistants\n  <<: *d\n", "assistants"},
		{"nested merge", "a: &a\n  mode: assistants\nb: &b\n  <<: *a\ncollaboration:\n  <<: *b\n", "assistants"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadModeLayers(t, "", tt.yaml)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Collaboration.Mode == nil || string(*cfg.Collaboration.Mode) != tt.want {
				t.Fatalf("Mode = %v, want %q", cfg.Collaboration.Mode, tt.want)
			}
		})
	}
}

func TestCollaborationMode_MergeKeyInvalidRejectedAtLoad(t *testing.T) {
	for _, v := range invalidModeValues {
		bad := mergedMode(v.line)
		t.Run("global/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, bad, "")
			assertModeErr(t, err, "global")
		})
		t.Run("project/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, "", bad)
			assertModeErr(t, err, "project")
		})
	}
}

func TestCollaborationMode_MergeKeyInvalidInSequenceRejected(t *testing.T) {
	bad := "a: &a\n  max_depth: 1\nb: &b\n  mode: solo\ncollaboration:\n  <<: [*a, *b]\n"
	_, err := loadModeLayers(t, "", bad)
	assertModeErr(t, err, "project")
}

func TestCollaborationMode_InvalidLowerLayerViaMergeKeyRejectedEvenWhenOverridden(t *testing.T) {
	valid := "collaboration:\n  mode: assistants\n"
	for _, v := range invalidModeValues {
		bad := mergedMode(v.line)
		t.Run("global/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, bad, valid)
			assertModeErr(t, err, "global")
		})
	}
}

func TestCollaborationMode_AliasToValidString(t *testing.T) {
	cfg, err := loadModeLayers(t, "", "defaults: &m assistants\ncollaboration:\n  mode: *m\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.Mode == nil || *cfg.Collaboration.Mode != "assistants" {
		t.Fatalf("Mode = %v, want assistants", cfg.Collaboration.Mode)
	}
}

// --- collaboration.mode: алиас в позиции КЛЮЧА и самоссылающийся merge key ---------------------------------

// TestCollaborationMode_KeyAliasInvalidRejected: ключ `*k`, указывающий на скаляр "mode", — это тот же ключ
// mode, поэтому его значение валидируется как обычное.
func TestCollaborationMode_KeyAliasInvalidRejected(t *testing.T) {
	for _, v := range invalidModeValues {
		// v.line имеет вид "mode: <значение>": подменяем ключ алиасом.
		line := strings.Replace(v.line, "mode:", "*k:", 1)
		bad := "key: &k mode\ncollaboration:\n  " + line + "\n"
		t.Run("global/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, bad, "")
			assertModeErr(t, err, "global")
		})
		t.Run("project/"+v.name, func(t *testing.T) {
			_, err := loadModeLayers(t, "", bad)
			assertModeErr(t, err, "project")
		})
	}
}

func TestCollaborationMode_KeyAliasValid(t *testing.T) {
	cfg, err := loadModeLayers(t, "", "key: &k mode\ncollaboration:\n  *k: assistants\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.Mode == nil || *cfg.Collaboration.Mode != "assistants" {
		t.Fatalf("Mode = %v, want assistants", cfg.Collaboration.Mode)
	}
}

// selfReferentialMergeEnv включает режим «краш-хелпера»: тест-бинарник, перезапущенный как подпроцесс,
// вызывает LoadFrom на конфиге с самоссылающимся merge key. Переполнение стека в Go фатально и не
// перехватывается recover, поэтому проверять его можно только в отдельном процессе.
const selfReferentialMergeEnv = "AFM_TEST_SELF_REFERENTIAL_MERGE"

func TestSelfReferentialMergeKeyHelper(t *testing.T) {
	if os.Getenv(selfReferentialMergeEnv) != "1" {
		t.Skip("запускается только как подпроцесс из TestCollaborationMode_SelfReferentialMergeKeyDoesNotCrash")
	}
	_, err := loadModeLayers(t, "", "collaboration: &c\n  <<: *c\n")
	if err == nil {
		t.Fatal("expected LoadFrom to reject a self-referential merge key")
	}
	t.Logf("LoadFrom error: %v", err)
}

func TestCollaborationMode_SelfReferentialMergeKeyDoesNotCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// #nosec G702 -- os.Args[0] re-executes this same compiled test binary with a fixed flag; no external input.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSelfReferentialMergeKeyHelper$", "-test.v")
	cmd.Env = append(os.Environ(), selfReferentialMergeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 2000 {
			tail = tail[:2000]
		}
		t.Fatalf("subprocess failed (stack overflow / hang?): %v\n%s", err, tail)
	}
}
