package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/skills"
)

// The host preflights cycle 1 and hands its libraries to the container; the container then reloads the next
// flow. Whatever the reload accepts a fresh host preflight must accept too, so a symlinked project ancestor
// cannot make the next cycle read a library nobody checked.
func TestReloadAgainstHostVerdict(t *testing.T) {
	type tc struct {
		prev, next  func(root, project string) *flow.Flow
		cfg         func(root string) config.Config
		wantRestart bool
	}
	lib := func(dir string) *flow.Flow {
		return &flow.Flow{Name: "f", SkillsDir: skills.Dir(dir), Stages: []flow.Stage{{ID: "a"}}}
	}
	none := func(root string) config.Config { return config.Config{} }
	for name, c := range map[string]tc{
		"r5 f1: relative link resolves to A on the host, B in the container": {
			prev: func(root, p string) *flow.Flow { return lib(filepath.Join(root, "shared")) },
			next: func(root, p string) *flow.Flow { return lib("skills") },
			cfg: func(root string) config.Config {
				return config.Config{SkillsDir: skills.Dir(filepath.Join(root, "shared"))}
			},
			wantRestart: true,
		},
		"unchanged flow with a project-internal library": {
			prev: func(root, p string) *flow.Flow { return lib("inner") },
			next: func(root, p string) *flow.Flow { return lib("inner") },
			cfg:  none,
		},
		"unchanged flow whose library is a link out (mounted by its target)": {
			prev: func(root, p string) *flow.Flow { return lib("out") },
			next: func(root, p string) *flow.Flow { return lib("out") },
			cfg: func(root string) config.Config {
				return config.Config{SkillsDir: skills.Dir(filepath.Join(root, "ext"))}
			},
		},
		"new project-internal library in a later cycle": {
			prev:        func(root, p string) *flow.Flow { return lib("inner") },
			next:        func(root, p string) *flow.Flow { return lib("inner2") },
			cfg:         none,
			wantRestart: true,
		},
		"new library inside an extra mount": {
			prev: func(root, p string) *flow.Flow { return lib("inner") },
			next: func(root, p string) *flow.Flow { return lib(filepath.Join(root, "ext", "lib")) },
			cfg: func(root string) config.Config {
				return config.Config{Docker: config.DockerConfig{ExtraMounts: config.ExtraMounts{{Path: filepath.Join(root, "ext")}}}}
			},
			wantRestart: true,
		},
		"nested path below a checked library": {
			prev: func(root, p string) *flow.Flow { return lib(filepath.Join(root, "nest")) },
			next: func(root, p string) *flow.Flow { return lib(filepath.Join(root, "nest", "inner")) },
			cfg: func(root string) config.Config {
				return config.Config{SkillsDir: skills.Dir(filepath.Join(root, "nest"))}
			},
			wantRestart: true,
		},
		"library removed": {
			prev: func(root, p string) *flow.Flow { return lib("inner") },
			next: func(root, p string) *flow.Flow { return &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "a"}}} },
			cfg:  none,
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			realParent := mkdirAll(t, filepath.Join(root, "real", "dir"))
			realProject := mkdirAll(t, filepath.Join(realParent, "proj"))
			writeCLISkill(t, mkdirAll(t, filepath.Join(root, "real", "shared")), "a")
			writeCLISkill(t, mkdirAll(t, filepath.Join(root, "shared")), "b")
			writeCLISkill(t, mkdirAll(t, filepath.Join(root, "ext", "lib")), "e")
			writeCLISkill(t, mkdirAll(t, filepath.Join(realProject, "inner")), "i")
			writeCLISkill(t, mkdirAll(t, filepath.Join(root, "nest")), "inner")
			writeCLISkill(t, filepath.Join(root, "nest", "inner"), "x")
			writeCLISkill(t, mkdirAll(t, filepath.Join(realProject, "inner2")), "i2")
			symlinkTo(t, "../../shared", filepath.Join(realProject, "skills"))
			symlinkTo(t, filepath.Join(root, "ext", "lib"), filepath.Join(realProject, "out"))
			alias := symlinkTo(t, realParent, filepath.Join(root, "alias"))
			project := filepath.Join(alias, "proj")
			home := mkdirAll(t, filepath.Join(root, "home"))
			cfg := c.cfg(root)
			prev, next := c.prev(root, project), c.next(root, project)

			_, checked, err := prepareSkillMounts(project, "", home, prev, cfg, nil)
			if err != nil {
				t.Fatalf("host preflight of cycle 1: %v", err)
			}
			old := rootDir
			rootDir = project
			t.Cleanup(func() { rootDir = old })
			t.Setenv("AFM_IN_DOCKER", "1")
			t.Setenv(docker.SkillLibrariesEnvVar, strings.Join(checked, string(os.PathListSeparator)))
			err = compatLoop(cfg).checkReloadCompatible(prev, next)
			gotRestart := err != nil && strings.Contains(err.Error(), "restart `afm run`")
			if gotRestart != c.wantRestart {
				t.Fatalf("reload err = %v, want restart = %t (checked %v)", err, c.wantRestart, checked)
			}
			if c.wantRestart {
				return
			}
			// What reload accepts, a fresh host preflight accepts too.
			if _, _, err := prepareSkillMounts(project, "", home, next, cfg, nil); err != nil {
				t.Fatalf("reload accepted a flow the host rejects: %v", err)
			}
		})
	}
}
