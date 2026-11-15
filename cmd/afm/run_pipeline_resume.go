package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/akopichin/afm-engine/engine"
	implstate "github.com/akopichin/afm-engine/engine/inspection/state"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/contracts/worktree"
	"github.com/akopichin/afm/pkg/pipeline"
)

// pipelinePlan is the outcome of preflight: which package version this process runs, and whether it continues a
// recorded run or starts a new one.
type pipelinePlan struct {
	setup    pipelineSetup
	resume   *pipeline.Binding // the run to continue; nil starts a new one
	runName  string
	worktree string
	forceNew bool
	notices  []string // continuation notices, printed with the banner
	others   []string // other unfinished runs of this pipeline that the plan did not pick
}

func (p *pipelinePlan) mode() string {
	if p.resume != nil {
		return "continuing run " + p.resume.RunID
	}
	return "new run"
}

// planInput is what preflight needs from the command line and the loaded base configuration.
type planInput struct {
	source   pipeline.Source
	options  *pipelineOptions
	base     config.Config
	worktree string // the --worktree value, empty without the flag
	clear    worktree.ClearMode
}

// planPipelineRun decides, before any worktree or run exists, which snapshot to use and whether to continue a run:
// only one whose binding matches the source and --worktree and that the engine would reopen. Otherwise the source
// is resolved afresh, and an unfinished run of that name which this pipeline does not own stops the start.
func planPipelineRun(ctx context.Context, in planInput) (*pipelinePlan, error) {
	if in.clear == worktree.ClearNone {
		choice, err := findResumable(ctx, in)
		if err != nil {
			return nil, err
		}
		if choice != nil {
			return resumePlan(ctx, in, choice)
		}
	}
	loaded, commit, err := loadFreshPipeline(ctx, in.source)
	if err != nil {
		return nil, err
	}
	runName := runNameFor(in.worktree, loaded.Flow.Name)
	if in.clear == worktree.ClearNone {
		if err := refuseForeignUnfinishedRun(runName, loaded.Flow); err != nil {
			return nil, err
		}
	}
	setup, err := assemblePipeline(in.base, loaded, in.source, commit, in.options)
	if err != nil {
		return nil, err
	}
	return &pipelinePlan{setup: setup, runName: runName, worktree: in.worktree, forceNew: true}, nil
}

// refuseForeignUnfinishedRun stops a start that the engine would turn into a continuation of a run this pipeline
// invocation does not own: it would run the new package's flow over foreign progress.
func refuseForeignUnfinishedRun(runName string, f *flow.Flow) error {
	saved, err := engine.SavedRunFor(worktree.ClearNone, runsDir(), runName, f)
	if err != nil {
		return err
	}
	if saved == nil {
		return nil
	}
	return fmt.Errorf("an unfinished run %q exists in %s that was not started from this pipeline source with this --worktree; "+
		"continue it with the same --pipeline and --worktree, start separately with --worktree <new name>, or discard it with --worktree-clear", runName, runsDir())
}

// resumeChoice is the recorded run a start will continue, with the snapshot it must continue on.
type resumeChoice struct {
	binding pipeline.Binding
	loaded  pipeline.Loaded
	others  []string
}

// findResumable returns the binding whose run the engine would reopen for this command, or nil.
func findResumable(ctx context.Context, in planInput) (*resumeChoice, error) {
	bindings, readErr := pipeline.ReadBindings(pipeline.BindingsDir(absRootDir()))
	if readErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", readErr)
	}
	var matches []resumeChoice
	for _, b := range pipeline.FindBindings(bindings, in.source.Identity(), in.worktree) {
		// Cheap first: only the newest run of a name can be reopened, and older runs need no snapshot.
		if latestRunID(runsDir(), b.RunName) != b.RunID {
			continue
		}
		loaded, err := loadBoundSnapshot(ctx, b)
		if err != nil {
			if runCompleted(filepath.Join(runsDir(), b.RunID)) {
				continue
			}
			return nil, fmt.Errorf("run %s of pipeline %s cannot be continued: %w", b.RunID, b.Package.Name, err)
		}
		expected := runNameFor(in.worktree, loaded.Flow.Name)
		if b.RunName != expected {
			continue
		}
		saved, err := engine.SavedRunFor(worktree.ClearNone, runsDir(), expected, loaded.Flow)
		if err != nil {
			return nil, err
		}
		if saved != nil {
			matches = append(matches, resumeChoice{binding: b, loaded: loaded})
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	chosen := matches[0] // FindBindings is newest first
	for _, other := range matches[1:] {
		chosen.others = append(chosen.others, other.binding.RunID)
	}
	return &chosen, nil
}

// loadBoundSnapshot reads the snapshot a run was started on. A missing Git snapshot is rebuilt from the recorded
// commit in the local cache, without network; a local one cannot be rebuilt because its source may have changed.
func loadBoundSnapshot(ctx context.Context, b pipeline.Binding) (pipeline.Loaded, error) {
	loaded, err := pipeline.LoadSnapshot(b.SnapshotDir)
	if err == nil {
		if loaded.Snapshot.SHA256 != b.ContentSHA256 {
			return pipeline.Loaded{}, fmt.Errorf("snapshot %s has content %s, recorded %s", b.SnapshotDir, loaded.Snapshot.SHA256, b.ContentSHA256)
		}
		return loaded, nil
	}
	if b.Source.Kind != pipeline.SourceGit || b.Commit == "" || !errors.Is(err, os.ErrNotExist) {
		return pipeline.Loaded{}, err
	}
	cacheRoot, rootErr := pipelinesCacheRoot()
	if rootErr != nil {
		return pipeline.Loaded{}, rootErr
	}
	fetched, fetchErr := fetchPipeline(ctx, b.Source, cacheRoot, b.Commit)
	if fetchErr != nil {
		return pipeline.Loaded{}, fmt.Errorf("restore snapshot from commit %s: %w", b.Commit, fetchErr)
	}
	if fetched.snapshot.SHA256 != b.ContentSHA256 {
		return pipeline.Loaded{}, fmt.Errorf("commit %s now yields content %s, recorded %s", b.Commit, fetched.snapshot.SHA256, b.ContentSHA256)
	}
	return pipeline.LoadSnapshot(fetched.snapshot.Dir)
}

// resumePlan builds the continuation of a chosen run: its recorded snapshot with the options of THIS command.
func resumePlan(ctx context.Context, in planInput, choice *resumeChoice) (*pipelinePlan, error) {
	b := choice.binding
	setup, err := assemblePipeline(in.base, choice.loaded, b.Source, b.Commit, in.options)
	if err != nil {
		return nil, err
	}
	runDir := filepath.Join(runsDir(), b.RunID)
	if err := ensureRunUnlocked(runDir); err != nil {
		return nil, err
	}
	notices := resumeNotices(ctx, in, b, setup, runDir)
	return &pipelinePlan{setup: setup, resume: &b, runName: b.RunName, worktree: in.worktree, notices: notices, others: choice.others}, nil
}

// ensureRunUnlocked fails before the engine opens a run that a live afm process holds: the engine would silently
// start a different run, which this plan did not predict.
func ensureRunUnlocked(runDir string) error {
	lock, err := implstate.TryLockRun(runDir)
	if errors.Is(err, implstate.ErrRunLocked) {
		return fmt.Errorf("run %s is held by another afm process; stop it or wait before continuing", filepath.Base(runDir))
	}
	if err != nil {
		return fmt.Errorf("check run %s lock: %w", filepath.Base(runDir), err)
	}
	return lock.Close()
}

// resumeNotices lists what a user should know about a continuation: a changed local source, options that differ
// from the last start, and a client switch on an interactive stage whose saved session the new client cannot use.
func resumeNotices(ctx context.Context, in planInput, b pipeline.Binding, setup pipelineSetup, runDir string) []string {
	var notices []string
	if in.source.Ref != "" && in.source.Ref != b.Source.Ref {
		notices = append(notices, fmt.Sprintf("the requested ref %q is not applied to a continued run; it stays on commit %s", in.source.Ref, b.Commit))
	}
	if b.Source.Kind == pipeline.SourceLocal {
		if n := localSourceChange(ctx, b); n != "" {
			notices = append(notices, n)
		}
	}
	notices = append(notices, optionsNotices(b.Options, setup.options)...)
	notices = append(notices, interactiveSwitchNotices(b, setup, runDir)...)
	return notices
}

// localSourceChange compares the source directory now with the content the run started on. It is best effort: a
// source that cannot be read now simply yields no notice, since the run continues on its snapshot regardless.
func localSourceChange(ctx context.Context, b pipeline.Binding) string {
	cacheRoot, err := pipelinesCacheRoot()
	if err != nil {
		return ""
	}
	fetched, err := fetchPipeline(ctx, b.Source, cacheRoot, "")
	if err != nil || fetched.snapshot.SHA256 == b.ContentSHA256 {
		return ""
	}
	return fmt.Sprintf("the package source %s changed since the run started (content sha256:%.12s, now sha256:%.12s); continuing on the recorded snapshot",
		b.Source.Location, b.ContentSHA256, fetched.snapshot.SHA256)
}

// optionsNotices warns when a run that used an options file is continued without it or with another file. The same
// file with new contents is the normal edit-and-continue case and stays quiet. Options are never reapplied silently.
func optionsNotices(recorded *pipeline.OptionsRef, current *pipelineOptions) []string {
	switch {
	case recorded == nil:
		return nil
	case current == nil:
		return []string{fmt.Sprintf("this run was started with options %s; none are given now, so the package settings apply", recorded.Path)}
	case current.ref.Path != recorded.Path:
		return []string{fmt.Sprintf("this run was started with options %s; now using %s", recorded.Path, current.ref.Path)}
	}
	return nil
}

// interactiveSwitchNotices warns about interactive stages that are not done and whose command now runs another
// client: the saved session id goes to the new client, which cannot continue the old client's conversation.
func interactiveSwitchNotices(b pipeline.Binding, setup pipelineSetup, runDir string) []string {
	current, err := recipeRecords(setup.loaded.Flow, setup.cfg)
	if err != nil {
		return nil
	}
	rs, err := implstate.LoadRunState(runDir)
	if err != nil {
		return nil
	}
	var notices []string
	for _, stage := range setup.loaded.Flow.Stages {
		if !stage.Interactive || rs.Stages[stage.ID].Status == state.StatusDone {
			continue
		}
		was, hadRecord := b.Recipes[stage.Command]
		now := current[stage.Command]
		if stage.Command == "" || !hadRecord || was.Type == now.Type {
			continue
		}
		notices = append(notices, fmt.Sprintf("interactive stage %q: command %s changed client from %s to %s; its saved conversation does not carry over, the new client starts without it",
			stage.ID, stage.Command, was.Type, now.Type))
	}
	return notices
}

// latestRunID is the newest run directory of runName, or "" when there is none.
func latestRunID(base, runName string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && implstate.RunIDBelongsTo(e.Name(), runName) {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return slices.Max(ids)
}

// runCompleted reports a finished run with every stage done, which can no longer be continued and so needs no
// snapshot to be found again.
func runCompleted(runDir string) bool {
	rs, err := implstate.LoadRunState(runDir)
	return err == nil && rs.RunStatus == state.RunStatusFinished && rs.AllDone()
}

// printNotices writes the continuation notices and the other unfinished runs.
func (p *pipelinePlan) printNotices(w io.Writer) {
	for _, n := range p.notices {
		_, _ = fmt.Fprintf(w, "afm: notice: %s\n", n)
	}
	if len(p.others) > 0 {
		_, _ = fmt.Fprintf(w, "afm: other unfinished runs of this pipeline: %s\n", strings.Join(p.others, ", "))
	}
}
