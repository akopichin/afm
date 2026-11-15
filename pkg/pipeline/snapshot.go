package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	snapshotsDir = "snapshots"
	// Caps keep a hostile package from filling the disk through the cache.
	maxSnapshotFiles = 50_000
	maxSnapshotBytes = 256 << 20
)

// Snapshot is an immutable copy of the declared package resources, stored under its content hash.
type Snapshot struct {
	Dir    string `json:"dir"`
	SHA256 string `json:"sha256"`
}

// SnapshotsRoot is where snapshots live inside the pipelines cache root.
func SnapshotsRoot(cacheRoot string) string { return filepath.Join(cacheRoot, snapshotsDir) }

type snapshotEntry struct {
	rel  string // slash-separated path inside the package
	dir  bool
	exec bool
}

// BuildSnapshot copies the manifest's resources (plus manifest, entrypoint and agents) from srcRoot into
// <cacheRoot>/snapshots/<sha256>. An existing snapshot with the same content is reused, never rewritten.
func BuildSnapshot(srcRoot string, m Manifest, cacheRoot string) (Snapshot, error) {
	src, err := os.OpenRoot(srcRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open package %s: %w", srcRoot, err)
	}
	defer src.Close()
	entries, err := collectEntries(src, m)
	if err != nil {
		return Snapshot{}, err
	}
	root := SnapshotsRoot(cacheRoot)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot cache: %w", err)
	}
	tmp, err := os.MkdirTemp(root, ".tmp-")
	if err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = removeTree(tmp)
		}
	}()
	if err := copyEntries(src, tmp, entries); err != nil {
		return Snapshot{}, err
	}
	// Hashing the staged copy, not the source, makes the hash describe exactly what is published even if the
	// source changes while it is being copied.
	sum, err := hashTree(tmp)
	if err != nil {
		return Snapshot{}, err
	}
	final := filepath.Join(root, sum)
	snap := Snapshot{Dir: final, SHA256: sum}
	if _, err := os.Lstat(final); err == nil {
		return snap, reuseExisting(snap)
	}
	if err := makeReadOnly(tmp); err != nil {
		return Snapshot{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		// A concurrent builder published the same content first.
		if _, statErr := os.Lstat(final); statErr == nil {
			return snap, reuseExisting(snap)
		}
		return Snapshot{}, fmt.Errorf("publish snapshot: %w", err)
	}
	published = true
	return snap, os.Chmod(final, 0o555)
}

func reuseExisting(snap Snapshot) error {
	if _, err := VerifySnapshot(snap.Dir); err != nil {
		return fmt.Errorf("existing snapshot is damaged (remove %s to rebuild it): %w", snap.Dir, err)
	}
	return os.Chmod(snap.Dir, 0o555)
}

// VerifySnapshot recomputes the content hash of dir and checks that it is the directory's own name.
func VerifySnapshot(dir string) (string, error) {
	sum, err := hashTree(dir)
	if err != nil {
		return "", err
	}
	if filepath.Base(dir) != sum {
		return sum, fmt.Errorf("snapshot %s: content hash %s does not match the directory name", dir, sum)
	}
	return sum, nil
}

// collectEntries resolves the declared resources to a sorted, de-duplicated list, rejecting anything that is not a
// plain file or directory reached without a symlink.
func collectEntries(src *os.Root, m Manifest) ([]snapshotEntry, error) {
	declared := []string{ManifestFile, m.Entrypoint}
	if m.Agents != "" {
		declared = append(declared, m.Agents)
	}
	declared = append(declared, m.Resources...)

	found := map[string]snapshotEntry{}
	for _, rel := range declared {
		if err := checkPackagePath("resource", rel); err != nil {
			return nil, err
		}
		err := fs.WalkDir(noFollowFS{src}, path.Clean(rel), func(relPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Name() == ".git" {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				return fmt.Errorf("resource %s is a symlink; symlinks are not supported in a package", relPath)
			case info.IsDir():
				found[relPath] = snapshotEntry{rel: relPath, dir: true}
			case info.Mode().IsRegular():
				found[relPath] = snapshotEntry{rel: relPath, exec: info.Mode()&0o111 != 0}
			default:
				return fmt.Errorf("resource %s is not a regular file or directory", relPath)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("package resource %q: %w", rel, err)
		}
	}
	for _, rel := range append([]string{ManifestFile, m.Entrypoint}, m.Agents) {
		if e, ok := found[rel]; rel != "" && (!ok || e.dir) {
			return nil, fmt.Errorf("package file %q must be a regular file", rel)
		}
	}
	if len(found) > maxSnapshotFiles {
		return nil, fmt.Errorf("package has more than %d entries", maxSnapshotFiles)
	}
	entries := make([]snapshotEntry, 0, len(found))
	for _, e := range found {
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b snapshotEntry) int { return strings.Compare(a.rel, b.rel) })
	return entries, nil
}

func copyEntries(src *os.Root, dst string, entries []snapshotEntry) error {
	var total int64
	for _, e := range entries {
		target := filepath.Join(dst, filepath.FromSlash(e.rel))
		if e.dir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("stage %s: %w", e.rel, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("stage %s: %w", e.rel, err)
		}
		n, err := copyFile(src, e.rel, target, e.exec, maxSnapshotBytes-total)
		if err != nil {
			return fmt.Errorf("stage %s: %w", e.rel, err)
		}
		total += n
	}
	return nil
}

// copyFile copies a regular file of at most budget bytes, refusing a link or special file put in its place after the walk.
func copyFile(src *os.Root, rel, dst string, exec bool, budget int64) (int64, error) {
	in, err := openNoFollow(src, rel)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("not a regular file")
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, budget+1))
	if err := out.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr == nil && n > budget {
		copyErr = fmt.Errorf("package is larger than %d bytes", maxSnapshotBytes)
	}
	if copyErr == nil {
		copyErr = os.Chmod(dst, mode) // not subject to umask
	}
	return n, copyErr
}

// hashTree hashes (path, type, exec bit, size, bytes) of every entry in sorted order. A snapshot containing a link
// or special file fails verification, including one swapped in after the walk listed a regular file.
func hashTree(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("hash snapshot %s: %w", dir, err)
	}
	defer root.Close()
	h := sha256.New()
	var files int
	err = fs.WalkDir(noFollowFS{root}, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if files++; files > maxSnapshotFiles {
			return fmt.Errorf("more than %d entries", maxSnapshotFiles)
		}
		switch {
		case info.IsDir():
			_, err = fmt.Fprintf(h, "%s\x00d\x000\x000\x00", rel)
			return err
		case info.Mode().IsRegular():
			return hashFile(h, root, rel)
		default:
			return fmt.Errorf("%s is not a regular file or directory", rel)
		}
	})
	if err != nil {
		return "", fmt.Errorf("hash snapshot %s: %w", dir, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashFile hashes a regular file's header and bytes, both taken from the opened file rather than the walk.
func hashFile(h io.Writer, root *os.Root, rel string) error {
	f, err := openNoFollow(root, rel)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", rel)
	}
	exec := 0
	if info.Mode()&0o111 != 0 {
		exec = 1
	}
	if _, err := fmt.Fprintf(h, "%s\x00f\x00%d\x00%d\x00", rel, exec, info.Size()); err != nil {
		return err
	}
	_, err = io.Copy(h, f)
	return err
}

// makeReadOnly strips write permission, deepest directories first so the walk can still enter them. It goes
// through an os.Root so a swapped symlink cannot redirect a chmod outside the snapshot.
func makeReadOnly(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("make snapshot read-only: %w", err)
	}
	defer root.Close()
	var dirs []string
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return root.Chmod(rel, info.Mode().Perm()&^0o222)
	})
	if err != nil {
		return fmt.Errorf("make snapshot read-only: %w", err)
	}
	for _, rel := range slices.Backward(dirs) {
		// macOS requires the staging root to remain writable until it is renamed into place.
		if rel == "." {
			continue
		}
		if err := root.Chmod(rel, 0o555); err != nil {
			return fmt.Errorf("make snapshot read-only: %w", err)
		}
	}
	return nil
}

// removeTree deletes a tree that makeReadOnly may have locked.
func removeTree(dir string) error {
	if root, err := os.OpenRoot(dir); err == nil {
		_ = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = root.Chmod(rel, 0o755)
			}
			return nil
		})
		root.Close()
	}
	return os.RemoveAll(dir)
}
