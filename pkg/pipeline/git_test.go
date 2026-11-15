package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const gitTestManifest = "schema_version: 2\nname: demo\nversion: \"1.0.0\"\nentrypoint: flow.yaml\n"

// gitFixture is a bare repository with a package in pipelines/demo: branch main, tag v1 on the first commit
// and a second commit on main.
type gitFixture struct {
	url        string
	bareDir    string
	first      string
	second     string
	cacheRoot  string
	workDir    string
	gitEnvBase []string
}

func (f *gitFixture) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(f.gitEnvBase,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	f := &gitFixture{
		cacheRoot:  filepath.Join(root, "cache"),
		workDir:    filepath.Join(root, "work"),
		bareDir:    filepath.Join(root, "repo.git"),
		gitEnvBase: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"},
	}
	f.git(t, root, "init", "--quiet", "-b", "main", f.workDir)
	pkg := filepath.Join(f.workDir, "pipelines", "demo")
	writeTestFile(t, filepath.Join(pkg, "pipeline.yaml"), gitTestManifest)
	writeTestFile(t, filepath.Join(pkg, "flow.yaml"), "name: first\n")
	f.git(t, f.workDir, "add", ".")
	f.git(t, f.workDir, "commit", "--quiet", "-m", "first")
	f.first = f.git(t, f.workDir, "rev-parse", "HEAD")
	f.git(t, f.workDir, "tag", "-a", "-m", "v1", "v1")
	writeTestFile(t, filepath.Join(pkg, "flow.yaml"), "name: second\n")
	f.git(t, f.workDir, "commit", "--quiet", "-am", "second")
	f.second = f.git(t, f.workDir, "rev-parse", "HEAD")
	f.git(t, root, "clone", "--quiet", "--bare", f.workDir, f.bareDir)
	f.url = "file://" + f.bareDir
	// The code under test sanitizes GIT_* itself; HOME keeps it away from the developer's git config.
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	return f
}

func (f *gitFixture) source(ref string) Source {
	return Source{Kind: SourceGit, Location: f.url, Subdir: "pipelines/demo", Ref: ref}
}

func (f *gitFixture) resolve(t *testing.T, ref, offline string) (Resolved, error) {
	t.Helper()
	return ResolveGit(context.Background(), f.source(ref), f.cacheRoot, offline)
}

func flowName(t *testing.T, snap Snapshot) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(snap.Dir, "flow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func cleanupCache(t *testing.T, f *gitFixture) {
	t.Helper()
	t.Cleanup(func() { _ = removeTree(f.cacheRoot) })
}

func TestResolveGitRefs(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	cases := []struct {
		name, ref, commit, flow string
	}{
		{"default head", "", f.second, "name: second"},
		{"branch", "main", f.second, "name: second"},
		{"annotated tag", "v1", f.first, "name: first"},
		{"full commit", f.first, f.first, "name: first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.resolve(t, tc.ref, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.Commit != tc.commit {
				t.Errorf("commit = %s, want %s", got.Commit, tc.commit)
			}
			if name := flowName(t, got.Snapshot); name != tc.flow {
				t.Errorf("flow = %q, want %q", name, tc.flow)
			}
			if _, err := VerifySnapshot(got.Snapshot.Dir); err != nil {
				t.Errorf("snapshot does not verify: %v", err)
			}
		})
	}
}

func TestResolveGitRepoRootPackage(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	src := f.source("main")
	src.Subdir = ""
	// The repository root has no pipeline.yaml, only pipelines/demo does.
	if _, err := ResolveGit(context.Background(), src, f.cacheRoot, ""); err == nil {
		t.Fatal("expected a missing manifest error for the repository root")
	}
}

func TestResolveGitErrors(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	t.Run("unknown ref", func(t *testing.T) {
		if _, err := f.resolve(t, "no-such-ref", ""); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("unknown subdir", func(t *testing.T) {
		src := f.source("main")
		src.Subdir = "pipelines/missing"
		_, err := ResolveGit(context.Background(), src, f.cacheRoot, "")
		if err == nil || !strings.Contains(err.Error(), "pipelines/missing") {
			t.Fatalf("err = %v, want one naming the subdirectory", err)
		}
	})
	t.Run("credentials never reach git", func(t *testing.T) {
		src := Source{Kind: SourceGit, Location: "https://user:tok123@example.invalid/r.git", Ref: "main"} //nolint:gosec // G101: deliberate fake credential for the redaction check
		_, err := ResolveGit(context.Background(), src, f.cacheRoot, "")
		if err == nil || strings.Contains(err.Error(), "tok123") {
			t.Fatalf("err = %v, want a rejection without the token", err)
		}
	})
	t.Run("ref starting with dash", func(t *testing.T) {
		if _, err := f.resolve(t, "--upload-pack=evil", ""); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("local source", func(t *testing.T) {
		if _, err := ResolveGit(context.Background(), Source{Kind: SourceLocal, Location: "/x"}, f.cacheRoot, ""); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("short recorded commit", func(t *testing.T) {
		if _, err := f.resolve(t, "main", f.first[:7]); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestResolveGitOffline(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	online, err := f.resolve(t, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	moved := f.bareDir + ".moved"
	if err := os.Rename(f.bareDir, moved); err != nil {
		t.Fatal(err)
	}

	t.Run("recorded commit", func(t *testing.T) {
		got, err := f.resolve(t, "main", online.Commit)
		if err != nil {
			t.Fatal(err)
		}
		if got.Commit != online.Commit || got.Snapshot != online.Snapshot {
			t.Errorf("got %+v, want %+v", got, online)
		}
	})
	t.Run("full sha ref", func(t *testing.T) {
		got, err := f.resolve(t, online.Commit, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Commit != online.Commit {
			t.Errorf("commit = %s, want %s", got.Commit, online.Commit)
		}
	})
	t.Run("branch needs the network", func(t *testing.T) {
		if _, err := f.resolve(t, "main", ""); err == nil {
			t.Fatal("expected a fetch error: a branch must not be resolved from a stale cache")
		}
	})
	t.Run("commit absent from the cache", func(t *testing.T) {
		_, err := f.resolve(t, "main", strings.Repeat("a", 40))
		if err == nil || !strings.Contains(err.Error(), "not in the local cache") {
			t.Fatalf("err = %v, want a missing-cache error", err)
		}
	})
}

func TestResolveGitConcurrent(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	const workers = 6
	results := make([]Resolved, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			results[i], errs[i] = f.resolve(t, "main", "")
		})
	}
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Errorf("worker %d got %+v, want %+v", i, results[i], results[0])
		}
	}
	assertNoTempDirs(t, f.cacheRoot)
}

// TestResolveGitSeparateProcesses re-executes the test binary so the lock is exercised across processes.
func TestResolveGitSeparateProcesses(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	const procs = 4
	outs := make([]bytes.Buffer, procs)
	cmds := make([]*exec.Cmd, procs)
	for i := range procs {
		cmd := exec.Command(os.Args[0], "-test.run=^TestResolveGitHelperProcess$") //nolint:gosec // G702: os.Args[0] here is the test binary itself
		cmd.Env = append(os.Environ(), "AFM_PIPELINE_GIT_HELPER=1",
			"AFM_PIPELINE_GIT_URL="+f.url, "AFM_PIPELINE_GIT_CACHE="+f.cacheRoot)
		cmd.Stdout = &outs[i]
		cmd.Stderr = &outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	var dirs []string
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("process %d: %v\n%s", i, err, outs[i].String())
		}
		for line := range strings.SplitSeq(outs[i].String(), "\n") {
			if dir, ok := strings.CutPrefix(line, "SNAPSHOT="); ok {
				dirs = append(dirs, dir)
			}
		}
	}
	if len(dirs) != procs {
		t.Fatalf("got %d snapshot lines, want %d", len(dirs), procs)
	}
	for _, d := range dirs {
		if d != dirs[0] {
			t.Errorf("snapshot %s differs from %s", d, dirs[0])
		}
	}
	assertNoTempDirs(t, f.cacheRoot)
}

func TestResolveGitHelperProcess(t *testing.T) {
	if os.Getenv("AFM_PIPELINE_GIT_HELPER") != "1" {
		t.Skip("helper for TestResolveGitSeparateProcesses")
	}
	src := Source{Kind: SourceGit, Location: os.Getenv("AFM_PIPELINE_GIT_URL"), Subdir: "pipelines/demo", Ref: "main"}
	got, err := ResolveGit(context.Background(), src, os.Getenv("AFM_PIPELINE_GIT_CACHE"), "")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("SNAPSHOT=" + got.Snapshot.Dir + "\n")
}

func TestResolveGitFailureLeavesNoTempDirs(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	if _, err := f.resolve(t, "no-such-ref", ""); err == nil {
		t.Fatal("expected an error")
	}
	assertNoTempDirs(t, f.cacheRoot)
}

func TestResolveGitCanceledContext(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveGit(ctx, f.source("main"), f.cacheRoot, ""); err == nil {
		t.Fatal("expected a context error")
	}
	assertNoTempDirs(t, f.cacheRoot)
}

func TestResolveGitSkipsSymlinksButReportsThem(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	pkg := filepath.Join(f.workDir, "pipelines", "demo")
	if err := os.Symlink("/etc/passwd", filepath.Join(pkg, "extra.txt")); err != nil {
		t.Fatal(err)
	}
	f.git(t, f.workDir, "add", ".")
	f.git(t, f.workDir, "commit", "--quiet", "-m", "symlink")
	f.git(t, f.workDir, "push", "--quiet", f.bareDir, "main")

	got, err := f.resolve(t, "main", "")
	if err != nil {
		t.Fatalf("an undeclared symlink must not break the package: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(got.Snapshot.Dir, "extra.txt")); !os.IsNotExist(err) {
		t.Errorf("symlink reached the snapshot: %v", err)
	}
}

func TestResolveGitRejectsSymlinkInDeclaredResource(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	pkg := filepath.Join(f.workDir, "pipelines", "demo")
	writeTestFile(t, filepath.Join(pkg, "pipeline.yaml"), gitTestManifest+"resources:\n  - scripts\n")
	writeTestFile(t, filepath.Join(pkg, "scripts", "real.sh"), "#!/bin/sh\n")
	if err := os.Symlink("real.sh", filepath.Join(pkg, "scripts", "link.sh")); err != nil {
		t.Fatal(err)
	}
	f.git(t, f.workDir, "add", ".")
	f.git(t, f.workDir, "commit", "--quiet", "-m", "declared symlink")
	f.git(t, f.workDir, "push", "--quiet", f.bareDir, "main")

	_, err := f.resolve(t, "main", "")
	if err == nil || !strings.Contains(err.Error(), "scripts/link.sh") {
		t.Fatalf("err = %v, want a rejection naming scripts/link.sh", err)
	}
	assertNoTempDirs(t, f.cacheRoot)
}

func TestRejectSkippedResources(t *testing.T) {
	m := Manifest{Entrypoint: "flow.yaml", Agents: "agents", Resources: []string{"scripts", "docs/guide.md"}}
	cases := []struct {
		skipped string
		reject  bool
	}{
		{"scripts/link.sh", true},
		{"scripts/deep/link.sh", true},
		{"docs", true}, // a symlinked parent of a declared file
		{"docs/guide.md", true},
		{"flow.yaml", true},
		{"agents/a.yaml", true},
		{"other/link", false},
		{"scripts2/link", false},
	}
	for _, tc := range cases {
		err := rejectSkippedResources(m, []string{tc.skipped})
		if (err != nil) != tc.reject {
			t.Errorf("skipped %q: err = %v, want rejection %v", tc.skipped, err, tc.reject)
		}
	}
}

func TestCheckRefSyntaxRejectsRefspecForms(t *testing.T) {
	for _, ref := range []string{"main:refs/replace/" + strings.Repeat("a", 40), "+main", "a..b", "a b", "a^", "a~1", "a*", "a?", "a[b", `a\b`, "main@{1}", "a\x00b"} {
		if err := checkRefSyntax(ref); err == nil {
			t.Errorf("ref %q accepted", ref)
		}
	}
	for _, ref := range []string{"", "main", "v1.0", "feature/x-y_z", "HEAD", strings.Repeat("a", 40)} {
		if err := checkRefSyntax(ref); err != nil {
			t.Errorf("ref %q rejected: %v", ref, err)
		}
	}
}

func TestResolveGitRefspecCannotPoisonCache(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	if _, err := f.resolve(t, "main", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.resolve(t, "main:refs/replace/"+f.second, ""); err == nil {
		t.Fatal("a destination refspec must be rejected")
	}
	gitDir := filepath.Join(f.cacheRoot, gitCacheDir, sha256Hex(f.url), "repo.git")
	if out, err := runGit(context.Background(), gitDir, "for-each-ref", "refs/replace"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("refs/replace = %q, err %v; want none", out, err)
	}
}

func TestResolveGitIgnoresReplaceRefsInPoisonedCache(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	online, err := f.resolve(t, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(f.cacheRoot, gitCacheDir, sha256Hex(f.url), "repo.git")
	// A cache poisoned by an older build: the trusted commit is replaced by the first one.
	if _, err := runGit(context.Background(), gitDir, "update-ref", "refs/replace/"+online.Commit, f.first); err != nil {
		t.Fatal(err)
	}
	for _, offline := range []string{"", online.Commit} {
		got, err := f.resolve(t, online.Commit, offline)
		if err != nil {
			t.Fatal(err)
		}
		if name := flowName(t, got.Snapshot); name != "name: second" {
			t.Errorf("offline=%q: flow = %q, replacement object was honoured", offline, name)
		}
	}
}

func TestGitCommandDropsTransportEnvironment(t *testing.T) {
	sentinels := map[string]string{
		"AFM_SECRET_CLAUDE":           "sentinel-secret",
		"AFM_SYSPROMPT_CLAUDE":        "sentinel-prompt",
		"AFM_HOOK_SECRET_TOKEN":       "sentinel-hook",
		"AFM_BROWSER_TRANSPORT_TOKEN": "sentinel-browser",
		"AFM_BROWSER_ENDPOINT":        "sentinel-endpoint",
	}
	for k, v := range sentinels {
		t.Setenv(k, v)
	}
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("GIT_DIR", "/tmp/elsewhere")
	env := strings.Join(newGitCmd(context.Background(), "/cache/repo.git", "status").Env, "\n")
	for k, v := range sentinels {
		if strings.Contains(env, k) || strings.Contains(env, v) {
			t.Errorf("git child environment leaks %s", k)
		}
	}
	for _, want := range []string{"HTTPS_PROXY=", "GIT_DIR=/cache/repo.git", "GIT_NO_REPLACE_OBJECTS=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("git child environment lacks %s", want)
		}
	}
	if strings.Contains(env, "/tmp/elsewhere") {
		t.Error("inherited GIT_DIR survived sanitizing")
	}
}

func TestGitLocationCredentialsNeverReachGitOrErrors(t *testing.T) {
	f := newGitFixture(t)
	cleanupCache(t, f)
	for _, location := range []string{
		"ssh://git:tok123@example.invalid/r.git",
		"file://git:tok123@localhost" + f.bareDir,
		"file://git@localhost" + f.bareDir,
		"https://user:tok123@example.invalid/r.git",
	} {
		_, err := ResolveGit(context.Background(), Source{Kind: SourceGit, Location: location, Ref: "main"}, f.cacheRoot, "")
		if err == nil || strings.Contains(err.Error(), "tok123") {
			t.Errorf("%s: err = %v, want a rejection without the password", location, err)
		}
	}
	if err := rejectLocationUserinfo("ssh://git@example.invalid/r.git"); err != nil {
		t.Errorf("a bare ssh username must stay allowed: %v", err)
	}
	if got := redactLocation("ssh://git:tok123@example.invalid/r.git"); strings.Contains(got, "tok123") {
		t.Errorf("redactLocation = %q", got)
	}
}

func TestExtractTar(t *testing.T) {
	build := func(entries ...tar.Header) *bytes.Reader {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, h := range entries {
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				if _, err := tw.Write(make([]byte, h.Size)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		return bytes.NewReader(buf.Bytes())
	}

	t.Run("escaping path", func(t *testing.T) {
		dst := t.TempDir()
		_, err := extractTar(build(tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644}), dst)
		if err == nil {
			t.Fatal("expected an error")
		}
		if _, statErr := os.Lstat(filepath.Join(filepath.Dir(dst), "evil")); statErr == nil {
			t.Fatal("file escaped the staging directory")
		}
	})
	t.Run("symlink is not created and later entries cannot pass through it", func(t *testing.T) {
		dst, outside := t.TempDir(), t.TempDir()
		skipped, err := extractTar(build(
			tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: outside},
			tar.Header{Name: "link/pwned", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644},
		), dst)
		if err != nil {
			t.Fatal(err)
		}
		if len(skipped) != 1 || skipped[0] != "link" {
			t.Errorf("skipped = %v, want [link]", skipped)
		}
		if _, statErr := os.Lstat(filepath.Join(outside, "pwned")); statErr == nil {
			t.Fatal("file was written through a symlink")
		}
	})
	t.Run("regular files keep the exec bit", func(t *testing.T) {
		dst := t.TempDir()
		_, err := extractTar(build(
			tar.Header{Name: "bin/run.sh", Typeflag: tar.TypeReg, Size: 1, Mode: 0o755},
			tar.Header{Name: "a.txt", Typeflag: tar.TypeReg, Size: 1, Mode: 0o600},
		), dst)
		if err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Stat(filepath.Join(dst, "bin", "run.sh")); info == nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("run.sh lost its exec bit: %v", info)
		}
		if info, _ := os.Stat(filepath.Join(dst, "a.txt")); info == nil || info.Mode().Perm()&0o111 != 0 {
			t.Errorf("a.txt must not be executable: %v", info)
		}
	})
}

func assertNoTempDirs(t *testing.T, cacheRoot string) {
	t.Helper()
	for _, dir := range []string{filepath.Join(cacheRoot, gitCacheDir), SnapshotsRoot(cacheRoot)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".stage-") || strings.HasPrefix(e.Name(), ".tmp-") {
				t.Errorf("leftover temporary directory %s", filepath.Join(dir, e.Name()))
			}
		}
	}
}
