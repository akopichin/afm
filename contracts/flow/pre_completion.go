package flow

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// PreCompletionSpec controls corrective author turns before verification.
type PreCompletionSpec struct {
	MaxFailures *int `yaml:"max_failures,omitempty"`
}

// CorrectionLimit preserves the distinction between an omitted budget and explicit zero.
func (s *PreCompletionSpec) CorrectionLimit() int {
	if s == nil || s.MaxFailures == nil {
		return 1
	}
	return *s.MaxFailures
}

func (s *PreCompletionSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("pre_completion must be a mapping")
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value != "max_failures" {
			return fmt.Errorf("pre_completion: unknown field %q", n.Content[i].Value)
		}
		if n.Content[i+1].ShortTag() != "!!int" {
			return fmt.Errorf("pre_completion.max_failures must be an integer")
		}
	}
	type plain PreCompletionSpec
	return n.Decode((*plain)(s))
}
