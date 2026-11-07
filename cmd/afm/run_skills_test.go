package main

import (
	"path/filepath"
	"strings"
	"testing"

	implgraph "github.com/akopichin/afm-engine/testkit/orchestrator/graph"
	"github.com/akopichin/afm/contracts/skills"
)

// executeFlow settles skills_dir before it starts any stage: the registry freezes the absolute library, and a
// missing one stops the run with no stage started.
func TestExecuteFlow_SettlesAndChecksSkillLibraries(t *testing.T) {
	t.Run("a library is frozen as an absolute path", func(t *testing.T) {
		fx := newCycleFixture(t)
		lib := mkdirAll(t, filepath.Join(fx.root, "lib"))
		writeCLISkill(t, lib, "tdd")
		fx.write("flow.yaml", "skills_dir: lib\n"+fx.flowYAML(flowSpec{}))

		if err := fx.runWithTimeout(testCycleConfig()); err != nil {
			t.Fatal(err)
		}

		saved, found, err := implgraph.LoadRegistry(filepath.Join(fx.root, ".afm", "runs", fx.runDirs()[0]))
		if err != nil || !found || saved[0].SkillsDir != skills.Dir(lib) {
			t.Fatalf("registry = %+v, found %v, err %v; want %s", saved, found, err, lib)
		}
	})
	t.Run("a missing library stops the run before any stage starts", func(t *testing.T) {
		fx := newCycleFixture(t)
		fx.write("flow.yaml", "skills_dir: nope\n"+fx.flowYAML(flowSpec{}))

		err := fx.runWithTimeout(testCycleConfig())

		if err == nil || !strings.Contains(err.Error(), "nope") || fx.ticks() != 0 {
			t.Fatalf("err = %v, ticks = %d; want an error naming nope and no stage started", err, fx.ticks())
		}
	})
}
