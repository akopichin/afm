//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/pipeline"
)

// A named pipe referenced by a package would hang any code that opens it; loading must reject the reference by name.
// Each case puts a FIFO at the exact path the reference names, so a regression blocks instead of failing with ENOENT;
// inspect runs as a child that startAfmChild's cleanup kills and reaps, since a blocked open(2) cannot be cancelled.
func TestPipelinesInspectNeverOpensFifoReferences(t *testing.T) {
	manifest := "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nagents: agents.yaml\n"
	cases := []struct {
		name string
		// recipe builds the recipe YAML given the pipe path; fifoPath is the pipe's location given the package dir.
		recipe   func(fifo string) string
		fifoPath func(pkgDir string) string
		want     string
	}{
		{"auth from file", func(fifo string) string {
			return `{type: claude, model: m, auth: {from: "file:` + fifo + `", to: "env:ANTHROPIC_API_KEY"}}`
		}, func(pkgDir string) string { return filepath.Join(filepath.Dir(pkgDir), "auth.fifo") }, "docker.agents.tpkg-a.auth.from"},
		{"system prompt outside", func(string) string {
			return `{type: claude, model: m, system_prompt: "file:../prompt.fifo", auth: {to: "env:ANTHROPIC_API_KEY"}}`
		}, func(pkgDir string) string { return filepath.Join(filepath.Dir(pkgDir), "prompt.fifo") }, "docker.agents.tpkg-a.system_prompt"},
		{"system prompt inside", func(string) string {
			return `{type: claude, model: m, system_prompt: "file:prompt.fifo", auth: {to: "env:ANTHROPIC_API_KEY"}}`
		}, func(pkgDir string) string { return filepath.Join(pkgDir, "prompt.fifo") }, "docker.agents.tpkg-a.system_prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pf := newPipelineFixture(t)
			// The directory is fixed by writePackage, so write the package first and add the pipe at its target.
			dir := pf.writePackage("tpkg", "pkgflow", map[string]string{pipeline.ManifestFile: manifest, "agents.yaml": "docker:\n  agents:\n    tpkg-a: {type: claude, model: m}\n"})
			fifo := tc.fifoPath(dir)
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
			agents := "docker:\n  agents:\n    tpkg-a: " + tc.recipe(fifo) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "agents.yaml"), []byte(agents), 0o644); err != nil {
				t.Fatal(err)
			}
			// The fixture's HOME, so the snapshot inspect caches is unlocked by the fixture's cleanup.
			child := startAfmChild(t, pf.root, []string{"HOME=" + pf.home}, "pipelines", "inspect", "--dir", pf.root, dir)
			select {
			case <-child.done:
			case <-time.After(10 * time.Second):
				t.Fatal("inspect hung: it must not open a named pipe")
			}
			if code, output := child.exitCode(t), child.out.String(); code == 0 || !strings.Contains(output, tc.want) {
				t.Fatalf("code=%d output=%q, want a failure containing %q", code, output, tc.want)
			}
		})
	}
}
