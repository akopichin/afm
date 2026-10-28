package main

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/fsutil"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/skills"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/worktree"
)

// effectiveRoot is the project root every relative skills_dir is taken from: the worktree, else root_dir
// from the afm root, else the afm root. It is not the agents' CWD, which stays empty without root_dir.
func effectiveRoot(afmRoot, worktreeDir string, f *flow.Flow) (string, error) {
	if worktreeDir != "" {
		return filepath.Abs(worktreeDir)
	}
	return worktreeBase(afmRoot, f)
}

// skillLibraryDirs lists the distinct libraries of stages and members, sorted. Stages and members are
// expected to carry resolved (absolute) skills_dir values.
func skillLibraryDirs(stages []flow.Stage, team flow.Team) []string {
	dirs := map[string]bool{}
	for _, s := range stages {
		if s.SkillsDir != "" {
			dirs[string(s.SkillsDir)] = true
		}
	}
	for _, m := range team {
		if m.SkillsDir != "" {
			dirs[string(m.SkillsDir)] = true
		}
	}
	return slices.Sorted(maps.Keys(dirs))
}

// sameSpellingDests are the Docker destinations that show the host directory of the same path, so the host can
// read them for the container: the project and the extra mounts except "~" ones under a home other than the container's.
func sameSpellingDests(project, home string, extra config.ExtraMounts) []string {
	dests := []string{project}
	for _, m := range extra {
		inContainer := docker.ExtraMountContainerPaths(project, config.ExtraMounts{m})[0]
		if docker.ResolveMountPath(project, m.Path, home) == inContainer {
			dests = append(dests, inContainer)
		}
	}
	return dests
}

// skillMountDirs picks the read-only mounts the libraries need. The container sees only the Docker mount
// destinations and follows a link by its literal target, so each library is walked that way and the first path
// no destination covers is mounted at itself. Nothing else is read: the host and the container agree.
func skillMountDirs(libs, dests []string) []string {
	dests = slices.Clone(dests)
	var mounts []string
	for _, lib := range slices.Compact(slices.Sorted(slices.Values(libs))) {
		for dir := firstUnmounted(lib, dests); dir != ""; dir = firstUnmounted(lib, dests) {
			dests = append(dests, dir)
			mounts = append(mounts, dir)
		}
	}
	slices.Sort(mounts)
	return mounts
}

// checkSkillLibrariesSame: preflight read each library through the host's links, while the container follows
// them from the mount destinations. A project reached through a linked ancestor can make these end in two
// different directories, and the run would then use a library nobody checked.
func checkSkillLibrariesSame(libs, dests []string) error {
	for _, lib := range libs {
		inContainer := containerEnd(lib, dests)
		if canonicalOrSelf(inContainer) != canonicalOrSelf(lib) {
			return fmt.Errorf("skills_dir %s leads to %s inside the container, a different directory than on the host; point skills_dir at the library itself", lib, inContainer)
		}
	}
	return nil
}

// containerEnd is where path ends inside a container that has only dests: every link followed by its literal
// target, until none is left or the walk cannot go on.
func containerEnd(path string, dests []string) string {
	for range maxLinkHops {
		dest := longestCovering(dests, path)
		if dest == "" {
			return path
		}
		next, linked := followFirstLink(dest, path)
		if !linked {
			return path
		}
		path = next
	}
	return path
}

// maxLinkHops bounds a chain of links, which may loop.
const maxLinkHops = 40

// firstUnmounted follows path inside the container and returns the first path no destination covers, or ""
// when the whole path is visible. A path that cannot be read ends the walk: the library check reports it.
func firstUnmounted(path string, dests []string) string {
	end := containerEnd(path, dests)
	if longestCovering(dests, end) == "" {
		return end
	}
	return ""
}

func longestCovering(dests []string, path string) string {
	var best string
	for _, dest := range dests {
		if isSubPath(dest, path) && len(dest) > len(best) {
			best = dest
		}
	}
	return best
}

// followFirstLink walks path below dest and returns it with the first link replaced by that link's target.
func followFirstLink(dest, path string) (string, bool) {
	rel, err := filepath.Rel(dest, path)
	if err != nil || rel == "." {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := dest
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return "", false
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(cur)
		if err != nil {
			return "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cur), target)
		}
		return filepath.Join(append([]string{target}, parts[i+1:]...)...), true
	}
	return "", false
}

// checkSkillMounts is the host-side trust rule for mounts: flow.yaml may only ask for a library the user
// allowed in config (skills_dir or docker.extra_mounts), and never for a path that exposes the whole
// filesystem, the home directory or the project's parents. Paths are judged after resolving symlinks.
func checkSkillMounts(dirs []string, project, home string, cfg config.Config, base string) error {
	allowed := allowedSkillRoots(cfg, project, home, base)
	for _, dir := range dirs {
		// Docker gets this very string, and ResolveMountPath would read "~" or a relative one differently.
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("skills_dir %s: a Docker mount source must be an absolute path", dir)
		}
		if strings.Contains(dir, ":") {
			return fmt.Errorf("skills_dir %s: ':' cannot be part of a Docker mount path", dir)
		}
		// A source missing on the host would be created by `docker -v` as an empty root-owned directory.
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return fmt.Errorf("skills_dir mount source %s is not an existing directory on the host; a link on the way to skills_dir resolves differently inside the container, so point skills_dir at the library itself", dir)
		}
		resolved, err := fsutil.CanonicalPath(dir)
		if err != nil {
			return fmt.Errorf("skills_dir %s: %w", dir, err)
		}
		if exposesTooMuch(dir, project, home) || exposesTooMuch(resolved, canonicalOrSelf(project), canonicalOrSelf(home)) {
			return fmt.Errorf("skills_dir %s is too broad to mount into Docker (the filesystem root, the home directory or a parent of the project); point it at the skill library itself", dir)
		}
		insideProject := isSubPath(canonicalOrSelf(project), resolved) // a link back into the project exposes nothing new
		if !insideProject && !slices.ContainsFunc(allowed, func(root string) bool { return isSubPath(root, resolved) }) {
			return fmt.Errorf("skills_dir %s is outside the project and is not allowed to be mounted into Docker; to allow it, set skills_dir: %s in config.yaml or add it to docker.extra_mounts", dir, dir)
		}
	}
	return nil
}

// allowedSkillRoots are the canonical directories the user has chosen for external mounts, minus those
// too broad to mount at all.
func allowedSkillRoots(cfg config.Config, project, home, base string) []string {
	var roots []string
	if cfg.SkillsDir != "" {
		roots = append(roots, cfg.SkillsDir.Resolve(base))
	}
	for _, m := range cfg.Docker.ExtraMounts {
		roots = append(roots, filepath.Clean(docker.ResolveMountPath(project, m.Path, home)))
	}
	project, home = canonicalOrSelf(project), canonicalOrSelf(home)
	var allowed []string
	for _, root := range roots {
		root = canonicalOrSelf(root)
		if !exposesTooMuch(root, project, home) { // a broad root would let flow.yaml mount ~/.ssh
			allowed = append(allowed, root)
		}
	}
	return allowed
}

func canonicalOrSelf(path string) string {
	if resolved, err := fsutil.CanonicalPath(path); err == nil {
		return resolved
	}
	return path
}

// exposesTooMuch reports a mount source that is the filesystem root or holds the home directory or the project.
func exposesTooMuch(path, project, home string) bool {
	return filepath.Dir(path) == path || isSubPath(path, home) || isSubPath(path, project)
}

// savedRun is what a resumed run was started with, read from its journal and stage registry.
type savedRun struct {
	state  state.RunState
	stages []flow.Stage // the frozen registry; nil when the run has none yet
}

// savedRunFor is the run a start in this clear mode would resume: a clear flag starts a fresh run, so there
// is nothing saved to read.
func savedRunFor(mode worktree.ClearMode, runsDir, runName string, f *flow.Flow) (*savedRun, error) {
	if mode != worktree.ClearNone {
		return nil, nil
	}
	return loadSavedRun(runsDir, runName, f)
}

// loadSavedRun finds the run `afm run` would resume (the latest unfinished one under runName) and reads it
// read-only, without the writer lock. nil means a new run, also for an unreadable journal. A run held by
// another live afm is not told apart: resolveRun starts a new one then, at worst a mount is not needed.
func loadSavedRun(runsDir, runName string, f *flow.Flow) (*savedRun, error) {
	runDir, err := state.FindLatestRunDir(runsDir, runName)
	if err != nil {
		return nil, nil // no earlier run
	}
	rs, err := state.LoadRunState(runDir)
	if err != nil || !hasUnfinishedStage(rs, f.Stages) {
		return nil, nil // resolveRun starts a new run for these
	}
	stages, _, err := graph.LoadRegistry(runDir)
	if err != nil {
		return nil, fmt.Errorf("restore stage registry: %w", err)
	}
	return &savedRun{state: rs, stages: stages}, nil
}

// hasUnfinishedStage mirrors resolveRun: a stage the journal has not seen yet counts as pending.
func hasUnfinishedStage(rs state.RunState, stages []flow.Stage) bool {
	for _, s := range stages {
		if rs.Stages[s.ID].Status != state.StatusDone {
			return true
		}
	}
	return !rs.AllDone()
}

// plannedSkillRun returns the stages and members whose libraries the run about to start will read. A new
// run uses the declaration. A resumed one keeps its saved registry and live saved agents, and takes the
// declaration only for a member whose agent has not been created yet.
func plannedSkillRun(f *flow.Flow, cfg config.Config, base string, saved *savedRun) ([]flow.Stage, flow.Team) {
	stages, team := withSkillLibraries(f.Stages, f.Team, f, cfg, base)
	if saved != nil {
		if saved.stages != nil {
			stages = saved.stages
		}
		stages = unfinishedStages(stages, saved.state)
	}
	if !resolveCollaboration(cfg, f).Enabled {
		return stages, nil // without collaboration no team agent is added or woken
	}
	if saved == nil {
		return stages, team
	}
	team = slices.DeleteFunc(team, func(m flow.TeamMember) bool {
		return orchestrator.TeamMemberSaved(saved.state, m.ID)
	})
	for _, id := range slices.Sorted(maps.Keys(saved.state.Assistants.Tasks)) {
		task := saved.state.Assistants.Tasks[id]
		if task.SkillsDir != "" && !task.Status.Terminal() {
			team = append(team, flow.TeamMember{ID: cmp.Or(task.Title, string(id)), SkillsDir: skills.Dir(task.SkillsDir)})
		}
	}
	return stages, team
}

// declaredSkillLibraries are the existing libraries of the flow as declared now. A resumed run reads the
// saved ones, but a repeat cycle starts a new run from the declaration, and the container cannot be given
// a mount later. A library that is missing is left out: it fails the cycle that needs it, not this resume.
func declaredSkillLibraries(f *flow.Flow, cfg config.Config, base string) []string {
	return slices.DeleteFunc(skillLibraryDirs(plannedSkillRun(f, cfg, base, nil)), func(dir string) bool {
		info, err := os.Stat(dir)
		return err != nil || !info.IsDir()
	})
}

// prepareSkillMounts is the host preflight before the Docker re-exec: every library must be readable (Docker
// would create a missing source as a root-owned directory) and the external ones become read-only mounts. It
// returns the only libraries the container may read without a restart: just the host can tell they are the same.
func prepareSkillMounts(afmRoot, worktreeDir, home string, f *flow.Flow, cfg config.Config, saved *savedRun) ([]config.ExtraMount, []string, error) {
	project, err := filepath.Abs(afmRoot)
	if err != nil {
		return nil, nil, err
	}
	base, err := effectiveRoot(project, worktreeDir, f)
	if err != nil {
		return nil, nil, err
	}
	stages, team := plannedSkillRun(f, cfg, base, saved)
	if err := checkSkillLibraries(stages, team); err != nil {
		return nil, nil, err
	}
	libs := skillLibraryDirs(stages, team)
	if saved != nil {
		libs = append(libs, declaredSkillLibraries(f, cfg, base)...)
	}
	libs = slices.Compact(slices.Sorted(slices.Values(libs)))
	// The list travels as one PATH-style env value, so a ':' would split a library into two "checked" ones.
	if i := slices.IndexFunc(libs, func(lib string) bool { return strings.ContainsRune(lib, os.PathListSeparator) }); i >= 0 {
		return nil, nil, fmt.Errorf("skills_dir %s: ':' cannot be part of a library path under Docker", libs[i])
	}
	dests := sameSpellingDests(project, home, cfg.Docker.ExtraMounts)
	dirs := skillMountDirs(libs, dests)
	if err := checkSkillMounts(dirs, project, home, cfg, base); err != nil {
		return nil, nil, err
	}
	if err := checkSkillLibrariesSame(libs, slices.Concat(dests, dirs)); err != nil {
		return nil, nil, err
	}
	mounts := make([]config.ExtraMount, len(dirs))
	for i, dir := range dirs {
		mounts[i] = config.ExtraMount{Path: dir}
	}
	return mounts, libs, nil
}
