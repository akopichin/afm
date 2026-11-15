package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifest = `schema_version: 2
name: demo-pipe
version: 1.0.0
description: demo
entrypoint: flow.yaml
agents: agents.yaml
resources: [skills, scripts/run.sh]
roles:
  writer: {command: demo-pipe-writer, profile: demo-pipe-writer, description: writes}
requirements:
  docker_auto_shim: true
  collaboration: true
  tools: [git, python3]
`

func TestLoadManifestValid(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ManifestFile, validManifest)
	m, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "demo-pipe" || m.Entrypoint != "flow.yaml" || m.Roles["writer"].Command != "demo-pipe-writer" || !m.Requirements.DockerAutoShim || len(m.Requirements.Tools) != 2 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestParseManifestRejects(t *testing.T) {
	cases := []struct {
		name, mutate, want string
	}{
		{"v1 catalog", "schema_version: 1\nname: x\nversion: 1\nentrypoint: flow.yaml\ninstallation: {}\n", "catalog installer"},
		{"unknown version", "schema_version: 9\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\n", "unsupported schema_version 9"},
		{"missing version", "name: demo-pipe\nversion: 1\nentrypoint: flow.yaml\n", "schema_version is required"},
		{"unknown key", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\nsetup: {}\n", "field setup not found"},
		{"bad name", "schema_version: 2\nname: Demo\nversion: 1\nentrypoint: flow.yaml\n", "name"},
		{"one letter name", "schema_version: 2\nname: a\nversion: 1\nentrypoint: flow.yaml\n", "name"},
		{"no version", "schema_version: 2\nname: demo-pipe\nentrypoint: flow.yaml\n", "version is required"},
		{"entrypoint escapes", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: ../flow.yaml\n", "leaves the package root"},
		{"absolute entrypoint", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: /etc/passwd\n", "relative"},
		{"resource escapes", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\nresources: [skills/../../x]\n", "resources[0]"},
		{"resource whole root", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\nresources: [.]\n", "leaves the package root"},
		{"role without command", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\nroles: {w: {description: x}}\n", "roles.w.command"},
		{"tool path", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\nrequirements: {tools: [/bin/sh]}\n", "requirements.tools[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.mutate))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadManifestRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "m.yaml")
	if err := os.WriteFile(outside, []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ManifestFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v", err)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
