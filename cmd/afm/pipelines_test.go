package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/pipeline"
)

const inspectAgents = `docker:
  agents:
    tpkg-writer:
      type: claude
      model: claude-sonnet-5-5
      reasoning: medium
      auth: {from: "env:CLAUDE_CODE_OAUTH_TOKEN", to: "env:CLAUDE_CODE_OAUTH_TOKEN"}
collaboration:
  profiles:
    tpkg-writer: {title: Writer, command: tpkg-writer}
`

const inspectManifest = `schema_version: 2
name: tpkg
version: 2.1.0
description: inspect demo
entrypoint: flow.yaml
agents: agents.yaml
roles:
  writer: {command: tpkg-writer, profile: tpkg-writer, description: writes}
requirements:
  collaboration: true
  tools: [definitely-not-installed-tool]
`

func (pf *pipelineFixture) inspectPackage() string {
	return pf.writePackage("tpkg", "pkgflow", map[string]string{pipeline.ManifestFile: inspectManifest, "agents.yaml": inspectAgents})
}

func (pf *pipelineFixture) inspect(args ...string) (code int, stdout, stderr string) {
	pf.t.Helper()
	full := append([]string{"pipelines", "inspect", "--dir", pf.root}, args...)
	stdout, stderr = captureOutput(pf.t, func() { code = runMain(full) })
	return code, stdout, stderr
}

func TestPipelinesInspectJSON(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.inspectPackage()
	options := pf.write("o.yaml", "docker:\n  agents:\n    tpkg-writer: {type: codex, model: gpt-6-sol}\n")
	// From another working directory, with a relative path.
	t.Chdir(filepath.Dir(dir))
	code, stdout, stderr := pf.inspect("tpkg", "--pipeline-options", options, "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var report inspectReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if report.Source.Kind != pipeline.SourceLocal || report.Source.Location != dir || report.Package.Name != "tpkg" || report.Package.Version != "2.1.0" {
		t.Fatalf("source/package = %+v %+v", report.Source, report.Package)
	}
	if len(report.Snapshot.SHA256) != 64 || report.Options == nil || report.Options.Path != options {
		t.Fatalf("snapshot/options = %+v %+v", report.Snapshot, report.Options)
	}
	if len(report.Roles) != 1 {
		t.Fatalf("roles = %+v", report.Roles)
	}
	role := report.Roles[0]
	if role.ID != "writer" || role.Client != "codex" || role.Model != "gpt-6-sol" || role.CommandLayer != "options" || role.ProfileLayer != "package" || role.ProfileTitle != "Writer" {
		t.Fatalf("role = %+v", role)
	}
	missing := map[string]string{}
	for _, c := range report.Requirements {
		missing[c.Name] = c.Status
	}
	if missing["tool:definitely-not-installed-tool"] != checkMissing || missing["collaboration"] != checkOK {
		t.Fatalf("requirements = %+v", report.Requirements)
	}
	if report.Warnings == nil {
		t.Fatal("warnings must be a JSON array, not null")
	}
	// Inspect writes nothing about the project: no config, no runs, no bindings.
	if _, err := os.Stat(filepath.Join(pf.root, ".afm")); err == nil {
		t.Fatal("inspect must not create .afm in the project")
	}
}

func TestPipelinesInspectTextShowsRolesAndMissing(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.inspectPackage()
	code, stdout, stderr := pf.inspect(dir)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{"Pipeline tpkg 2.1.0", "writer: command tpkg-writer, client claude, model claude-sonnet-5-5, reasoning medium, auth env:CLAUDE_CODE_OAUTH_TOKEN, from package", "[MISSING] tool:definitely-not-installed-tool", "[ok] collaboration"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

// An options profile with only a title replaces the package profile and names no command: inspect says the helper's
// executor is inherited where it starts, in JSON and text, instead of naming one fixed command.
func TestPipelinesInspectReportsInheritedHelperExecutor(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.inspectPackage()
	options := pf.write("title-only.yaml", "collaboration:\n  profiles:\n    tpkg-writer: {title: User title}\n")
	code, stdout, stderr := pf.inspect(dir, "--pipeline-options", options, "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var report struct {
		Roles []map[string]any `json:"roles"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if len(report.Roles) != 1 || report.Roles[0]["profile_title"] != "User title" || report.Roles[0]["profile_executor"] != nil || report.Roles[0]["profile_executor_inheritance"] != inheritedOrder {
		t.Fatalf("roles = %v", report.Roles)
	}
	code, stdout, stderr = pf.inspect(dir, "--pipeline-options", options)
	if code != 0 || strings.Contains(stdout, "helper profile runs") || !strings.Contains(stdout, "helper profile executor depends on where it starts: "+inheritedOrder) {
		t.Fatalf("code=%d stderr=%q text:\n%s", code, stderr, stdout)
	}
}

func TestPipelinesInspectRejectsInvalidInput(t *testing.T) {
	pf := newPipelineFixture(t)
	options := func(body string) string { return pf.write("bad-options.yaml", body) }
	cases := []struct {
		name    string
		files   map[string]string
		options string
		want    string
	}{
		{"catalog manifest", map[string]string{pipeline.ManifestFile: "schema_version: 1\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\n"}, "", "catalog installer"},
		{"unsupported version", map[string]string{pipeline.ManifestFile: "schema_version: 9\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\n"}, "", "unsupported schema_version"},
		{"options image", map[string]string{pipeline.ManifestFile: inspectManifest, "agents.yaml": inspectAgents}, options("docker:\n  image: evil\n"), "docker.image"},
		{"alias without prefix", map[string]string{
			pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nagents: agents.yaml\n",
			"agents.yaml":         strings.ReplaceAll(inspectAgents, "tpkg-writer", "writer"),
		}, "", "docker.agents.writer"},
		{"missing role command", map[string]string{
			pipeline.ManifestFile: strings.Replace(inspectManifest, "command: tpkg-writer", "command: tpkg-gone", 1),
			"agents.yaml":         inspectAgents,
		}, "", "roles.writer.command"},
		{"unknown helper profile in flow", map[string]string{
			pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\n",
			"flow.yaml":           "name: f\nstages:\n  - id: a\n    name: A\n    agents: [planning, implementation]\n    assistants: [{profile: nobody}]\n",
		}, "", "nobody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := pf.writePackage("tpkg", "pkgflow", tc.files)
			args := []string{dir}
			if tc.options != "" {
				args = append(args, "--pipeline-options", tc.options)
			}
			code, _, stderr := pf.inspect(args...)
			if code == 0 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stderr=%q, want a failure containing %q", code, stderr, tc.want)
			}
		})
	}
}

func TestPipelinesInspectGitSource(t *testing.T) {
	pf := newPipelineFixture(t)
	repo := pf.newGitPackageRepo("pkgflow")
	commit := repo.git(repo.work, "rev-parse", "HEAD")
	repo.git(repo.work, "tag", "v1")
	repo.git(repo.work, "push", "-q", repo.bare, "main", "v1")
	code, stdout, stderr := pf.inspect(repo.source("v1"), "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var report inspectReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report.Source.Kind != pipeline.SourceGit || report.Source.Ref != "v1" || report.Source.Subdir != "pipelines/tpkg" || report.Commit != commit || len(report.Snapshot.SHA256) != 64 {
		t.Fatalf("report = %+v", report)
	}
}

func TestPipelinesInspectNeverEchoesURLCredentials(t *testing.T) {
	pf := newPipelineFixture(t)
	code, stdout, stderr := pf.inspect("https://u:tok123@example.invalid/r.git//p@v1")
	if code == 0 || strings.Contains(stdout+stderr, "tok123") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
