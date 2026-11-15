package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newPackage(t *testing.T) (string, Manifest) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, ManifestFile, validManifest)
	writeFile(t, root, "flow.yaml", "name: f\nstages: []\n")
	writeFile(t, root, "agents.yaml", "{}\n")
	writeFile(t, root, "skills/a/SKILL.md", "skill")
	writeFile(t, root, "scripts/run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(root, "scripts/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "extra/not-declared.txt", "x")
	writeFile(t, root, "skills/.git/config", "git internals")
	m, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, m
}

func cacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { _ = removeTree(dir) })
	return dir
}

func TestBuildSnapshotCopiesOnlyDeclaredResources(t *testing.T) {
	root, m := newPackage(t)
	cache := cacheDir(t)
	snap, err := BuildSnapshot(root, m, cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pipeline.yaml", "flow.yaml", "agents.yaml", "skills/a/SKILL.md", "scripts/run.sh"} {
		if _, err := os.Stat(filepath.Join(snap.Dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	for _, absent := range []string{"extra", "skills/.git"} {
		if _, err := os.Lstat(filepath.Join(snap.Dir, absent)); err == nil {
			t.Errorf("%s must not be copied", absent)
		}
	}
	for _, dir := range []string{".", "skills", "skills/a", "scripts"} {
		info, err := os.Stat(filepath.Join(snap.Dir, dir))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o555 {
			t.Errorf("directory %s mode = %v", dir, info.Mode().Perm())
		}
	}
	if info, _ := os.Stat(filepath.Join(snap.Dir, "scripts/run.sh")); info.Mode().Perm() != 0o555 {
		t.Errorf("exec script mode = %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Join(snap.Dir, "flow.yaml")); info.Mode().Perm() != 0o444 {
		t.Errorf("flow mode = %v", info.Mode().Perm())
	}
	if filepath.Base(snap.Dir) != snap.SHA256 || filepath.Dir(snap.Dir) != SnapshotsRoot(cache) {
		t.Errorf("snapshot dir = %s", snap.Dir)
	}
	if sum, err := VerifySnapshot(snap.Dir); err != nil || sum != snap.SHA256 {
		t.Errorf("verify = %q, %v", sum, err)
	}
}

func TestSnapshotIgnoresLaterSourceEdits(t *testing.T) {
	root, m := newPackage(t)
	snap, err := BuildSnapshot(root, m, cacheDir(t))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "skills/a/SKILL.md", "edited")
	got, err := os.ReadFile(filepath.Join(snap.Dir, "skills/a/SKILL.md"))
	if err != nil || string(got) != "skill" {
		t.Fatalf("snapshot changed: %q %v", got, err)
	}
	if _, err := VerifySnapshot(snap.Dir); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSnapshotIsReusedForSameContent(t *testing.T) {
	root, m := newPackage(t)
	cache := cacheDir(t)
	first, err := BuildSnapshot(root, m, cache)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildSnapshot(root, m, cache)
	if err != nil || second != first {
		t.Fatalf("second = %+v, %v; first = %+v", second, err, first)
	}
	entries, _ := os.ReadDir(SnapshotsRoot(cache))
	if len(entries) != 1 {
		t.Fatalf("staging directories left behind: %v", entries)
	}
	writeFile(t, root, "skills/a/SKILL.md", "edited")
	third, err := BuildSnapshot(root, m, cache)
	if err != nil || third.SHA256 == first.SHA256 {
		t.Fatalf("changed content must give a new snapshot: %+v %v", third, err)
	}
}

func TestBuildSnapshotConcurrent(t *testing.T) {
	root, m := newPackage(t)
	cache := cacheDir(t)
	var wg sync.WaitGroup
	results := make([]Snapshot, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = BuildSnapshot(root, m, cache)
		})
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != results[0] {
			t.Fatalf("build %d: %+v %v (first %+v)", i, results[i], errs[i], results[0])
		}
	}
	if _, err := VerifySnapshot(results[0].Dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(SnapshotsRoot(cache)); len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
}

func TestBuildSnapshotRejectsSymlinksAndSpecialFiles(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "secret", "s")
	cases := []struct {
		name  string
		setup func(root string) error
		want  string
	}{
		{"file symlink in resource dir", func(root string) error {
			return os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "skills/a/link"))
		}, "symlink"},
		{"directory symlink resource", func(root string) error {
			return os.Symlink(outside, filepath.Join(root, "skills/outside"))
		}, "symlink"},
		{"symlinked resource root", func(root string) error {
			if err := os.RemoveAll(filepath.Join(root, "scripts")); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(root, "scripts"))
		}, "symlink"},
		{"symlinked entrypoint", func(root string) error {
			if err := os.Remove(filepath.Join(root, "flow.yaml")); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "flow.yaml"))
		}, "symlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, m := newPackage(t)
			if err := tc.setup(root); err != nil {
				t.Fatal(err)
			}
			cache := cacheDir(t)
			if _, err := BuildSnapshot(root, m, cache); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if entries, _ := os.ReadDir(SnapshotsRoot(cache)); len(entries) != 0 {
				t.Fatalf("failed build left %v", entries)
			}
		})
	}
}

func TestBuildSnapshotRejectsMissingResource(t *testing.T) {
	root, m := newPackage(t)
	m.Resources = append(m.Resources, "nope")
	if _, err := BuildSnapshot(root, m, cacheDir(t)); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildSnapshotRejectsDirectoryEntrypoint(t *testing.T) {
	root, m := newPackage(t)
	m.Entrypoint = "skills"
	if _, err := BuildSnapshot(root, m, cacheDir(t)); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifySnapshotDetectsTampering(t *testing.T) {
	root, m := newPackage(t)
	snap, err := BuildSnapshot(root, m, cacheDir(t))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(snap.Dir, "flow.yaml")
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySnapshot(snap.Dir); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	// A damaged snapshot is never reused silently.
	if _, err := BuildSnapshot(root, m, filepath.Dir(filepath.Dir(snap.Dir))); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("rebuild err = %v", err)
	}
}

func TestVerifySnapshotRejectsAddedSymlink(t *testing.T) {
	root, m := newPackage(t)
	snap, err := BuildSnapshot(root, m, cacheDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(snap.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(snap.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySnapshot(snap.Dir); err == nil {
		t.Fatal("expected verification to fail")
	}
}

func TestSnapshotHashCoversExecBit(t *testing.T) {
	root, m := newPackage(t)
	first, err := BuildSnapshot(root, m, cacheDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "scripts/run.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := BuildSnapshot(root, m, cacheDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 == second.SHA256 {
		t.Fatal("the exec bit must change the hash")
	}
}

// onOpenComponent runs fn just before each path element is opened, for this test only.
func onOpenComponent(t *testing.T, fn func(rel, prefix string)) {
	t.Helper()
	t.Cleanup(func() { beforeOpenComponent = func(string, string) {} })
	beforeOpenComponent = fn
}

// linkSkills moves root/skills aside and puts a symlink to target in its place; unlinkSkills undoes it.
func linkSkills(t *testing.T, root, target string) {
	t.Helper()
	if err := os.Rename(filepath.Join(root, "skills"), filepath.Join(root, "skills.real")); err != nil {
		t.Error(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "skills")); err != nil {
		t.Error(err)
	}
}

func unlinkSkills(t *testing.T, root string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, "skills")); err != nil {
		t.Error(err)
	}
	if err := os.Rename(filepath.Join(root, "skills.real"), filepath.Join(root, "skills")); err != nil {
		t.Error(err)
	}
}

func requireNoSnapshots(t *testing.T, cache string) {
	t.Helper()
	if entries, _ := os.ReadDir(SnapshotsRoot(cache)); len(entries) != 0 {
		t.Fatalf("failed build left %v", entries)
	}
}

func TestBuildSnapshotRefusesAncestorLinkedBeforeItIsOpened(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "a/SKILL.md", "outside-secret")
	cases := []struct{ name, target string }{
		{"link outside the source", outside},
		{"relative link inside the source", "private"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, m := newPackage(t)
			writeFile(t, root, "private/a/SKILL.md", "undeclared-secret")
			linked := false
			onOpenComponent(t, func(rel, prefix string) {
				if rel == "skills/a/SKILL.md" && prefix == "skills" && !linked {
					linkSkills(t, root, tc.target)
					linked = true
				}
			})
			cache := cacheDir(t)
			_, err := BuildSnapshot(root, m, cache)
			if !linked {
				t.Fatal("the copy never reached the swapped ancestor")
			}
			if err == nil || !strings.Contains(err.Error(), "skills is a symlink") {
				t.Fatalf("err = %v, want a symlink refusal", err)
			}
			requireNoSnapshots(t, cache)
		})
	}
}

// The link is in place for every lookup below skills and gone whenever skills itself is looked up, so while one file
// is copied it is swapped in, out and in again; a recheck of the path cannot tell.
func TestBuildSnapshotCopiesOnlyDeclaredBytesWhileAncestorFlips(t *testing.T) {
	root, m := newPackage(t)
	writeFile(t, root, "private/a/SKILL.md", "undeclared-secret")
	linked, linkedDuringLookup := false, false
	onOpenComponent(t, func(rel, prefix string) {
		if rel != "skills/a/SKILL.md" {
			return
		}
		if prefix == "skills" && linked {
			unlinkSkills(t, root)
			linked = false
		} else if prefix != "skills" && !linked {
			linkSkills(t, root, "private")
			linked, linkedDuringLookup = true, true
		}
	})
	cache := cacheDir(t)
	snap, err := BuildSnapshot(root, m, cache)
	if !linkedDuringLookup {
		t.Fatal("the attack never swapped skills during a lookup")
	}
	if err != nil {
		requireNoSnapshots(t, cache)
		return
	}
	got, err := os.ReadFile(filepath.Join(snap.Dir, "skills/a/SKILL.md"))
	if err != nil || string(got) != "skill" {
		t.Fatalf("snapshot holds %q (%v), want only the declared bytes", got, err)
	}
}

// The walk stats skills and then lists it; a link swapped in between must not make it list another directory.
func TestBuildSnapshotRefusesDirectoryListedThroughSwappedLink(t *testing.T) {
	root, m := newPackage(t)
	if err := os.MkdirAll(filepath.Join(root, "private/undeclared-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	opens := 0
	onOpenComponent(t, func(rel, prefix string) {
		if rel == "skills" && prefix == "skills" {
			if opens++; opens == 2 {
				linkSkills(t, root, "private")
			}
		}
	})
	cache := cacheDir(t)
	snap, err := BuildSnapshot(root, m, cache)
	if opens < 2 {
		t.Fatal("the walk never listed skills after the swap")
	}
	if err == nil {
		_, statErr := os.Stat(filepath.Join(snap.Dir, "skills/undeclared-empty"))
		t.Fatalf("snapshot built from the swapped listing (undeclared-empty copied: %t)", statErr == nil)
	}
	if !strings.Contains(err.Error(), "skills is a symlink") {
		t.Fatalf("err = %v, want a symlink refusal", err)
	}
	requireNoSnapshots(t, cache)
}

func TestBuildSnapshotRefusesFileSwappedForLinkBeforeItIsOpened(t *testing.T) {
	root, m := newPackage(t)
	writeFile(t, root, "private/a/SKILL.md", "undeclared-secret")
	leaf := filepath.Join(root, "skills/a/SKILL.md")
	swapped := false
	onOpenComponent(t, func(rel, prefix string) {
		if rel == "skills/a/SKILL.md" && prefix == rel && !swapped {
			swapped = true
			if err := os.Remove(leaf); err != nil {
				t.Error(err)
			}
			if err := os.Symlink("../../private/a/SKILL.md", leaf); err != nil {
				t.Error(err)
			}
		}
	})
	cache := cacheDir(t)
	_, err := BuildSnapshot(root, m, cache)
	if !swapped {
		t.Fatal("the copy never opened the swapped file")
	}
	if err == nil || !strings.Contains(err.Error(), "SKILL.md is a symlink") {
		t.Fatalf("err = %v, want a symlink refusal", err)
	}
	requireNoSnapshots(t, cache)
}
