package memorypipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aymanbagabas/go-udiff"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
)

// AtomicWriter is the seam over memory.AtomicWrite used by Finalize's
// promotion step (review #8): a test injects a recording/failing writer to
// observe or interrupt the durable rename of a final target/dataset. It
// receives (path, bytes) so it can FULLY replace the write, not just observe.
type AtomicWriter func(path string, data []byte) error

// CaptureRequest — parameters for CaptureAll: which run's stage dirs to read,
// the offline work dir to stage datasets into, the flow stages, whether to
// force a fresh reflect for every stage, and the manifest to advance.
type CaptureRequest struct {
	RunDir       string
	WorkDir      string
	Stages       []flow.Stage
	ForceReflect bool
	ManifestPath string
}

// FinalizeRequest — parameters for Finalize. The caller MUST already hold the
// memory lock for MemoryDir (Finalize never takes it, review #2). writer is a
// test seam; nil -> memory.AtomicWrite.
type FinalizeRequest struct {
	Meta         OperationMeta
	RunDir       string
	WorkDir      string
	MemoryDir    string
	Stages       []flow.Stage
	Memory       flow.MemoryConfig
	Datasets     []DatasetResult
	DryRun       bool
	ManifestPath string
	writer       AtomicWriter
}

// projectTargetName labels the project-wide memory.md distill/target so its
// agent specs are distinguishable from per-stage ones (spec.StageName).
const projectTargetName = "flow-memory"

// CaptureStage is the single low-level capture primitive (review #3): it
// writes (or reuses) ONE stage's dataset at the caller-chosen datasetOut.
// The offline pass passes a staging path; the live orchestrator passes the
// canonical path directly. This is the explicit "output mode" the first plan
// lacked.
//
// Reuse fires ONLY when the caller targets the canonical path AND !force AND
// the canonical file already validates — otherwise the reflect agent runs and
// writes datasetOut fresh.
func (p *Pipeline) CaptureStage(ctx context.Context, stage flow.Stage, stageDir, datasetOut, logDir string, force bool) (DatasetResult, error) {
	canonical := filepath.Join(stageDir, "reflect_dataset.yaml")

	// Reuse a valid canonical dataset by default — INDEPENDENT of datasetOut
	// (spec §7.1): the offline pass targets a staging datasetOut that can never
	// equal canonical, yet must still avoid re-running the reflect agent when a
	// good canonical already exists. Only --force-reflect (force) regenerates.
	if !force {
		if data, err := os.ReadFile(canonical); err == nil {
			if verr := ValidateDataset(data); verr == nil {
				return DatasetResult{
					StageID:       stage.ID,
					Source:        "reused",
					DatasetPath:   canonical,
					CanonicalPath: canonical,
					DatasetSHA256: sha256Hex(data),
				}, nil
			}
		}
	}

	inv, err := SourceInventory(stageDir)
	if err != nil {
		return DatasetResult{}, err
	}
	if !inv.HasAgentSession() {
		return DatasetResult{}, &StepError{
			StageID: stage.ID,
			Step:    KindReflect,
			Err:     fmt.Errorf("no agent-session sources in %s", stageDir),
		}
	}

	// The staging dir (and log dir) may not exist yet — a pending stage often
	// has no on-disk artifacts and the offline work dir is fresh.
	if err := os.MkdirAll(filepath.Dir(datasetOut), 0755); err != nil {
		return DatasetResult{}, err
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return DatasetResult{}, err
	}

	logPath := filepath.Join(logDir, "reflect.log")
	_ = os.Remove(datasetOut)
	if err := p.run(ctx, AgentSpec{
		Kind:       KindReflect,
		StageName:  stage.Name,
		Sources:    inv.All(),
		DatasetOut: datasetOut,
		LogFile:    logPath,
	}); err != nil {
		return DatasetResult{}, &StepError{StageID: stage.ID, Step: KindReflect, LogPath: logPath, Err: err}
	}
	if err := requireFreshFile(datasetOut, maxDatasetBytes); err != nil {
		return DatasetResult{}, &StepError{StageID: stage.ID, Step: KindReflect, LogPath: logPath, Err: err}
	}
	data, err := os.ReadFile(datasetOut)
	if err != nil {
		return DatasetResult{}, &StepError{StageID: stage.ID, Step: KindReflect, LogPath: logPath, Err: err}
	}
	if err := ValidateDataset(data); err != nil {
		return DatasetResult{}, &StepError{StageID: stage.ID, Step: KindReflect, LogPath: logPath, Err: err}
	}

	sources := inv.All()
	hashes, err := sha256Each(sources)
	if err != nil {
		return DatasetResult{}, err
	}
	return DatasetResult{
		StageID:       stage.ID,
		Source:        "generated",
		DatasetPath:   datasetOut,
		CanonicalPath: canonical,
		SourceFiles:   sources,
		SourceHashes:  hashes,
		DatasetSHA256: sha256Hex(data),
	}, nil
}

// CaptureAll iterates CaptureStage over eligible write-reflect stages
// (declaration order), always writing generated datasets into
// <WorkDir>/stages/<id>/reflect_dataset.yaml (canonical untouched until
// promotion). No shared memory lock is needed. Requires a non-empty
// ManifestPath (the CLI creates the running manifest first) and advances it
// to "capturing".
func (p *Pipeline) CaptureAll(ctx context.Context, req CaptureRequest) ([]DatasetResult, error) {
	if req.ManifestPath == "" {
		return nil, errors.New("CaptureAll requires a non-empty ManifestPath")
	}

	man, err := LoadManifest(req.ManifestPath)
	if err != nil {
		return nil, err
	}
	man.Status = StatusCapturing
	if err := WriteManifest(req.ManifestPath, man); err != nil {
		return nil, err
	}

	var results []DatasetResult
	for _, stage := range req.Stages {
		if !isWriteReflect(stage) {
			continue
		}
		stageDir := filepath.Join(req.RunDir, stage.ID)
		stagingDir := filepath.Join(req.WorkDir, "stages", stage.ID)
		datasetOut := filepath.Join(stagingDir, "reflect_dataset.yaml")
		res, err := p.CaptureStage(ctx, stage, stageDir, datasetOut, stagingDir, req.ForceReflect)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}

	man.Datasets = results
	if err := WriteManifest(req.ManifestPath, man); err != nil {
		return nil, err
	}
	return results, nil
}

// isWriteReflect reports whether a stage participates in the memory write
// chain: it declares reflect with a write mode and is not a script stage
// (there is no agent session to reflect on).
func isWriteReflect(s flow.Stage) bool {
	return s.Reflect != nil && s.Reflect.CanWrite() && !s.IsScript()
}

// stageGroup — one final memory file shared by ≥1 write-reflect stage,
// resolved from reflect.File. Stages within a group are chained: each
// distills from the prior stage's candidate (see Finalize step 2).
type stageGroup struct {
	file      string
	finalPath string
	stages    []flow.Stage
}

// buildStageGroups groups eligible write-reflect stages by their resolved
// reflect.File, preserving first-declaration order both across groups and
// within a group.
func buildStageGroups(stages []flow.Stage, memoryDir string) []stageGroup {
	idx := map[string]int{}
	var groups []stageGroup
	for _, s := range stages {
		if !isWriteReflect(s) {
			continue
		}
		if i, ok := idx[s.Reflect.File]; ok {
			groups[i].stages = append(groups[i].stages, s)
			continue
		}
		idx[s.Reflect.File] = len(groups)
		groups = append(groups, stageGroup{
			file:      s.Reflect.File,
			finalPath: memory.StageFile(memoryDir, s.Reflect.File),
			stages:    []flow.Stage{s},
		})
	}
	return groups
}

// Finalize distills the captured datasets into candidate memory files,
// computes per-target diffs, and (unless DryRun) promotes changed candidates
// to their final paths. It ASSUMES the caller already holds the memory lock
// for MemoryDir. It writes/updates ManifestPath at each step and NEVER writes
// "completed" or commits — the CLI does that after the commit decision
// (review #2). The step ORDER is load-bearing (containment pre-check ->
// distill -> diff -> ctx check -> promote); see the inline step comments.
func (p *Pipeline) Finalize(ctx context.Context, req FinalizeRequest) (Report, error) {
	man, err := loadManifestIfSet(req.ManifestPath)
	if err != nil {
		return Report{}, err
	}
	pending := []TargetResult{}
	datasets := append([]DatasetResult(nil), req.Datasets...)
	writeManifest := func(status string) error {
		if man == nil {
			return nil
		}
		man.Status = status
		man.Targets = pending
		man.Datasets = datasets
		return WriteManifest(req.ManifestPath, man)
	}
	report := func() Report {
		return Report{RunID: req.Meta.RunID, WorkDir: req.WorkDir, Targets: pending, Datasets: datasets}
	}

	groups := buildStageGroups(req.Stages, req.MemoryDir)

	// Determine whether the project-wide memory.md is a target (needs its
	// containment checked and its distill run).
	var projectDatasets []string
	if req.Memory.CanWriteProject() {
		projectDatasets, err = nonEmptyDatasetPaths(datasets)
		if err != nil {
			return report(), err
		}
	}
	projFinal := memory.ProjectFile(req.MemoryDir)
	projectApplies := len(projectDatasets) > 0

	// --- Step 0: containment pre-check under lock (review #5) ---------------
	// Validate every final target BEFORE seeding — an external process may
	// have swapped a parent to a symlink after Task 3's lexical check.
	for _, g := range groups {
		if err := validateTargetUnderMemoryDir(req.MemoryDir, g.finalPath); err != nil {
			return report(), err
		}
	}
	if projectApplies {
		if err := validateTargetUnderMemoryDir(req.MemoryDir, projFinal); err != nil {
			return report(), err
		}
	}

	// --- Step 1: manifest -> distilling ------------------------------------
	if err := writeManifest(StatusDistilling); err != nil {
		return report(), err
	}

	// --- Step 2: per-stage distill (chained per group) ---------------------
	for _, g := range groups {
		seed := g.finalPath
		var lastCandidate string
		var noHigh bool
		for _, st := range g.stages {
			dsPath, ok := datasetPathForStage(datasets, st.ID)
			if !ok {
				return report(), fmt.Errorf("no captured dataset for stage %q", st.ID)
			}
			art, err := p.DistillTarget(ctx, DistillTarget{
				Name:       st.ID,
				Datasets:   []string{dsPath},
				StagingDir: filepath.Join(req.WorkDir, "stages", st.ID, "distill"),
				SeedFrom:   seed,
				MaxRules:   req.Memory.MaxRules,
			})
			if err != nil {
				return report(), err
			}
			seed = art.CandidatePath
			lastCandidate = art.CandidatePath
			noHigh = art.NoHigh
		}
		pending = append(pending, TargetResult{
			Label:         g.file,
			FinalPath:     g.finalPath,
			CandidatePath: lastCandidate,
			NoHigh:        noHigh,
		})
	}

	// --- Step 3: project distill -------------------------------------------
	if projectApplies {
		art, err := p.DistillTarget(ctx, DistillTarget{
			Name:       projectTargetName,
			Datasets:   projectDatasets,
			StagingDir: filepath.Join(req.WorkDir, "flow"),
			SeedFrom:   projFinal,
			MaxRules:   req.Memory.MaxRules,
		})
		if err != nil {
			return report(), err
		}
		pending = append(pending, TargetResult{
			Label:         projectTargetName,
			FinalPath:     projFinal,
			CandidatePath: art.CandidatePath,
			NoHigh:        art.NoHigh,
		})
	}

	// --- Step 4: compute change/diff ---------------------------------------
	for i := range pending {
		if err := computeTargetDiff(&pending[i]); err != nil {
			return report(), err
		}
	}
	for i := range datasets {
		if err := computeDatasetChanged(&datasets[i]); err != nil {
			return report(), err
		}
	}

	// --- Step 5: last interruption point (review #15) ----------------------
	if err := ctx.Err(); err != nil {
		_ = writeManifest(StatusCancelled)
		return report(), err
	}

	// --- Step 6: dry run ----------------------------------------------------
	if req.DryRun {
		if err := writeManifest(StatusDryRun); err != nil {
			return report(), err
		}
		return report(), nil
	}

	// --- Step 7: promotion (review #6/#8) ----------------------------------
	// Once we start, we drive the short rename loop to a consistent state; ctx
	// is no longer consulted. On a mid-promotion error we leave the manifest
	// "promoting" with accurate Published flags (no rollback) so recovery can
	// resume from disk.
	//
	// Only the real publish path needs MemoryDir to exist on disk — dry run
	// (handled above) distills into WorkDir and only READS real targets (a
	// missing target is treated as empty), so it must never create an empty
	// memory dir as a side effect (final-review Fix 1: "writes nothing" must
	// hold literally, including "creates nothing").
	if err := os.MkdirAll(req.MemoryDir, 0755); err != nil {
		return report(), err
	}
	if err := writeManifest(StatusPromoting); err != nil {
		return report(), err
	}
	w := req.writer
	if w == nil {
		w = memory.AtomicWrite
	}

	for i := range pending {
		tr := &pending[i]
		if !tr.Changed {
			continue
		}
		// Final guard immediately before the write (review #5).
		if err := validateTargetUnderMemoryDir(req.MemoryDir, tr.FinalPath); err != nil {
			return report(), err
		}
		if err := backupOldTarget(req.WorkDir, tr); err != nil {
			return report(), err
		}
		cand, err := os.ReadFile(tr.CandidatePath)
		if err != nil {
			return report(), err
		}
		if err := w(tr.FinalPath, cand); err != nil {
			_ = writeManifest(StatusPromoting)
			return report(), err
		}
		tr.Published = true
		if err := writeManifest(StatusPromoting); err != nil {
			return report(), err
		}
	}

	for i := range datasets {
		d := &datasets[i]
		if d.Source != "generated" || !d.Changed {
			continue
		}
		cand, err := os.ReadFile(d.DatasetPath)
		if err != nil {
			return report(), err
		}
		if err := w(d.CanonicalPath, cand); err != nil {
			_ = writeManifest(StatusPromoting)
			return report(), err
		}
		d.Published = true
		if err := writeManifest(StatusPromoting); err != nil {
			return report(), err
		}
	}

	final := StatusPublished
	if req.Meta.EffectiveCommit {
		final = StatusAwaitingCommit
	}
	if err := writeManifest(final); err != nil {
		return report(), err
	}
	return report(), nil
}

// loadManifestIfSet loads the manifest at path, or returns (nil, nil) when
// path is empty (Finalize can run manifest-less in tests).
func loadManifestIfSet(path string) (*Manifest, error) {
	if path == "" {
		return nil, nil
	}
	return LoadManifest(path)
}

// computeTargetDiff fills OldSHA256/NewSHA256/Changed/Diff for one pending
// target by comparing its candidate bytes against the current final file (a
// missing final counts as changed).
func computeTargetDiff(tr *TargetResult) error {
	cand, err := os.ReadFile(tr.CandidatePath)
	if err != nil {
		return err
	}
	tr.NewSHA256 = sha256Hex(cand)

	oldBytes, err := os.ReadFile(tr.FinalPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		tr.Changed = true
	} else {
		tr.OldSHA256 = sha256Hex(oldBytes)
		tr.Changed = !bytes.Equal(oldBytes, cand)
	}
	if tr.Changed {
		tr.Diff = udiff.Unified("old", "new", string(oldBytes), string(cand))
	}
	return nil
}

// computeDatasetChanged marks a generated dataset changed when its candidate
// (staging) bytes differ from the canonical file (a missing canonical counts
// as changed). Reused datasets already point at canonical — nothing to do.
func computeDatasetChanged(d *DatasetResult) error {
	if d.Source != "generated" {
		return nil
	}
	cand, err := os.ReadFile(d.DatasetPath)
	if err != nil {
		return err
	}
	canon, err := os.ReadFile(d.CanonicalPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		d.Changed = true
		return nil
	}
	d.Changed = !bytes.Equal(cand, canon)
	return nil
}

// backupOldTarget copies the current final bytes (if any) into
// <workDir>/backup/<oldSHA>.bak before it is overwritten, so a promotion can
// be inspected/undone. A missing final has nothing to back up.
func backupOldTarget(workDir string, tr *TargetResult) error {
	oldBytes, err := os.ReadFile(tr.FinalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	bak := filepath.Join(workDir, "backup", tr.OldSHA256+".bak")
	return memory.AtomicWrite(bak, oldBytes)
}

// nonEmptyDatasetPaths returns the DatasetPath of each dataset that has at
// least one project_level or session_level item (empties are filtered so the
// project aggregate never runs on nothing, review #11).
func nonEmptyDatasetPaths(datasets []DatasetResult) ([]string, error) {
	var out []string
	for _, d := range datasets {
		empty, err := datasetsAllEmpty([]string{d.DatasetPath})
		if err != nil {
			return nil, err
		}
		if !empty {
			out = append(out, d.DatasetPath)
		}
	}
	return out, nil
}

// datasetPathForStage finds the captured dataset path for a stage id.
func datasetPathForStage(datasets []DatasetResult, stageID string) (string, bool) {
	for _, d := range datasets {
		if d.StageID == stageID {
			return d.DatasetPath, true
		}
	}
	return "", false
}

// validateTargetUnderMemoryDir rejects a final target whose real location
// escapes memoryDir or whose deepest-existing path component is a symlink
// (review #5). memoryDir itself need NOT exist (Fix 1, final review: dry run
// no longer MkdirAll's it) — its deepest-existing ancestor is resolved with
// the same splitAtExisting technique used below for the target's ancestor,
// so a fresh memory.path still containment-checks correctly, and a dangling
// symlink ancestor of memoryDir is still rejected rather than silently
// swallowed.
func validateTargetUnderMemoryDir(memoryDir, target string) error {
	mdExisting, mdMissing := splitAtExisting(memoryDir)
	md, err := filepath.EvalSymlinks(mdExisting)
	if err != nil {
		return fmt.Errorf("resolve memory dir: %w", err)
	}
	for i := len(mdMissing) - 1; i >= 0; i-- {
		md = filepath.Join(md, mdMissing[i])
	}
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target %s is a symlink", target)
	}

	// Resolve the deepest EXISTING ancestor of the target's parent. A failure
	// to EvalSymlinks an ancestor that DOES exist means a dangling/broken
	// symlink component (review #2) — treat it as a containment rejection with
	// a clean diagnostic, rather than letting it fall through to a generic
	// mkdir/ENOENT deep inside AtomicWrite.
	existing, missing := splitAtExisting(filepath.Dir(target))
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return fmt.Errorf("target %s: ancestor %s is an unresolvable (dangling) symlink: %w", target, existing, err)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}

	rel, err := filepath.Rel(md, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("target %s escapes memory dir %s", target, md)
	}
	return nil
}

// splitAtExisting walks up from abs until it hits a path that exists on disk
// (Lstat succeeds — a symlink counts as existing, it is not followed here),
// returning that deepest-existing ancestor plus the trailing components that
// do NOT exist, in reverse order (base-first). The filesystem root always
// exists, so existing is never empty in practice.
func splitAtExisting(abs string) (existing string, missing []string) {
	cur := abs
	for {
		if _, err := os.Lstat(cur); err == nil {
			return cur, missing
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur, missing // reached the root
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

// canonicalizeExistingAncestor walks up from abs until it hits an existing
// path, resolves that with EvalSymlinks (following any symlinked ancestors),
// then re-joins the missing trailing components. It returns abs unchanged if
// EvalSymlinks fails (e.g. a dangling symlink ancestor) — callers that must
// REJECT such a case use validateTargetUnderMemoryDir, which detects the
// EvalSymlinks failure explicitly. This helper lets containment checks see
// through a parent that is a symlink even when the final target file does not
// exist yet. (Defined here; Task 9 reuses it.)
func canonicalizeExistingAncestor(abs string) string {
	existing, missing := splitAtExisting(abs)
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return abs
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return resolved
}

// sha256Hex returns the hex-encoded SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sha256Each returns the hex SHA-256 of each file in paths, positionally
// aligned with paths.
func sha256Each(paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out[i] = sha256Hex(data)
	}
	return out, nil
}
