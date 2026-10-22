package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

func releaseCommand(dir, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	return cmd
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out, err := releaseCommand(dir, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func releaseRepo(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"git", "sh", "make"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("release tests require %s: %v", tool, err)
		}
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Makefile", "scripts/release.sh"} {
		data, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.name", "Release test")
	run(t, dir, "git", "config", "user.email", "release-test@example.com")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "Initial commit")
	return dir
}

func TestReleaseDryRun(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name string
		tags []string
		args []string
		want string
	}
	tests := []testCase{
		{"no tags", nil, []string{"major", "alpha"}, "v1.0.0-alpha.1"},
		{"only prerelease tags", []string{"v1.0.0-alpha.1"}, []string{"major", "alpha"}, "v1.0.0-alpha.2"},
		{"repeat alpha", []string{"v1.2.17", "v2.0.0-alpha.1"}, []string{"major", "alpha"}, "v2.0.0-alpha.2"},
		{"numeric counter", []string{"v1.2.17", "v2.0.0-alpha.2", "v2.0.0-alpha.9", "v2.0.0-alpha.10"}, []string{"major", "alpha"}, "v2.0.0-alpha.11"},
		{"switch to beta", []string{"v1.2.17", "v2.0.0-alpha.9"}, []string{"major", "beta"}, "v2.0.0-beta.1"},
		{"repeat beta", []string{"v1.2.17", "v2.0.0-alpha.9", "v2.0.0-beta.2"}, []string{"major", "beta"}, "v2.0.0-beta.3"},
		{"stable major", []string{"v1.2.17", "v2.0.0-beta.2"}, []string{"major"}, "v2.0.0"},
		{"explicit stable patch", []string{"v1.2.17", "v2.0.0-alpha.1"}, []string{"patch"}, "v1.2.18"},
		{"new stable major", []string{"v1.2.17", "v2.0.0-alpha.9", "v2.0.0"}, []string{"major", "alpha"}, "v3.0.0-alpha.1"},
		{"new patch counter", []string{"v1.2.17", "v1.2.18-alpha.9", "v1.2.18"}, []string{"patch", "alpha"}, "v1.2.19-alpha.1"},
		{"ignore malformed tags", []string{"v1.2.17", "v9.0.0-experiment", "v9.0", "v01.0.0", "v2.0.0-alpha.01", "v2.0.0-alpha.9.extra", "sdk/v9.0.0"}, []string{"major", "alpha"}, "v2.0.0-alpha.1"},
		{"numeric stable version", []string{"v1.9.99", "v1.10.0"}, []string{"minor", "beta"}, "v1.11.0-beta.1"},
	}
	for _, level := range []struct{ name, version string }{
		{"major", "v2.0.0"}, {"minor", "v1.3.0"}, {"patch", "v1.2.18"},
	} {
		for _, channel := range []string{"", "alpha", "beta"} {
			args := []string{level.name}
			want := level.version
			if channel != "" {
				args = append(args, channel)
				want += "-" + channel + ".1"
			}
			tests = append(tests, testCase{strings.Join(args, " "), []string{"v1.2.17"}, args, want})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := releaseRepo(t)
			for _, tag := range tt.tags {
				run(t, dir, "git", "tag", tag)
			}
			before := run(t, dir, "git", "tag", "--list")
			args := append([]string{"./scripts/release.sh", "--dry-run"}, tt.args...)
			if got := run(t, dir, "sh", args...); got != tt.want {
				t.Fatalf("next version = %q, want %q", got, tt.want)
			}
			if after := run(t, dir, "git", "tag", "--list"); after != before {
				t.Fatal("dry-run changed tags")
			}
		})
	}
}

func TestReleaseUnmergedTags(t *testing.T) {
	t.Parallel()
	dir := releaseRepo(t)
	run(t, dir, "git", "tag", "v1.2.17")
	run(t, dir, "git", "checkout", "-b", "future")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "Future release")
	run(t, dir, "git", "tag", "v9.0.0")
	run(t, dir, "git", "tag", "v2.0.0-alpha.7")
	run(t, dir, "git", "checkout", "main")
	if got := run(t, dir, "sh", "./scripts/release.sh", "--dry-run", "major", "alpha"); got != "v2.0.0-alpha.8" {
		t.Fatalf("next version = %q, want v2.0.0-alpha.8", got)
	}
}

func TestReleaseMakeTargets(t *testing.T) {
	t.Parallel()
	dir := releaseRepo(t)
	for _, tt := range []struct {
		name string
		env  []string
	}{
		{"direct", []string{"MAKELEVEL=0", "MAKEFLAGS=", "MFLAGS="}},
		{"recursive", []string{"MAKELEVEL=1", "MAKEFLAGS=w", "MFLAGS=-w"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, level := range []string{"major", "minor", "patch"} {
				for _, channel := range []string{"", "alpha", "beta"} {
					target := "release-" + level
					want := "./scripts/release.sh " + level
					if channel != "" {
						target += "-" + channel
						want += " " + channel
					}
					// Assert the recipe only, even when make test enables recursive
					// directory messages through MAKELEVEL or inherited MAKEFLAGS.
					cmd := releaseCommand(dir, "make", "--no-print-directory", "-n", target)
					cmd.Env = append(cmd.Env, tt.env...)
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("%s: %v\n%s", target, err, out)
					}
					if got := strings.TrimSpace(string(out)); got != want {
						t.Fatalf("%s = %q, want %q", target, got, want)
					}
				}
			}
		})
	}
}

func TestReleasePublishSequence(t *testing.T) {
	t.Parallel()
	dir := releaseRepo(t)
	origin := t.TempDir()
	run(t, origin, "git", "init", "--bare")
	run(t, dir, "git", "remote", "add", "origin", origin)
	run(t, dir, "git", "tag", "-a", "v1.2.17", "-m", "Stable release")
	for _, step := range []struct{ target, tag string }{
		{"release-major-alpha", "v2.0.0-alpha.1"},
		{"release-major-alpha", "v2.0.0-alpha.2"},
		{"release-major-beta", "v2.0.0-beta.1"},
		{"release-major-beta", "v2.0.0-beta.2"},
		{"release-major", "v2.0.0"},
	} {
		run(t, dir, "make", step.target)
		if got := run(t, origin, "git", "cat-file", "-t", "refs/tags/"+step.tag); got != "tag" {
			t.Fatalf("%s: expected an annotated tag in origin, got %q", step.tag, got)
		}
	}
}

func TestReleaseRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	dir := releaseRepo(t)
	for _, args := range [][]string{nil, {"--dry-run"}, {"bad"}, {"major", "rc"}, {"major", "alpha", "extra"}} {
		cmd := releaseCommand(dir, "sh", append([]string{"./scripts/release.sh"}, args...)...)
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 || !strings.Contains(string(out), "usage:") {
			t.Fatalf("args %v: expected usage and exit 2, got %v\n%s", args, err, out)
		}
	}
}

func TestReleaseRejectsDirtyTree(t *testing.T) {
	t.Parallel()
	dir := releaseRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "uncommitted"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := releaseCommand(dir, "sh", "./scripts/release.sh", "major", "alpha")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "working tree not clean") {
		t.Fatalf("expected dirty-tree rejection, got %v\n%s", err, out)
	}
	if tags := run(t, dir, "git", "tag", "--list"); tags != "" {
		t.Fatalf("dirty-tree release created tags: %s", tags)
	}
}

func renderReleaseTemplate(t *testing.T, source, prerelease string) string {
	t.Helper()
	tmpl, err := template.New("release").Funcs(template.FuncMap{"split": strings.Split}).Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, struct{ Prerelease string }{prerelease}); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

func TestHomebrewReleaseChannels(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../.goreleaser.yml")
	if err != nil {
		t.Fatal(err)
	}
	type releasePolicy struct {
		Prerelease string
		MakeLatest string `yaml:"make_latest"`
	}
	type caskHook struct{ Install string }
	type homebrewCask struct {
		Name       string
		SkipUpload string `yaml:"skip_upload"`
		Binaries   []string
		Hooks      map[string]caskHook
	}
	var config struct {
		Release releasePolicy  `yaml:"release"`
		Casks   []homebrewCask `yaml:"homebrew_casks"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Release.Prerelease != "auto" || len(config.Casks) != 3 {
		t.Fatal("expected automatic prereleases and three Homebrew channels")
	}
	for _, tt := range []struct{ prerelease, cask, latest string }{
		{"", "afm", "true"}, {"alpha.1", "afm-alpha", "false"},
		{"alpha.10", "afm-alpha", "false"}, {"beta.1", "afm-beta", "false"}, {"rc.1", "", "false"},
	} {
		t.Run(tt.prerelease, func(t *testing.T) {
			if got := renderReleaseTemplate(t, config.Release.MakeLatest, tt.prerelease); got != tt.latest {
				t.Fatalf("make_latest = %q, want %q", got, tt.latest)
			}
			var published []string
			for _, cask := range config.Casks {
				skip := renderReleaseTemplate(t, cask.SkipUpload, tt.prerelease)
				if skip == "true" || (skip == "auto" && tt.prerelease != "") {
					continue
				}
				published = append(published, cask.Name)
				if len(cask.Binaries) != 1 || renderReleaseTemplate(t, cask.Binaries[0], tt.prerelease) != tt.cask {
					t.Fatalf("%s must install command %s", cask.Name, tt.cask)
				}
				pre := renderReleaseTemplate(t, cask.Hooks["pre"].Install, tt.prerelease)
				post := renderReleaseTemplate(t, cask.Hooks["post"].Install, tt.prerelease)
				if tt.prerelease != "" && !strings.Contains(pre, `staged_path.join("`+tt.cask+`")`) {
					t.Fatalf("preflight must rename afm to %s: %s", tt.cask, pre)
				}
				if !strings.Contains(post, "#{staged_path}/"+tt.cask+`"`) {
					t.Fatalf("postflight must sign %s: %s", tt.cask, post)
				}
			}
			want := []string{tt.cask}
			if tt.cask == "" {
				want = nil
			}
			if !slices.Equal(published, want) {
				t.Fatalf("published casks = %v, want %v", published, want)
			}
		})
	}
}

func TestReleaseDockerTags(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Name, Run, Uses string
		Env             map[string]string
	}
	type job struct{ Steps []step }
	var workflow struct{ Jobs map[string]job }
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var script string
	var releaseTag string
	for _, step := range workflow.Jobs["release"].Steps {
		if strings.Contains(step.Name, "docker image") {
			script = step.Run
		}
		if strings.HasPrefix(step.Uses, "goreleaser/goreleaser-action@") {
			releaseTag = step.Env["GORELEASER_CURRENT_TAG"]
		}
	}
	if releaseTag != "${{ github.ref_name }}" {
		t.Fatal("GoReleaser must use the triggering tag when multiple channels tag the same commit")
	}
	if script == "" {
		t.Fatal("Docker build step missing")
	}
	// A shell function, not a fake executable in PATH: a just-written file can briefly fail to exec (ETXTBSY
	// while a parallel test forks), and sh then silently falls through to the real docker later in PATH.
	fakeDocker := "docker() { printf '%s\\n' \"$@\"; }\n"
	for _, tag := range []string{"v1.2.18", "v2.0.0-alpha.1", "v2.0.0-beta.1"} {
		cmd := releaseCommand(t.TempDir(), "sh", "-eu", "-c", fakeDocker+script)
		cmd.Env = append(cmd.Env, "RELEASE_TAG="+tag)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Docker step: %v\n%s", err, out)
		}
		args := strings.Split(strings.TrimSpace(string(out)), "\n")
		if !slices.Contains(args, "akopichin/afm:"+tag) || !slices.Contains(args, "AFM_VERSION="+tag) {
			t.Fatalf("missing version tag or build argument: %s", out)
		}
		if latest := slices.Contains(args, "akopichin/afm:latest"); latest == strings.Contains(tag, "-") {
			t.Fatalf("incorrect latest tag for %s: %s", tag, out)
		}
	}
}

func TestReleasesRequireExplicitTags(t *testing.T) {
	t.Parallel()
	ci, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var ciWorkflow struct{ Permissions map[string]string }
	if err := yaml.Unmarshal(ci, &ciWorkflow); err != nil {
		t.Fatal(err)
	}
	if ciWorkflow.Permissions["contents"] != "read" {
		t.Fatal("validation CI must use a read-only token so branch pushes cannot create release tags")
	}
	if strings.Contains(string(ci), "secrets.RELEASE_TOKEN") || strings.Contains(string(ci), "scripts/release.sh") {
		t.Fatal("validation CI must not have release credentials or invoke the release script")
	}
	release, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	type pushTrigger struct {
		Tags, Branches []string
	}
	var releaseWorkflow struct {
		On map[string]pushTrigger
	}
	if err := yaml.Unmarshal(release, &releaseWorkflow); err != nil {
		t.Fatal(err)
	}
	push, ok := releaseWorkflow.On["push"]
	if !ok || !slices.Equal(push.Tags, []string{"v*.*.*"}) || len(push.Branches) != 0 || len(releaseWorkflow.On) != 1 {
		t.Fatal("publishing must be triggered only by an explicit version tag push")
	}
}
