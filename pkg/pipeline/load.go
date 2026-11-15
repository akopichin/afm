package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/skills"
)

// parentDir is the path element that leaves a package root.
const parentDir = ".."

const maxAgentsFileBytes = 1 << 20

// Loaded is a verified snapshot read back into memory: what the host and the container both assemble a run from.
type Loaded struct {
	Snapshot Snapshot
	Manifest Manifest
	Flow     *flow.Flow
	Layer    Layer // the package agent layer, empty when the manifest names no agents file
}

// LoadSnapshot verifies the snapshot at dir against its hash and reads manifest, package layer and flow from it.
func LoadSnapshot(dir string) (Loaded, error) {
	sum, err := VerifySnapshot(dir)
	if err != nil {
		return Loaded{}, err
	}
	snap := Snapshot{Dir: dir, SHA256: sum}
	m, err := LoadManifest(dir)
	if err != nil {
		return Loaded{}, err
	}
	layer, err := LoadPackageLayer(snap, m)
	if err != nil {
		return Loaded{}, err
	}
	f, err := LoadFlow(snap, m)
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{Snapshot: snap, Manifest: m, Flow: f, Layer: layer}, nil
}

// LoadPackageLayer reads and validates the package's agents file from the snapshot.
func LoadPackageLayer(snap Snapshot, m Manifest) (Layer, error) {
	if m.Agents == "" {
		layer, _ := ParseLayer(nil, OriginPackage, snap.Dir)
		return layer, layer.ValidatePackage(m)
	}
	f, err := os.Open(filepath.Join(snap.Dir, filepath.FromSlash(m.Agents)))
	if err != nil {
		return Layer{}, fmt.Errorf("package agents file: %w", err)
	}
	defer f.Close()
	data, err := ReadLimited(f, maxAgentsFileBytes)
	if err != nil {
		return Layer{}, fmt.Errorf("package agents file %s: %w", m.Agents, err)
	}
	layer, err := ParseLayer(data, OriginPackage, snap.Dir)
	if err != nil {
		return Layer{}, fmt.Errorf("package agents file %s: %w", m.Agents, err)
	}
	if err := layer.ValidatePackage(m); err != nil {
		return Layer{}, fmt.Errorf("package agents file %s: %w", m.Agents, err)
	}
	return layer, nil
}

// LoadFlow parses the entrypoint from the snapshot and anchors every relative skills_dir at the snapshot, so the
// libraries resolve the same way at start, on repeat cycles and in a container. Prompt texts, root_dir, memory and
// artifact paths keep their ordinary meaning.
func LoadFlow(snap Snapshot, m Manifest) (*flow.Flow, error) {
	f, err := flow.ParseFile(filepath.Join(snap.Dir, filepath.FromSlash(m.Entrypoint)))
	if err != nil {
		return nil, fmt.Errorf("parse flow: %w", err)
	}
	pin := func(at string, dir *skills.Dir) error {
		if *dir == "" {
			return nil
		}
		abs, err := pinSkillsDir(snap.Dir, string(*dir))
		if err != nil {
			return fmt.Errorf("%s skills_dir: %w", at, err)
		}
		*dir = skills.Dir(abs)
		return nil
	}
	if err := pin("flow", &f.SkillsDir); err != nil {
		return nil, err
	}
	for i := range f.Stages {
		stage := &f.Stages[i]
		if err := pin("stage "+stage.ID, &stage.SkillsDir); err != nil {
			return nil, err
		}
		for j := range stage.Assistants {
			if err := pin(fmt.Sprintf("stage %s assistant %s", stage.ID, stage.Assistants[j].Profile), &stage.Assistants[j].SkillsDir); err != nil {
				return nil, err
			}
		}
	}
	for i := range f.Team {
		if err := pin("team "+f.Team[i].ID, &f.Team[i].SkillsDir); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// pinSkillsDir makes a relative library path absolute inside the snapshot and requires it to exist. An absolute path
// is the author's explicit choice of a host library and is left alone.
func pinSkillsDir(root, dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return dir, nil
	}
	clean := filepath.Clean(dir)
	if clean == parentDir || strings.HasPrefix(clean, parentDir+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the package", dir)
	}
	abs := filepath.Join(root, clean)
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory in the package; add it to resources", dir)
	}
	return abs, nil
}
