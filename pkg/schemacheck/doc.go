// Package schemacheck holds a guard test that keeps the hand-written JSON
// Schemas (schema/flow.schema.json, schema/config.schema.json) in sync with the
// Go structs they describe (flow.Flow, config.Config). It has no runtime code —
// the schemas are hand-written for accuracy (several fields use a custom
// UnmarshalYAML whose YAML shape diverges from the Go type), and this test only
// verifies that no struct field is missing from its schema.
package schemacheck
