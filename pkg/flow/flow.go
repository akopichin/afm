package flow

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentType defines which built-in agents a stage uses.
type AgentType string

const (
	AgentPlanning       AgentType = "planning"
	AgentImplementation AgentType = "implementation"
	AgentReview         AgentType = "review"
)

// Artifact describes a file that a stage produces for other stages.
type Artifact struct {
	Name        string `yaml:"name"`
	Path        string `yaml:"path"`
	Description string `yaml:"description"`
	Inline      *bool  `yaml:"inline"`
}

// IsInline returns whether the artifact content should be inlined into the prompt.
// Defaults to true when Inline is nil.
func (a Artifact) IsInline() bool {
	return a.Inline == nil || *a.Inline
}

// Input describes an artifact that a stage consumes from a dependency.
// Supports unmarshalling from a plain string "stage.artifact" or an object {ref, optional}.
type Input struct {
	Ref      string `yaml:"ref"`
	Optional bool   `yaml:"optional"`
}

// UnmarshalYAML allows Input to be parsed from a string or an object.
func (inp *Input) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		inp.Ref = value.Value
		return nil
	}
	type plain Input
	return value.Decode((*plain)(inp))
}

// Stage represents a single stage in a flow.
type Stage struct {
	ID          string      `yaml:"id"`
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Agents      []AgentType `yaml:"agents"`
	Skills      []string    `yaml:"skills"`
	DependsOn   []string    `yaml:"depends_on"`
	// Plan is an optional path to an existing plan file.
	// If set, the planning agent is skipped.
	Plan string `yaml:"plan"`
	// Command overrides the global client command for this stage.
	Command string `yaml:"command"`
	// MaxParallel limits concurrent stages using the same command.
	MaxParallel int        `yaml:"max_parallel"`
	Artifacts   []Artifact `yaml:"artifacts"`
	Inputs      []Input    `yaml:"inputs"`
}

// isBuiltIn reports whether the agent type is one of the three built-in phases.
func isBuiltIn(a AgentType) bool {
	return a == AgentPlanning || a == AgentImplementation || a == AgentReview
}

// HasAgent reports whether the stage uses a specific agent type.
// For AgentImplementation, any custom (non-built-in) agent also counts.
func (s *Stage) HasAgent(a AgentType) bool {
	for _, ag := range s.Agents {
		if ag == a {
			return true
		}
	}
	// Custom agents (e.g. "senior-go-architect") count as implementation.
	if a == AgentImplementation {
		for _, ag := range s.Agents {
			if !isBuiltIn(ag) {
				return true
			}
		}
	}
	return false
}

// ImplAgent returns the agent type used for the implementation phase.
// Custom agents take priority; falls back to AgentImplementation.
func (s *Stage) ImplAgent() AgentType {
	for _, ag := range s.Agents {
		if !isBuiltIn(ag) {
			return ag
		}
	}
	return AgentImplementation
}

// NeedsPlanning reports whether a planning agent will run for this stage.
func (s *Stage) NeedsPlanning() bool {
	return s.Plan == "" && s.HasAgent(AgentPlanning)
}

// Flow is the top-level structure parsed from a flow YAML file.
type Flow struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description"`
	MaxParallel int     `yaml:"max_parallel"`
	Stages      []Stage `yaml:"stages"`
}

// ParseFile reads and validates a flow YAML file.
func ParseFile(path string) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flow file: %w", err)
	}
	var f Flow
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *Flow) validate() error {
	ids := make(map[string]bool, len(f.Stages))
	for _, s := range f.Stages {
		if ids[s.ID] {
			return fmt.Errorf("duplicate stage id: %q", s.ID)
		}
		ids[s.ID] = true
	}

	for _, s := range f.Stages {
		for _, dep := range s.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("stage %q depends_on unknown stage %q", s.ID, dep)
			}
		}
	}

	for _, s := range f.Stages {
		if s.Plan == "" && !s.HasAgent(AgentPlanning) {
			return fmt.Errorf("stage %q: must have planning agent or a plan path", s.ID)
		}
	}

	if err := detectCycles(f.Stages); err != nil {
		return err
	}

	// Build artifact index: stageID -> artifactName -> true
	artifactIndex := make(map[string]map[string]bool, len(f.Stages))
	for _, s := range f.Stages {
		names := make(map[string]bool, len(s.Artifacts))
		for _, a := range s.Artifacts {
			if names[a.Name] {
				return fmt.Errorf("stage %q: duplicate artifact name %q", s.ID, a.Name)
			}
			names[a.Name] = true
		}
		artifactIndex[s.ID] = names
	}

	// Validate inputs
	for _, s := range f.Stages {
		depsSet := make(map[string]bool, len(s.DependsOn))
		for _, d := range s.DependsOn {
			depsSet[d] = true
		}

		for _, inp := range s.Inputs {
			parts := strings.SplitN(inp.Ref, ".", 2)
			if len(parts) != 2 {
				return fmt.Errorf("stage %q: invalid input ref %q (expected stage.artifact)", s.ID, inp.Ref)
			}
			stageID, artName := parts[0], parts[1]

			if !ids[stageID] {
				return fmt.Errorf("stage %q: input ref %q references unknown stage %q", s.ID, inp.Ref, stageID)
			}
			if !depsSet[stageID] {
				return fmt.Errorf("stage %q: input ref %q references stage %q which is not in depends_on", s.ID, inp.Ref, stageID)
			}
			arts, ok := artifactIndex[stageID]
			if !ok || !arts[artName] {
				return fmt.Errorf("stage %q: input ref %q references unknown artifact %q in stage %q", s.ID, inp.Ref, artName, stageID)
			}
		}
	}

	return nil
}

func detectCycles(stages []Stage) error {
	deps := make(map[string][]string, len(stages))
	for _, s := range stages {
		deps[s.ID] = s.DependsOn
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(stages))

	var visit func(id string) error
	visit = func(id string) error {
		if color[id] == black {
			return nil
		}
		if color[id] == gray {
			return fmt.Errorf("cycle detected involving stage %q", id)
		}
		color[id] = gray
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}

	for _, s := range stages {
		if err := visit(s.ID); err != nil {
			return err
		}
	}
	return nil
}
