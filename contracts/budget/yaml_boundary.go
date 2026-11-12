package budget

import "gopkg.in/yaml.v3"

// EffectiveYAMLValue returns the value node yaml.v3 decodes for key in a mapping and whether the key is set.
// yaml.v3 itself resolves key aliases, tags and << merges, while a null value stays visible as a node.
func EffectiveYAMLValue(mapping *yaml.Node, key string) (yaml.Node, bool, error) {
	var values map[string]yaml.Node
	if err := mapping.Decode(&values); err != nil {
		return yaml.Node{}, false, err
	}
	value, set := values[key]
	return value, set, nil
}
