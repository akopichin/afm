package pipeline

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type GitCatalog struct {
	Source      Source   `json:"source"`
	Commit      string   `json:"commit"`
	Directories []string `json:"directories"`
}

// DiscoverGit lists regular package manifests at one revision. A package at the requested root takes precedence.
func DiscoverGit(ctx context.Context, src Source, cacheRoot string) (GitCatalog, error) {
	if err := checkGitSource(src, ""); err != nil {
		return GitCatalog{}, err
	}
	root := filepath.Join(cacheRoot, gitCacheDir, sha256Hex(src.Location))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return GitCatalog{}, err
	}
	unlock, err := lockGitCache(ctx, filepath.Join(root, "lock"))
	if err != nil {
		return GitCatalog{}, err
	}
	defer unlock()
	dir := filepath.Join(root, "repo.git")
	commit, err := initAndPin(ctx, src, dir, "")
	if err != nil {
		return GitCatalog{}, err
	}
	listing, err := runGit(ctx, dir, "ls-tree", "-rz", commit+":"+src.Subdir)
	if err != nil {
		return GitCatalog{}, err
	}
	result := GitCatalog{Source: src, Commit: commit}
	for entry := range strings.SplitSeq(listing, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		regular := strings.HasPrefix(meta, "100644 blob ") || strings.HasPrefix(meta, "100755 blob ")
		if !ok || path.Base(name) != ManifestFile || !regular {
			continue
		}
		if name == ManifestFile {
			result.Directories = []string{src.Subdir}
			return result, nil
		}
		result.Directories = append(result.Directories, path.Join(src.Subdir, path.Dir(name)))
	}
	if len(result.Directories) == 0 {
		return GitCatalog{}, errors.New("no pipeline.yaml packages found in this Git source")
	}
	return result, nil
}
