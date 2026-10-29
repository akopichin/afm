package main

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/skills"
	"github.com/akopichin/afm/pkg/state"
)

// withSkillLibraries settles skills_dir for every stage and member on copies: the nearest explicit value
// (stage or member, flow, config) wins whole and a relative path is taken from base. The results are frozen
// into the registry and the agents' tasks, which keeps a resumed run on the library it started with.
func withSkillLibraries(stages []flow.Stage, team flow.Team, f *flow.Flow, cfg config.Config, base string) ([]flow.Stage, flow.Team) {
	settle := func(own skills.Dir) skills.Dir {
		return skills.Dir(skills.FirstDir(own, f.SkillsDir, cfg.SkillsDir).Resolve(base))
	}
	stages = slices.Clone(stages)
	for i := range stages {
		stages[i].SkillsDir = settle(stages[i].SkillsDir)
	}
	team = slices.Clone(team)
	for i := range team {
		team[i].SkillsDir = settle(team[i].SkillsDir)
	}
	return stages, team
}

// settleSkillRun resolves skills_dir for the stages and the team, checks the libraries the run is about to
// read and only then freezes the stage registry. A mistyped path of a new run is thus never saved, and the
// next `afm run` takes the corrected flow.yaml. A resumed run is checked against its saved registry.
func settleSkillRun(runDir string, stages []flow.Stage, f *flow.Flow, cfg config.Config, base string, snap state.RunState) ([]flow.Stage, flow.Team, error) {
	fresh, team := withSkillLibraries(stages, f.Team, f, cfg, base)
	saved, found, err := graph.LoadRegistry(runDir)
	if err != nil {
		return nil, nil, fmt.Errorf("restore stage registry: %w", err)
	}
	planned := &savedRun{state: snap, stages: fresh}
	if found {
		planned.stages = saved
	}
	plannedStages, plannedTeam := plannedSkillRun(f, cfg, base, planned)
	if err := checkSkillLibraries(plannedStages, plannedTeam); err != nil {
		return nil, nil, err
	}
	if config.ReExecedIntoContainer() {
		if err := checkedByHost(skillLibraryDirs(plannedStages, plannedTeam)); err != nil {
			return nil, nil, err
		}
	}
	stages, err = graph.RestoreOrPersist(runDir, fresh)
	if err != nil {
		return nil, nil, fmt.Errorf("restore stage registry: %w", err)
	}
	return stages, team, nil
}

// checkSkillLibraries reads every library the run is about to use, so a missing or invalid one stops the run
// before any agent starts instead of failing its stage later. A path must be absolute and clean, as Resolve
// leaves it: a saved value an agent could write ("~", relative) would be read from the CWD but mounted elsewhere.
func checkSkillLibraries(stages []flow.Stage, team flow.Team) error {
	checked := map[skills.Dir]bool{}
	check := func(who string, dir skills.Dir) error {
		if dir == "" || checked[dir] {
			return nil
		}
		checked[dir] = true
		if !filepath.IsAbs(string(dir)) || filepath.Clean(string(dir)) != string(dir) {
			return fmt.Errorf("%s: skills_dir %q must be a clean absolute path", who, dir)
		}
		if err := orchestrator.CheckSkillLibrary(string(dir)); err != nil {
			return fmt.Errorf("%s: %w", who, err)
		}
		return nil
	}
	for _, s := range stages {
		if err := check(fmt.Sprintf("stage %q", s.ID), s.SkillsDir); err != nil {
			return err
		}
	}
	for _, m := range team {
		if err := check(fmt.Sprintf("team member %q", m.ID), m.SkillsDir); err != nil {
			return err
		}
	}
	return nil
}

// unfinishedStages drops the stages already done in snap: a library a finished stage used no longer matters,
// so it must not stop the rest of the run from resuming.
func unfinishedStages(stages []flow.Stage, snap state.RunState) []flow.Stage {
	return slices.DeleteFunc(slices.Clone(stages), func(s flow.Stage) bool {
		return snap.Stages[s.ID].Status == state.StatusDone
	})
}
