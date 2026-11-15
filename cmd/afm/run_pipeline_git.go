package main

import (
	"context"

	"github.com/akopichin/afm/pkg/pipeline"
)

// resolveGitSource fetches a Git package into the cache and snapshots it; offlineCommit restores a recorded commit
// without network.
func resolveGitSource(ctx context.Context, src pipeline.Source, cacheRoot, offlineCommit string) (fetchedPackage, error) {
	resolved, err := pipeline.ResolveGit(ctx, src, cacheRoot, offlineCommit)
	if err != nil {
		return fetchedPackage{}, err
	}
	return fetchedPackage{snapshot: resolved.Snapshot, commit: resolved.Commit}, nil
}
