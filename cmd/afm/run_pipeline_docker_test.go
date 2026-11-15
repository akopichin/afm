//go:build !windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	implstate "github.com/akopichin/afm-engine/engine/inspection/state"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/pkg/pipeline"
)

const (
	harnessImage     = "afm-pipeline-test-image"
	harnessSecretVar = "FAKE_RECIPE_TOKEN" //nolint:gosec // name of a fake env var, not a credential
	harnessCommand   = "tpkg-writer"
	testdataFakes    = "testdata/pipeline-docker"
)

const harnessAgents = `docker:
  agents:
    tpkg-writer:
      type: claude
      model: claude-sonnet-5-5
      auth: {from: "env:FAKE_RECIPE_TOKEN", to: "env:CLAUDE_CODE_OAUTH_TOKEN"}
`

const harnessManifest = `schema_version: 2
name: tpkg
version: 1.0.0
entrypoint: flow.yaml
agents: agents.yaml
roles:
  writer: {command: tpkg-writer}
requirements:
  docker_auto_shim: true
`

// dockerCall is one captured `docker run` the host made.
type dockerCall struct {
	argv []string
	env  []string
}

// dockerHarness drives the host side of a Docker run in process, with `docker` replaced by a function that starts the
// container side as a child process of this test binary, with AFM_IN_DOCKER=1, the arguments and the environment the
// host handed to docker, and fake agents instead of accounts.
type dockerHarness struct {
	*pipelineFixture
	bin      string
	marker   string
	argvFile string // one line per fake agent invocation: the client and its arguments
	release  string
	calls    []dockerCall
	configSH string
	testArgs []string // os.Args of the test binary, before any host run replaces them
	afmBin   string   // a real afm binary for the container side; empty runs the test binary
	goCaches []string // GOMODCACHE and GOCACHE of the user running the tests, for building afm

	// beforeContainer runs after the host's preflight, right before the container side starts.
	beforeContainer func()
	// interruptAfter sends the container side SIGINT once the agents have written this many marker lines in total;
	// interruptWhen does so once it reports true. Either may be set.
	interruptAfter int
	interruptWhen  func() bool
	// containerEnv adds variables the real image would carry.
	containerEnv []string
	// output is everything the container side printed.
	output strings.Builder
	exits  []int
}

func newDockerHarness(t *testing.T) *dockerHarness {
	t.Helper()
	if _, err := os.Stat("/usr/local/bin/codex-as-claude"); err != nil {
		t.Skip("the generated Codex wrapper needs /usr/local/bin/codex-as-claude (present in the afm image)")
	}
	goCaches := goEnvPaths(t) // before the fixture replaces HOME, which the Go caches derive from
	pf := newPipelineFixture(t)
	h := &dockerHarness{goCaches: goCaches, pipelineFixture: pf, testArgs: os.Args, bin: t.TempDir(), marker: filepath.Join(pf.root, "agent.markers"), argvFile: filepath.Join(pf.root, "agent.argv"), release: filepath.Join(pf.root, "agent.release")}
	for name, target := range map[string]string{"claude": "fake-claude", "codex": "fake-codex"} {
		data, err := os.ReadFile(filepath.Join(testdataFakes, target))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.bin, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", h.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AFM_USE_DOCKER", "1")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "harness-client-token")
	t.Setenv(harnessSecretVar, "recipe-secret-one")
	t.Setenv(config.SecretEnvVar(harnessCommand), "") // ReExec sets it in this process: register the restore
	t.Setenv("FAKE_AGENT_MARKER_FILE", h.marker)
	t.Setenv("FAKE_AGENT_ARGV_FILE", h.argvFile)
	t.Setenv("FAKE_AGENT_RELEASE_FILE", h.release)
	t.Setenv("FAKE_AGENT_RELEASE_TIMEOUT", "120")
	t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return false }))
	docker.SetExecFunc(h.dockerRun)
	t.Cleanup(docker.ResetExecFunc)

	h.write(filepath.Join(".afm", "config.yaml"), "docker:\n  autoShim: true\n  image: "+harnessImage+"\n")
	h.configSH = h.configHash()
	return h
}

func (h *dockerHarness) configHash() string {
	data, err := os.ReadFile(filepath.Join(h.root, ".afm", "config.yaml"))
	if err != nil {
		h.t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// write overrides the fixture helper: the harness writes below the afm root, creating directories.
func (h *dockerHarness) write(rel, body string) string {
	h.t.Helper()
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return p
}

// agentFlow is a script stage followed by one agent stage on the package command; extra is appended to stages.
func (h *dockerHarness) agentFlow(extra string) string {
	return fmt.Sprintf(`name: pkgflow
stages:
  - id: prep
    name: Prep
    script: echo prepared >> %s
  - id: write
    name: Write
    depends_on: [prep]
    agents: [auto]
    command: %s
%s`, filepath.Join(h.root, "prep.log"), harnessCommand, extra)
}

func (h *dockerHarness) agentPackage(flowYAML string) string {
	return h.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: harnessManifest,
		"agents.yaml":         harnessAgents,
		"flow.yaml":           flowYAML,
	})
}

// codexOptions switches the package command to a Codex recipe.
func (h *dockerHarness) codexOptions() string {
	return h.write("options-codex.yaml", "docker:\n  agents:\n    tpkg-writer: {type: codex, model: gpt-6-sol}\n")
}

func (h *dockerHarness) markers() []string {
	data, err := os.ReadFile(h.marker)
	if err != nil {
		return nil
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(data)), "\n", " "))
}

// argvLines is what each fake agent invocation received, in order.
func (h *dockerHarness) argvLines() []string {
	data, err := os.ReadFile(h.argvFile)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// dockerRun is docker.ReExec's exec seam: record the call, then run the container side.
func (h *dockerHarness) dockerRun(_ string, argv, env []string) error {
	h.calls = append(h.calls, dockerCall{argv: append([]string(nil), argv...), env: append([]string(nil), env...)})
	image := indexOf(argv, harnessImage)
	if image < 0 {
		h.t.Fatalf("docker argv has no image %q: %v", harnessImage, argv)
	}
	if h.beforeContainer != nil {
		h.beforeContainer()
	}
	// startAfmChild re-runs os.Args[0], the test binary; the host run replaced os.Args with the afm command line.
	hostArgs := os.Args
	os.Args = h.testArgs
	var child *afmChild
	if h.afmBin != "" {
		child = startAfmBinary(h.t, h.afmBin, h.root, h.containerEnvFor(argv[:image], env), argv[image+1:]...)
	} else {
		child = startAfmChild(h.t, h.root, h.containerEnvFor(argv[:image], env), argv[image+1:]...)
	}
	os.Args = hostArgs
	if h.interruptAfter > 0 || h.interruptWhen != nil {
		waitFor(h.t, fmt.Sprintf("the interrupt condition (%d agent markers)", h.interruptAfter), func() bool {
			select {
			case <-child.done:
				h.t.Fatalf("container side exited before the agent started:\n%s", child.out.String())
			default:
			}
			if h.interruptWhen != nil {
				return h.interruptWhen()
			}
			data, _ := os.ReadFile(h.marker)
			return strings.Count(string(data), "\n") >= h.interruptAfter
		})
		_ = child.cmd.Process.Signal(syscall.SIGINT)
	}
	code := child.exitCode(h.t)
	h.output.WriteString(child.out.String())
	h.exits = append(h.exits, code)
	if code != 0 {
		return &docker.SubprocessExitError{Code: code}
	}
	return nil
}

// containerEnvFor is what the container process gets from `docker run`: the -e values, bare names taken from the
// host's environment, and the variables the image itself provides.
func (h *dockerHarness) containerEnvFor(args, hostEnv []string) []string {
	env := []string{"HOME=" + h.home, "AFM_IN_DOCKER=1", "AFM_WORKTREES=0", "PATH=" + os.Getenv("PATH"),
		"FAKE_AGENT_MARKER_FILE=" + h.marker, "FAKE_AGENT_ARGV_FILE=" + h.argvFile, "FAKE_AGENT_RELEASE_FILE=" + h.release, "FAKE_AGENT_RELEASE_TIMEOUT=120"}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-e" {
			continue
		}
		if name, _, ok := strings.Cut(args[i+1], "="); ok {
			env = append(env, args[i+1])
		} else if v, found := lookupEnv(hostEnv, name); found {
			env = append(env, name+"="+v)
		}
	}
	return append(env, h.containerEnv...)
}

func lookupEnv(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

func (h *dockerHarness) lastCall() dockerCall { return h.calls[len(h.calls)-1] }

// artifact stores evidence for the live run when AFM_IT_ARTIFACTS names a directory.
func (h *dockerHarness) artifact(name, content string) {
	dir := os.Getenv("AFM_IT_ARTIFACTS")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Logf("artifacts: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		h.t.Logf("artifacts: %v", err)
	}
}

func (h *pipelineFixture) runDirState(runID string) (stages map[string]string, finished bool) {
	rs := h.runState(runID)
	stages = map[string]string{}
	for id, st := range rs.Stages {
		stages[id] = string(st.Status)
	}
	return stages, string(rs.RunStatus) == "finished"
}

func TestPipelineDocker(t *testing.T) {
	t.Run("argv_mounts", testPipelineDockerArgvMounts)
	t.Run("container_no_fetch", testPipelineDockerContainerNoFetch)
	t.Run("container_options_from_launch_record", testPipelineDockerOptionsFromLaunchRecord)
	t.Run("client_switch", testPipelineDockerClientSwitch)
	t.Run("secret_only_change", testPipelineDockerSecretOnlyChange)
	t.Run("interactive_switch", testPipelineDockerInteractiveSwitch)
	t.Run("team_executor_changed", testPipelineDockerTeamExecutorChanged)
	t.Run("container_lock", testPipelineDockerContainerLock)
}

func testPipelineDockerArgvMounts(t *testing.T) {
	h := newDockerHarness(t)
	dir := h.agentPackage(h.agentFlow(""))
	h.beforeContainer = func() {}
	var launchSeen string
	var launchBody []byte
	docker.SetExecFunc(func(bin string, argv, env []string) error {
		h.calls = append(h.calls, dockerCall{argv: append([]string(nil), argv...), env: append([]string(nil), env...)})
		for _, a := range argv {
			if path, ok := strings.CutPrefix(a, "--pipeline-launch="); ok {
				launchSeen = path
				launchBody, _ = os.ReadFile(path)
			}
		}
		return nil
	})
	code, stdout, stderr := h.runAfm("--pipeline", dir)
	if code != 0 || len(h.calls) != 1 {
		t.Fatalf("code=%d calls=%d\n%s%s", code, len(h.calls), stdout, stderr)
	}
	argv := h.lastCall().argv
	b := h.bindingsAfterHostOnly()
	snapshot := b.SnapshotDir
	if want := snapshot + ":" + snapshot + ":ro"; indexOf(argv, want) < 0 || indexOf(argv, "-v") < 0 {
		t.Fatalf("argv lacks the read-only snapshot mount %q:\n%v", want, argv)
	}
	if launchSeen == "" || filepath.Dir(launchSeen) != pipelineLaunchDir() {
		t.Fatalf("argv lacks --pipeline-launch in %s: %v", pipelineLaunchDir(), argv)
	}
	joined := strings.Join(argv, "\n")
	for _, secret := range []string{"recipe-secret-one", "harness-client-token"} {
		if strings.Contains(joined, secret) || strings.Contains(string(launchBody), secret) {
			t.Fatalf("a secret value reached argv or the launch record: %s", secret)
		}
	}
	if !strings.Contains(string(launchBody), snapshot) || strings.Contains(string(launchBody), `"options"`) {
		t.Fatalf("launch record = %s", launchBody)
	}
	if indexOf(argv, "AFM_SECRET_TPKG_WRITER") < 0 && !hasBareSecretFlag(argv) {
		t.Fatalf("the recipe secret must travel as a bare -e NAME: %v", argv)
	}
	if _, err := os.Stat(launchSeen); !os.IsNotExist(err) {
		t.Fatalf("the host must remove its launch record after docker returns: %v", err)
	}
	if h.configHash() != h.configSH {
		t.Fatal("package mode rewrote .afm/config.yaml")
	}
	h.artifact("argv_mounts.argv.txt", strings.Join(argv, "\n"))
}

func hasBareSecretFlag(argv []string) bool {
	for i, a := range argv {
		if a == "-e" && i+1 < len(argv) && strings.HasPrefix(argv[i+1], "AFM_SECRET_") && !strings.Contains(argv[i+1], "=") {
			return true
		}
	}
	return false
}

// bindingsAfterHostOnly: with docker stubbed out no container writes a binding, so the snapshot is read from the
// launch record the host wrote... which is gone by now; the snapshot dir is the only snapshot in the cache.
func (h *dockerHarness) bindingsAfterHostOnly() pipeline.Binding {
	h.t.Helper()
	entries, err := os.ReadDir(pipeline.SnapshotsRoot(filepath.Join(h.home, config.AfmDir, "cache", "pipelines")))
	if err != nil || len(entries) != 1 {
		h.t.Fatalf("snapshots: %v %v", entries, err)
	}
	return pipeline.Binding{SnapshotDir: filepath.Join(pipeline.SnapshotsRoot(filepath.Join(h.home, config.AfmDir, "cache", "pipelines")), entries[0].Name())}
}

// startAndInterrupt runs the package until the first agent of the wanted client starts, then interrupts the container
// side like Ctrl+C would. It returns the unfinished run's id.
func (h *dockerHarness) startAndInterrupt(dir string, extraArgs ...string) string {
	h.t.Helper()
	h.interruptAfter = 1
	defer func() { h.interruptAfter = 0 }()
	code, stdout, stderr := h.runAfm(append([]string{"--pipeline", dir}, extraArgs...)...)
	h.output.WriteString(stdout + stderr)
	if code == 0 {
		h.t.Fatalf("an interrupted run must not exit 0:\n%s%s%s", stdout, stderr, h.output.String())
	}
	runs := h.runDirs()
	if len(runs) != 1 {
		h.t.Fatalf("run dirs = %v", runs)
	}
	stages, finished := h.runDirState(runs[0])
	if finished || stages["prep"] != "done" || stages["write"] == "done" {
		h.t.Fatalf("after the interrupt: stages=%v finished=%v\n%s", stages, finished, h.output.String())
	}
	return runs[0]
}

func (h *dockerHarness) releaseAgents() {
	h.t.Helper()
	if err := os.WriteFile(h.release, nil, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// The key regression: a stage stopped on Claude continues on Codex under the same command name, with earlier results
// kept and the user's config untouched.
func testPipelineDockerClientSwitch(t *testing.T) {
	h := newDockerHarness(t)
	dir := h.agentPackage(h.agentFlow(""))
	runID := h.startAndInterrupt(dir)
	before := h.bindingFor(runID)
	if before.Recipes[harnessCommand].Type != "claude" {
		t.Fatalf("binding recipes = %+v", before.Recipes)
	}

	h.releaseAgents()
	code, stdout, stderr := h.runAfm("--pipeline", dir, "--pipeline-options", h.codexOptions())
	h.output.WriteString(stdout + stderr)
	if code != 0 {
		t.Fatalf("resume with Codex: code=%d\n%s", code, h.output.String())
	}
	markers := strings.Join(h.markers(), " ")
	if !strings.HasPrefix(markers, "claude ") || !strings.Contains(markers, " codex ") {
		t.Fatalf("agent markers = %q, want claude first, then codex for the same stage", markers)
	}
	if runs := h.runDirs(); len(runs) != 1 || runs[0] != runID {
		t.Fatalf("the same run must continue: %v", runs)
	}
	stages, finished := h.runDirState(runID)
	if !finished || stages["prep"] != "done" || stages["write"] != "done" {
		t.Fatalf("after resume: stages=%v finished=%v\n%s", stages, finished, h.output.String())
	}
	if prep, _ := os.ReadFile(filepath.Join(h.root, "prep.log")); strings.Count(string(prep), "prepared") != 1 {
		t.Fatalf("the finished stage must not rerun: %q", prep)
	}
	if h.configHash() != h.configSH {
		t.Fatal("package mode rewrote .afm/config.yaml")
	}
	after := h.bindingFor(runID)
	if after.Recipes[harnessCommand].Type != "codex" || after.Recipes[harnessCommand].Identity == before.Recipes[harnessCommand].Identity {
		t.Fatalf("binding must record the new recipe: %+v", after.Recipes)
	}
	for _, call := range h.calls {
		if strings.Contains(strings.Join(call.argv, "\n"), "recipe-secret-one") {
			t.Fatal("a secret value reached docker argv")
		}
	}
	h.artifact("client_switch.markers.txt", markers)
	h.artifact("client_switch.output.txt", h.output.String())
}

func (h *pipelineFixture) bindingFor(runID string) pipeline.Binding {
	h.t.Helper()
	b, ok, err := pipeline.ReadBinding(pipeline.BindingsDir(h.root), runID)
	if err != nil || !ok {
		h.t.Fatalf("binding of %s: ok=%v err=%v", runID, ok, err)
	}
	return b
}

// Changing only the value of the secret a recipe references is not a client change: the stage continues on the
// same client with the same recorded identity, and the value never reaches argv.
func testPipelineDockerSecretOnlyChange(t *testing.T) {
	h := newDockerHarness(t)
	dir := h.agentPackage(h.agentFlow(""))
	runID := h.startAndInterrupt(dir)
	before := h.bindingFor(runID)

	t.Setenv(harnessSecretVar, "recipe-secret-two")
	h.releaseAgents()
	code, stdout, stderr := h.runAfm("--pipeline", dir)
	h.output.WriteString(stdout + stderr)
	if code != 0 {
		t.Fatalf("resume: code=%d\n%s", code, h.output.String())
	}
	if markers := strings.Join(h.markers(), " "); strings.Contains(markers, "codex") || strings.Count(markers, "claude") != 2 {
		t.Fatalf("agent markers = %q, want claude both times", markers)
	}
	after := h.bindingFor(runID)
	if after.Recipes[harnessCommand] != before.Recipes[harnessCommand] || after.Recipes[harnessCommand].Type != "claude" {
		t.Fatalf("recipe identity changed with the secret value: %+v -> %+v", before.Recipes, after.Recipes)
	}
	if strings.Contains(h.output.String(), "changed client") {
		t.Fatalf("no client-change warning is expected:\n%s", h.output.String())
	}
	if len(h.calls) != 2 {
		t.Fatalf("docker calls = %d", len(h.calls))
	}
	for i, call := range h.calls {
		joined := strings.Join(call.argv, "\n")
		if strings.Contains(joined, "recipe-secret-one") || strings.Contains(joined, "recipe-secret-two") {
			t.Fatalf("docker call %d carries a secret value in argv", i)
		}
	}
	if secret, _ := lookupEnv(h.calls[1].env, config.SecretEnvVar(harnessCommand)); secret != "recipe-secret-two" {
		t.Fatalf("the new secret value must travel in the environment, got %q", secret)
	}
	h.artifact("secret_only_change.markers.txt", strings.Join(h.markers(), " "))
}

// A run locked after the host's preflight is refused by the container before Open. Without the guard the binder still
// fails the start, but after the engine opened another run, so the test checks for no new run, agent or change.
func testPipelineDockerContainerLock(t *testing.T) {
	h := newDockerHarness(t)
	dir := h.agentPackage(h.agentFlow(""))
	runID := h.startAndInterrupt(dir)
	before := h.runFingerprint(runID)
	agentsBefore := len(h.argvLines())
	h.beforeContainer = func() {
		lock, err := implstate.TryLockRun(filepath.Join(runsDir(), runID))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lock.Close() })
	}
	h.releaseAgents()
	h.output.Reset()
	code, stdout, stderr := h.runAfm("--pipeline", dir)
	container := h.output.String()
	if code == 0 || !strings.Contains(container, "run "+runID+" is held by another afm process") {
		t.Fatalf("code=%d, the container must refuse the locked run:\n%s\nhost:\n%s%s", code, container, stdout, stderr)
	}
	if strings.Contains(container, "afm: running") {
		t.Fatalf("the container side opened a run despite the lock:\n%s", container)
	}
	if runs := h.runDirs(); len(runs) != 1 || runs[0] != runID {
		t.Fatalf("run dirs = %v, want only %s", runs, runID)
	}
	if got := len(h.argvLines()); got != agentsBefore {
		t.Fatalf("an agent started: %d invocations, %d before", got, agentsBefore)
	}
	if h.runFingerprint(runID) != before {
		t.Fatal("the locked run's binding or journal changed")
	}
	h.artifact("container_lock.output.txt", container)
}

// The container side builds flow and config from the snapshot alone: the Git repository vanishes after the host
// resolved it, and the run still completes; the host's launch record is deleted afterwards.
func testPipelineDockerContainerNoFetch(t *testing.T) {
	h := newDockerHarness(t)
	repo := h.newGitPackageRepo("pkgflow")
	h.openGate()
	h.beforeContainer = repo.makeUnavailable
	code, stdout, stderr := h.runAfm("--pipeline", repo.source("main"))
	if code != 0 {
		t.Fatalf("code=%d\n%s%s\n%s", code, stdout, stderr, h.output.String())
	}
	if _, err := os.Stat(repo.bare); err == nil {
		t.Fatal("the repository was supposed to be gone by the time the container started")
	}
	runs := h.runDirs()
	if len(runs) != 1 {
		t.Fatalf("run dirs = %v", runs)
	}
	if _, finished := h.runDirState(runs[0]); !finished {
		t.Fatalf("the container run did not finish:\n%s", h.output.String())
	}
	b := h.bindingFor(runs[0])
	if b.Source.Kind != pipeline.SourceGit || len(b.Commit) != 40 {
		t.Fatalf("binding = %+v", b)
	}
	if left, _ := os.ReadDir(pipelineLaunchDir()); len(left) != 0 {
		t.Fatalf("launch records left behind: %v", left)
	}
	h.artifact("container_no_fetch.output.txt", h.output.String())
}

// The container gets the options through the host's launch record, not by reading --pipeline-options again: the
// file is rewritten and its prompt removed after the host's preflight, and the run must still use what the host saw,
// with the relative prompt reference pinned to the host path.
func testPipelineDockerOptionsFromLaunchRecord(t *testing.T) {
	h := newDockerHarness(t)
	dir := h.agentPackage(h.agentFlow(""))
	optionsDir := t.TempDir()
	optionsPath := filepath.Join(optionsDir, "options.yaml")
	hostOptions := "docker:\n  agents:\n    tpkg-writer: {type: codex, model: gpt-6-sol, system_prompt: \"file:prompt.md\"}\n"
	if err := os.WriteFile(optionsPath, []byte(hostOptions), 0o644); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(optionsDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("host prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	layer, err := pipeline.ParseLayer([]byte(hostOptions), pipeline.OriginOptions, optionsDir)
	if err != nil {
		t.Fatal(err)
	}
	hostRecipe := layer.Agents[harnessCommand]
	if hostRecipe.SystemPrompt != "file:"+promptPath {
		t.Fatalf("the host must pin the relative prompt to %s: %q", promptPath, hostRecipe.SystemPrompt)
	}
	h.beforeContainer = func() {
		changed := "docker:\n  agents:\n    tpkg-writer: {type: codex, model: gpt-other}\n"
		if err := os.WriteFile(optionsPath, []byte(changed), 0o644); err != nil {
			t.Error(err)
		}
		if err := os.Remove(promptPath); err != nil {
			t.Error(err)
		}
	}
	h.releaseAgents()
	code, stdout, stderr := h.runAfm("--pipeline", dir, "--pipeline-options", optionsPath)
	h.output.WriteString(stdout + stderr)
	if code != 0 {
		t.Fatalf("code=%d\n%s", code, h.output.String())
	}
	runs := h.runDirs()
	if len(runs) != 1 {
		t.Fatalf("run dirs = %v", runs)
	}
	if got := h.bindingFor(runs[0]).Recipes[harnessCommand]; got.Type != "codex" || got.Identity != hostRecipe.Identity() {
		t.Fatalf("the container must use the host's recipe: %+v, want identity %s", got, hostRecipe.Identity())
	}
	argv := strings.Join(h.argvLines(), "\n")
	if !strings.Contains(argv, "-m\\ gpt-6-sol") || strings.Contains(argv, "gpt-other") {
		t.Fatalf("the agent must run with the host's model: %q", argv)
	}
	h.artifact("container_options_from_launch_record.output.txt", argv+"\n\n"+h.output.String())
}

// savedSessionIDQuiet is the session id the run's chat stage has saved, or "" when there is none (yet). It never
// fails the test, so it can serve as a polling condition.
func (h *dockerHarness) savedSessionIDQuiet() string {
	matches, _ := filepath.Glob(filepath.Join(h.root, ".afm", "runs", "*", "chat", "implementation.session.json"))
	if len(matches) != 1 {
		return ""
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return ""
	}
	var saved struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(data, &saved) != nil {
		return ""
	}
	return saved.SessionID
}

// An interactive stage whose command switches client warns that its saved conversation belongs to the old client.
// From a turn saved while running, the engine offers the session to either client: the Codex adapter drops it, Claude
// resumes it even when Codex saved it; Claude to Claude is the control that the fixture reaches session reuse.
func testPipelineDockerInteractiveSwitch(t *testing.T) {
	cases := []struct {
		name                       string
		firstOptions, secondOption bool // codex options on the first / second start
		from, to                   string
	}{
		{"claude to codex", false, true, "claude", "codex"},
		{"codex to claude", true, false, "codex", "claude"},
		{"claude to claude", false, false, "claude", "claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newDockerHarness(t)
			dir := h.agentPackage(fmt.Sprintf("name: pkgflow\nstages:\n  - id: chat\n    name: Chat\n    interactive: true\n    command: %s\n", harnessCommand))
			// An interactive stage only runs its agent with a dashboard (port > 0); the container publishes it.
			// --debug logs the session the engine hands each turn.
			port := fmt.Sprintf("--port=%d", freeLoopbackPort(t))
			args := func(codex bool) []string {
				out := []string{"--pipeline", dir, port, "--debug"}
				if codex {
					out = append(out, "--pipeline-options", h.codexOptions())
				}
				return out
			}
			saved := h.saveRunningTurn(args(tc.firstOptions))
			firstLines := h.argvLines()
			for _, line := range firstLines {
				if !strings.HasPrefix(line, tc.from+" ") {
					t.Fatalf("the first start must run %s only: %q", tc.from, firstLines)
				}
			}
			if tc.from == "claude" && !strings.Contains(strings.Join(firstLines, "\n"), "--session-id\\ "+saved.session) {
				t.Fatalf("claude's turn must have saved the session it was started with: saved=%q argv=%q", saved.session, firstLines)
			}
			h.restoreRunningTurn(saved)
			debugBefore, _ := os.ReadFile(saved.debugLog)

			h.beforeContainer = func() {
				if got := h.savedSessionIDQuiet(); got != saved.session {
					t.Fatalf("the saved session is gone before the container starts: %q, want %q", got, saved.session)
				}
			}
			h.interruptWhen = func() bool { return len(h.argvLines()) > len(firstLines) }
			code, stdout, stderr := h.runAfm(args(tc.secondOption)...)
			h.interruptWhen = nil
			h.output.WriteString(stdout + stderr)
			// The harness stops the switched agent with SIGINT once it has started: 130 is that interrupt, not a
			// result of the provider accepting or rejecting the session.
			if code != interruptedExitCode {
				t.Fatalf("second start: code=%d, want the harness interrupt %d:\n%s", code, interruptedExitCode, h.output.String())
			}
			warning := fmt.Sprintf("interactive stage %q: command %s changed client from %s to %s", "chat", harnessCommand, tc.from, tc.to)
			if tc.from != tc.to && !strings.Contains(stdout, warning) {
				t.Fatalf("the host output lacks %q:\n%s", warning, stdout)
			}
			if tc.from == tc.to && strings.Contains(stdout, "changed client") {
				t.Fatalf("the same client must not be reported as a client change:\n%s", stdout)
			}
			debugAfter, err := os.ReadFile(saved.debugLog)
			if err != nil {
				t.Fatal(err)
			}
			if resumed := string(debugAfter[len(debugBefore):]); !strings.Contains(resumed, "session="+saved.session+" resume=true") {
				t.Fatalf("the engine must offer the saved session %s to the next turn:\n%s", saved.session, resumed)
			}
			switched := h.argvLines()[len(firstLines):]
			if len(switched) == 0 || !strings.HasPrefix(switched[0], tc.to+" ") {
				t.Fatalf("the second start must run %s first: %q", tc.to, switched)
			}
			switch tc.to {
			case "codex":
				if strings.Contains(switched[0], saved.session) || strings.Contains(switched[0], "resume") {
					t.Fatalf("codex must start fresh, without the saved session: %q", switched[0])
				}
			case "claude":
				if strings.Contains(switched[0], "--session-id") || !strings.Contains(switched[0], "--resume\\ "+saved.session) {
					t.Fatalf("claude must resume the saved session %s: %q", saved.session, switched[0])
				}
			default:
				t.Fatalf("unexpected target client %q", tc.to)
			}
			h.artifact("interactive_switch."+strings.ReplaceAll(tc.name, " ", "_")+".txt",
				fmt.Sprintf("second start exit code: %d (the harness's SIGINT)\nsaved running session: %q\nargv:\n%s\n\n%s",
					code, saved.session, strings.Join(h.argvLines(), "\n"), h.output.String()))
		})
	}
}

// savedTurn is a run captured while its interactive turn was running: the journal and the session sidecar as they
// were at that moment.
type savedTurn struct {
	runDir, session, debugLog string
	journal, sidecar          []byte
}

// saveRunningTurn holds the first agent, captures the run once its session is saved and interrupts the container;
// the interrupt fails the turn and drops the session, so the test restores the capture afterwards.
func (h *dockerHarness) saveRunningTurn(args []string) savedTurn {
	h.t.Helper()
	var saved savedTurn
	h.interruptWhen = func() bool {
		saved.session = h.savedSessionIDQuiet()
		runs := h.runDirs()
		if saved.session == "" || len(h.argvLines()) == 0 || len(runs) != 1 {
			return false
		}
		saved.runDir = filepath.Join(runsDir(), runs[0])
		var err error
		if saved.journal, err = os.ReadFile(filepath.Join(saved.runDir, "events.jsonl")); err != nil {
			h.t.Fatal(err)
		}
		if saved.sidecar, err = os.ReadFile(filepath.Join(saved.runDir, "chat", "implementation.session.json")); err != nil {
			h.t.Fatal(err)
		}
		return true
	}
	code, stdout, stderr := h.runAfm(args...)
	h.interruptWhen = nil
	h.output.WriteString(stdout + stderr)
	if code == 0 {
		h.t.Fatalf("an interrupted run must not exit 0:\n%s", h.output.String())
	}
	// A line the engine was appending at the capture is not part of the saved state.
	saved.journal = saved.journal[:bytes.LastIndexByte(saved.journal, '\n')+1]
	saved.debugLog = filepath.Join(saved.runDir, "debug.log")
	return saved
}

// restoreRunningTurn puts the captured journal and session back and drops the derived state cache, so the run
// replays with its turn still running and its session saved.
func (h *dockerHarness) restoreRunningTurn(saved savedTurn) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(saved.runDir, "events.jsonl"), saved.journal, 0o600); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(saved.runDir, "state.json")); err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saved.runDir, "chat", "implementation.session.json"), saved.sidecar, 0o600); err != nil {
		h.t.Fatal(err)
	}
	stages, finished := h.runDirState(filepath.Base(saved.runDir))
	if finished || stages["chat"] != "running" || h.savedSessionIDQuiet() != saved.session {
		h.t.Fatalf("the restored run is not a running turn with session %s: stages=%v finished=%v", saved.session, stages, finished)
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

const teamAgents = `docker:
  agents:
    tpkg-writer:
      type: claude
      model: m-writer
      auth: {from: "env:FAKE_RECIPE_TOKEN", to: "env:CLAUDE_CODE_OAUTH_TOKEN"}
    tpkg-helper:
      type: claude
      model: m-helper
      auth: {from: "env:FAKE_RECIPE_TOKEN", to: "env:CLAUDE_CODE_OAUTH_TOKEN"}
collaboration:
  profiles:
    tpkg-helper: {title: Helper, command: tpkg-helper}
`

// teamClaude is a fake claude that tells the stage agent (model m-writer) from the helper (m-helper): the stage agent
// messages the team member through the real afm CLI and then waits for the release file.
const teamClaude = `#!/usr/bin/env bash
set -uo pipefail
cat >/dev/null 2>&1 || true
if [[ "$*" == *m-helper* ]]; then
  printf 'helper-claude\n' >>"$FAKE_AGENT_MARKER_FILE"
else
  printf 'writer-claude\n' >>"$FAKE_AGENT_MARKER_FILE" # before the send: the helper starts as soon as it is delivered
  afm agent send --to helper --kind message --body ping >>"$FAKE_AGENT_MARKER_FILE.send" 2>&1
  for _ in $(seq 1 120); do [[ -e "$FAKE_AGENT_RELEASE_FILE" ]] && break; sleep 1; done
fi
if [[ -n "${AFM_STAGE_DIR:-}" ]]; then printf '# Summary\n\nok\n' >"$AFM_STAGE_DIR/execution_summary.md"; fi
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'
printf '%s\n' '{"type":"result","subtype":"success"}'
`

// A helper or team agent created before the switch keeps its recorded recipe: after the package command moves to
// Codex its next turn fails with executor_changed, exactly as it did before packages existed.
func testPipelineDockerTeamExecutorChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an afm binary: the agents call `afm agent send`")
	}
	h := newDockerHarness(t)
	if err := os.WriteFile(filepath.Join(h.bin, "claude"), []byte(teamClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	h.useRealAfm()
	dir := h.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: harnessManifest,
		"agents.yaml":         teamAgents,
		"flow.yaml": fmt.Sprintf("name: pkgflow\nteam:\n  - {id: helper, profile: tpkg-helper, title: Helper, instruction: wait for messages}\n"+
			"stages:\n  - id: write\n    name: Write\n    agents: [auto]\n    command: %s\n", harnessCommand),
	})
	// The dashboard port makes the team agent live; the stage agent messages it and then waits.
	port := fmt.Sprintf("--port=%d", freeLoopbackPort(t))
	h.interruptAfter = 2 // the stage agent and the helper's first turn
	code, stdout, stderr := h.runAfm("--pipeline", dir, port)
	h.output.WriteString(stdout + stderr)
	if code == 0 {
		t.Fatalf("an interrupted run must not exit 0:\n%s", h.output.String())
	}
	if got := strings.Join(h.markers(), " "); got != "writer-claude helper-claude" {
		t.Fatalf("markers after the first start = %q", got)
	}

	h.interruptAfter = 0
	// The helper may fail before the restarted stage agent writes its marker: stop only after both happened.
	h.interruptWhen = func() bool { return h.runContains(executorChangedReason) && len(h.markers()) >= 3 }
	options := h.write("options-helper-codex.yaml", "docker:\n  agents:\n    tpkg-helper: {type: codex, model: gpt-6-sol}\n")
	code, stdout, stderr = h.runAfm("--pipeline", dir, "--pipeline-options", options, port)
	h.output.WriteString(stdout + stderr)
	if code == 0 {
		t.Fatalf("the second start was stopped by the test, not finished:\n%s", h.output.String())
	}
	if !h.runContains(executorChangedReason) {
		t.Fatalf("the team agent must fail with executor_changed after its recipe changed:\n%s", h.output.String())
	}
	if got := strings.Join(h.markers(), " "); got != "writer-claude helper-claude writer-claude" {
		t.Fatalf("the helper must not run on either client after the change: markers = %q", got)
	}
	h.artifact("team_executor_changed.output.txt", h.output.String())
}

// executorChangedReason is how the journal words a turn refused because its recipe changed; the test's own temp
// directory name contains "executor_changed", so the bare code would match paths.
const executorChangedReason = "executor_changed: the recipe changed after the task was created"

// runContains reports whether any file of the run directory mentions text.
func (h *dockerHarness) runContains(text string) bool {
	out, err := exec.Command("grep", "-rl", "--", text, filepath.Join(h.root, ".afm", "runs")).Output()
	return err == nil && len(out) > 0
}

// useRealAfm runs the container side with a real afm binary built from this tree instead of the test binary, so the
// agents' `afm agent ...` calls work.
func (h *dockerHarness) useRealAfm() {
	h.t.Helper()
	bin := filepath.Join(h.t.TempDir(), "afm")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOMODCACHE="+h.goCaches[0], "GOCACHE="+h.goCaches[1], "GOFLAGS=-mod=readonly", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		h.t.Fatalf("go build afm: %v\n%s", err, out)
	}
	h.afmBin = bin
	h.containerEnv = append(h.containerEnv, "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// startAfmBinary starts a real afm binary with exactly env as its environment (the container has no other).
func startAfmBinary(t *testing.T, bin, dir string, env []string, args ...string) *afmChild {
	t.Helper()
	cmd := exec.Command(bin, args...) //nolint:gosec // the binary this test built
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append([]string{"AFM_SIDEAGENT=0", "AFM_BROWSER=0"}, env...)
	c := &afmChild{cmd: cmd, out: &childOutput{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = c.out, c.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		c.err = cmd.Wait()
		close(c.done)
	}()
	t.Cleanup(func() {
		select {
		case <-c.done:
			return
		default:
		}
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-c.done:
		case <-time.After(childShutdownGrace):
			c.kill(t)
		}
	})
	return c
}

func goEnvPaths(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "env", "GOMODCACHE", "GOCACHE").Output()
	paths := strings.Fields(string(out))
	if err != nil || len(paths) != 2 {
		t.Fatalf("go env: %v %q", err, out)
	}
	return paths
}

func waitTick() { time.Sleep(20 * time.Millisecond) }
