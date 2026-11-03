package config

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	commandNamesKey       = "command_names"
	unknownCommandNameKey = "unknown_command_name"

	// DefaultUnknownCommandName is the dashboard fallback label prefix when no unknown_command_name is set.
	DefaultUnknownCommandName = "Unknown"
)

// builtinCommandNames maps the base names (without a directory or .sh) of known agent shims to readable
// names. A provider is never guessed from a fragment of an unknown file name.
var builtinCommandNames = map[string]string{
	"claude":                 "Claude Code",
	"codex-as-claude":        "Codex",
	"cursor-agent-as-claude": "Cursor Agent",
	"openai-as-claude":       "OpenAI",
	"openai-agent-as-claude": "OpenAI Agent",
}

// ResolveCommandName returns the display name of an agent command: an exact override, then a built-in name,
// then the file's base name. It never touches the file system, so host, container and tests agree.
func ResolveCommandName(overrides map[string]string, command string) string {
	if command == "" {
		return ""
	}
	if name, ok := overrides[command]; ok {
		return name
	}
	base := path.Base(command)
	if name, ok := builtinCommandNames[strings.TrimSuffix(base, ".sh")]; ok {
		return name
	}
	return base
}

// EffectiveUnknownCommandName returns the fallback label prefix for commands the server did not name.
func (c Config) EffectiveUnknownCommandName() string {
	if c.UnknownCommandName == "" {
		return DefaultUnknownCommandName
	}
	return c.UnknownCommandName
}

// validateCommandNaming checks the raw YAML because yaml.v3 would silently turn `123` or `true` into a
// string. Entries come from the decoder itself, so merge keys and aliases are validated as decoded.
func validateCommandNaming(root *yaml.Node, layer string) error {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	top, err := decodeEntries(root)
	if err != nil {
		return err
	}
	if node, ok := top[commandNamesKey]; ok {
		if err := validateCommandNames(&node, layer); err != nil {
			return err
		}
	}
	if node, ok := top[unknownCommandNameKey]; ok && !isNonBlankString(&node) {
		return fmt.Errorf("%s config: %s must be a non-empty string", layer, unknownCommandNameKey)
	}
	return nil
}

// validateCommandNames rejects blank keys and non-string or blank values. Keys are compared exactly.
func validateCommandNames(node *yaml.Node, layer string) error {
	node = resolveAlias(node)
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s config: %s must be a mapping of command to name", layer, commandNamesKey)
	}
	if hasNullKey(node) {
		return fmt.Errorf("%s config: %s keys must be non-empty commands", layer, commandNamesKey)
	}
	entries, err := decodeEntries(node)
	if err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s config: %s keys must be non-empty commands", layer, commandNamesKey)
		}
		value := entries[key]
		if !isNonBlankString(&value) {
			return fmt.Errorf("%s config: %s.%s must be a non-empty string", layer, commandNamesKey, key)
		}
	}
	return nil
}

// decodeEntries applies the decoder's own merge-key and alias rules, so validation sees the effective
// keys and value nodes that a later Decode into Config will use.
func decodeEntries(node *yaml.Node) (map[string]yaml.Node, error) {
	var entries map[string]yaml.Node
	if err := node.Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// hasNullKey finds keys YAML reads as null (`~`, `null`, an empty explicit key), including those pulled in
// through `<<`. yaml.v3 drops them while decoding, so the user's entry would vanish without an error.
func hasNullKey(node *yaml.Node) bool {
	node = resolveAlias(node)
	switch node.Kind {
	case yaml.SequenceNode:
		return slices.ContainsFunc(node.Content, hasNullKey)
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if resolveAlias(key).ShortTag() == "!!null" {
				return true
			}
			if key.ShortTag() == "!!merge" && hasNullKey(value) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func resolveAlias(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}

func isNonBlankString(node *yaml.Node) bool {
	node = resolveAlias(node)
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!str" && strings.TrimSpace(node.Value) != ""
}
