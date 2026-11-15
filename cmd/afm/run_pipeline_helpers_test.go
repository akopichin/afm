package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/pkg/pipeline"
)

// teamFlow is the fixture's two script stages plus one team member whose profile only an options file defines.
func (pf *pipelineFixture) teamFlow(profile string) string {
	return strings.Replace(pf.packageFlow("pkgflow"), "stages:", "team:\n  - {id: w, profile: "+profile+", title: W}\nstages:", 1)
}

func (pf *pipelineFixture) writeOptions(content string) string {
	pf.t.Helper()
	path := filepath.Join(pf.t.TempDir(), "options.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		pf.t.Fatal(err)
	}
	return path
}

// A team agent created from an options profile keeps its saved profile: continuing without the options file is
// what the engine accepts, so preflight must not call the profile unknown.
func TestPipelineResumeAcceptsSavedTeamMemberWithoutOptionsProfile(t *testing.T) {
	pf := newPipelineFixture(t)
	binDir := writeFakeCodexHelper(t, "pass", "Saved team member.")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{"flow.yaml": pf.teamFlow("opt-p")})
	options := pf.writeOptions("collaboration:\n  profiles:\n    opt-p: {title: Optional, command: codex-as-claude}\n")
	if code, _, stderr := pf.runAfm("--pipeline", dir, "--pipeline-options", options); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	code, stdout, stderr := pf.runAfm("--pipeline", dir)
	if code != 0 || strings.Contains(stderr, "unknown assistant profile") {
		t.Fatalf("continuing a saved team member: code=%d\n%s%s", code, stdout, stderr)
	}
}

func TestPipelineNewRunStillNeedsTheTeamProfile(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{"flow.yaml": pf.teamFlow("opt-p")})
	code, _, stderr := pf.runAfm("--pipeline", dir)
	if code == 0 || !strings.Contains(stderr, "unknown assistant profile") || len(pf.runDirs()) != 0 {
		t.Fatalf("code=%d runs=%v stderr=%s", code, pf.runDirs(), stderr)
	}
}

// Options with an invalid profile id are refused before a run exists even when the flow declares no helpers.
func TestPipelineRejectsInvalidProfileRegistryWithoutDeclaredHelpers(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	options := pf.writeOptions("collaboration:\n  profiles:\n    invalid/name: {title: Bad}\n")
	code, _, stderr := pf.runAfm("--pipeline", dir, "--pipeline-options", options)
	if code == 0 || !strings.Contains(stderr, "collaboration.profiles") || len(pf.runDirs()) != 0 {
		t.Fatalf("code=%d runs=%v stderr=%s", code, pf.runDirs(), stderr)
	}
}

// A recipe alias is a wrapper file name in Docker: one that climbs out of the wrapper directory must stop the start
// before the engine opens a run and writes anything.
func TestPipelineRejectsTraversingRecipeAliasBeforeAnyRun(t *testing.T) {
	pf := newPipelineFixture(t)
	sentinel := filepath.Join(pf.root, "escaped.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := "docker:\n  agents:\n    \"tpkg-a/../../../escaped.txt\": {type: codex}\n"
	manifest := "schema_version: 2\nname: tpkg\nversion: 1.0.0\nentrypoint: flow.yaml\nagents: agents.yaml\n"
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{pipeline.ManifestFile: manifest, "agents.yaml": agents})
	code, _, stderr := pf.runAfm("--pipeline", dir)
	if code == 0 || !strings.Contains(stderr, "command name") || len(pf.runDirs()) != 0 {
		t.Fatalf("code=%d runs=%v stderr=%s", code, pf.runDirs(), stderr)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatalf("sentinel changed: %q %v", got, err)
	}
}

// With a helper profile that selects another command, the role report names the executor the helper really runs.
func TestDescribeRolesReportsEffectiveHelperProfileExecutor(t *testing.T) {
	m := pipeline.Manifest{Name: "tpkg", Roles: map[string]pipeline.Role{"reviewer": {Command: "tpkg-helper", Profile: "tpkg-reviewer"}}}
	cfg := config.Config{}
	cfg.Docker.Agents = map[string]config.AgentRecipe{
		"tpkg-helper": {Type: "claude", Model: "package-model"},
		"user-codex":  {Type: "codex", Model: "user-model"},
	}
	cfg.Collaboration.Profiles = map[string]config.ProfileConfig{"tpkg-reviewer": {Title: "Reviewer", Command: "user-codex"}}
	prov := pipeline.Provenance{
		Agents:   map[string]pipeline.LayerOrigin{"tpkg-helper": pipeline.OriginPackage, "user-codex": pipeline.OriginOptions},
		Profiles: map[string]pipeline.LayerOrigin{"tpkg-reviewer": pipeline.OriginOptions},
	}
	roles := describeRoles(m, cfg, prov)
	if len(roles) != 1 || roles[0].Client != "claude" || roles[0].Model != "package-model" {
		t.Fatalf("role's own command: %+v", roles)
	}
	helper := roles[0].ProfileExecutor
	if helper == nil || helper.Command != "user-codex" || helper.Client != "codex" || helper.Model != "user-model" || helper.CommandLayer != "options" || roles[0].ProfileExecutorInheritance != "" {
		t.Fatalf("helper executor: %+v %q", helper, roles[0].ProfileExecutorInheritance)
	}
	text := describeRoleSettings(roles[0])
	if !strings.Contains(text, "helper profile runs command user-codex, client codex, model user-model") {
		t.Fatalf("banner text: %s", text)
	}
}

// A profile without a command inherits the executor of wherever the helper starts (its parent, stage or client), so
// the report names that rule instead of a fixed executor such as client.command.
func TestDescribeRolesReportsInheritedHelperExecutorAsContextDependent(t *testing.T) {
	m := pipeline.Manifest{Name: "tpkg", Roles: map[string]pipeline.Role{"reviewer": {Command: "tpkg-reviewer", Profile: "tpkg-reviewer"}}}
	cfg := config.Config{}
	cfg.Client.Command = config.ClaudeCommand
	cfg.Docker.Agents = map[string]config.AgentRecipe{"tpkg-reviewer": {Type: "claude", Model: "review-model"}}
	cfg.Collaboration.Profiles = map[string]config.ProfileConfig{"tpkg-reviewer": {Title: "Only title"}}
	role := describeRoles(m, cfg, pipeline.Provenance{})[0]
	wire := roleWire(t, role)
	if _, fixed := wire["profile_executor"]; fixed || wire["profile_executor_inheritance"] != inheritedOrder {
		t.Fatalf("role report: %v", wire)
	}
	if text := describeRoleSettings(role); strings.Contains(text, "helper profile runs") || !strings.Contains(text, "helper profile executor depends on where it starts: "+inheritedOrder) {
		t.Fatalf("banner text: %s", text)
	}
}

// inheritedOrder is how the report words the executor of a profile without a command.
const inheritedOrder = "the delegating agent's executor, else stage.command, else client.command"

// roleWire is a role report as inspect --json prints it.
func roleWire(t *testing.T, role roleReport) map[string]any {
	t.Helper()
	data, err := json.Marshal(role)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestDescribeRolesHidesHelperExecutorWhenProfileUsesTheRoleCommand(t *testing.T) {
	m := pipeline.Manifest{Name: "tpkg", Roles: map[string]pipeline.Role{"w": {Command: "tpkg-w", Profile: "tpkg-w"}}}
	cfg := config.Config{}
	cfg.Docker.Agents = map[string]config.AgentRecipe{"tpkg-w": {Type: "codex"}}
	cfg.Collaboration.Profiles = map[string]config.ProfileConfig{"tpkg-w": {Title: "W", Command: "tpkg-w"}}
	roles := describeRoles(m, cfg, pipeline.Provenance{Agents: map[string]pipeline.LayerOrigin{}, Profiles: map[string]pipeline.LayerOrigin{}})
	if wire := roleWire(t, roles[0]); wire["profile_executor"] != nil || wire["profile_executor_inheritance"] != nil {
		t.Fatalf("unexpected helper executor: %v", wire)
	}
}
