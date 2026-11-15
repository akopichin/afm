package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/pipeline"
)

// pipelineFixture is an isolated afm root plus a HOME whose cache holds the package snapshots. Packages use script
// stages only, so a run needs no agent.
type pipelineFixture struct {
	*cycleFixture
	home string
	log  string // one line per stage start: "first <AFM_PIPELINE_DIR>" / "second"
	gate string // the second stage fails until this file exists
}

func newPipelineFixture(t *testing.T) *pipelineFixture {
	t.Helper()
	fx := newCycleFixture(t)
	home := t.TempDir()
	// Registered after the TempDir so it runs first: snapshots are read-only and TempDir's RemoveAll cannot enter them.
	t.Cleanup(func() { unlockTree(home) })
	t.Setenv("HOME", home)
	t.Setenv(pipelineDirEnv, "") // package mode exports it process-wide; register the restore
	for _, kv := range [][2]string{{"AFM_WORKTREES", "0"}, {"AFM_USE_DOCKER", "0"}, {"AFM_BROWSER", "0"}, {"AFM_EXIT_ON_FAIL", ""}, {"AFM_COLLABORATION", "1"}} {
		t.Setenv(kv[0], kv[1])
	}
	return &pipelineFixture{cycleFixture: fx, home: home, log: filepath.Join(fx.root, "stages.log"), gate: filepath.Join(fx.root, "gate")}
}

func unlockTree(root string) {
	tree, err := os.OpenRoot(root)
	if err != nil {
		return
	}
	defer tree.Close()
	_ = fs.WalkDir(tree.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = tree.Chmod(rel, 0o755)
		}
		return nil
	})
}

// packageFlow is two script stages: the first records AFM_PIPELINE_DIR, the second fails until the gate file exists.
func (pf *pipelineFixture) packageFlow(flowName string) string {
	return fmt.Sprintf(`name: %s
stages:
  - id: first
    name: First
    script: |
      echo "first $AFM_PIPELINE_DIR" >> %s
  - id: second
    name: Second
    depends_on: [first]
    script: |
      echo second >> %s
      [ -f %s ]
`, flowName, pf.log, pf.log, pf.gate)
}

// writePackage creates a v2 package directory (outside the afm root) and returns it.
func (pf *pipelineFixture) writePackage(name, flowName string, files map[string]string) string {
	pf.t.Helper()
	dir := filepath.Join(pf.t.TempDir(), name)
	manifest := fmt.Sprintf("schema_version: 2\nname: %s\nversion: 1.0.0\nentrypoint: flow.yaml\n", name)
	if m, ok := files[pipeline.ManifestFile]; ok {
		manifest = m
	}
	all := map[string]string{pipeline.ManifestFile: manifest, "flow.yaml": pf.packageFlow(flowName)}
	for k, v := range files {
		all[k] = v
	}
	for rel, content := range all {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			pf.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			pf.t.Fatal(err)
		}
	}
	return dir
}

// runAfm runs `afm run` in process against the fixture root and returns exit code and captured output.
func (pf *pipelineFixture) runAfm(args ...string) (code int, stdout, stderr string) {
	pf.t.Helper()
	full := append([]string{"run", "--dir", pf.root, "--port=0"}, args...)
	// A Docker re-exec forwards os.Args[1:] to the container, so they must look like the real command line.
	savedArgs := os.Args
	os.Args = append([]string{"afm"}, full...)
	defer func() { os.Args = savedArgs }()
	stdout, stderr = captureOutput(pf.t, func() { code = runMain(full) })
	return code, stdout, stderr
}

func (pf *pipelineFixture) logLines() []string {
	data, err := os.ReadFile(pf.log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (pf *pipelineFixture) bindings() []pipeline.Binding {
	pf.t.Helper()
	bs, err := pipeline.ReadBindings(pipeline.BindingsDir(pf.root))
	if err != nil {
		pf.t.Fatal(err)
	}
	return bs
}

func (pf *pipelineFixture) openGate() {
	pf.t.Helper()
	if err := os.WriteFile(pf.gate, nil, 0o644); err != nil {
		pf.t.Fatal(err)
	}
}

func TestPipelineRunFlagConflicts(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	flowFile := pf.write("flow.yaml", pf.packageFlow("plain"))
	optionsFile := pf.write("o.yaml", "docker: {agents: {}}\n")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"pipeline and positional flow", []string{"--pipeline", dir, flowFile}, "mutually exclusive"},
		{"options without pipeline", []string{"--pipeline-options", optionsFile, flowFile}, "requires --pipeline"},
		{"launch without pipeline", []string{"--pipeline-launch", "/x.json", flowFile}, "requires --pipeline"},
		{"launch outside a container", []string{"--pipeline", dir, "--pipeline-launch", "/x.json"}, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := pf.runAfm(tc.args...)
			if code == 0 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stderr=%q, want a failure containing %q", code, stderr, tc.want)
			}
			if _, err := os.Stat(filepath.Join(pf.root, ".afm")); err == nil {
				t.Fatal("a refused command line must create nothing under .afm")
			}
			if _, err := os.Stat(filepath.Join(pf.home, ".afm")); err == nil {
				t.Fatal("a refused command line must not touch the pipeline cache")
			}
		})
	}
}

func TestPipelinePlainFlowIgnoresManifest(t *testing.T) {
	pf := newPipelineFixture(t)
	pf.openGate()
	dir := pf.writePackage("tpkg", "plainflow", nil)
	code, stdout, stderr := pf.runAfm(filepath.Join(dir, "flow.yaml"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "afm: pipeline") {
		t.Fatalf("a manifest next to the flow must not switch on package mode:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(pf.root, ".afm", "pipelines")); err == nil {
		t.Fatal("a plain run must not write pipeline bindings")
	}
	if lines := pf.logLines(); len(lines) != 2 || strings.TrimSpace(strings.TrimPrefix(lines[0], "first")) != "" {
		t.Fatalf("AFM_PIPELINE_DIR must stay unset for a plain flow, log = %q", lines)
	}
}

func TestPipelineRunBindsRunToSnapshot(t *testing.T) {
	pf := newPipelineFixture(t)
	pf.openGate()
	configPath := pf.write(".afm-config-guard", "")
	_ = configPath
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	code, stdout, stderr := pf.runAfm("--pipeline", dir)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "afm: pipeline tpkg 1.0.0 (new run)") || !strings.Contains(stdout, "content: sha256:") {
		t.Fatalf("banner missing:\n%s", stdout)
	}
	bindings := pf.bindings()
	if len(bindings) != 1 {
		t.Fatalf("bindings = %+v", bindings)
	}
	b := bindings[0]
	wantSnapshots := filepath.Join(pf.home, ".afm", "cache", "pipelines", "snapshots")
	if filepath.Dir(b.SnapshotDir) != wantSnapshots || b.Source.Location != dir || b.RunName != "pkgflow" || b.Package.Name != "tpkg" {
		t.Fatalf("binding = %+v", b)
	}
	if lines := pf.logLines(); len(lines) != 2 || lines[0] != "first "+b.SnapshotDir {
		t.Fatalf("stage log = %q, want AFM_PIPELINE_DIR=%s", lines, b.SnapshotDir)
	}
	if _, err := os.Stat(filepath.Join(pf.root, ".afm", "config.yaml")); err == nil {
		t.Fatal("package mode must not write a config file")
	}
	if len(pf.runDirs()) != 1 || pf.runState(pf.runDirs()[0]).RunStatus != state.RunStatusFinished {
		t.Fatalf("run dirs = %v", pf.runDirs())
	}
}

func TestPipelineResumeContinuesSameRunOnRecordedSnapshot(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	if code, _, stderr := pf.runAfm("--pipeline", dir); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	runs := pf.runDirs()
	if len(runs) != 1 || pf.runState(runs[0]).Stages["second"].Status == state.StatusDone {
		t.Fatalf("expected one unfinished run, got %v", runs)
	}
	first := pf.bindings()[0]

	// The source changes after the run started; the continuation must stay on the recorded snapshot.
	if err := os.WriteFile(filepath.Join(dir, "flow.yaml"), []byte(strings.Replace(pf.packageFlow("pkgflow"), "name: First", "name: Edited", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	pf.openGate()
	code, stdout, stderr := pf.runAfm("--pipeline", dir)
	if code != 0 {
		t.Fatalf("resume: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "continuing run "+runs[0]) || !strings.Contains(stdout, "changed since the run started") {
		t.Fatalf("resume banner or source-change notice missing:\n%s", stdout)
	}
	if got := pf.runDirs(); len(got) != 1 || pf.runState(got[0]).RunStatus != state.RunStatusFinished {
		t.Fatalf("run dirs after resume = %v", got)
	}
	if lines := pf.logLines(); strings.Count(strings.Join(lines, "\n"), "first ") != 1 {
		t.Fatalf("the finished first stage must not rerun: %q", lines)
	}
	after := pf.bindings()
	if len(after) != 1 || after[0].SnapshotDir != first.SnapshotDir || after[0].ContentSHA256 != first.ContentSHA256 || !after[0].UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("binding must keep the recorded snapshot and move updated_at: before %+v after %+v", first, after)
	}
}

func TestPipelineResumeWarnsWhenOptionsDisappear(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	options := pf.write("o.yaml", "collaboration:\n  profiles:\n    tpkg-x: {title: X}\n")
	if code, _, stderr := pf.runAfm("--pipeline", dir, "--pipeline-options", options); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	pf.openGate()
	code, stdout, stderr := pf.runAfm("--pipeline", dir)
	if code != 0 {
		t.Fatalf("resume: code=%d %s", code, stderr)
	}
	if !strings.Contains(stdout, "started with options "+options) || !strings.Contains(stdout, "none are given now") {
		t.Fatalf("options warning missing:\n%s", stdout)
	}
	if b := pf.bindings()[0]; b.Options != nil {
		t.Fatalf("options must not be carried over silently: %+v", b.Options)
	}
}

func TestPipelineResumeSameOptionsFileIsQuiet(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	options := pf.write("o.yaml", "collaboration:\n  profiles:\n    tpkg-x: {title: X}\n")
	if code, _, stderr := pf.runAfm("--pipeline", dir, "--pipeline-options", options); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	pf.write("o.yaml", "collaboration:\n  profiles:\n    tpkg-x: {title: Renamed}\n")
	pf.openGate()
	code, stdout, stderr := pf.runAfm("--pipeline", dir, "--pipeline-options", options)
	if code != 0 || strings.Contains(stdout, "started with options") {
		t.Fatalf("editing the same options file is the normal continue flow: code=%d\n%s%s", code, stdout, stderr)
	}
}

func TestPipelineUnfinishedRunOfAnotherSourceStopsBeforeStart(t *testing.T) {
	pf := newPipelineFixture(t)
	dirA := pf.writePackage("tpkg", "pkgflow", nil)
	dirB := pf.writePackage("tpkg", "pkgflow", map[string]string{"extra.txt": "other source"})
	if code, _, stderr := pf.runAfm("--pipeline", dirA); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	runsBefore := pf.runDirs()
	code, _, stderr := pf.runAfm("--pipeline", dirB)
	if code == 0 || !strings.Contains(stderr, "unfinished run") || !strings.Contains(stderr, "--worktree") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got := pf.runDirs(); strings.Join(got, ",") != strings.Join(runsBefore, ",") {
		t.Fatalf("a refused start must not create a run: %v -> %v", runsBefore, got)
	}
}

func TestPipelineUnfinishedPlainRunStopsPackageStart(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	flowFile := pf.write("plain.yaml", pf.packageFlow("pkgflow"))
	if code, _, stderr := pf.runAfm(flowFile); code != 0 {
		t.Fatalf("plain start: code=%d %s", code, stderr)
	}
	code, _, stderr := pf.runAfm("--pipeline", dir)
	if code == 0 || !strings.Contains(stderr, "unfinished run") {
		t.Fatalf("a package must not continue a run it does not own: code=%d stderr=%q", code, stderr)
	}
}

func TestPipelineClearFlagStartsFreshRun(t *testing.T) {
	pf := newPipelineFixture(t)
	t.Setenv("AFM_WORKTREES", "0")
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	if code, _, stderr := pf.runAfm("--pipeline", dir); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	// Without worktrees enabled the clear flag is refused before anything is created, so this checks the order:
	// the refusal comes from worktree resolution, not from the unfinished-run guard.
	code, _, stderr := pf.runAfm("--pipeline", dir, "--worktree-clear")
	if code == 0 || strings.Contains(stderr, "unfinished run") {
		t.Fatalf("clear flag must bypass the unfinished-run guard: code=%d stderr=%q", code, stderr)
	}
}

func TestPipelineRunRejectsInvalidPackages(t *testing.T) {
	pf := newPipelineFixture(t)
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"catalog manifest", map[string]string{pipeline.ManifestFile: "schema_version: 1\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\n"}, "catalog installer"},
		{"unknown version", map[string]string{pipeline.ManifestFile: "schema_version: 9\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\n"}, "unsupported schema_version"},
		{"agents file with image", map[string]string{
			pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nagents: agents.yaml\n",
			"agents.yaml":         "docker:\n  image: evil\n",
		}, "docker.image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := pf.writePackage("tpkg", "pkgflow", tc.files)
			code, _, stderr := pf.runAfm("--pipeline", dir)
			if code == 0 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stderr=%q, want containing %q", code, stderr, tc.want)
			}
			if got := pf.runDirs(); len(got) != 0 {
				t.Fatalf("an invalid package must not create a run: %v", got)
			}
		})
	}
}

func TestPipelineRunReportsMissingRequirementsBeforeCreatingRun(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nrequirements: {tools: [definitely-not-installed-tool]}\n",
	})
	code, _, stderr := pf.runAfm("--pipeline", dir)
	if code == 0 || !strings.Contains(stderr, "tool:definitely-not-installed-tool") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got := pf.runDirs(); len(got) != 0 || len(pf.bindings()) != 0 {
		t.Fatalf("preflight failure left runs %v / bindings", got)
	}
}

func TestPipelineRunWithOptionsAppliesAndPins(t *testing.T) {
	pf := newPipelineFixture(t)
	pf.openGate()
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nagents: agents.yaml\nroles:\n  writer: {command: tpkg-writer}\n",
		"agents.yaml":         "docker:\n  agents:\n    tpkg-writer: {type: claude, model: claude-sonnet-5-5, auth: {from: \"env:CLAUDE_CODE_OAUTH_TOKEN\", to: \"env:CLAUDE_CODE_OAUTH_TOKEN\"}}\n",
	})
	options := pf.write("o.yaml", "docker:\n  agents:\n    tpkg-writer: {type: claude, model: claude-opus-5-5, auth: {from: \"env:CLAUDE_CODE_OAUTH_TOKEN\", to: \"env:CLAUDE_CODE_OAUTH_TOKEN\"}}\n")
	code, stdout, stderr := pf.runAfm("--pipeline", dir, "--pipeline-options", options)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "role writer: command tpkg-writer, client claude, model claude-opus-5-5") || !strings.Contains(stdout, "from options") {
		t.Fatalf("banner must show the effective model and layer:\n%s", stdout)
	}
	b := pf.bindings()[0]
	if b.Options == nil || b.Options.Path != options || len(b.Options.SHA256) != 64 {
		t.Fatalf("binding = %+v", b)
	}
	if data, _ := os.ReadFile(filepath.Join(pf.root, ".afm", "pipelines", "runs", b.RunID+".json")); strings.Contains(string(data), "CLAUDE_CODE_OAUTH_TOKEN") && strings.Contains(string(data), "secret") {
		t.Fatalf("binding must hold no secret material: %s", data)
	}
}

func TestRunNameChoiceFollowsFlowNameWithoutWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	pf := newPipelineFixture(t)
	t.Setenv("AFM_WORKTREES", "1")
	pf.initProjectRepo()
	dir := pf.writePackage("tpkg", "pkgflow", nil)

	// An unfinished named run and an unfinished default run of the same source.
	if code, _, stderr := pf.runAfm("--pipeline", dir, "--worktree", "feature"); code != 0 {
		t.Fatalf("named start: code=%d %s", code, stderr)
	}
	named := pf.runDirs()
	if len(named) != 1 || !strings.HasPrefix(named[0], "feature-") {
		t.Fatalf("run dirs = %v", named)
	}
	// Only the named run is unfinished: a start without --worktree is a NEW run named after the flow, and the
	// named run and its binding are left alone.
	_, stdout, _ := pf.runAfm("--pipeline", dir)
	if !strings.Contains(stdout, "(new run)") || strings.Contains(stdout, "continuing run feature-") {
		t.Fatalf("a start without --worktree must not continue the named run:\n%s", stdout)
	}
	pf.assertBindingUntouched(named[0], "feature")

	// Both are now unfinished; each command continues exactly its own run.
	pf.openGate()
	_, stdout, _ = pf.runAfm("--pipeline", dir)
	if !strings.Contains(stdout, "continuing run pkgflow-") {
		t.Fatalf("without --worktree the run named after the flow continues:\n%s", stdout)
	}
	_, stdout, _ = pf.runAfm("--pipeline", dir, "--worktree", "feature")
	if !strings.Contains(stdout, "continuing run "+named[0]) {
		t.Fatalf("--worktree feature continues the named run:\n%s", stdout)
	}
}

// runFingerprint is the bytes of a run's binding and event journal: a run another command must not touch.
func (pf *pipelineFixture) runFingerprint(runID string) string {
	pf.t.Helper()
	binding, err := os.ReadFile(filepath.Join(pipeline.BindingsDir(pf.root), runID+".json"))
	if err != nil {
		pf.t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join(runsDirOf(pf.root), runID, "events.jsonl"))
	if err != nil {
		pf.t.Fatal(err)
	}
	return string(binding) + "\x00" + string(journal)
}

func runsDirOf(root string) string { return filepath.Join(root, ".afm", "runs") }

func (pf *pipelineFixture) assertBindingUntouched(runID, worktreeName string) {
	pf.t.Helper()
	for _, b := range pf.bindings() {
		if b.RunID == runID && b.Worktree == worktreeName {
			return
		}
	}
	pf.t.Fatalf("binding of %s (worktree %q) is gone: %+v", runID, worktreeName, pf.bindings())
}

func (pf *pipelineFixture) initProjectRepo() {
	pf.t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = pf.root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			pf.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestRecipeRecordsCoverUsedCommandsOnly(t *testing.T) {
	f := mustParseFlow(t, "name: f\nstages:\n  - {id: a, name: A, agents: [planning, implementation], command: pkg-used}\n  - {id: b, name: B, script: echo}\n")
	cfg := testCycleConfig()
	cfg.Docker.Agents = map[string]config.AgentRecipe{
		"pkg-used":   {Type: "codex"},
		"pkg-unused": {Type: "claude", Model: "m", Auth: config.RecipeAuth{To: "env:ANTHROPIC_API_KEY"}},
	}
	records, err := recipeRecords(f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records["pkg-used"].Type != "codex" || records["pkg-used"].Identity != cfg.Docker.Agents["pkg-used"].Identity() {
		t.Fatalf("records = %+v", records)
	}
}

func mustParseFlow(t *testing.T, yamlText string) *flow.Flow {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flow.yaml")
	if err := os.WriteFile(p, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := flow.ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// gitPackageRepo is a bare repository (reachable as file://) holding the package under pipelines/tpkg.
type gitPackageRepo struct {
	t    *testing.T
	work string
	bare string
}

func (pf *pipelineFixture) newGitPackageRepo(flowName string) *gitPackageRepo {
	pf.t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		pf.t.Skip("git is not available")
	}
	base := pf.t.TempDir()
	r := &gitPackageRepo{t: pf.t, work: filepath.Join(base, "work"), bare: filepath.Join(base, "repo.git")}
	if err := os.MkdirAll(r.work, 0o755); err != nil {
		pf.t.Fatal(err)
	}
	r.git(r.work, "init", "-q", "-b", "main")
	r.commitPackage(pf, "tpkg", flowName)
	r.git(r.work, "clone", "-q", "--bare", r.work, r.bare)
	return r
}

func (r *gitPackageRepo) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitPackage writes the package files into pipelines/<name> of the work tree and commits them.
func (r *gitPackageRepo) commitPackage(pf *pipelineFixture, name, flowName string) {
	r.t.Helper()
	dir := filepath.Join(r.work, "pipelines", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	manifest := fmt.Sprintf("schema_version: 2\nname: %s\nversion: 1.0.0\nentrypoint: flow.yaml\n", name)
	for rel, content := range map[string]string{pipeline.ManifestFile: manifest, "flow.yaml": pf.packageFlow(flowName)} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "-q", "-m", "package "+flowName)
}

func (r *gitPackageRepo) url() string { return "file://" + r.bare }

func (r *gitPackageRepo) source(ref string) string {
	return r.url() + "//pipelines/tpkg@" + ref
}

// makeUnavailable moves the repository away so any fetch fails.
func (r *gitPackageRepo) makeUnavailable() {
	r.t.Helper()
	if err := os.Rename(r.bare, r.bare+".moved"); err != nil {
		r.t.Fatal(err)
	}
}

func TestPipelineResumeSelectsRunName(t *testing.T) {
	pf := newPipelineFixture(t)
	t.Setenv("AFM_WORKTREES", "1")
	pf.initProjectRepo()
	repo := pf.newGitPackageRepo("pkgflow")
	source := repo.source("main")

	for _, args := range [][]string{{"--pipeline", source}, {"--pipeline", source, "--worktree", "feature"}} {
		if code, _, stderr := pf.runAfm(args...); code != 0 {
			t.Fatalf("start %v: code=%d %s", args, code, stderr)
		}
	}
	runs := pf.runDirs()
	if len(runs) != 2 {
		t.Fatalf("run dirs = %v", runs)
	}
	defaultRun, namedRun := runs[0], runs[1]
	if strings.HasPrefix(defaultRun, "feature-") {
		defaultRun, namedRun = namedRun, defaultRun
	}
	repo.makeUnavailable() // continuing must not need the repository at all
	pf.openGate()

	namedBefore := pf.runFingerprint(namedRun)
	code, stdout, stderr := pf.runAfm("--pipeline", source)
	if code != 0 || !strings.Contains(stdout, "continuing run "+defaultRun) || strings.Contains(stdout, namedRun) {
		t.Fatalf("without --worktree the flow-named run continues: code=%d\n%s%s", code, stdout, stderr)
	}
	if rs := pf.runState(defaultRun); rs.RunStatus != state.RunStatusFinished || !rs.AllDone() {
		t.Fatalf("the selected run must complete: %v", rs.RunStatus)
	}
	pf.assertBindingUntouched(namedRun, "feature")
	if got := pf.runFingerprint(namedRun); got != namedBefore {
		t.Fatal("the other run's binding or journal changed")
	}
	if rs := pf.runState(namedRun); rs.RunStatus == state.RunStatusFinished {
		t.Fatal("the named run must not be touched by the other command")
	}
	defaultBefore := pf.runFingerprint(defaultRun)
	code, stdout, stderr = pf.runAfm("--pipeline", source, "--worktree", "feature")
	if code != 0 || !strings.Contains(stdout, "continuing run "+namedRun) {
		t.Fatalf("--worktree feature continues the named run: code=%d\n%s%s", code, stdout, stderr)
	}
	if rs := pf.runState(namedRun); rs.RunStatus != state.RunStatusFinished || !rs.AllDone() {
		t.Fatalf("the named run must complete: %v", rs.RunStatus)
	}
	if got := pf.runFingerprint(defaultRun); got != defaultBefore {
		t.Fatal("the finished run's binding or journal changed")
	}
	if got := pf.runDirs(); len(got) != 2 {
		t.Fatalf("no extra run may appear: %v", got)
	}
}

func TestPipelineResumeFromRecordedCommitWithoutNetwork(t *testing.T) {
	pf := newPipelineFixture(t)
	repo := pf.newGitPackageRepo("pkgflow")
	commit := repo.git(repo.work, "rev-parse", "HEAD")
	if code, stdout, stderr := pf.runAfm("--pipeline", repo.source("main")); code != 0 {
		t.Fatalf("first start: code=%d %s%s", code, stdout, stderr)
	}
	if b := pf.bindings()[0]; b.Commit != commit || b.Source.Kind != pipeline.SourceGit {
		t.Fatalf("binding = %+v, want commit %s", b, commit)
	}
	repo.makeUnavailable()
	pf.openGate()
	code, stdout, stderr := pf.runAfm("--pipeline", repo.source("main"))
	if code != 0 || !strings.Contains(stdout, "continuing run") {
		t.Fatalf("resume without the repository: code=%d\n%s%s", code, stdout, stderr)
	}
	// A new run of a branch needs the network: the stale resolution is never reused.
	code, _, stderr = pf.runAfm("--pipeline", repo.source("main"), "--worktree", "other")
	if code == 0 {
		t.Fatalf("a new run from a branch must fail without the repository: %s", stderr)
	}
}

func TestPipelineResumeRestoresMissingGitSnapshotFromCache(t *testing.T) {
	pf := newPipelineFixture(t)
	repo := pf.newGitPackageRepo("pkgflow")
	if code, _, stderr := pf.runAfm("--pipeline", repo.source("main")); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	b := pf.bindings()[0]
	unlockTree(b.SnapshotDir)
	if err := os.RemoveAll(b.SnapshotDir); err != nil {
		t.Fatal(err)
	}
	repo.makeUnavailable()
	pf.openGate()
	code, stdout, stderr := pf.runAfm("--pipeline", repo.source("main"))
	if code != 0 || !strings.Contains(stdout, "continuing run "+b.RunID) {
		t.Fatalf("code=%d\n%s%s", code, stdout, stderr)
	}
	if _, err := pipeline.VerifySnapshot(b.SnapshotDir); err != nil {
		t.Fatalf("restored snapshot: %v", err)
	}
}

func TestPipelineResumeFailsWhenLocalSnapshotIsGone(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	if code, _, stderr := pf.runAfm("--pipeline", dir); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	b := pf.bindings()[0]
	unlockTree(b.SnapshotDir)
	if err := os.RemoveAll(b.SnapshotDir); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := pf.runAfm("--pipeline", dir)
	if code == 0 || !strings.Contains(stderr, "cannot be continued") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got := pf.runDirs(); len(got) != 1 {
		t.Fatalf("no run may be created: %v", got)
	}
}

func TestPipelineResumeRenamedFlowPicksLatestBinding(t *testing.T) {
	pf := newPipelineFixture(t)
	t.Setenv("AFM_WORKTREES", "1")
	pf.initProjectRepo()
	repo := pf.newGitPackageRepo("oldname")
	source := repo.source("main")
	if code, _, stderr := pf.runAfm("--pipeline", source); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	// The package renames its flow; a clear flag starts a new run under the new name.
	repo.commitPackage(pf, "tpkg", "newname")
	repo.git(repo.work, "push", "-q", repo.bare, "main")
	if code, _, stderr := pf.runAfm("--pipeline", source, "--worktree-clear"); code != 0 {
		t.Fatalf("renamed start: code=%d %s", code, stderr)
	}
	pf.openGate()
	_, stdout, stderr := pf.runAfm("--pipeline", source)
	if !strings.Contains(stdout, "continuing run newname-") || !strings.Contains(stdout, "other unfinished runs of this pipeline: oldname-") {
		t.Fatalf("the binding with the latest update continues and the other run is listed:\n%s%s", stdout, stderr)
	}
}
