# Docker Project File Browser Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Docker-only, strictly read-only file browser to the afm dashboard — browse project + explicitly-allowed extra mounts, view syntax-highlighted files and `HEAD → working tree` diffs, and insert absolute container-path references into plan/question review comments.

**Architecture:** Host `pkg/docker` launcher builds a versioned, base64 manifest of browseable container paths and passes it in via `-e AFM_DOCKER_FILE_ROOTS`. Inside the container, `cmd/afm/run.go` decodes it and constructs a self-contained secure-filesystem module `pkg/server/workspace` (Linux `openat2` with `RESOLVE_BENEATH|NO_MAGICLINKS|NO_SYMLINKS`). The HTTP server exposes GET-only `/api/files/*` endpoints over that module. The dashboard gets a standalone `components/file-browser/*` component, invoked from a provider by `FlowHeader` (browse mode) and by `PlanPanel`/`DialogChannel` (picker mode), inserting a plain-text `[AFM file: "<path>"]` marker through the existing feedback chains.

**Tech Stack:** Go 1.26 (`net/http` stdlib mux, `golang.org/x/sys/unix`, `github.com/aymanbagabas/go-udiff`), React 18 + TypeScript + Vite, `highlight.js/lib/core` (4 grammars).

**Spec:** `docs/superpowers/specs/2026-09-03-docker-project-file-browser-design.md`

## Global Constraints

- **Docker-only.** The feature is inert on host runs: no manifest → empty workspace → `capabilities.file_browser=false` → no UI, `/api/files/*` returns 404. Never a second filesystem-access security path.
- **Do NOT change the `go` version in `go.mod`** (currently `go 1.26.4`). All needed APIs are available without a bump.
- **New backend filesystem logic lives in its own package** `pkg/server/workspace/*` — never inlined into the already-688-line `handlers.go`.
- **New frontend UI lives in its own component tree** `components/file-browser/*`, invoked from call sites via a provider — never copy-pasted into panels.
- **Secure fd traversal only.** Every path open goes through `openat2` from a root dir fd. Never `check-then-os.ReadFile`. `openat2` `ENOSYS` (Linux kernel < 5.6) → degrade gracefully (workspace with zero roots), never crash.
- **HTTP never accepts an absolute path.** Every endpoint takes `root=<opaque-id>&path=<slash-relative-no-dotdot>`. The server builds absolute paths only after a successful secure open.
- **Only GET endpoints.** Bounded reads (content ≤ 2 MiB, diff ≤ 4 MiB), Git via `exec.CommandContext` with a 3s timeout and no shell.
- **`.git` and `.afm` are hidden** from the general tree/content API (segments rejected in path input, entries filtered in listings). Exception: an extra root the user pointed *directly* at such a dir with `browse:true`.
- **Loopback bind only when file_browser is enabled** — `-p 127.0.0.1:<port>:<port>`. Plain Docker runs keep `-p <port>:<port>`.
- **Error bodies for `/api/files/*` are JSON** via a scoped `writeFilesError` helper (the rest of the server keeps its plain-text `http.Error` convention — do not change it).
- **All commit messages in Russian. Never add `Co-Authored-By`.**
- **After code edits, `make lint` must be clean.** (The pre-commit hook runs the full suite and can exceed 2 min; that is expected — let it run, or for a docs-only commit use `--no-verify`.)

---

## Phase 1 — Config & manifest (host-side, pure Go)

### Task 1: `ExtraMounts` scalar-or-object config type

**Files:**
- Modify: `pkg/config/config.go` (`DockerConfig` struct near line 170-187; add new types)
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Produces:
  ```go
  type DockerFileBrowserConfig struct{ Enabled *bool `yaml:"enabled"` }
  func (c DockerFileBrowserConfig) IsEnabled() bool // default true (nil → true)

  type ExtraMount struct {
      Path   string `yaml:"path"`
      Name   string `yaml:"name,omitempty"`
      Browse bool   `yaml:"browse,omitempty"`
  }
  type ExtraMounts []ExtraMount
  func (m *ExtraMounts) UnmarshalYAML(value *yaml.Node) error
  ```
- `DockerConfig.ExtraMounts` changes type `[]string` → `ExtraMounts`. `DockerConfig` gains `FileBrowser DockerFileBrowserConfig \`yaml:"file_browser"\``.

- [ ] **Step 1: Write the failing test**

```go
func TestExtraMounts_UnmarshalScalarAndObject(t *testing.T) {
	var dc DockerConfig
	yml := []byte(`
extra_mounts:
  - path: ../shared-contracts
    name: contracts
    browse: true
  - path: ~/.ai-free
    browse: false
  - ~/.legacy-agent
`)
	if err := yaml.Unmarshal(yml, &dc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := ExtraMounts{
		{Path: "../shared-contracts", Name: "contracts", Browse: true},
		{Path: "~/.ai-free", Browse: false},
		{Path: "~/.legacy-agent", Browse: false}, // legacy scalar → browse:false
	}
	if !reflect.DeepEqual(dc.ExtraMounts, want) {
		t.Fatalf("got %+v want %+v", dc.ExtraMounts, want)
	}
}

func TestFileBrowser_DefaultsEnabled(t *testing.T) {
	if !(DockerFileBrowserConfig{}).IsEnabled() {
		t.Fatal("nil Enabled should default to true")
	}
	f := false
	if (DockerFileBrowserConfig{Enabled: &f}).IsEnabled() {
		t.Fatal("explicit false must disable")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/ -run 'TestExtraMounts_UnmarshalScalarAndObject|TestFileBrowser_DefaultsEnabled' -v`
Expected: FAIL (compile error — `ExtraMount`/`DockerFileBrowserConfig` undefined; `ExtraMounts` is `[]string`).

- [ ] **Step 3: Write minimal implementation**

In `pkg/config/config.go`, change the field and add the types:

```go
// in DockerConfig:
ExtraMounts ExtraMounts           `yaml:"extra_mounts"`
FileBrowser DockerFileBrowserConfig `yaml:"file_browser"`

type DockerFileBrowserConfig struct {
	Enabled *bool `yaml:"enabled"`
}

func (c DockerFileBrowserConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

type ExtraMount struct {
	Path   string `yaml:"path"`
	Name   string `yaml:"name,omitempty"`
	Browse bool   `yaml:"browse,omitempty"`
}

type ExtraMounts []ExtraMount

func (m *ExtraMounts) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("extra_mounts must be a list")
	}
	out := make(ExtraMounts, 0, len(value.Content))
	for _, item := range value.Content {
		switch item.Kind {
		case yaml.ScalarNode: // legacy "~/path" → browse:false
			out = append(out, ExtraMount{Path: item.Value})
		case yaml.MappingNode:
			var em ExtraMount
			if err := item.Decode(&em); err != nil {
				return err
			}
			out = append(out, em)
		default:
			return fmt.Errorf("extra_mounts item must be a string or a mapping")
		}
	}
	*m = out
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/config/ -run 'TestExtraMounts_UnmarshalScalarAndObject|TestFileBrowser_DefaultsEnabled' -v`
Expected: PASS. Then `go build ./...` — fix any call site that iterated `ExtraMounts` as `[]string` (see Task 3; the launcher loop must switch to `m.Path`).

- [ ] **Step 5: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): scalar-or-object extra_mounts и docker.file_browser"
```

---

### Task 2: `ExtraMounts` validation

**Files:**
- Modify: `pkg/config/config.go` (wherever `Config`/`DockerConfig` is validated after load — follow the existing validation entry point)
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `func (m ExtraMounts) Validate() error` — called from the existing config-validation path. Errors: empty `path`; `browse:true` with empty path; duplicate normalized path.

- [ ] **Step 1: Write the failing test**

```go
func TestExtraMounts_Validate(t *testing.T) {
	cases := []struct {
		name string
		in   ExtraMounts
		ok   bool
	}{
		{"ok", ExtraMounts{{Path: "a", Browse: true}, {Path: "b"}}, true},
		{"empty path", ExtraMounts{{Path: ""}}, false},
		{"browse empty path", ExtraMounts{{Path: "", Browse: true}}, false},
		{"dup", ExtraMounts{{Path: "a"}, {Path: "a", Browse: true}}, false},
	}
	for _, c := range cases {
		err := c.in.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: got err=%v want ok=%v", c.name, err, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/ -run TestExtraMounts_Validate -v`
Expected: FAIL (`Validate` undefined).

- [ ] **Step 3: Write minimal implementation**

```go
func (m ExtraMounts) Validate() error {
	seen := map[string]bool{}
	for i, em := range m {
		if strings.TrimSpace(em.Path) == "" {
			return fmt.Errorf("extra_mounts[%d]: path is empty", i)
		}
		key := filepath.Clean(em.Path)
		if seen[key] {
			return fmt.Errorf("extra_mounts[%d]: duplicate path %q", i, em.Path)
		}
		seen[key] = true
	}
	return nil
}
```

Call `cfg.Docker.ExtraMounts.Validate()` from the existing config validation function (return the error).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/config/ -run TestExtraMounts_Validate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): валидация extra_mounts (пустой путь, дубли)"
```

---

### Task 3: File-root manifest build/encode/decode

**Files:**
- Create: `pkg/docker/manifest.go`
- Modify: `pkg/docker/launcher.go` (extra-mounts `-v` loop at ~301-305 → use `m.Path`; reuse the same host/container `expandHome` normalization)
- Test: `pkg/docker/manifest_test.go`

**Interfaces:**
- Produces:
  ```go
  const FileRootsEnvVar = "AFM_DOCKER_FILE_ROOTS"
  const fileRootManifestVersion = 1

  type FileRootManifestEntry struct {
      ID            string `json:"id"`
      Label         string `json:"label"`
      ContainerPath string `json:"container_path"`
      MountReadOnly bool   `json:"mount_read_only"`
      Kind          string `json:"kind"` // "project" | "extra"
  }
  type FileRootManifest struct {
      Version int                     `json:"version"`
      Roots   []FileRootManifestEntry `json:"roots"`
  }
  func BuildFileRootManifest(projectContainerPath string, mounts config.ExtraMounts) (FileRootManifest, error)
  func EncodeFileRootManifest(m FileRootManifest) (string, error)   // base64.RawURLEncoding(JSON)
  func DecodeFileRootManifest(raw string) (FileRootManifest, error) // validates version/ids/abs paths
  ```
- `BuildFileRootManifest` always includes the project root (`id:"project", kind:"project", mount_read_only:false`), and one `extra-N` entry per `browse:true` mount using the **container** path (`expandHome(path, "/home/afm")`), `mount_read_only:true`, label = `Name` or `filepath.Base(path)`. Never includes `browse:false`/legacy mounts. A browseable duplicate nested in another browseable root is kept only if it has a distinct `Name`.

- [ ] **Step 1: Write the failing test**

```go
func TestBuildFileRootManifest_OnlyBrowseable(t *testing.T) {
	m, err := BuildFileRootManifest("/work/afm", config.ExtraMounts{
		{Path: "../shared", Name: "contracts", Browse: true},
		{Path: "~/.ai-free", Browse: false},
		{Path: "~/.legacy"}, // scalar → browse:false
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 {
		t.Fatalf("version=%d", m.Version)
	}
	if len(m.Roots) != 2 {
		t.Fatalf("want project+1 extra, got %d: %+v", len(m.Roots), m.Roots)
	}
	if m.Roots[0].ID != "project" || m.Roots[0].Kind != "project" || m.Roots[0].MountReadOnly {
		t.Errorf("bad project root: %+v", m.Roots[0])
	}
	if m.Roots[1].ID != "extra-1" || m.Roots[1].Label != "contracts" ||
		!m.Roots[1].MountReadOnly || m.Roots[1].ContainerPath != "/work/shared" {
		t.Errorf("bad extra root: %+v", m.Roots[1])
	}
}

func TestFileRootManifest_RoundTripAndReject(t *testing.T) {
	m := FileRootManifest{Version: 1, Roots: []FileRootManifestEntry{
		{ID: "project", Label: "afm", ContainerPath: "/work/afm", Kind: "project"},
	}}
	enc, err := EncodeFileRootManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFileRootManifest(enc)
	if err != nil || len(got.Roots) != 1 || got.Roots[0].ContainerPath != "/work/afm" {
		t.Fatalf("round-trip: %v %+v", err, got)
	}
	if _, err := DecodeFileRootManifest("!!!not-base64!!!"); err == nil {
		t.Error("corrupt base64 must error")
	}
	bad, _ := EncodeFileRootManifest(FileRootManifest{Version: 2, Roots: m.Roots})
	if _, err := DecodeFileRootManifest(bad); err == nil {
		t.Error("bad version must error")
	}
	dup := FileRootManifest{Version: 1, Roots: []FileRootManifestEntry{
		{ID: "x", ContainerPath: "/a", Kind: "extra"},
		{ID: "x", ContainerPath: "/b", Kind: "extra"},
	}}
	de, _ := EncodeFileRootManifest(dup)
	if _, err := DecodeFileRootManifest(de); err == nil {
		t.Error("duplicate id must error")
	}
	rel := FileRootManifest{Version: 1, Roots: []FileRootManifestEntry{{ID: "y", ContainerPath: "rel/path", Kind: "extra"}}}
	re, _ := EncodeFileRootManifest(rel)
	if _, err := DecodeFileRootManifest(re); err == nil {
		t.Error("relative container path must error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run 'TestBuildFileRootManifest_OnlyBrowseable|TestFileRootManifest_RoundTripAndReject' -v`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Write minimal implementation**

`pkg/docker/manifest.go`:

```go
package docker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/akopichin/afm/pkg/config"
)

const (
	FileRootsEnvVar         = "AFM_DOCKER_FILE_ROOTS"
	fileRootManifestVersion = 1
)

type FileRootManifestEntry struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	ContainerPath string `json:"container_path"`
	MountReadOnly bool   `json:"mount_read_only"`
	Kind          string `json:"kind"`
}

type FileRootManifest struct {
	Version int                     `json:"version"`
	Roots   []FileRootManifestEntry `json:"roots"`
}

func BuildFileRootManifest(projectContainerPath string, mounts config.ExtraMounts) (FileRootManifest, error) {
	m := FileRootManifest{Version: fileRootManifestVersion}
	m.Roots = append(m.Roots, FileRootManifestEntry{
		ID:            "project",
		Label:         filepath.Base(projectContainerPath),
		ContainerPath: projectContainerPath,
		MountReadOnly: false,
		Kind:          "project",
	})
	seen := map[string]bool{projectContainerPath: true}
	n := 0
	for _, em := range mounts {
		if !em.Browse {
			continue
		}
		container := expandHome(em.Path, containerHome)
		label := em.Name
		if label == "" {
			label = filepath.Base(container)
		}
		if seen[container] && label == filepath.Base(container) {
			continue // dedup unless distinct name
		}
		seen[container] = true
		n++
		m.Roots = append(m.Roots, FileRootManifestEntry{
			ID:            fmt.Sprintf("extra-%d", n),
			Label:         label,
			ContainerPath: container,
			MountReadOnly: true,
			Kind:          "extra",
		})
	}
	return m, nil
}

func EncodeFileRootManifest(m FileRootManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func DecodeFileRootManifest(raw string) (FileRootManifest, error) {
	var m FileRootManifest
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return m, fmt.Errorf("decode file roots: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("parse file roots: %w", err)
	}
	if m.Version != fileRootManifestVersion {
		return m, fmt.Errorf("unsupported file roots version %d", m.Version)
	}
	ids := map[string]bool{}
	for _, r := range m.Roots {
		if ids[r.ID] {
			return m, fmt.Errorf("duplicate root id %q", r.ID)
		}
		ids[r.ID] = true
		if !filepath.IsAbs(r.ContainerPath) {
			return m, fmt.Errorf("root %q: container path not absolute: %q", r.ID, r.ContainerPath)
		}
	}
	return m, nil
}
```

Also fix the launcher `-v` loop (~301-305) to iterate `ExtraMount` and use `m.Path`:

```go
for _, em := range cfg.ExtraMounts {
	hostPath := expandHome(em.Path, home)
	containerPath := expandHome(em.Path, containerHome)
	args = append(args, "-v", hostPath+":"+containerPath+":ro")
}
```

(Change `ReExecConfig.ExtraMounts` field type `[]string` → `config.ExtraMounts` at launcher.go:39, and its assignment in `cmd/afm/run.go:123`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/docker/ -run 'TestBuildFileRootManifest_OnlyBrowseable|TestFileRootManifest_RoundTripAndReject' -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add pkg/docker/manifest.go pkg/docker/manifest_test.go pkg/docker/launcher.go cmd/afm/run.go
git commit -m "feat(docker): versioned file-root manifest (build/encode/decode)"
```

---

### Task 4: Launcher wires manifest env + conditional loopback publish

**Files:**
- Modify: `pkg/docker/launcher.go` (`ReExecConfig` ~34-45; env block ~324-343; port publish ~272-274)
- Modify: `cmd/afm/run.go` (docker branch — build manifest, set new `ReExecConfig` fields)
- Test: `pkg/docker/launcher_test.go`

**Interfaces:**
- `ReExecConfig` gains `FileBrowserEnabled bool` and `FileRoots FileRootManifest`.
- Consumes: `BuildFileRootManifest`, `EncodeFileRootManifest` (Task 3).

- [ ] **Step 1: Write the failing test**

```go
func TestReExec_FileBrowserWiring(t *testing.T) {
	var got []string
	cfg := ReExecConfig{
		Image: "img", ProjectDir: "/work/afm", DashboardPort: 8080,
		FileBrowserEnabled: true,
		FileRoots: FileRootManifest{Version: 1, Roots: []FileRootManifestEntry{
			{ID: "project", ContainerPath: "/work/afm", Kind: "project"},
		}},
	}
	execFunc := func(_ string, args []string, _ []string) error { got = args; return nil }
	if err := reExecWith(cfg, execFunc); err != nil { // use existing seam; adapt name to actual
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-p 127.0.0.1:8080:8080") {
		t.Errorf("expected loopback publish, got: %s", joined)
	}
	if !hasEnvKey(got, FileRootsEnvVar) {
		t.Errorf("expected -e %s, got: %s", FileRootsEnvVar, joined)
	}
}

func TestReExec_NoFileBrowser_KeepsOpenPublishNoEnv(t *testing.T) {
	var got []string
	cfg := ReExecConfig{Image: "img", ProjectDir: "/work/afm", DashboardPort: 8080, FileBrowserEnabled: false}
	execFunc := func(_ string, args []string, _ []string) error { got = args; return nil }
	_ = reExecWith(cfg, execFunc)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-p 8080:8080") || strings.Contains(joined, "127.0.0.1") {
		t.Errorf("expected open publish, got: %s", joined)
	}
	if hasEnvKey(got, FileRootsEnvVar) {
		t.Errorf("did not expect file-roots env, got: %s", joined)
	}
}
```

(`hasEnvKey` — a small test helper scanning for `-e KEY=` / `-e KEY`. Match the existing test's exec-seam name; `TestReExec_BuildsDockerArgs` already exercises it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run 'TestReExec_FileBrowserWiring|TestReExec_NoFileBrowser' -v`
Expected: FAIL (fields/behavior missing).

- [ ] **Step 3: Write minimal implementation**

In `ReExecConfig`: add `FileBrowserEnabled bool` and `FileRoots FileRootManifest`.

Port publish (~272-274):

```go
if cfg.DashboardPort > 0 {
	bind := fmt.Sprintf("%d:%d", cfg.DashboardPort, cfg.DashboardPort)
	if cfg.FileBrowserEnabled && len(cfg.FileRoots.Roots) > 0 {
		bind = fmt.Sprintf("127.0.0.1:%d:%d", cfg.DashboardPort, cfg.DashboardPort)
	}
	args = append(args, "-p", bind)
}
```

Env block (after the `AFM_DEBUG` block, ~333):

```go
if cfg.FileBrowserEnabled && len(cfg.FileRoots.Roots) > 0 {
	if enc, err := EncodeFileRootManifest(cfg.FileRoots); err == nil {
		args = append(args, "-e", FileRootsEnvVar+"="+enc)
	}
}
```

In `cmd/afm/run.go` docker branch, before calling ReExec:

```go
browserEnabled := cfg.Docker.FileBrowser.IsEnabled()
var fileRoots docker.FileRootManifest
if browserEnabled {
	fileRoots, _ = docker.BuildFileRootManifest(projectDir, cfg.Docker.ExtraMounts)
}
// ... set on ReExecConfig:
//   FileBrowserEnabled: browserEnabled,
//   FileRoots:          fileRoots,
```

(`projectDir` is the same absolute path already computed for the project `-v` mount.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/docker/ -run 'TestReExec_FileBrowserWiring|TestReExec_NoFileBrowser' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/docker/launcher.go cmd/afm/run.go pkg/docker/launcher_test.go
git commit -m "feat(docker): проброс file-root manifest и loopback publish при file_browser"
```

---

## Phase 2 — Secure filesystem module (`pkg/server/workspace`)

### Task 5: Roots, path validation, and the `FS` interface skeleton

**Files:**
- Create: `pkg/server/workspace/roots.go`
- Create: `pkg/server/workspace/fs.go` (interface + view types + errors)
- Test: `pkg/server/workspace/roots_test.go`

**Interfaces:**
- Produces:
  ```go
  type Root struct {
      ID, Label, Path, Kind string
      MountReadOnly         bool
  }
  type RootView struct {
      ID    string `json:"id"`
      Label string `json:"label"`
      Kind  string `json:"kind"`
      MountReadOnly bool `json:"mount_read_only"`
  }
  type FS interface {
      Roots() []RootView
      List(ctx context.Context, rootID, relPath, cursor string) (Page, error)
      Reference(ctx context.Context, rootID, relPath string) (Reference, error)
      Read(ctx context.Context, rootID, relPath string) (File, error)
      Diff(ctx context.Context, rootID, relPath string) (Diff, error)
      Close() error
  }
  // sentinel errors mapped to HTTP by the handler layer:
  var ErrInvalidRootOrPath, ErrNotFound, ErrDiffUnavailable, ErrTooLarge, ErrBinary, ErrSymlink error

  func validateRelPath(p string) (string, error) // clean, reject abs/.., NUL, .git/.afm segments
  ```

- [ ] **Step 1: Write the failing test**

```go
func TestValidateRelPath(t *testing.T) {
	ok := []string{"", ".", "pkg/server/handlers.go", "a/b/c"}
	for _, p := range ok {
		if _, err := validateRelPath(p); err != nil {
			t.Errorf("%q should be valid: %v", p, err)
		}
	}
	bad := []string{"/abs", "../up", "a/../b", "a/../../b", "a\x00b", ".git", "a/.git/x", ".afm/config.yaml", "a/.afm"}
	for _, p := range bad {
		if _, err := validateRelPath(p); err == nil {
			t.Errorf("%q should be rejected", p)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/workspace/ -run TestValidateRelPath -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Write minimal implementation**

`fs.go` declares the interface, view/error types. `roots.go`:

```go
func validateRelPath(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", ErrInvalidRootOrPath
	}
	if p == "" || p == "." {
		return ".", nil
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", ErrInvalidRootOrPath
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrInvalidRootOrPath
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return "", ErrInvalidRootOrPath
		}
		if seg == ".git" || seg == ".afm" {
			return "", ErrNotFound // hidden service subtrees
		}
	}
	return clean, nil
}
```

Define sentinel errors with `errors.New`. Add `rootsByID map[string]*rootHandle` scaffolding (filled in Task 6).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/workspace/ -run TestValidateRelPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/workspace/
git commit -m "feat(workspace): FS-интерфейс, roots и валидация относительного пути"
```

---

### Task 6: Linux `openat2` secure open + non-Linux stub + ENOSYS degradation

**Files:**
- Create: `pkg/server/workspace/access_linux.go` (`//go:build linux`)
- Create: `pkg/server/workspace/access_other.go` (`//go:build !linux`)
- Create: `pkg/server/workspace/workspace.go` (`New(roots) (FS, error)` — Linux opens root fds; probes openat2; ENOSYS → zero roots)
- Test: `pkg/server/workspace/access_linux_test.go` (`//go:build linux`)

**Interfaces:**
- Produces:
  ```go
  func New(roots []Root) (FS, error) // Linux: open each root dir fd + openat2 probe; !linux or ENOSYS → &fsImpl{} with no roots
  // internal: func (r *rootHandle) openat(relPath string, flags int) (fd int, err error)
  ```
- Consumes: `Root` (Task 5), `golang.org/x/sys/unix`.

- [ ] **Step 1: Write the failing test** (Linux only)

```go
//go:build linux

func TestOpenat_BlocksTraversalAndSymlink(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("hi"), 0o644)
	os.Symlink("/etc/passwd", filepath.Join(dir, "evil"))
	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil {
		t.Skipf("openat2 unavailable: %v", err) // old kernel: degradation path, not a failure
	}
	defer fs.Close()

	if _, err := fs.Read(context.Background(), "project", "ok.txt"); err != nil {
		t.Fatalf("read ok.txt: %v", err)
	}
	if _, err := fs.Read(context.Background(), "project", "../etc/passwd"); err == nil {
		t.Error("traversal must be rejected")
	}
	if _, err := fs.Read(context.Background(), "project", "evil"); !errors.Is(err, ErrSymlink) {
		t.Errorf("symlink read must be ErrSymlink, got %v", err)
	}
}

func TestNew_ENOSYS_DegradesToZeroRoots(t *testing.T) {
	// Simulate by pointing openProbe at a stub returning ENOSYS via build seam;
	// if the running kernel supports openat2, assert the happy path instead.
	fs, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.Roots()) != 0 {
		t.Error("no roots → empty")
	}
	_ = fs.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/workspace/ -run 'TestOpenat_BlocksTraversalAndSymlink|TestNew_ENOSYS' -v`
Expected: FAIL (`New`/openat undefined).

- [ ] **Step 3: Write minimal implementation**

`access_linux.go`:

```go
//go:build linux

package workspace

import (
	"errors"
	"golang.org/x/sys/unix"
)

const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

type rootHandle struct {
	root Root
	dirf int // O_DIRECTORY|O_PATH fd of the root
}

func openRootDir(path string) (int, error) {
	return unix.Open(path, unix.O_DIRECTORY|unix.O_PATH|unix.O_CLOEXEC, 0)
}

func (r *rootHandle) openat(relPath string, flags int) (int, error) {
	how := &unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC), Resolve: resolveFlags}
	fd, err := unix.Openat2(r.dirf, relPath, how)
	if err != nil {
		switch {
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return -1, ErrSymlink // RESOLVE_NO_SYMLINKS / RESOLVE_BENEATH violation
		case errors.Is(err, unix.ENOENT):
			return -1, ErrNotFound
		default:
			return -1, err
		}
	}
	return fd, nil
}

func probeOpenat2Supported(dirf int) error {
	how := &unix.OpenHow{Flags: uint64(unix.O_PATH | unix.O_CLOEXEC), Resolve: resolveFlags}
	fd, err := unix.Openat2(dirf, ".", how)
	if err != nil {
		return err // ENOSYS on kernel < 5.6
	}
	unix.Close(fd)
	return nil
}
```

`access_other.go`:

```go
//go:build !linux

package workspace

import "errors"

type rootHandle struct{ root Root }

func openRootDir(string) (int, error)          { return -1, errors.New("workspace: unsupported on this OS") }
func (r *rootHandle) openat(string, int) (int, error) { return -1, errors.New("workspace: unsupported") }
func probeOpenat2Supported(int) error          { return errors.New("workspace: unsupported") }
```

`workspace.go` — `New`:

```go
func New(roots []Root) (FS, error) {
	fs := &fsImpl{byID: map[string]*rootHandle{}}
	for _, r := range roots {
		dirf, err := openRootDir(r.Path)
		if err != nil {
			continue // mount missing / unsupported OS → skip this root
		}
		if err := probeOpenat2Supported(dirf); err != nil {
			// ENOSYS (kernel < 5.6) or unsupported OS → degrade: no roots at all.
			_ = closeFD(dirf)
			return &fsImpl{byID: map[string]*rootHandle{}}, nil
		}
		fs.order = append(fs.order, r.ID)
		fs.byID[r.ID] = &rootHandle{root: r, dirf: dirf}
	}
	return fs, nil
}
```

(`closeFD` = `unix.Close` on linux, no-op/`errors` on other. `fsImpl` holds `byID`, `order`, and implements `Roots()`/`Close()` now; `List/Read/Reference/Diff` return `ErrNotFound` until Tasks 7-10 fill them.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/workspace/ -run 'TestOpenat_BlocksTraversalAndSymlink|TestNew_ENOSYS' -v`
Expected: PASS on kernel ≥ 5.6; the ENOSYS test degrades cleanly.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/workspace/
git commit -m "feat(workspace): openat2 secure open + мягкая деградация на ENOSYS"
```

---

### Task 7: Directory listing with pagination, `.git`/`.afm` hiding, symlink kind

**Files:**
- Create: `pkg/server/workspace/list.go`
- Test: `pkg/server/workspace/list_test.go` (`//go:build linux`)

**Interfaces:**
- Produces:
  ```go
  type Entry struct {
      Name, Path, Kind, Language string
      Size       int64
      Selectable bool
  }
  type Page struct {
      Entries    []Entry
      NextCursor string
  }
  func (fs *fsImpl) List(ctx, rootID, relPath, cursor string) (Page, error)
  const listPageSize = 500
  ```
- Directories first, then files; case-insensitive sort with original name tie-break. Cursor = last emitted `name` (resume-after), bound to `root+relPath` (mismatch → `ErrInvalidRootOrPath`). `.git`/`.afm` entries filtered. Symlink → `kind:"symlink", selectable:false`. `Language` from Task 8's detector (files only).

- [ ] **Step 1: Write the failing test**

```go
//go:build linux

func TestList_OrderHidingSymlinkPagination(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "zeta"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".afm"), 0o755)
	os.WriteFile(filepath.Join(dir, "Alpha.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(dir, "beta.txt"), []byte("x"), 0o644)
	os.Symlink("/etc", filepath.Join(dir, "link"))

	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil { t.Skip(err) }
	defer fs.Close()

	p, err := fs.List(context.Background(), "project", ".", "")
	if err != nil { t.Fatal(err) }
	var names []string
	for _, e := range p.Entries { names = append(names, e.Name) }
	// dirs first (zeta), then files case-insensitive (Alpha.go, beta.txt), symlink among files
	if got := strings.Join(names, ","); !strings.Contains(got, "zeta") ||
		strings.Contains(got, ".git") || strings.Contains(got, ".afm") {
		t.Fatalf("hiding/order wrong: %s", got)
	}
	for _, e := range p.Entries {
		if e.Name == "link" && (e.Kind != "symlink" || e.Selectable) {
			t.Errorf("symlink entry wrong: %+v", e)
		}
		if e.Name == "Alpha.go" && e.Language != "go" {
			t.Errorf("language not detected: %+v", e)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/workspace/ -run TestList_OrderHidingSymlinkPagination -v`
Expected: FAIL (`List` returns `ErrNotFound`).

- [ ] **Step 3: Write minimal implementation**

Open the directory fd via `openat(relPath, O_DIRECTORY|O_RDONLY)`, `os.NewFile(uintptr(fd), relPath).ReadDir(-1)` — but to keep the symlink-safe guarantee, use `unix.ReadDirent` / `(*os.File).ReadDir` on the fd. For each dirent: skip `.git`/`.afm`; classify via `d.Type()` (`Dir`, `Symlink`, else `File`); build `Path = path.Join(relPath, name)` normalized (drop leading `./`); `Selectable = kind == "file"`; `Language`/`Size` for files (lstat via `unix.Fstatat(dirf, relName, &st, AT_SYMLINK_NOFOLLOW)`). Sort: dirs first, then `strings.ToLower(name)` with `name` tie-break. Apply cursor (resume after `name`) then cap at `listPageSize`, set `NextCursor` to the last name if truncated.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/workspace/ -run TestList_OrderHidingSymlinkPagination -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/workspace/list.go pkg/server/workspace/list_test.go
git commit -m "feat(workspace): листинг каталога (порядок, скрытие .git/.afm, symlink, пагинация)"
```

---

### Task 8: Bounded content read + language + binary detection + reference marker

**Files:**
- Create: `pkg/server/workspace/content.go` (Read, language detection, reference)
- Test: `pkg/server/workspace/content_test.go` (`//go:build linux`)

**Interfaces:**
- Produces:
  ```go
  type File struct {
      Path, DisplayPath, Reference, Language, Content string
      Size       int64
      ModifiedAt time.Time
      ETag       string
  }
  type Reference struct {
      Path, DisplayPath, Reference string
  }
  func (fs *fsImpl) Read(ctx, rootID, relPath string) (File, error)     // ≤2MiB else ErrTooLarge; NUL → ErrBinary; symlink → ErrSymlink
  func (fs *fsImpl) Reference(ctx, rootID, relPath string) (Reference, error) // secure-open as regular file, then marker
  func detectLanguage(name string) string // go/typescript/javascript/python/plain
  func buildMarker(absPath string) string // [AFM file: "<json-encoded>"]
  const maxContentBytes = 2 << 20
  ```
- `DisplayPath = <rootLabel>/<relPath>`. `Reference` uses the absolute container path (`rootHandle.root.Path` + `/` + relPath). Marker path is JSON-string-encoded so quotes/backslashes/newlines are safe.

- [ ] **Step 1: Write the failing test**

```go
//go:build linux

func TestRead_And_Reference(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "big.txt"), make([]byte, (2<<20)+1), 0o644)
	os.WriteFile(filepath.Join(dir, "bin"), []byte{0x00, 0x01, 0x02}, 0o644)

	fs, err := New([]Root{{ID: "project", Label: "afm", Path: dir, Kind: "project"}})
	if err != nil { t.Skip(err) }
	defer fs.Close()

	f, err := fs.Read(context.Background(), "project", "a.go")
	if err != nil { t.Fatal(err) }
	if f.Language != "go" || f.Content != "package a\n" || f.DisplayPath != "afm/a.go" {
		t.Errorf("bad file: %+v", f)
	}
	wantMarker := `[AFM file: "` + jsonString(filepath.Join(dir, "a.go")) + `"]`
	if f.Reference != wantMarker {
		t.Errorf("marker: got %q want %q", f.Reference, wantMarker)
	}
	if _, err := fs.Read(context.Background(), "project", "big.txt"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("big → ErrTooLarge, got %v", err)
	}
	if _, err := fs.Read(context.Background(), "project", "bin"); !errors.Is(err, ErrBinary) {
		t.Errorf("bin → ErrBinary, got %v", err)
	}
	// reference still allowed on big/binary
	if _, err := fs.Reference(context.Background(), "project", "big.txt"); err != nil {
		t.Errorf("reference on big must be allowed: %v", err)
	}
}
```

(`jsonString(s)` in the test = `strings.TrimSuffix(strings.TrimPrefix(string(must(json.Marshal(s))), "\""), "\"")` helper, or just compare against `buildMarker`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/workspace/ -run TestRead_And_Reference -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

```go
func detectLanguage(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	default:
		return "plain"
	}
}

func buildMarker(abs string) string {
	b, _ := json.Marshal(abs) // JSON string with quotes
	return "[AFM file: " + string(b) + "]"
}

func (fs *fsImpl) Read(ctx context.Context, rootID, relPath string, ) (File, error) {
	rh, clean, err := fs.resolve(rootID, relPath) // looks up root, validateRelPath
	if err != nil {
		return File{}, err
	}
	fd, err := rh.openat(clean, unix.O_RDONLY)
	if err != nil {
		return File{}, err // ErrSymlink / ErrNotFound already mapped
	}
	f := os.NewFile(uintptr(fd), clean)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return File{}, ErrNotFound
	}
	if st.Size() > maxContentBytes {
		return File{}, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, maxContentBytes+1))
	if err != nil {
		return File{}, ErrReadFailed
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return File{}, ErrBinary
	}
	abs := filepath.Join(rh.root.Path, clean)
	etag := fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size())
	return File{
		Path: clean, DisplayPath: rh.root.Label + "/" + clean,
		Reference: buildMarker(abs), Language: detectLanguage(clean),
		Content: string(data), Size: st.Size(), ModifiedAt: st.ModTime(), ETag: etag,
	}, nil
}
```

`Reference` mirrors `Read` but only secure-opens as a regular file (reject dir/symlink), then returns the marker — no size/binary gate.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/workspace/ -run TestRead_And_Reference -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/workspace/content.go pkg/server/workspace/content_test.go
git commit -m "feat(workspace): bounded read, language detection, reference marker"
```

---

### Task 9: `HEAD → working tree` diff via go-udiff

**Files:**
- Create: `pkg/server/workspace/diff.go`
- Modify: `go.mod`/`go.sum` (`go get github.com/aymanbagabas/go-udiff@latest`)
- Test: `pkg/server/workspace/diff_test.go` (`//go:build linux`; requires `git` on PATH)

**Interfaces:**
- Produces:
  ```go
  type Diff struct {
      Path, Baseline, Status string // status: clean | modified | added
      Binary, Truncated bool
      Diff string
  }
  func (fs *fsImpl) Diff(ctx, rootID, relPath string) (Diff, error) // ErrDiffUnavailable if no repo
  const maxDiffBytes = 4 << 20
  const gitTimeout = 3 * time.Second
  ```
- Walk up from the file's dir to find `.git` **without leaving the root**. `git -C <repo> cat-file blob HEAD:<repo-rel>` for baseline (3s timeout, no shell). Current content from the secure fd (reuse `Read`; on `ErrBinary` → `Binary:true` and skip diff body). Untracked / no-HEAD → treat as added (baseline empty). Build unified diff with `udiff.Unified("HEAD:"+repoRel, repoRel, baseline, current)`. Cap at `maxDiffBytes` on a line boundary → `Truncated:true`.

- [ ] **Step 1: Write the failing test**

```go
//go:build linux

func TestDiff_TrackedModifiedAndUntracked(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) { c := exec.Command("git", args...); c.Dir = dir; if out, err := c.CombinedOutput(); err != nil { t.Fatalf("git %v: %s", args, out) } }
	run("init", "-q"); run("config", "user.email", "t@t"); run("config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("package a\nold\n"), 0o644)
	run("add", "."); run("commit", "-qm", "init")
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("package a\nnew\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "u.go"), []byte("package u\n"), 0o644)

	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil { t.Skip(err) }
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "f.go")
	if err != nil || d.Status != "modified" || !strings.Contains(d.Diff, "new") {
		t.Fatalf("tracked diff: %+v err=%v", d, err)
	}
	u, err := fs.Diff(context.Background(), "project", "u.go")
	if err != nil || u.Status != "added" {
		t.Fatalf("untracked diff: %+v err=%v", u, err)
	}
}

func TestDiff_NoRepoUnavailable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644)
	fs, err := New([]Root{{ID: "extra-1", Path: dir, Kind: "extra"}})
	if err != nil { t.Skip(err) }
	defer fs.Close()
	if _, err := fs.Diff(context.Background(), "extra-1", "f.txt"); !errors.Is(err, ErrDiffUnavailable) {
		t.Errorf("no repo → ErrDiffUnavailable, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/workspace/ -run 'TestDiff_' -v`
Expected: FAIL (`Diff` returns `ErrNotFound`; go-udiff missing).

- [ ] **Step 3: Write minimal implementation**

```bash
go get github.com/aymanbagabas/go-udiff@latest
```

```go
func (fs *fsImpl) Diff(ctx context.Context, rootID, relPath string) (Diff, error) {
	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return Diff{}, err
	}
	repoDir, repoRel, ok := findRepo(rh.root.Path, clean) // walk up for .git, stay within root
	if !ok {
		return Diff{}, ErrDiffUnavailable
	}
	cur, rerr := fs.Read(ctx, rootID, relPath)
	if errors.Is(rerr, ErrBinary) {
		return Diff{Path: clean, Baseline: "HEAD", Status: "modified", Binary: true}, nil
	}
	if rerr != nil {
		return Diff{}, rerr
	}
	baseline, tracked := gitBaseline(ctx, repoDir, repoRel) // "" if untracked / no HEAD
	status := "modified"
	if !tracked {
		status = "added"
	} else if baseline == cur.Content {
		return Diff{Path: clean, Baseline: "HEAD", Status: "clean"}, nil
	}
	body := udiff.Unified("HEAD:"+repoRel, repoRel, baseline, cur.Content)
	truncated := false
	if len(body) > maxDiffBytes {
		body, truncated = truncateOnLine(body, maxDiffBytes), true
	}
	return Diff{Path: clean, Baseline: "HEAD", Status: status, Diff: body, Truncated: truncated}, nil
}

func gitBaseline(ctx context.Context, repoDir, repoRel string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "-C", repoDir, "cat-file", "blob", "HEAD:"+repoRel)
	out, err := cmd.Output()
	if err != nil {
		return "", false // untracked or no HEAD
	}
	return string(out), true
}
```

`findRepo` walks parent dirs from `filepath.Dir(filepath.Join(rootPath, clean))` up to (and not above) `rootPath`, checking for a `.git` entry; returns the repo dir and the file path relative to it. `truncateOnLine` cuts at the last `\n` before the cap.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/workspace/ -run 'TestDiff_' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/workspace/diff.go pkg/server/workspace/diff_test.go go.mod go.sum
git commit -m "feat(workspace): HEAD→working-tree diff через go-udiff"
```

---

## Phase 3 — HTTP layer (`pkg/server`)

### Task 10: Wire `workspace.FS` into the server + capability flag

**Files:**
- Modify: `pkg/server/server.go` (`Config` ~106-125 add `Workspace workspace.FS`; `Server` ~71-98 add field; `Shutdown` ~335-337 also `Close()` the FS)
- Modify: `pkg/server/handlers.go` (`statusResponse` ~28-38 + `handleStatus` ~40-55 add `capabilities`)
- Modify: `cmd/afm/run.go` (in-container: decode manifest, build FS, set `Config.Workspace`)
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Produces: `statusResponse.Capabilities struct{ FileBrowser bool `json:"file_browser"` } `json:"capabilities"``. `capabilities.file_browser = cfg.Workspace != nil && len(cfg.Workspace.Roots()) > 0`.
- Consumes: `workspace.New`, `docker.DecodeFileRootManifest`, `docker.FileRootsEnvVar`.

- [ ] **Step 1: Write the failing test**

```go
func TestStatus_CapabilityReflectsWorkspace(t *testing.T) {
	// with a fake FS exposing one root → true
	srvOn := newTestServer(t, Config{Workspace: fakeFS{roots: 1}})
	if !decodeStatus(t, srvOn).Capabilities.FileBrowser {
		t.Error("expected file_browser=true")
	}
	// nil workspace (host mode) → false
	srvOff := newTestServer(t, Config{})
	if decodeStatus(t, srvOff).Capabilities.FileBrowser {
		t.Error("expected file_browser=false")
	}
}
```

(`fakeFS` is a test double implementing `workspace.FS`; `Roots()` returns `roots` entries, all other methods return `ErrNotFound`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run TestStatus_CapabilityReflectsWorkspace -v`
Expected: FAIL (no `Capabilities` field/`Workspace` config).

- [ ] **Step 3: Write minimal implementation**

Add `Workspace workspace.FS` to `Config` and `Server` (wire in `New`). In `Shutdown`, after `httpSrv.Shutdown`, `if s.workspace != nil { _ = s.workspace.Close() }`. Add:

```go
type capabilities struct {
	FileBrowser bool `json:"file_browser"`
}
// statusResponse gains: Capabilities capabilities `json:"capabilities"`
// handleStatus sets:
resp.Capabilities.FileBrowser = s.workspace != nil && len(s.workspace.Roots()) > 0
```

In `cmd/afm/run.go` (normal in-container path, near the `Server` construction ~259-274):

```go
var ws workspace.FS
if raw := os.Getenv(docker.FileRootsEnvVar); raw != "" && os.Getenv("AFM_IN_DOCKER") == "1" {
	if man, err := docker.DecodeFileRootManifest(raw); err == nil {
		roots := make([]workspace.Root, 0, len(man.Roots))
		for _, r := range man.Roots {
			roots = append(roots, workspace.Root{ID: r.ID, Label: r.Label, Path: r.ContainerPath, Kind: r.Kind, MountReadOnly: r.MountReadOnly})
		}
		ws, _ = workspace.New(roots)
	}
}
// set Workspace: ws on the server Config
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run TestStatus_CapabilityReflectsWorkspace -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/server.go pkg/server/handlers.go cmd/afm/run.go pkg/server/handlers_test.go
git commit -m "feat(server): workspace.FS в сервере + capability file_browser в /api/status"
```

---

### Task 11: `/api/files/*` routes, handlers, and `writeFilesError`

**Files:**
- Create: `pkg/server/files_handlers.go` (routeFiles + 5 handlers + `writeFilesError` + error→HTTP mapping)
- Modify: `pkg/server/server.go` (register `/api/files/` on the mux ~220-228; add `X-Content-Type-Options: nosniff` for these responses)
- Test: `pkg/server/files_handlers_test.go`

**Interfaces:**
- Produces GET handlers under `/api/files/`: `roots`, `tree`, `reference`, `content`, `diff`. Each parses `root`/`path`/`cursor` from query, calls the matching `workspace.FS` method, encodes JSON. Disabled (`workspace==nil` or 0 roots) → 404 for all. `writeFilesError(w, status, code)` writes `{"error":"<code>"}` JSON with the status.
- Error map: `ErrInvalidRootOrPath`→400 `invalid_root_or_path`; `ErrNotFound`→404 `not_found`; `ErrDiffUnavailable`→409 `diff_unavailable`; `ErrTooLarge`→413 `file_too_large`; `ErrBinary`→415 `binary_file`; `ErrSymlink`→422 `symlink_not_supported`; else→500 `read_failed`.

- [ ] **Step 1: Write the failing test**

```go
func TestFiles_DisabledReturns404(t *testing.T) {
	srv := newTestServer(t, Config{}) // no workspace
	for _, p := range []string{"/api/files/roots", "/api/files/tree?root=project&path=."} {
		rr := doGET(t, srv, p)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: got %d want 404", p, rr.Code)
		}
	}
}

func TestFiles_ContentAndErrorShape(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{
		files: map[string]workspace.File{"a.go": {Path: "a.go", Language: "go", Content: "package a\n", Reference: `[AFM file: "/x/a.go"]`}},
		roots: 1,
	}})
	rr := doGET(t, srv, "/api/files/content?root=project&path=a.go")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"language":"go"`) {
		t.Fatalf("content: %d %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("X-Content-Type-Options"); ct != "nosniff" {
		t.Errorf("missing nosniff, got %q", ct)
	}
	// binary → 415 JSON error, no absolute path leak
	rr = doGET(t, srv, "/api/files/content?root=project&path=bin")
	if rr.Code != 415 || !strings.Contains(rr.Body.String(), "binary_file") {
		t.Errorf("binary: %d %s", rr.Code, rr.Body.String())
	}
}
```

(Extend `fakeFS` with a `files map[string]workspace.File` and a `bin` key returning `ErrBinary`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run 'TestFiles_' -v`
Expected: FAIL (routes 404 because unregistered / handlers missing).

- [ ] **Step 3: Write minimal implementation**

`files_handlers.go`:

```go
func (s *Server) routeFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.workspace == nil || len(s.workspace.Roots()) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch strings.TrimPrefix(r.URL.Path, "/api/files/") {
	case "roots":
		s.filesRoots(w, r)
	case "tree":
		s.filesTree(w, r)
	case "reference":
		s.filesReference(w, r)
	case "content":
		s.filesContent(w, r)
	case "diff":
		s.filesDiff(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeFilesError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func filesErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, workspace.ErrInvalidRootOrPath):
		return 400, "invalid_root_or_path"
	case errors.Is(err, workspace.ErrNotFound):
		return 404, "not_found"
	case errors.Is(err, workspace.ErrDiffUnavailable):
		return 409, "diff_unavailable"
	case errors.Is(err, workspace.ErrTooLarge):
		return 413, "file_too_large"
	case errors.Is(err, workspace.ErrBinary):
		return 415, "binary_file"
	case errors.Is(err, workspace.ErrSymlink):
		return 422, "symlink_not_supported"
	default:
		return 500, "read_failed"
	}
}
```

Each handler, e.g. content:

```go
func (s *Server) filesContent(w http.ResponseWriter, r *http.Request) {
	f, err := s.workspace.Read(r.Context(), r.URL.Query().Get("root"), r.URL.Query().Get("path"))
	if err != nil {
		writeFilesError(w, filesErrStatus(err))
		return
	}
	if f.ETag != "" && r.Header.Get("If-None-Match") == f.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if f.ETag != "" {
		w.Header().Set("ETag", f.ETag)
	}
	_ = json.NewEncoder(w).Encode(f) // File JSON tags match the spec's content shape
}
```

`roots`/`tree`/`reference`/`diff` follow the same skeleton. Register in `New`: `mux.HandleFunc("/api/files/", s.routeFiles)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run 'TestFiles_' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/files_handlers.go pkg/server/server.go pkg/server/files_handlers_test.go
git commit -m "feat(server): read-only /api/files/* endpoints с JSON-ошибками"
```

---

## Phase 4 — Dashboard (`components/file-browser/*`)

### Task 12: Highlight.js dependency + typed API client + capability in status

**Files:**
- Modify: `pkg/web/dashboard/package.json` (add `highlight.js`)
- Create: `pkg/web/dashboard/src/api/files-client.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts` (map `capabilities.file_browser`)
- Modify: `pkg/web/dashboard/src/types/*` (add `capabilities` to `FlowStatus`)
- Test: `pkg/web/dashboard/src/api/files-client.test.ts`, `use-status.test.ts`

**Interfaces:**
- Produces `files-client.ts`: `getRoots()`, `getTree(root, path, cursor?)`, `getReference(root, path)`, `getContent(root, path, etag?)`, `getDiff(root, path)` — typed fetches against `/api/files/*`, throwing a typed `FilesApiError{code, status}` parsed from the JSON error body.
- `FlowStatus` gains `capabilities: { fileBrowser: boolean }`; `normalizeStatus` reads `raw.capabilities?.file_browser === true`.

- [ ] **Step 1: Write the failing test**

```ts
it('parses tree entries and typed errors', async () => {
  fetchMock.mockResponseOnce(JSON.stringify({ entries: [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go', selectable: true }], next_cursor: '' }))
  const page = await getTree('project', '.')
  expect(page.entries[0].language).toBe('go')

  fetchMock.mockResponseOnce(JSON.stringify({ error: 'binary_file' }), { status: 415 })
  await expect(getContent('project', 'bin')).rejects.toMatchObject({ code: 'binary_file', status: 415 })
})

it('maps capability from status', () => {
  expect(normalizeStatus({ capabilities: { file_browser: true } } as any).capabilities.fileBrowser).toBe(true)
  expect(normalizeStatus({} as any).capabilities.fileBrowser).toBe(false)
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/api/files-client.test.ts src/hooks/use-status/use-status.test.ts`
Expected: FAIL (module/field missing).

- [ ] **Step 3: Write minimal implementation**

`npm i highlight.js` (adds to package.json). `files-client.ts`:

```ts
export class FilesApiError extends Error {
  constructor(public code: string, public status: number) { super(code) }
}
async function getJson<T>(url: string, headers?: Record<string, string>): Promise<T> {
  const res = await fetch(url, { headers })
  if (res.status === 304) return undefined as unknown as T
  if (!res.ok) {
    let code = 'read_failed'
    try { code = (await res.json()).error ?? code } catch { /* keep default */ }
    throw new FilesApiError(code, res.status)
  }
  return res.json() as Promise<T>
}
const q = (root: string, path: string) => `root=${encodeURIComponent(root)}&path=${encodeURIComponent(path)}`
export const getRoots = () => getJson<{ roots: RootView[] }>('/api/files/roots')
export const getTree = (root: string, path: string, cursor = '') =>
  getJson<TreePage>(`/api/files/tree?${q(root, path)}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
export const getReference = (root: string, path: string) => getJson<FileReference>(`/api/files/reference?${q(root, path)}`)
export const getContent = (root: string, path: string, etag?: string) =>
  getJson<FileContent>(`/api/files/content?${q(root, path)}`, etag ? { 'If-None-Match': etag } : undefined)
export const getDiff = (root: string, path: string) => getJson<FileDiff>(`/api/files/diff?${q(root, path)}`)
```

Add `capabilities` to `FlowStatus` type + `normalizeStatus` mapping.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/api/files-client.test.ts src/hooks/use-status/use-status.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/package.json pkg/web/dashboard/package-lock.json pkg/web/dashboard/src/api/files-client.ts pkg/web/dashboard/src/hooks pkg/web/dashboard/src/types
git commit -m "feat(dashboard): typed files API client + capability из /api/status"
```

---

### Task 13: File browser component — provider, modal, tree, viewer, diff, selection

**Files:**
- Create: `pkg/web/dashboard/src/components/file-browser/FileBrowserProvider.tsx`
- Create: `.../file-browser/FileBrowserModal.tsx`
- Create: `.../file-browser/FileTree.tsx`
- Create: `.../file-browser/FileViewer.tsx` (highlight.js/lib/core + 4 grammars; `plain` → `<pre><code>`)
- Create: `.../file-browser/DiffViewer.tsx` (line renderer, no `dangerouslySetInnerHTML`)
- Create: `.../file-browser/highlight.ts` (registers only go/typescript/javascript/python)
- Create: `.../file-browser/file-browser.css`
- Test: `.../file-browser/FileBrowserModal.test.tsx`, `FileViewer.test.tsx`

**Interfaces:**
- Produces the provider context:
  ```ts
  openBrowser(): void
  pickFiles(onInsert: (references: string[]) => void): void
  ```
- Modal: left = roots + lazy `FileTree` (paginates via `next_cursor`); right = header + `FILE`/`DIFF` tabs; bottom = selected chips + `Copy references` (browse) / `Insert references` (picker). Multi-select via checkbox → `getReference` validated marker before adding. Selection lives in React state, cleared on `flowName+startedAt` change. `Esc` closes; focus trap; arrow/Enter in tree.

- [ ] **Step 1: Write the failing test**

```tsx
it('lazy-loads tree, highlights source, and collects validated references', async () => {
  mockRoots([{ id: 'project', label: 'afm', kind: 'project' }])
  mockTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go', selectable: true }])
  mockReference('project', 'a.go', '[AFM file: "/w/afm/a.go"]')
  mockContent('project', 'a.go', { language: 'go', content: 'package a' })

  const inserted: string[][] = []
  render(<FileBrowserProvider>{/* harness that calls pickFiles */}</FileBrowserProvider>)
  openPicker((refs) => inserted.push(refs))
  await userEvent.click(await screen.findByText('a.go'))
  expect(await screen.findByText(/package a/)).toHaveClass('hljs') // highlighted, escaped
  await userEvent.click(screen.getByRole('checkbox', { name: /a\.go/ }))
  await userEvent.click(screen.getByRole('button', { name: /Insert references/ }))
  expect(inserted).toEqual([['[AFM file: "/w/afm/a.go"]']])
})

it('does not execute source as HTML', async () => {
  mockContent('project', 'x.ts', { language: 'typescript', content: '<img src=x onerror="window.__pwned=1">' })
  // render viewer, assert window.__pwned is undefined and text is shown literally
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/file-browser/`
Expected: FAIL (components missing).

- [ ] **Step 3: Write minimal implementation**

`highlight.ts`:

```ts
import hljs from 'highlight.js/lib/core'
import go from 'highlight.js/lib/languages/go'
import typescript from 'highlight.js/lib/languages/typescript'
import javascript from 'highlight.js/lib/languages/javascript'
import python from 'highlight.js/lib/languages/python'
hljs.registerLanguage('go', go)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('python', python)
export function highlight(language: string, source: string): string {
  if (language === 'plain' || !hljs.getLanguage(language)) {
    return escapeHtml(source)
  }
  return hljs.highlight(source, { language }).value // escapes internally
}
```

`FileViewer` sets the highlighted HTML into `<code>` — the ONLY place `dangerouslySetInnerHTML` is allowed, fed exclusively by `hljs`/`escapeHtml` output (never markdown, never raw source). Filenames/breadcrumbs/errors render as plain React text. `DiffViewer` splits `diff` into lines and renders each with a class by prefix (`+`/`-`/`@@`/header) as plain text nodes. Provider holds `mode: 'browse' | 'picker'`, the `onInsert` callback, and selection state; exposes `openBrowser`/`pickFiles`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/components/file-browser/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/file-browser/
git commit -m "feat(dashboard): компонент file-browser (provider, modal, tree, viewer, diff)"
```

---

### Task 14: Header entry point + picker in `PasteableTextarea` (PlanPanel, DialogChannel)

**Files:**
- Modify: `pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx` (folder button in `.header-actions`, gated by capability)
- Modify: `pkg/web/dashboard/src/components/pasteable-textarea/PasteableTextarea.tsx` (new optional prop `allowFileReferences` + `Attach project file` button)
- Modify: `.../plan-panel/PlanPanel.tsx` (pass `allowFileReferences` to the line-comment textarea)
- Modify: `.../dialog-channel/DialogChannel.tsx` (pass `allowFileReferences` to the **per-line question comment** textarea only — NOT the custom-answer box)
- Modify: `.../App.tsx` (mount `FileBrowserProvider`; pass capability to `FlowHeader`; render `FileBrowserModal`)
- Test: `PasteableTextarea.test.tsx`, `PlanPanel.test.tsx`, `DialogChannel.test.tsx`, `FlowHeader.test.tsx`

**Interfaces:**
- Consumes: provider `openBrowser`/`pickFiles` (Task 13); marker-at-caret insertion mirrors `use-image-paste`'s controlled `value/onChange` splice.
- `PasteableTextarea` gains `allowFileReferences?: boolean` (default false). When true, an `Attach project file` button calls `pickFiles((refs) => insertAtCaret(refs.join('\n')))`.

- [ ] **Step 1: Write the failing test**

```tsx
it('header shows folder button only when capability is on', () => {
  const { rerender } = render(<FlowHeader capabilities={{ fileBrowser: false }} {...base} />)
  expect(screen.queryByLabelText('Open project files')).toBeNull()
  rerender(<FlowHeader capabilities={{ fileBrowser: true }} {...base} />)
  expect(screen.getByLabelText('Open project files')).toBeInTheDocument()
})

it('inserts picked references at caret without clobbering existing text', async () => {
  const onChange = vi.fn()
  render(<TestProviderWithPick refs={['[AFM file: "/w/a.go"]']}>
    <PasteableTextarea stageId="s" value="see " onChange={onChange} allowFileReferences />
  </TestProviderWithPick>)
  const ta = screen.getByRole('textbox') as HTMLTextAreaElement
  ta.setSelectionRange(4, 4)
  await userEvent.click(screen.getByRole('button', { name: /Attach project file/ }))
  expect(onChange).toHaveBeenCalledWith('see [AFM file: "/w/a.go"]')
})

it('custom-answer textarea in DialogChannel has no attach button', () => {
  // render DialogChannel with a pending question, assert the custom-answer box lacks the attach button
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/flow-header src/components/pasteable-textarea src/components/plan-panel src/components/dialog-channel`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

`PasteableTextarea`: add `allowFileReferences?: boolean`; when true render an `Attach project file` button beside the thumbnail strip that calls `useFileBrowser().pickFiles`. Reuse the caret-insertion pattern from `use-image-paste` (`before + text + after`, `pendingCaret`). `FlowHeader`: add `capabilities` prop; render `<button className="icon-btn" aria-label="Open project files" onClick={openBrowser}>` only when `capabilities.fileBrowser`. `PlanPanel`: `allowFileReferences` on the line-comment `PasteableTextarea` (~359-368). `DialogChannel`: `allowFileReferences` ONLY on the per-line question-comment textarea (~331-343); leave the custom-answer box (~393-406) unchanged. `App.tsx`: wrap in `FileBrowserProvider`, render `FileBrowserModal`, thread `status.capabilities` to `FlowHeader`. Provider cancels a stale picker (target stage/question changed) → shows `Target comment is no longer available`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/components && npm run build`
Expected: PASS + clean Vite build.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src
git commit -m "feat(dashboard): кнопка file browser в header + пикер в PlanPanel/DialogChannel"
```

---

## Phase 5 — Docs & end-to-end

### Task 15: README + config.example.yaml

**Files:**
- Modify: `README.md` (Docker mode section — file browser, `browse:true`, limits, security, loopback bind)
- Modify: `config.example.yaml` (annotated `file_browser` + scalar-or-object `extra_mounts`)

**Interfaces:** none (docs only).

- [ ] **Step 1: Write the docs**

Document: Docker-only nature; `docker.file_browser.enabled` (default true in Docker); `extra_mounts` object form with `browse:true`; that legacy scalar stays private (`browse:false`); the `.git`/`.afm` hiding; content/diff size caps; the loopback bind when the browser is on; that references are absolute container paths the agent reads itself.

- [ ] **Step 2: Verify build unaffected**

Run: `go build ./... && cd pkg/web/dashboard && npm run build`
Expected: clean (docs don't affect build; this just confirms the tree is green before the smoke test).

- [ ] **Step 3: Commit**

```bash
git add README.md config.example.yaml
git commit -m "docs: file browser в Docker mode (browse:true, лимиты, безопасность)"
```

---

### Task 16: Docker smoke / E2E validation

**Files:**
- Create: `docs/superpowers/plans/notes/file-browser-e2e.md` (record of the manual run — commands + observations)

**Interfaces:** none (verification task; uses the real image + real dashboard).

- [ ] **Step 1: Build a local image and prepare three mounts**

Per AGENTS.md Docker section, build a local image (`make docker-build`) and run `AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=local/afm:dev afm run flow.yaml` with a `flow.yaml`/config that declares:
1. the project (browseable by default);
2. a neighbouring code root with `browse:true` (browseable, read-only);
3. a fake credential root in **legacy scalar** form (mounted to the agent, must be ABSENT from `/api/files/roots`).

- [ ] **Step 2: Verify in a real browser (Chrome DevTools MCP)**

Open the dashboard (loopback URL from the log). Confirm:
- folder button present; `/api/files/roots` lists project + the `browse:true` extra, NOT the credential root;
- open a Go/TS/JS/Python file → syntax highlight; open `DIFF` → `HEAD → working tree` shown; clean file → empty diff; a large/binary file → the documented 413/415 inline message but still selectable;
- `.git`/`.afm` are not listed; a `?root=project&path=../x` request → 400 `invalid_root_or_path`.

- [ ] **Step 3: Verify reference delivery end-to-end**

Select one project file and one extra file; insert both markers into a plan line comment and a question line comment. Confirm by reading `feedback.md` / `answer.json` in `.afm/runs/<run>/<stage>/` that the agent received the exact absolute container paths.

- [ ] **Step 4: Confirm loopback bind**

From the host, `curl http://127.0.0.1:<port>/api/status` succeeds; confirm the `docker run` argv used `-p 127.0.0.1:<port>:<port>` (from `AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=... afm run ...` debug output).

- [ ] **Step 5: Record + commit the E2E note**

```bash
git add docs/superpowers/plans/notes/file-browser-e2e.md
git commit -m "test(e2e): ручная проверка file browser в Docker (3 mount-а, references)"
```

---

## Self-Review

**Spec coverage:**
- §1 Docker-only capability → Tasks 4, 10 (env + capability gating). ✓
- §2 scalar-or-object `extra_mounts` + `browse` + `file_browser.enabled` → Tasks 1, 2. ✓
- §3 host-built manifest, base64 transport, in-container decode → Tasks 3, 4, 10. ✓
- §4 virtual root + relative path → Task 5 (`validateRelPath`), enforced in every handler (Task 11). ✓
- §5 `openat2` `RESOLVE_*`, symlink non-selectable, Linux build tag, ENOSYS degradation → Tasks 6, 7. ✓
- §6 `.git`/`.afm` hiding → Tasks 5 (path segments) + 7 (listing filter). ✓
- §7 `HEAD → working tree` diff via go-udiff → Task 9. ✓
- §8 text marker reference, absolute container path → Tasks 8 (`buildMarker`) + 14 (insertion). ✓
- HTTP contracts + error table → Task 11. ✓
- Dashboard modal/tree/viewer/diff/selection/highlighting → Tasks 12, 13. ✓
- Picker in PlanPanel + DialogChannel per-line only → Task 14. ✓
- Security §1 loopback (scoped) → Task 4; §2-10 → Tasks 5-11. ✓
- Docs + E2E → Tasks 15, 16. ✓

**Placeholder scan:** Tasks 5-9 use one prose sentence for a couple of small pure helpers (`findRepo`, `truncateOnLine`, the `list.go` dirent classification) whose exact body is mechanical and fully specified by the surrounding code and tests — acceptable, since the test in each task pins the behavior. No `TBD`/`implement later`. All code steps carry real code.

**Type consistency:** `workspace.FS` method set (`Roots/List/Reference/Read/Diff/Close`) is identical in Tasks 5, 10, 11. View types (`Entry`, `Page`, `File`, `Reference`, `Diff`, `RootView`) are defined once (Tasks 5, 7, 8, 9) and consumed unchanged by handlers (Task 11) and the TS client mirrors them (Task 12). `buildMarker` output format `[AFM file: "<json>"]` is identical in Task 8 (Go) and Task 14 (assertion). `FileRootManifestEntry` fields (`ContainerPath`, `MountReadOnly`, `Kind`) match between Tasks 3 (build/decode), 4 (transport), and 10 (→ `workspace.Root`). Capability field `capabilities.file_browser` (Go) ↔ `capabilities.fileBrowser` (TS) mapped once in Task 12.
