package schemacheck_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/pkg/pipeline"
)

const examplePackageDir = "../../examples/pipeline-package"

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(examplePackageDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// snapshotExample publishes the example into a temporary cache; snapshots are read-only, so cleanup unlocks them.
func snapshotExample(t *testing.T) pipeline.Loaded {
	t.Helper()
	m, err := pipeline.LoadManifest(examplePackageDir)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(cache, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755) //nolint:gosec // G122: cleanup of a t.TempDir() this test owns
			}
			return nil
		})
	})
	snap, err := pipeline.BuildSnapshot(examplePackageDir, m, cache)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := pipeline.LoadSnapshot(snap.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestPipelineExampleMatchesSchemas(t *testing.T) {
	if err := compileSchema(t, pipelineSchemaPath).Validate(jsonInstance(t, readExample(t, "pipeline.yaml"))); err != nil {
		t.Errorf("pipeline.yaml does not match the pipeline schema: %v", err)
	}
	configSchema := compileSchema(t, configSchemaPath)
	for _, name := range []string{"agents.yaml", "pipeline-options.example.yaml"} {
		if err := configSchema.Validate(jsonInstance(t, readExample(t, name))); err != nil {
			t.Errorf("%s does not match the config schema: %v", name, err)
		}
	}
}

func TestPipelineExampleLoadsFromSnapshot(t *testing.T) {
	loaded := snapshotExample(t)
	if loaded.Manifest.Name != "demo-review" || loaded.Flow.Name != "demo-review" {
		t.Fatalf("manifest %q / flow %q, want demo-review", loaded.Manifest.Name, loaded.Flow.Name)
	}
	if want := filepath.Join(loaded.Snapshot.Dir, "skills"); string(loaded.Flow.SkillsDir) != want {
		t.Errorf("flow skills_dir = %q, want %q", loaded.Flow.SkillsDir, want)
	}
	for _, role := range loaded.Manifest.Roles {
		if _, ok := loaded.Layer.Agents[role.Command]; !ok {
			t.Errorf("role command %q has no package recipe", role.Command)
		}
	}
}

func TestPipelineExampleOptionsSwitchClient(t *testing.T) {
	loaded := snapshotExample(t)
	optionsDir := examplePackageDir
	opts, err := pipeline.ParseLayer(readExample(t, "pipeline-options.example.yaml"), pipeline.OriginOptions, optionsDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, prov, _, err := pipeline.Assemble(config.Config{}, loaded.Layer, &opts, loaded.Snapshot.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Docker.Agents["demo-review-writer"].Type; got != config.RecipeTypeCodex {
		t.Errorf("writer type = %q, want codex", got)
	}
	if got := cfg.Docker.Agents["demo-review-reviewer"].Type; got != "" {
		t.Errorf("reviewer recipe must stay the package default, got type %q", got)
	}
	if prov.Agents["demo-review-writer"] != pipeline.OriginOptions || prov.Agents["demo-review-reviewer"] != pipeline.OriginPackage {
		t.Errorf("unexpected provenance: %+v", prov.Agents)
	}
	if got := cfg.Collaboration.Profiles["demo-review-reviewer"].Command; got != "claude" {
		t.Errorf("reviewer profile command = %q, want the user's existing claude", got)
	}
}

func TestPipelineExampleScriptChecksNotes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	script, err := filepath.Abs(filepath.Join(examplePackageDir, "scripts", "check-notes.sh"))
	if err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(t.TempDir(), "RELEASE_NOTES.md")
	run := func() error {
		cmd := exec.Command(bash, script, notes)
		cmd.Env = append(os.Environ(), "AFM_PIPELINE_DIR=/snapshot")
		return cmd.Run()
	}
	if err := run(); err == nil {
		t.Error("a missing file must fail the check")
	}
	if err := os.WriteFile(notes, []byte("# Release notes\n\nTODO\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Error("TODO must fail the check")
	}
	if err := os.WriteFile(notes, []byte("# Release notes\n\nFixed Y.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Errorf("good notes must pass: %v", err)
	}
}
