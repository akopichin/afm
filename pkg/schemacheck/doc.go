// Package schemacheck holds guard tests that keep the hand-written JSON
// Schemas (schema/flow.schema.json, schema/config.schema.json) in sync with the
// Go structs and validation that yaml.v3 decodes into. It contains no production
// code: tests compile both schemas as draft-07, verify struct fields at their
// exact schema paths, compare important edge cases with flow.ParseFile, and
// validate the repository's example YAML files.
package schemacheck
