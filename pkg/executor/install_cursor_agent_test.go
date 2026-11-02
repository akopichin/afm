package executor

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const installScriptName = "install-cursor-agent.sh"

func repoFile(t *testing.T, elems ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(append([]string{filepath.Dir(file), "..", ".."}, elems...)...)
}

// buildCursorPackage returns a gzip tarball shaped like the real package (one top-level directory holding the
// launcher) and its sha256.
func buildCursorPackage(t *testing.T) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "agent-cli-package.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	launcher := "#!/bin/sh\necho fake-cursor-agent\n"
	if err := tw.WriteHeader(&tar.Header{Name: "dist-package/cursor-agent", Mode: 0o755, Size: int64(len(launcher)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(launcher)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []io.Closer{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	return path, hex.EncodeToString(h[:])
}

// installHarness fakes curl (serving the package, recording the URL) and uname (a pinned Linux platform).
type installHarness struct {
	binDir, prefix, urlFile string
}

func newInstallHarness(t *testing.T, packagePath, machine string) installHarness {
	t.Helper()
	h := installHarness{binDir: t.TempDir(), prefix: filepath.Join(t.TempDir(), "prefix")}
	h.urlFile = filepath.Join(h.binDir, "url.log")
	curl := fmt.Sprintf(`#!/usr/bin/env bash
out=""; url=""
while [[ $# -gt 0 ]]; do
    case "$1" in -o) out="$2"; shift ;; http*) url="$1" ;; esac
    shift
done
printf '%%s\n' "$url" >> %q
cp %q "$out"
`, h.urlFile, packagePath)
	uname := fmt.Sprintf("#!/usr/bin/env bash\ncase \"$1\" in -s) echo Linux ;; -m) echo %s ;; esac\n", machine)
	for name, body := range map[string]string{"curl": curl, "uname": uname} {
		if err := os.WriteFile(filepath.Join(h.binDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h installHarness) run(t *testing.T, script string) (string, error) {
	t.Helper()
	return h.runIn(t, "", script, h.prefix)
}

// runIn runs the install script from dir with the given prefix argument.
func (h installHarness) runIn(t *testing.T, dir, script, prefix string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", script, prefix)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// pinnedTo returns a copy of the install script whose pins accept the given sha256.
func pinnedTo(t *testing.T, sum string) string {
	t.Helper()
	raw, err := os.ReadFile(repoFile(t, "scripts", installScriptName))
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`(?m)^(SHA256_LINUX_\w+)="[^"]*"`).ReplaceAllString(string(raw), `$1="`+sum+`"`)
	path := filepath.Join(t.TempDir(), installScriptName)
	if err := os.WriteFile(path, []byte(pinned), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireBashAndTar(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
}

func TestInstallCursorAgent_VerifiedPackageIsInstalled(t *testing.T) {
	requireBashAndTar(t)
	pkg, sum := buildCursorPackage(t)
	h := newInstallHarness(t, pkg, "x86_64")

	if out, err := h.run(t, pinnedTo(t, sum)); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}

	bin := filepath.Join(h.prefix, "bin", "cursor-agent")
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "fake-cursor-agent" {
		t.Fatalf("installed launcher: %q, %v", out, err)
	}
	if target, err := os.Readlink(bin); err != nil || !strings.Contains(target, filepath.Join("share", "cursor-agent", "2026.10.22-27dcf5f")) {
		t.Errorf("symlink target = %q, %v", target, err)
	}
}

// A relative PREFIX is taken from the current directory; the bin/ link must still reach the launcher.
func TestInstallCursorAgent_RelativePrefixGivesAWorkingLauncher(t *testing.T) {
	requireBashAndTar(t)
	pkg, sum := buildCursorPackage(t)
	h := newInstallHarness(t, pkg, "x86_64")
	dir := t.TempDir()

	if out, err := h.runIn(t, dir, pinnedTo(t, sum), "relative-prefix"); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}

	out, err := exec.Command(filepath.Join(dir, "relative-prefix", "bin", "cursor-agent")).Output()
	if err != nil || strings.TrimSpace(string(out)) != "fake-cursor-agent" {
		t.Fatalf("installed launcher: %q, %v", out, err)
	}
}

func TestInstallCursorAgent_HashMismatchInstallsNothing(t *testing.T) {
	requireBashAndTar(t)
	pkg, _ := buildCursorPackage(t)
	h := newInstallHarness(t, pkg, "x86_64")

	out, err := h.run(t, repoFile(t, "scripts", installScriptName)) // the real pins do not match the fake package
	if err == nil {
		t.Fatalf("a package with the wrong hash was accepted:\n%s", out)
	}
	if !strings.Contains(out, "sha256 mismatch") {
		t.Errorf("error does not name the mismatch:\n%s", out)
	}
	if _, statErr := os.Stat(h.prefix); statErr == nil {
		t.Errorf("prefix %s was created although verification failed", h.prefix)
	}
}

func TestInstallCursorAgent_DownloadsPinnedVersionPerPlatform(t *testing.T) {
	requireBashAndTar(t)
	pkg, _ := buildCursorPackage(t)
	for machine, platform := range map[string]string{"x86_64": "linux/x64", "aarch64": "linux/arm64"} {
		t.Run(machine, func(t *testing.T) {
			h := newInstallHarness(t, pkg, machine)
			_, _ = h.run(t, repoFile(t, "scripts", installScriptName))
			urls, _ := os.ReadFile(h.urlFile)
			want := "https://downloads.cursor.com/lab/2026.10.22-27dcf5f/" + platform + "/agent-cli-package.tar.gz"
			if strings.TrimSpace(string(urls)) != want {
				t.Errorf("downloaded %q, want %q", urls, want)
			}
		})
	}
}

func TestInstallCursorAgent_BothPinsAreRealHashes(t *testing.T) {
	raw, err := os.ReadFile(repoFile(t, "scripts", installScriptName))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SHA256_LINUX_X64", "SHA256_LINUX_ARM64"} {
		if !regexp.MustCompile(`(?m)^` + name + `="[0-9a-f]{64}"$`).Match(raw) {
			t.Errorf("%s is not pinned to a 64-hex sha256", name)
		}
	}
}

func TestInstallCursorAgent_UnsupportedPlatformFails(t *testing.T) {
	requireBashAndTar(t)
	pkg, _ := buildCursorPackage(t)
	h := newInstallHarness(t, pkg, "riscv64")
	if out, err := h.run(t, repoFile(t, "scripts", installScriptName)); err == nil || !strings.Contains(out, "unsupported platform") {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if urls, _ := os.ReadFile(h.urlFile); len(urls) != 0 {
		t.Errorf("downloaded on an unsupported platform: %s", urls)
	}
}

func TestDockerfileRuntime_InstallsCursorAgentAndItsAdapter(t *testing.T) {
	raw, err := os.ReadFile(repoFile(t, "Dockerfile.runtime"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(raw)
	for _, want := range []string{"scripts/install-cursor-agent.sh", "scripts/cursor-agent-as-claude.sh /usr/local/bin/cursor-agent-as-claude"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile.runtime lacks %q", want)
		}
	}
	if strings.Contains(dockerfile, "cursor-as-claude") {
		t.Error("Dockerfile.runtime still installs the removed Cursor Cloud Agents adapter")
	}
}
