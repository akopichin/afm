package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/skills"
)

func TestCheckReloadCompatible_ContainerNeedsRestartForANewSkillLibrary(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	chdirTemp(t)
	prev := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s"}}}
	same := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: "skills"}}}
	external := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: "/shared/skills"}}}

	for name, next := range map[string]*flow.Flow{"inside the project": same, "outside the project": external} {
		err := compatLoop(config.Config{}).checkReloadCompatible(prev, next)
		if err == nil || !strings.Contains(err.Error(), "restart `afm run`") {
			t.Fatalf("%s: err = %v, want a restart error: the host never checked this library", name, err)
		}
	}
}

// The container cannot see a link's target before it is mounted, so a new linked library must still ask for a restart.
func TestCheckReloadCompatible_ContainerRestartsForANewLinkedLibrary(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	dir := chdirTemp(t)
	external := mkdirAll(t, filepath.Join(t.TempDir(), "other"))
	symlinkTo(t, external, filepath.Join(dir, "skills2"))
	if err := os.Remove(external); err != nil { // not mounted in the container
		t.Fatal(err)
	}
	prev := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s"}}}
	next := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: "skills2"}}}

	err := compatLoop(config.Config{}).checkReloadCompatible(prev, next)
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("err = %v, want a restart error", err)
	}
}

// Only the host can tell whether a path leads to the same library on both sides, so a repeat cycle in the
// container takes exactly the libraries the host checked at re-exec; a directory that merely exists there
// (created by a stage, or from the image) is not one of them.
func TestCheckReloadCompatible_ContainerTakesOnlyHostCheckedLibraries(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	chdirTemp(t)
	shared := mkdirAll(t, filepath.Join(t.TempDir(), "shared"))
	sub := mkdirAll(t, filepath.Join(shared, "sub"))
	notMounted := filepath.Join(t.TempDir(), "later")
	createdInContainer := mkdirAll(t, filepath.Join(t.TempDir(), "newskills"))
	flowWith := func(dir string) *flow.Flow {
		return &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: skills.Dir(dir)}}}
	}

	t.Run("a path below a checked library is not the checked library", func(t *testing.T) {
		checkedAtReExec(t, shared)
		if err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(sub)); err == nil {
			t.Fatal("/shared → /shared/sub: want restart")
		}
	})
	t.Run("a library the host never checked stays a restart even when the flow did not change", func(t *testing.T) {
		checkedAtReExec(t, shared)
		err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(notMounted), flowWith(notMounted))
		if err == nil || !strings.Contains(err.Error(), notMounted) || !strings.Contains(err.Error(), "restart `afm run`") {
			t.Fatalf("err = %v, want a restart error naming %s", err, notMounted)
		}
	})
	t.Run("a directory that exists in the container but was never checked is a restart", func(t *testing.T) {
		checkedAtReExec(t, shared)
		err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(createdInContainer))
		if err == nil || !strings.Contains(err.Error(), createdInContainer) || !strings.Contains(err.Error(), "restart `afm run`") {
			t.Fatalf("err = %v, want a restart error naming %s", err, createdInContainer)
		}
	})
	t.Run("the same checked library is fine", func(t *testing.T) {
		checkedAtReExec(t, shared)
		if err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(shared)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an extra mount covering a path does not make it checked", func(t *testing.T) {
		checkedAtReExec(t, shared)
		cfg := config.Config{Docker: config.DockerConfig{ExtraMounts: config.ExtraMounts{{Path: shared}}}}
		if err := compatLoop(cfg).checkReloadCompatible(flowWith(shared), flowWith(sub)); err == nil {
			t.Fatal("want a restart: only the host's verdict counts")
		}
	})
	t.Run("without the host's list every library is a restart, none is fine", func(t *testing.T) {
		t.Setenv(docker.SkillLibrariesEnvVar, "")
		if err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(shared)); err == nil {
			t.Fatal("a library with no host verdict must restart")
		}
		none := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s"}}}
		if err := compatLoop(config.Config{}).checkReloadCompatible(none, none); err != nil {
			t.Fatalf("no library, nothing to check: %v", err)
		}
	})
}
