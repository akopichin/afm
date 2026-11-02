package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/cobra"
)

// The openai-agent shim has no JSON Schema validator of its own: it pipes the model's final answer into this
// command and asks the model to correct it when the code is output_invalid.

const (
	schemaErrOutputInvalid    = "output_invalid"
	schemaErrSchemaUnreadable = "schema_unreadable"
	schemaErrSchemaInvalid    = "schema_invalid"
)

const schemaResourceURL = "https://afm.invalid/validate.schema.json"

// newSchemaCmd registers `afm schema`, hidden: it is meant for agent shims, not for a human.
func newSchemaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "schema",
		Short:  "JSON Schema helpers for agent shims (internal)",
		Hidden: true,
	}
	cmd.AddCommand(newSchemaValidateCmd())
	return cmd
}

func newSchemaValidateCmd() *cobra.Command {
	var schemaFile string
	cmd := &cobra.Command{
		Use:   "validate --schema FILE",
		Short: "Validate the JSON document on stdin against a JSON Schema file",
		Long: "Exit 0 when the document matches. Exit 1 prints {\"error\",\"message\"} on stderr: output_invalid means the\n" +
			"document is wrong (the caller may ask for a correction); schema_unreadable and schema_invalid mean the\n" +
			"schema itself cannot be used.",
		SilenceUsage: true, SilenceErrors: true,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateStdinAgainstSchema(cmd.InOrStdin(), schemaFile); err != nil {
				return printStageError(cmd, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&schemaFile, "schema", "", "path to the JSON Schema file")
	return cmd
}

func validateStdinAgainstSchema(stdin io.Reader, schemaFile string) error {
	compiled, err := compileSchemaFile(schemaFile)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return &stageCLIError{"io_error", "read document from stdin: " + err.Error()}
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &stageCLIError{schemaErrOutputInvalid, "the answer is not a single valid JSON value: " + err.Error()}
	}
	if err := compiled.Validate(doc); err != nil {
		return &stageCLIError{schemaErrOutputInvalid, err.Error()}
	}
	return nil
}

func compileSchemaFile(path string) (*jsonschema.Schema, error) {
	if path == "" {
		return nil, &stageCLIError{schemaErrSchemaUnreadable, "--schema FILE is required"}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &stageCLIError{schemaErrSchemaUnreadable, err.Error()}
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, &stageCLIError{schemaErrSchemaInvalid, fmt.Sprintf("%s is not valid JSON: %v", path, err)}
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaResourceURL, schemaDoc); err != nil {
		return nil, &stageCLIError{schemaErrSchemaInvalid, err.Error()}
	}
	compiled, err := compiler.Compile(schemaResourceURL)
	if err != nil {
		return nil, &stageCLIError{schemaErrSchemaInvalid, err.Error()}
	}
	return compiled, nil
}
