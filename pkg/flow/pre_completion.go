package flow

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// PreCompletionSpec is a stage's pre_completion: block, the budget of the review round that runs the stage's
// pre_completion helpers before verify. It needs no declared helper: one can be attached while the run goes.
type PreCompletionSpec struct {
	// MaxFailures is how many needs_changes rounds send the work back to the author before the stage fails:
	// N = the number of corrections. nil means 1; 0 fails on the first needs_changes.
	MaxFailures *int
}

// DefaultPreCompletionMaxFailures is the correction budget of a stage that does not set one.
const DefaultPreCompletionMaxFailures = 1

const preCompletionKeyMaxFailures = "max_failures"

// UnmarshalYAML accepts only a mapping with max_failures, so a typo'd key fails loudly.
func (p *PreCompletionSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("pre_completion must be an object with max_failures")
	}
	var spec PreCompletionSpec
	for i := 0; i+1 < len(value.Content); i += 2 {
		key, val := value.Content[i].Value, value.Content[i+1]
		switch key {
		case preCompletionKeyMaxFailures:
			n, err := decodeInteger(val)
			if err != nil {
				return fmt.Errorf("pre_completion: invalid max_failures: %w", err)
			}
			spec.MaxFailures = &n
		default:
			return fmt.Errorf("pre_completion: unknown field %q; use max_failures", key)
		}
	}
	*p = spec
	return nil
}

// MarshalYAML keeps an explicitly set max_failures through a round trip.
func (p PreCompletionSpec) MarshalYAML() (any, error) {
	out := map[string]any{}
	if p.MaxFailures != nil {
		out[preCompletionKeyMaxFailures] = *p.MaxFailures
	}
	return out, nil
}

// PreCompletionMaxFailures is the stage's correction budget: the configured one, else the default.
func (s Stage) PreCompletionMaxFailures() int {
	if s.PreCompletion == nil || s.PreCompletion.MaxFailures == nil {
		return DefaultPreCompletionMaxFailures
	}
	return *s.PreCompletion.MaxFailures
}

func (s Stage) validatePreCompletion() error {
	if s.PreCompletion == nil {
		return nil
	}
	if s.IsScript() || !s.IsExecutionStage() {
		return fmt.Errorf("stage %q: pre_completion requires an execution stage (planning-only and script stages have no completion to review)", s.ID)
	}
	if s.PreCompletion.MaxFailures != nil && *s.PreCompletion.MaxFailures < 0 {
		return fmt.Errorf("stage %q: pre_completion: max_failures must not be negative", s.ID)
	}
	return nil
}
