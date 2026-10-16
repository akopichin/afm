package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/flow"
)

// registryFileName is the run-scoped durable artifact that freezes the stage registry a run started with
// (T8.6). It lives beside events.jsonl, not inside it: this is definitional/config-shaped data (frozen at
// creation time, same spirit as a task's ProfileSnapshot — AGENTS.md's assistants §T1.5 note), not an FSM
// transition, so it does not need the full envelope/replay machinery events.jsonl exists for.
const registryFileName = "graph_registry.json"

// SaveRegistry durably persists stages as this run's stage registry — but ONLY the first time: if
// registryFileName already exists for runDir, this is a no-op. Together with LoadRegistry, this is what
// makes a resumed run immune to flow.yaml edits made between runs (TestRestart_ChangedFlowYamlDoesNotReplaceStoredGraph):
// the run directory, not the YAML file on disk, is the source of truth for "what stages does this run have"
// once the run has started once.
func SaveRegistry(runDir string, stages []flow.Stage) error {
	path := filepath.Join(runDir, registryFileName)
	if _, err := os.Stat(path); err == nil {
		return nil // already persisted — resumed run keeps the original graph, never overwrites it
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat stage registry: %w", err)
	}

	if err := os.MkdirAll(runDir, 0755); err != nil {
		return fmt.Errorf("mkdir run dir: %w", err)
	}
	data, err := json.MarshalIndent(stages, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal stage registry: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write stage registry: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename stage registry: %w", err)
	}
	return nil
}

// LoadRegistry reads a run's previously persisted stage definitions, if any. found is false for a run that
// has never had SaveRegistry succeed (a brand-new run about to call SaveRegistry itself, or a run directory
// from before Stage 8 ever ran).
func LoadRegistry(runDir string) (stages []flow.Stage, found bool, err error) {
	data, err := os.ReadFile(filepath.Join(runDir, registryFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read stage registry: %w", err)
	}
	if err := json.Unmarshal(data, &stages); err != nil {
		return nil, false, fmt.Errorf("decode stage registry: %w", err)
	}
	return stages, true, nil
}

// RestoreOrPersist is the single call site both a fresh run and a resumed run use to settle this run's stage
// definitions BEFORE constructing any consumer (orchestrator.New, the dashboard's stage provider — T8.6): if
// a registry was already persisted for runDir, it wins over fresh (which may reflect a flow.yaml edited since
// the run started); otherwise fresh is persisted and returned.
func RestoreOrPersist(runDir string, fresh []flow.Stage) ([]flow.Stage, error) {
	stored, found, err := LoadRegistry(runDir)
	if err != nil {
		return nil, fmt.Errorf("load stage registry: %w", err)
	}
	if found {
		return stored, nil
	}
	if err := SaveRegistry(runDir, fresh); err != nil {
		return nil, fmt.Errorf("save stage registry: %w", err)
	}
	return fresh, nil
}
