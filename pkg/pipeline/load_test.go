package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const loadFlowYAML = `name: demo
prompt: "keep $AFM_PIPELINE_DIR and skills/ as text"
skills_dir: skills
team:
  - {id: w, profile: p, title: W, skills_dir: skills}
stages:
  - id: s1
    name: S1
    agents: [planning, implementation]
    prompt: "run scripts/run.sh"
    skills_dir: skills
    assistants:
      - {profile: demo-pipe-writer, skills_dir: skills}
  - id: s2
    name: S2
    script: echo done
`

func loadablePackage(t *testing.T, flowYAML string) (Snapshot, Manifest) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, ManifestFile, "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\nagents: agents.yaml\nresources: [skills]\n")
	writeFile(t, root, "flow.yaml", flowYAML)
	writeFile(t, root, "agents.yaml", "collaboration:\n  profiles:\n    demo-pipe-writer: {title: W}\n")
	writeFile(t, root, "skills/a/SKILL.md", "skill")
	m, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := BuildSnapshot(root, m, cacheDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return snap, m
}

func TestLoadFlowPinsSkillsDirsInsideSnapshot(t *testing.T) {
	snap, m := loadablePackage(t, loadFlowYAML)
	f, err := LoadFlow(snap, m)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(snap.Dir, "skills")
	got := []string{string(f.SkillsDir), string(f.Team[0].SkillsDir), string(f.Stages[0].SkillsDir), string(f.Stages[0].Assistants[0].SkillsDir)}
	for i, g := range got {
		if g != want {
			t.Errorf("skills_dir #%d = %q, want %q", i, g, want)
		}
	}
	if f.Stages[1].SkillsDir != "" {
		t.Errorf("an unset skills_dir must stay unset, got %q", f.Stages[1].SkillsDir)
	}
	if f.Prompt != "keep $AFM_PIPELINE_DIR and skills/ as text" || f.Stages[0].Prompt != "run scripts/run.sh" {
		t.Errorf("prompt text was rewritten: %q / %q", f.Prompt, f.Stages[0].Prompt)
	}
}

func TestLoadFlowMissingSkillsDirIsAnError(t *testing.T) {
	snap, m := loadablePackage(t, "name: demo\nstages:\n  - {id: s, name: S, agents: [planning, implementation], skills_dir: nope}\n")
	if _, err := LoadFlow(snap, m); err == nil || !strings.Contains(err.Error(), "stage s skills_dir") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadFlowRejectsSkillsDirEscapingPackage(t *testing.T) {
	snap, m := loadablePackage(t, "name: demo\nstages:\n  - {id: s, name: S, agents: [planning, implementation], skills_dir: ../elsewhere}\n")
	if _, err := LoadFlow(snap, m); err == nil || !strings.Contains(err.Error(), "leaves the package") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadSnapshotRoundTrip(t *testing.T) {
	snap, _ := loadablePackage(t, loadFlowYAML)
	loaded, err := LoadSnapshot(snap.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Manifest.Name != "demo-pipe" || loaded.Flow.Name != "demo" || loaded.Layer.Profiles["demo-pipe-writer"].Title != "W" || loaded.Snapshot != snap {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestLoadSnapshotRejectsTamperedSnapshot(t *testing.T) {
	snap, _ := loadablePackage(t, loadFlowYAML)
	if err := os.Chmod(snap.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(snap.Dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap.Dir, "skills", "extra.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(snap.Dir); err == nil {
		t.Fatal("expected a hash mismatch")
	}
}
