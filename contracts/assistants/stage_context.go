package assistants

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// StageContext selects root-stage inputs for a helper; a nil setting preserves the full stage context.
type StageContext struct {
	Inputs []string `yaml:"inputs" json:"inputs"`
}

var stageInputRef = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

func (s StageContext) validate() error {
	if s.Inputs == nil {
		return fmt.Errorf("stage_context.inputs must be a list, including [] to select no inputs")
	}
	for _, ref := range s.Inputs {
		if !stageInputRef.MatchString(ref) {
			return fmt.Errorf("stage_context.inputs: invalid reference %q (expected stage.artifact)", ref)
		}
	}
	return nil
}

// UnmarshalYAML checks tags before decoding, since yaml.v3 otherwise coerces numbers into strings.
func (s *StageContext) UnmarshalYAML(node *yaml.Node) error {
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("stage_context must be a mapping with inputs")
	}
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return err
	}
	for key := range fields {
		if key != "inputs" {
			return fmt.Errorf("stage_context: unknown field %q", key)
		}
	}
	inputs, ok := fields["inputs"]
	if !ok {
		return fmt.Errorf("stage_context.inputs is required")
	}
	for inputs.Kind == yaml.AliasNode {
		inputs = *inputs.Alias
	}
	if inputs.Kind != yaml.SequenceNode {
		return fmt.Errorf("stage_context.inputs must be a list")
	}
	out := StageContext{Inputs: make([]string, 0, len(inputs.Content))}
	for _, n := range inputs.Content {
		for n.Kind == yaml.AliasNode {
			n = n.Alias
		}
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
			return fmt.Errorf("stage_context.inputs must contain strings")
		}
		out.Inputs = append(out.Inputs, n.Value)
	}
	if err := out.validate(); err != nil {
		return err
	}
	*s = out
	return nil
}

// UnmarshalJSON requires exact field names so snapshots follow the same contract as configuration.
func (s *StageContext) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return fmt.Errorf("stage_context must be an object with inputs")
	}
	var out StageContext
	seen := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		if key != "inputs" {
			return fmt.Errorf("stage_context: unknown field %q", key)
		}
		if seen {
			return fmt.Errorf("stage_context.inputs is duplicated")
		}
		seen = true
		if err := dec.Decode(&out.Inputs); err != nil {
			return fmt.Errorf("stage_context.inputs: %w", err)
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if err := out.validate(); err != nil {
		return err
	}
	*s = out
	return nil
}

// RejectNullStageContext preserves the distinction between omission and an explicit null pointer.
func RejectNullStageContext(data []byte, key string) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return fmt.Errorf("profile must be an object")
	}
	seen := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if !strings.EqualFold(name, key) {
			continue
		}
		if seen {
			return fmt.Errorf("%s is given more than once", key)
		}
		seen = true
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s must not be null: provide inputs or omit the key", key)
		}
	}
	_, err = dec.Token()
	return err
}
