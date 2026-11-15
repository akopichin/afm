package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/pkg/pipeline"
)

func newPipelinesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipelines",
		Short: "Work with pipeline packages",
	}
	cmd.AddCommand(newPipelinesInspectCmd())
	return cmd
}

// inspectReport is the stable JSON shape of `afm pipelines inspect --json`.
type inspectReport struct {
	Source       pipeline.Source       `json:"source"`
	Package      inspectPackage        `json:"package"`
	Commit       string                `json:"commit,omitempty"`
	Snapshot     pipeline.Snapshot     `json:"snapshot"`
	Options      *pipeline.OptionsRef  `json:"options,omitempty"`
	Roles        []roleReport          `json:"roles"`
	Declared     pipeline.Requirements `json:"declared_requirements"`
	Requirements []requirementCheck    `json:"requirements"`
	Warnings     []string              `json:"warnings"`
}

type inspectPackage struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Description   string `json:"description,omitempty"`
	SchemaVersion int    `json:"schema_version"`
	Entrypoint    string `json:"entrypoint"`
}

func newPipelinesInspectCmd() *cobra.Command {
	var optionsPath string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inspect <source>",
		Short: "Show a pipeline package: version, roles with their effective models, requirements and what is missing",
		Long: "Load a pipeline package from a local directory or Git (<repo>[//<subdir>][@<ref>]) and show what\n" +
			"`afm run --pipeline` would use: source, commit, content hash, roles with their effective client,\n" +
			"model, reasoning, url and auth reference and the layer each came from, and the requirements the\n" +
			"package declares with what this machine lacks. It starts no agent and writes no config or run\n" +
			"state; the package snapshot is cached like a run would cache it. Exit status 0 means a valid\n" +
			"package, even when requirements are missing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := inspectPipeline(cmd, args[0], optionsPath)
			if err != nil {
				return err
			}
			return printInspect(cmd.OutOrStdout(), report, asJSON)
		},
	}
	cmd.Flags().StringVar(&optionsPath, "pipeline-options", "", "options file layered over the package, as afm run --pipeline-options does")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

// inspectPipeline loads and assembles a package with the same code `afm run --pipeline` uses, but only reads
// configuration, so nothing about the project or the user's config changes.
func inspectPipeline(cmd *cobra.Command, arg, optionsPath string) (inspectReport, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return inspectReport{}, err
	}
	src, err := pipeline.ParseSource(arg, cwd)
	if err != nil {
		return inspectReport{}, err
	}
	var opts *pipelineOptions
	if optionsPath != "" {
		if opts, err = loadPipelineOptions(optionsPath); err != nil {
			return inspectReport{}, err
		}
	}
	home, _ := os.UserHomeDir()
	base, err := loadProductConfig(filepath.Join(home, config.AfmDir), fmDir())
	if err != nil {
		return inspectReport{}, err
	}
	loaded, commit, err := loadFreshPipeline(commandContext(cmd), src)
	if err != nil {
		return inspectReport{}, err
	}
	setup, err := assemblePipeline(base, loaded, src, commit, opts)
	if err != nil {
		return inspectReport{}, err
	}
	if err := config.ValidateVerifySpecs(loaded.Flow, setup.cfg); err != nil {
		return inspectReport{}, fmt.Errorf("verify: %w", err)
	}
	if err := checkHelperProfiles(loaded.Flow, setup.cfg, ""); err != nil {
		return inspectReport{}, err
	}
	checks, err := checkPipelineRequirements(loaded.Flow, setup.cfg, loaded.Manifest)
	if err != nil {
		return inspectReport{}, err
	}
	m := loaded.Manifest
	report := inspectReport{
		Source:       src,
		Package:      inspectPackage{Name: m.Name, Version: m.Version, Description: m.Description, SchemaVersion: m.SchemaVersion, Entrypoint: m.Entrypoint},
		Commit:       commit,
		Snapshot:     loaded.Snapshot,
		Roles:        describeRoles(m, setup.cfg, setup.prov),
		Declared:     m.Requirements,
		Requirements: checks,
		Warnings:     []string{},
	}
	if opts != nil {
		report.Options = &opts.ref
	}
	for _, w := range setup.warnings {
		report.Warnings = append(report.Warnings, string(w))
	}
	return report, nil
}

func printInspect(w io.Writer, r inspectReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	_, _ = fmt.Fprintf(w, "Pipeline %s %s\n", r.Package.Name, r.Package.Version)
	if r.Package.Description != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", r.Package.Description)
	}
	_, _ = fmt.Fprintf(w, "  source:   %s\n", describeSource(r.Source))
	if r.Commit != "" {
		_, _ = fmt.Fprintf(w, "  commit:   %s\n", r.Commit)
	}
	_, _ = fmt.Fprintf(w, "  content:  sha256:%s\n  snapshot: %s\n", r.Snapshot.SHA256, r.Snapshot.Dir)
	if r.Options != nil {
		_, _ = fmt.Fprintf(w, "  options:  %s (sha256:%.12s)\n", r.Options.Path, r.Options.SHA256)
	}
	_, _ = fmt.Fprintln(w, "Roles:")
	for _, role := range r.Roles {
		_, _ = fmt.Fprintf(w, "  %s: %s\n", role.ID, describeRoleSettings(role))
		if role.Profile != "" {
			_, _ = fmt.Fprintf(w, "      profile %s %q (from %s)\n", role.Profile, role.ProfileTitle, role.ProfileLayer)
		}
	}
	_, _ = fmt.Fprintln(w, "Requirements:")
	for _, c := range r.Requirements {
		mark := map[string]string{checkOK: "ok", checkMissing: "MISSING", checkNotChecked: "not checked"}[c.Status]
		_, _ = fmt.Fprintf(w, "  [%s] %s", mark, c.Name)
		if c.Detail != "" {
			_, _ = fmt.Fprintf(w, " — %s", c.Detail)
		}
		_, _ = fmt.Fprintln(w)
	}
	for _, warning := range r.Warnings {
		_, _ = fmt.Fprintf(w, "warning: %s\n", warning)
	}
	return nil
}
