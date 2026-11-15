package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/fsutil"
	"github.com/akopichin/afm/pkg/pipeline"
)

const pipelineLaunchVersion = 1

// pipelineLaunch is the host's hand-over to the container side of a Docker run. The container rebuilds the same flow
// and configuration from it, without fetching, copying or re-resolving anything and without the options file. It
// holds references to secrets (auth.from), never their values.
type pipelineLaunch struct {
	Version       int                  `json:"version"`
	Source        pipeline.Source      `json:"source"`
	Commit        string               `json:"commit,omitempty"`
	SnapshotDir   string               `json:"snapshot_dir"`
	ContentSHA256 string               `json:"content_sha256"`
	Options       *pipeline.OptionsRef `json:"options,omitempty"`
	OptionsLayer  string               `json:"options_layer,omitempty"` // pipeline.EncodeLayer output
	RunName       string               `json:"run_name"`
	Worktree      string               `json:"worktree"`
	ForceNew      bool                 `json:"force_new"`
	Resume        *pipeline.Binding    `json:"resume,omitempty"`
	Notices       []string             `json:"notices,omitempty"`
}

func pipelineLaunchDir() string {
	return filepath.Join(absRootDir(), config.AfmDir, "pipelines", "launch")
}

// writePipelineLaunch stores the plan where the container can read it through the project mount and returns its path.
func writePipelineLaunch(plan *pipelinePlan) (string, error) {
	rec := pipelineLaunch{
		Version:       pipelineLaunchVersion,
		Source:        plan.setup.source,
		Commit:        plan.setup.commit,
		SnapshotDir:   plan.setup.loaded.Snapshot.Dir,
		ContentSHA256: plan.setup.loaded.Snapshot.SHA256,
		RunName:       plan.runName,
		Worktree:      plan.worktree,
		ForceNew:      plan.forceNew,
		Resume:        plan.resume,
		Notices:       plan.notices,
	}
	if opts := plan.setup.options; opts != nil {
		ref := opts.ref
		rec.Options = &ref
		layer, err := pipeline.EncodeLayer(opts.layer)
		if err != nil {
			return "", fmt.Errorf("encode pipeline options for the container: %w", err)
		}
		rec.OptionsLayer = string(layer)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	path := filepath.Join(pipelineLaunchDir(), hex.EncodeToString(nonce)+".json")
	if err := fsutil.AtomicWriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write pipeline launch record: %w", err)
	}
	return path, nil
}

// readPipelineLaunch reads a launch record, accepting only files from this project's launch directory.
func readPipelineLaunch(path string) (pipelineLaunch, error) {
	if filepath.Clean(filepath.Dir(path)) != pipelineLaunchDir() || filepath.Ext(path) != ".json" {
		return pipelineLaunch{}, fmt.Errorf("pipeline launch record %q is not in %s", path, pipelineLaunchDir())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return pipelineLaunch{}, fmt.Errorf("read pipeline launch record: %w", err)
	}
	var rec pipelineLaunch
	if err := json.Unmarshal(data, &rec); err != nil {
		return pipelineLaunch{}, fmt.Errorf("parse pipeline launch record: %w", err)
	}
	if rec.Version != pipelineLaunchVersion {
		return pipelineLaunch{}, fmt.Errorf("pipeline launch record version %d is not supported (expected %d): the image's afm differs from the host's", rec.Version, pipelineLaunchVersion)
	}
	return rec, nil
}

// planFromLaunch is the container's whole preflight: verify the mounted snapshot and rebuild the configuration the
// host planned. Nothing is fetched and no source path is resolved here.
func planFromLaunch(path string, base config.Config) (*pipelinePlan, error) {
	rec, err := readPipelineLaunch(path)
	if err != nil {
		return nil, err
	}
	loaded, err := pipeline.LoadSnapshot(rec.SnapshotDir)
	if err != nil {
		return nil, fmt.Errorf("load the pipeline snapshot in the container: %w", err)
	}
	if loaded.Snapshot.SHA256 != rec.ContentSHA256 {
		return nil, fmt.Errorf("snapshot %s has content %s in the container, the host recorded %s", rec.SnapshotDir, loaded.Snapshot.SHA256, rec.ContentSHA256)
	}
	var opts *pipelineOptions
	if rec.Options != nil {
		layer, err := pipeline.ParseLayer([]byte(rec.OptionsLayer), pipeline.OriginOptions, string(filepath.Separator))
		if err != nil {
			return nil, fmt.Errorf("pipeline options in the launch record: %w", err)
		}
		opts = &pipelineOptions{ref: *rec.Options, layer: layer}
	}
	setup, err := assemblePipeline(base, loaded, rec.Source, rec.Commit, opts)
	if err != nil {
		return nil, err
	}
	if rec.Resume != nil {
		if err := ensureRunUnlocked(filepath.Join(runsDir(), rec.Resume.RunID)); err != nil {
			return nil, err
		}
	}
	if rec.RunName == "" {
		return nil, errors.New("pipeline launch record has no run name")
	}
	return &pipelinePlan{setup: setup, resume: rec.Resume, runName: rec.RunName, worktree: rec.Worktree, forceNew: rec.ForceNew, notices: rec.Notices}, nil
}
