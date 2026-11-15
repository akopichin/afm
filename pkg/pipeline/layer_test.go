package pipeline

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
)

const pkgAgents = `docker:
  agents:
    demo-pipe-writer:
      type: claude
      model: claude-sonnet-5-5
      reasoning: medium
      system_prompt: file:prompts/writer.md
      auth: {from: "env:CLAUDE_CODE_OAUTH_TOKEN", to: "env:CLAUDE_CODE_OAUTH_TOKEN"}
collaboration:
  profiles:
    demo-pipe-writer: {title: Writer, command: demo-pipe-writer, delegate: true}
`

func testManifest() Manifest {
	return Manifest{SchemaVersion: 2, Name: "demo-pipe", Version: "1", Entrypoint: "flow.yaml",
		Roles: map[string]Role{"writer": {Command: "demo-pipe-writer", Profile: "demo-pipe-writer"}}}
}

func pkgSnapshotDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "prompts/writer.md", "be good")
	return dir
}

func TestAssemblePrecedenceAndWholeReplacement(t *testing.T) {
	snap := pkgSnapshotDir(t)
	pkg, err := ParseLayer([]byte(pkgAgents), OriginPackage, snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := pkg.ValidatePackage(testManifest()); err != nil {
		t.Fatal(err)
	}
	if got, want := pkg.Agents["demo-pipe-writer"].SystemPrompt, "file:"+filepath.Join(snap, "prompts/writer.md"); got != want {
		t.Fatalf("system_prompt = %q, want %q", got, want)
	}
	opts, err := ParseLayer([]byte(`docker:
  agents:
    demo-pipe-writer: {type: codex, model: gpt-6-sol}
collaboration:
  profiles:
    demo-pipe-writer: {title: Renamed}
`), OriginOptions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := config.Default()
	base.Docker.Agents = map[string]config.AgentRecipe{"mine": {Type: "claude", Model: "m", Auth: config.RecipeAuth{To: "env:ANTHROPIC_API_KEY"}}}
	cfg, prov, warnings, err := Assemble(base, pkg, &opts, snap)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Docker.Agents["demo-pipe-writer"]
	if got.Type != "codex" || got.Auth != (config.RecipeAuth{}) || got.Reasoning != "" || got.SystemPrompt != "" {
		t.Fatalf("recipe was merged instead of replaced: %+v", got)
	}
	profile := cfg.Collaboration.Profiles["demo-pipe-writer"]
	if profile.Title != "Renamed" || profile.Command != "" || profile.Delegate {
		t.Fatalf("profile was merged instead of replaced: %+v", profile)
	}
	if prov.Agents["demo-pipe-writer"] != OriginOptions || prov.Agents["mine"] != OriginBase || prov.Profiles["demo-pipe-writer"] != OriginOptions {
		t.Fatalf("provenance: %+v", prov)
	}
	if len(warnings) != 1 || !strings.Contains(string(warnings[0]), "sets no command") {
		t.Fatalf("warnings = %v", warnings)
	}
	if n := len(cfg.Docker.ExtraMounts); n != 1 || cfg.Docker.ExtraMounts[0].Path != snap {
		t.Fatalf("extra mounts = %+v", cfg.Docker.ExtraMounts)
	}
}

func TestAssembleDoesNotMutateBase(t *testing.T) {
	snap := pkgSnapshotDir(t)
	pkg, err := ParseLayer([]byte(pkgAgents), OriginPackage, snap)
	if err != nil {
		t.Fatal(err)
	}
	base := config.Default()
	base.Docker.Agents = map[string]config.AgentRecipe{}
	base.Collaboration.Profiles = map[string]config.ProfileConfig{}
	base.Docker.ExtraMounts = config.ExtraMounts{{Path: "/keep"}}
	if _, _, _, err := Assemble(base, pkg, nil, snap); err != nil {
		t.Fatal(err)
	}
	if len(base.Docker.Agents) != 0 || len(base.Collaboration.Profiles) != 0 || len(base.Docker.ExtraMounts) != 1 {
		t.Fatalf("base was mutated: %+v", base)
	}
}

func TestAssembleWarnsWhenPackageShadowsBase(t *testing.T) {
	snap := pkgSnapshotDir(t)
	pkg, _ := ParseLayer([]byte(pkgAgents), OriginPackage, snap)
	base := config.Default()
	base.Docker.Agents = map[string]config.AgentRecipe{"demo-pipe-writer": {Type: "codex"}}
	_, prov, warnings, err := Assemble(base, pkg, nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	if prov.Agents["demo-pipe-writer"] != OriginPackage || len(warnings) != 1 || !strings.Contains(string(warnings[0]), "shadows") {
		t.Fatalf("prov=%+v warnings=%v", prov, warnings)
	}
}

func TestAssembleKeepsExplicitZeroValues(t *testing.T) {
	opts, err := ParseLayer([]byte("collaboration:\n  profiles:\n    p: {title: T, command: c, delegate: false}\n"), OriginOptions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := Layer{Origin: OriginPackage, Profiles: map[string]config.ProfileConfig{"p": {Title: "old", Command: "old", Delegate: true}}}
	cfg, _, _, err := Assemble(config.Default(), pkg, &opts, "/snap")
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Collaboration.Profiles["p"]; p.Delegate || p.Command != "c" {
		t.Fatalf("profile = %+v", p)
	}
}

func TestLayerRejectsDisallowedKeys(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"image", "docker:\n  image: x\n", "docker.image"},
		{"top level", "theme: dark\n", "theme"},
		{"mounts", "docker:\n  extra_mounts: [/]\n", "docker.extra_mounts"},
		{"collaboration key", "collaboration:\n  enabled: false\n", "collaboration.enabled"},
		{"recipe key", "docker:\n  agents:\n    a:\n      type: claude\n      model: m\n      bogus: 1\n", "docker.agents.a.bogus"},
		{"auth key", "docker:\n  agents:\n    a:\n      model: m\n      auth: {to: \"env:ANTHROPIC_API_KEY\", x: 1}\n", "docker.agents.a.auth.x"},
		{"profile key", "collaboration:\n  profiles:\n    a: {title: t, nope: 1}\n", "collaboration.profiles.a.nope"},
		{"invalid recipe", "docker:\n  agents:\n    a: {type: claude}\n", "docker.agents.a: recipe: model is required"},
		{"null budget", "collaboration:\n  profiles:\n    a: {title: t, budget: null}\n", "collaboration.profiles.a"},
	}
	for _, tc := range cases {
		for _, origin := range []LayerOrigin{OriginPackage, OriginOptions} {
			t.Run(tc.name+"/"+string(origin), func(t *testing.T) {
				_, err := ParseLayer([]byte(tc.yaml), origin, t.TempDir())
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want containing %q", err, tc.want)
				}
			})
		}
	}
}

func TestLayerRejectsPackageFileReferences(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(t.TempDir(), "fifo")
	cases := []struct{ name, recipe, want string }{
		{"auth from file", `auth: {from: "file:` + fifo + `", to: "env:ANTHROPIC_API_KEY"}`, "docker.agents.demo-pipe-a.auth.from"},
		{"prompt outside", `system_prompt: "file:../fifo"`, "docker.agents.demo-pipe-a.system_prompt"},
		{"prompt absolute", `system_prompt: "file:` + fifo + `"`, "docker.agents.demo-pipe-a.system_prompt"},
		{"prompt missing", `system_prompt: "file:nope.md"`, "docker.agents.demo-pipe-a.system_prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := `auth: {to: "env:ANTHROPIC_API_KEY"},`
			if strings.HasPrefix(tc.recipe, "auth") {
				auth = ""
			}
			src := "docker:\n  agents:\n    demo-pipe-a: {type: claude, model: m, " + auth + " " + tc.recipe + "}\n"
			_, err := ParseLayer([]byte(src), OriginPackage, dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLayerPackagePromptMustBeRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ParseLayer([]byte("docker:\n  agents:\n    demo-pipe-a: {type: claude, model: m, system_prompt: \"file:p\", auth: {to: \"env:ANTHROPIC_API_KEY\"}}\n"), OriginPackage, dir)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v", err)
	}
}

func TestOptionsKeepFullSemanticsAndPinRelativePaths(t *testing.T) {
	dir := t.TempDir()
	layer, err := ParseLayer([]byte(`docker:
  agents:
    x:
      type: openai
      model: m
      url: https://gw.example/v1
      system_prompt: file:rel.md
      auth: {from: "file:secret.txt", to: "env:OPENAI_API_KEY"}
`), OriginOptions, dir)
	if err != nil {
		t.Fatal(err)
	}
	r := layer.Agents["x"]
	if r.SystemPrompt != "file:"+filepath.Join(dir, "rel.md") || r.Auth.From != "file:"+filepath.Join(dir, "secret.txt") {
		t.Fatalf("paths were not pinned: %+v", r)
	}
}

func TestValidatePackageNamesAndRoles(t *testing.T) {
	snap := pkgSnapshotDir(t)
	m := testManifest()
	cases := []struct{ name, yaml, want string }{
		{"agent prefix", strings.Replace(pkgAgents, "demo-pipe-writer:\n      type", "writer:\n      type", 1), "docker.agents.writer"},
		{"profile prefix", strings.Replace(pkgAgents, "    demo-pipe-writer: {title", "    writer: {title", 1), "collaboration.profiles.writer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layer, err := ParseLayer([]byte(tc.yaml), OriginPackage, snap)
			if err != nil {
				t.Fatal(err)
			}
			if err := layer.ValidatePackage(m); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
	layer, _ := ParseLayer([]byte(pkgAgents), OriginPackage, snap)
	m.Roles["other"] = Role{Command: "demo-pipe-missing"}
	if err := layer.ValidatePackage(m); err == nil || !strings.Contains(err.Error(), "roles.other.command") {
		t.Fatalf("err = %v", err)
	}
	delete(m.Roles, "other")
	m.Roles["writer"] = Role{Command: "demo-pipe-writer", Profile: "demo-pipe-gone"}
	if err := layer.ValidatePackage(m); err == nil || !strings.Contains(err.Error(), "roles.writer.profile") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLayerEmptyAndNonMapping(t *testing.T) {
	if l, err := ParseLayer(nil, OriginOptions, "/"); err != nil || len(l.Agents)+len(l.Profiles) != 0 {
		t.Fatalf("empty layer: %+v %v", l, err)
	}
	if _, err := ParseLayer([]byte("- a\n"), OriginOptions, "/"); err == nil {
		t.Fatal("expected an error for a list")
	}
}

func TestEncodeLayerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := `docker:
  agents:
    x:
      type: openai
      model: m
      url: https://gw.example/v1
      auth: {from: "env:OPENAI_API_KEY", to: "env:OPENAI_API_KEY"}
collaboration:
  profiles:
    p:
      title: T
      command: x
      delegate: false
      runtime_context: isolated
      budget: {tokens: 1000}
`
	layer, err := ParseLayer([]byte(src), OriginOptions, dir)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeLayer(layer)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseLayer(encoded, OriginOptions, "/elsewhere")
	if err != nil {
		t.Fatalf("encoded layer does not parse: %v\n%s", err, encoded)
	}
	if again.Agents["x"] != layer.Agents["x"] || again.Profiles["p"].Title != "T" || again.Profiles["p"].Budget == nil || again.Profiles["p"].RuntimeContext == nil {
		t.Fatalf("round trip lost data:\n%s", encoded)
	}
	if empty, err := EncodeLayer(Layer{}); err != nil {
		t.Fatalf("empty: %v %s", err, empty)
	} else if l, err := ParseLayer(empty, OriginOptions, "/"); err != nil || len(l.Agents)+len(l.Profiles) != 0 {
		t.Fatalf("empty layer round trip: %v %+v", err, l)
	}
}

func TestAssembleWarnsWhenAPackageRecipeSendsASecretToAURL(t *testing.T) {
	pkg, err := ParseLayer([]byte(`docker:
  agents:
    demo-pipe-gw:
      type: openai
      model: m
      url: https://gw.example/v1
      auth: {from: "env:OPENAI_API_KEY", to: "env:OPENAI_API_KEY"}
    demo-pipe-local: {type: claude, model: m, auth: {from: "env:ANTHROPIC_API_KEY", to: "env:ANTHROPIC_API_KEY"}}
`), OriginPackage, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, warnings, err := Assemble(config.Default(), pkg, nil, "/snap")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(string(warnings[0]), "demo-pipe-gw") || !strings.Contains(string(warnings[0]), "env:OPENAI_API_KEY") || !strings.Contains(string(warnings[0]), "https://gw.example/v1") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestOptionsExpandHomeInSystemPromptOnTheHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, home, ".afm/prompt.md", "home prompt")
	layer, err := ParseLayer([]byte("docker:\n  agents:\n    mine: {type: claude, model: m, auth: {to: \"env:ANTHROPIC_API_KEY\"}, system_prompt: \"file:~/.afm/prompt.md\"}\n"), OriginOptions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := "file:" + filepath.Join(home, ".afm/prompt.md")
	if got := layer.Agents["mine"].SystemPrompt; got != want {
		t.Fatalf("system_prompt = %q, want %q", got, want)
	}
	text, err := docker.ResolveSystemPrompt(layer.Agents["mine"].SystemPrompt)
	if err != nil || text != "home prompt" {
		t.Fatalf("existing resolver: %q, %v", text, err)
	}
}

func TestLayerRejectsCommandNamesThatAreNotPlainFileNames(t *testing.T) {
	for _, origin := range []LayerOrigin{OriginPackage, OriginOptions} {
		for _, name := range []string{"tpkg-a/../../x", "../x", "a/b", `a\b`, ".hidden", "a b", "-x"} {
			data := "docker:\n  agents:\n    " + strconv.Quote(name) + ": {type: claude, model: m}\n"
			if _, err := ParseLayer([]byte(data), origin, t.TempDir()); err == nil || !strings.Contains(err.Error(), "command name") {
				t.Errorf("%s %q: err = %v", origin, name, err)
			}
		}
	}
}
