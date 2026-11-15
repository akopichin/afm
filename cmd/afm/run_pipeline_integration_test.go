//go:build !windows

package main

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
	"github.com/akopichin/afm/pkg/pipeline"
)

// hostPipelineFixture runs packages on the host with a fake agent under the package command's name.
type hostPipelineFixture struct {
	*pipelineFixture
	marker, release string
}

func newHostPipelineFixture(t *testing.T) *hostPipelineFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	bin := t.TempDir()
	data, err := os.ReadFile(filepath.Join(testdataFakes, "fake-claude"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, harnessCommand), data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := &hostPipelineFixture{pipelineFixture: pf, marker: filepath.Join(pf.root, "agent.markers"), release: filepath.Join(pf.root, "agent.release")}
	t.Setenv("FAKE_AGENT_MARKER_FILE", h.marker)
	t.Setenv("FAKE_AGENT_RELEASE_FILE", h.release)
	t.Setenv("FAKE_AGENT_RELEASE_TIMEOUT", "120")
	return h
}

// cancelWhenAgentStarts makes the next cycle behave as if the user pressed Ctrl+C once the agent wrote its marker.
func (h *hostPipelineFixture) cancelWhenAgentStarts() (restore func()) {
	prev := executeCycle
	restore = func() { executeCycle = prev }
	h.t.Cleanup(restore)
	executeCycle = func(ctx context.Context, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, opts runOptions) (cycleResult, error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			for ctx.Err() == nil {
				if data, _ := os.ReadFile(h.marker); strings.Contains(string(data), "claude") {
					cancel()
					return
				}
				waitTick()
			}
		}()
		return prev(ctx, f, cfg, hooks, opts)
	}
	return restore
}

func (h *hostPipelineFixture) hostPackage() string {
	return h.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1.0.0\nentrypoint: flow.yaml\nagents: agents.yaml\nroles:\n  writer: {command: tpkg-writer}\n",
		"agents.yaml":         harnessAgents,
		"flow.yaml": "name: pkgflow\nstages:\n  - id: prep\n    name: Prep\n    script: echo prepared >> " + filepath.Join(h.root, "prep.log") +
			"\n  - id: write\n    name: Write\n    depends_on: [prep]\n    agents: [auto]\n    command: tpkg-writer\n",
	})
}

// A host run interrupted mid-stage continues on the same run with changed options: earlier results stay, the
// binding follows the new recipe, and the user's config is never written.
func TestPipelineHostResumeWithChangedOptions(t *testing.T) {
	h := newHostPipelineFixture(t)
	if err := os.MkdirAll(filepath.Join(h.root, ".afm"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(h.root, ".afm", "config.yaml")
	if err := os.WriteFile(configPath, []byte("client:\n  command: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256([]byte("client:\n  command: claude\n"))
	dir := h.hostPackage()

	restore := h.cancelWhenAgentStarts()
	code, stdout, stderr := h.runAfm("--pipeline", dir)
	restore()
	if code != interruptedExitCode {
		t.Fatalf("interrupted start: code=%d\n%s%s", code, stdout, stderr)
	}
	runs := h.runDirs()
	if len(runs) != 1 {
		t.Fatalf("run dirs = %v", runs)
	}
	first := h.bindingFor(runs[0])
	if first.Recipes[harnessCommand].Type != "claude" || first.Options != nil {
		t.Fatalf("first binding = %+v", first)
	}

	options := h.write("o.yaml", "docker:\n  agents:\n    tpkg-writer: {type: codex, model: gpt-6-sol}\n")
	if err := os.WriteFile(h.release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = h.runAfm("--pipeline", dir, "--pipeline-options", options)
	if code != 0 {
		t.Fatalf("resume: code=%d\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "continuing run "+runs[0]) {
		t.Fatalf("resume banner missing:\n%s", stdout)
	}
	stages, finished := h.runDirState(runs[0])
	if !finished || stages["prep"] != "done" || stages["write"] != "done" {
		t.Fatalf("stages=%v finished=%v", stages, finished)
	}
	if prep, _ := os.ReadFile(filepath.Join(h.root, "prep.log")); strings.Count(string(prep), "prepared") != 1 {
		t.Fatalf("the finished stage must not rerun: %q", prep)
	}
	after := h.bindingFor(runs[0])
	if after.Recipes[harnessCommand].Type != "codex" || after.Options == nil || after.Options.Path != options || after.SnapshotDir != first.SnapshotDir {
		t.Fatalf("binding after resume = %+v", after)
	}
	data, _ := os.ReadFile(configPath)
	if sha256.Sum256(data) != before {
		t.Fatal("package mode rewrote .afm/config.yaml")
	}
}
