package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm-engine/engine"
	implstate "github.com/akopichin/afm-engine/engine/inspection/state"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/worktree"
	"github.com/akopichin/afm/pkg/pipeline"
)

// pipelineDirEnv tells scripts and agents where the package snapshot lives; paths in the package are relative to it.
const pipelineDirEnv = "AFM_PIPELINE_DIR"

const maxOptionsBytes = 1 << 20

// pipelineFlags are the raw `afm run` flags that select package mode.
type pipelineFlags struct {
	source  string // --pipeline
	options string // --pipeline-options
	launch  string // --pipeline-launch: the host's launch record, read only by the container side of a Docker run
}

func (p pipelineFlags) active() bool { return p.source != "" }

// validate runs before anything is created or loaded, so a contradictory command line leaves no trace.
func (p pipelineFlags) validate(args []string) error {
	switch {
	case p.source != "" && len(args) > 0:
		return errors.New("--pipeline and a flow file argument are mutually exclusive: name one source of the flow")
	case p.source == "" && p.options != "":
		return errors.New("--pipeline-options requires --pipeline")
	case p.source == "" && p.launch != "":
		return errors.New("--pipeline-launch requires --pipeline")
	case p.launch != "" && !config.ReExecedIntoContainer():
		return errors.New("--pipeline-launch is internal: only the container side of a Docker run accepts it")
	}
	return nil
}

// pipelineOptions is the user's options file: its identity for resume notices and the parsed layer.
type pipelineOptions struct {
	ref   pipeline.OptionsRef
	layer pipeline.Layer
}

// loadPipelineOptions reads the options file once. Relative file: references inside it are pinned to absolute paths
// here, on the host, so the container never has to guess a working directory.
func loadPipelineOptions(path string) (*pipelineOptions, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("pipeline options: %w", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("pipeline options: %w", err)
	}
	defer f.Close()
	data, err := pipeline.ReadLimited(f, maxOptionsBytes)
	if err != nil {
		return nil, fmt.Errorf("pipeline options %s: %w", abs, err)
	}
	layer, err := pipeline.ParseLayer(data, pipeline.OriginOptions, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("pipeline options %s: %w", abs, err)
	}
	sum := sha256.Sum256(data)
	return &pipelineOptions{ref: pipeline.OptionsRef{Path: abs, SHA256: hex.EncodeToString(sum[:])}, layer: layer}, nil
}

// pipelineSetup is a package resolved for one run: the flow and configuration the engine gets, and what binds the run
// to this exact package version.
type pipelineSetup struct {
	source   pipeline.Source
	commit   string
	loaded   pipeline.Loaded
	options  *pipelineOptions
	cfg      config.Config
	prov     pipeline.Provenance
	warnings []pipeline.Warning
}

// assemblePipeline layers the package and options over base without touching any config file.
func assemblePipeline(base config.Config, loaded pipeline.Loaded, src pipeline.Source, commit string, opts *pipelineOptions) (pipelineSetup, error) {
	var layer *pipeline.Layer
	if opts != nil {
		layer = &opts.layer
	}
	cfg, prov, warnings, err := pipeline.Assemble(base, loaded.Layer, layer, loaded.Snapshot.Dir)
	if err != nil {
		return pipelineSetup{}, err
	}
	return pipelineSetup{source: src, commit: commit, loaded: loaded, options: opts, cfg: cfg, prov: prov, warnings: warnings}, nil
}

// pipelinesCacheRoot is the per-user cache of snapshots and Git clones; it lives outside every project.
func pipelinesCacheRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for the pipeline cache: %w", err)
	}
	return filepath.Join(home, config.AfmDir, "cache", "pipelines"), nil
}

// fetchedPackage is a package source turned into a snapshot; commit is set for Git sources.
type fetchedPackage struct {
	snapshot pipeline.Snapshot
	commit   string
}

// fetchPipeline snapshots a package. offlineCommit, when set, restores a Git package from the cache without network.
func fetchPipeline(ctx context.Context, src pipeline.Source, cacheRoot, offlineCommit string) (fetchedPackage, error) {
	if src.Kind == pipeline.SourceGit {
		return resolveGitSource(ctx, src, cacheRoot, offlineCommit)
	}
	m, err := pipeline.LoadManifest(src.Location)
	if err != nil {
		return fetchedPackage{}, err
	}
	snap, err := pipeline.BuildSnapshot(src.Location, m, cacheRoot)
	if err != nil {
		return fetchedPackage{}, err
	}
	return fetchedPackage{snapshot: snap}, nil
}

// loadFreshPipeline fetches the source as it is now and loads the snapshot.
func loadFreshPipeline(ctx context.Context, src pipeline.Source) (pipeline.Loaded, string, error) {
	cacheRoot, err := pipelinesCacheRoot()
	if err != nil {
		return pipeline.Loaded{}, "", err
	}
	fetched, err := fetchPipeline(ctx, src, cacheRoot, "")
	if err != nil {
		return pipeline.Loaded{}, "", err
	}
	loaded, err := pipeline.LoadSnapshot(fetched.snapshot.Dir)
	return loaded, fetched.commit, err
}

// printBanner tells the user which package version, content and effective roles this run uses.
func (s pipelineSetup) printBanner(w io.Writer, mode string) {
	m := s.loaded.Manifest
	_, _ = fmt.Fprintf(w, "afm: pipeline %s %s (%s)\n", m.Name, m.Version, mode)
	_, _ = fmt.Fprintf(w, "  source: %s\n", describeSource(s.source))
	if s.commit != "" {
		_, _ = fmt.Fprintf(w, "  commit: %s\n", s.commit)
	}
	_, _ = fmt.Fprintf(w, "  content: sha256:%s\n", s.loaded.Snapshot.SHA256)
	_, _ = fmt.Fprintf(w, "  snapshot: %s\n", s.loaded.Snapshot.Dir)
	if s.options != nil {
		_, _ = fmt.Fprintf(w, "  options: %s (sha256:%.12s)\n", s.options.ref.Path, s.options.ref.SHA256)
	}
	for _, r := range describeRoles(m, s.cfg, s.prov) {
		_, _ = fmt.Fprintf(w, "  role %s: %s\n", r.ID, describeRoleSettings(r))
	}
	for _, warning := range s.warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
}

func describeSource(s pipeline.Source) string {
	if s.Kind == pipeline.SourceLocal {
		return "local " + s.Location
	}
	text := "git " + s.Location
	if s.Subdir != "" {
		text += "//" + s.Subdir
	}
	if s.Ref != "" {
		text += "@" + s.Ref
	}
	return text
}

func describeRoleSettings(r roleReport) string {
	text := describeExecutorSettings(r.executorReport)
	if r.ProfileExecutor != nil {
		text += "; helper profile runs " + describeExecutorSettings(*r.ProfileExecutor)
	}
	if r.ProfileExecutorInheritance != "" {
		text += "; helper profile executor depends on where it starts: " + r.ProfileExecutorInheritance
	}
	return text
}

func describeExecutorSettings(e executorReport) string {
	parts := []string{"command " + e.Command}
	if e.Client != "" {
		parts = append(parts, "client "+e.Client)
	}
	for _, kv := range [][2]string{{"model", e.Model}, {"reasoning", e.Reasoning}, {"url", e.URL}, {"auth", e.AuthFrom}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+" "+kv[1])
		}
	}
	if e.CommandLayer != "" {
		parts = append(parts, "from "+e.CommandLayer)
	}
	return strings.Join(parts, ", ")
}

// recipeRecords captures which executor each used command had at this start, for continuation notices and the
// recorded identity check. Only selectors and digests are kept.
func recipeRecords(f *flow.Flow, cfg config.Config) (map[string]pipeline.RecipeRecord, error) {
	proj, err := engine.ProjectDocker(f, cfg, nil)
	if err != nil {
		return nil, err
	}
	records := make(map[string]pipeline.RecipeRecord, len(proj.Commands))
	for _, command := range proj.Commands {
		if recipe, ok := cfg.Docker.Agents[command]; ok {
			records[command] = pipeline.RecipeRecord{Type: recipeClient(recipe), Identity: recipe.Identity()}
		} else {
			records[command] = pipeline.RecipeRecord{Type: "command"}
		}
	}
	return records, nil
}

// pipelineBinder keeps the binding of each run this process opens in step with the run the engine really opened.
type pipelineBinder struct {
	setup    pipelineSetup
	runName  string
	worktree string
	// resume is the binding of the run the first cycle was predicted to continue; nil predicts a new run.
	resume *pipeline.Binding
	dir    string
	now    func() time.Time
}

func newPipelineBinder(setup pipelineSetup, runName, worktreeName string, resume *pipeline.Binding) *pipelineBinder {
	return &pipelineBinder{setup: setup, runName: runName, worktree: worktreeName, resume: resume, dir: pipeline.BindingsDir(absRootDir()), now: time.Now}
}

// runCreated records the run the engine opened. A new run other than the predicted one gets its truthful binding
// before the mismatch error, so it is never left without one; another run's progress is refused, never bound.
func (b *pipelineBinder) runCreated(runID string, cycle int) error {
	records, err := recipeRecords(b.setup.loaded.Flow, b.setup.cfg)
	if err != nil {
		return err
	}
	now := b.now().UTC()
	binding := pipeline.Binding{
		RunID:         runID,
		RunName:       b.runName,
		Worktree:      b.worktree,
		Package:       pipeline.PackageRef{Name: b.setup.loaded.Manifest.Name, Version: b.setup.loaded.Manifest.Version},
		Source:        b.setup.source,
		Commit:        b.setup.commit,
		ContentSHA256: b.setup.loaded.Snapshot.SHA256,
		SnapshotDir:   b.setup.loaded.Snapshot.Dir,
		Recipes:       records,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if b.setup.options != nil {
		ref := b.setup.options.ref
		binding.Options = &ref
	}
	predictedResume := cycle <= 1 && b.resume != nil
	if predictedResume && b.resume.RunID == runID {
		binding.CreatedAt = b.resume.CreatedAt
		// A resumed run stays on the recorded version, whatever the command line named this time.
		binding.Source, binding.Commit, binding.Package = b.resume.Source, b.resume.Commit, b.resume.Package
	}
	ownResume := predictedResume && b.resume.RunID == runID
	existing, hasBinding, readErr := pipeline.ReadBinding(b.dir, runID)
	switch {
	case readErr != nil:
		return fmt.Errorf("read pipeline binding of run %s: %w", runID, readErr)
	case hasBinding && !ownResume:
		return fmt.Errorf("run %s already belongs to pipeline %s (%s); refusing to rebind it; pipeline preflight expected %s", runID, existing.Package.Name, existing.Source.Identity(), b.expectedRun())
	case hasBinding && existing.Source.Identity() != b.resume.Source.Identity():
		return fmt.Errorf("run %s now belongs to pipeline %s (%s), not the pipeline preflight continued; refusing to rebind it", runID, existing.Package.Name, existing.Source.Identity())
	}
	if !ownResume {
		// A missing binding proves nothing: the engine also reopens unbound progress, such as a plain `afm run` of the
		// same flow. Only a run with an empty journal is new and may be bound.
		rs, err := implstate.LoadRunState(filepath.Join(runsDir(), runID))
		switch {
		case err != nil:
			return fmt.Errorf("read run %s to check that it is new: %w", runID, err)
		case rs.LastSeq != 0:
			return fmt.Errorf("run %s already has progress but no pipeline binding; refusing to bind it to this pipeline; pipeline preflight expected %s", runID, b.expectedRun())
		}
	}
	if err := pipeline.WriteBinding(b.dir, binding); err != nil {
		return fmt.Errorf("record pipeline binding for run %s: %w", runID, err)
	}
	if predictedResume && b.resume.RunID != runID {
		return fmt.Errorf("pipeline preflight expected to continue run %s, but the engine opened run %s; it is recorded as a new run of this pipeline, nothing was started — rerun the command", b.resume.RunID, runID)
	}
	return nil
}

func (b *pipelineBinder) expectedRun() string {
	if b.resume == nil {
		return "a new run"
	}
	return "to continue run " + b.resume.RunID
}

// preparePipelineRun is the whole preflight of `afm run --pipeline`, ending in a plan the rest of RunE follows.
// Everything that can be refused is refused here, before a worktree, a Docker re-exec or a run exists.
func preparePipelineRun(ctx context.Context, pf pipelineFlags, base config.Config, worktreeName string, clearWorktree, clearForce bool) (*pipelinePlan, error) {
	if worktreeName != "" {
		if err := worktree.ValidateName(worktreeName); err != nil {
			return nil, err
		}
	}
	mode, err := clearModeFromFlags(clearWorktree, clearForce)
	if err != nil {
		return nil, err
	}
	if config.ReExecedIntoContainer() {
		if pf.launch == "" {
			return nil, errors.New("a pipeline run in a container needs the host's launch record (--pipeline-launch); the host and the container must run the same afm version")
		}
		return planFromLaunch(pf.launch, base)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	src, err := pipeline.ParseSource(pf.source, cwd)
	if err != nil {
		return nil, err
	}
	var opts *pipelineOptions
	if pf.options != "" {
		if opts, err = loadPipelineOptions(pf.options); err != nil {
			return nil, err
		}
	}
	plan, err := planPipelineRun(ctx, planInput{source: src, options: opts, base: base, worktree: worktreeName, clear: mode})
	if err != nil {
		return nil, err
	}
	if err := config.ValidateVerifySpecs(plan.setup.loaded.Flow, plan.setup.cfg); err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	var continuedRunDir string
	if plan.resume != nil {
		continuedRunDir = filepath.Join(runsDir(), plan.resume.RunID)
	}
	if err := pipelinePreflight(plan.setup.loaded.Flow, plan.setup.cfg, plan.setup.loaded.Manifest, continuedRunDir); err != nil {
		return nil, err
	}
	return plan, nil
}

// announcePipeline exports the snapshot path for scripts and agents and, on the host only, prints the banner: the
// container side re-runs RunE and would otherwise print it twice.
func announcePipeline(plan *pipelinePlan) {
	_ = os.Setenv(pipelineDirEnv, plan.setup.loaded.Snapshot.Dir)
	if config.ReExecedIntoContainer() {
		return
	}
	plan.setup.printBanner(os.Stdout, plan.mode())
	plan.printNotices(os.Stdout)
}

// commandContext is the command's context; cobra leaves it nil when a command runs without Execute.
func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
