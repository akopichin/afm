package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const cmdInit = "init"

// generateAndValidateFlow marshals f to YAML, writes it to outPath, and
// validates the result via flow.ParseFile. Returns the rendered YAML and
// a validation error (if any) — the file is written regardless of
// validity, so the user (or the wizard's own repair loop) can inspect
// or edit it.
func generateAndValidateFlow(f *flow.Flow, outPath string) (string, error) {
	data, err := yaml.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("render flow.yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return "", fmt.Errorf("create flows dir: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return "", fmt.Errorf("write flow file: %w", err)
	}
	if _, err := flow.ParseFile(outPath); err != nil {
		return string(data), fmt.Errorf("invalid flow.yaml: %w", err)
	}
	return string(data), nil
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   cmdInit,
		Short: "Create a flow.yaml interactively",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanner := bufio.NewScanner(os.Stdin)
			f := runInitWizard(scanner, os.Stdout)
			outPath := filepath.Join(flowsDir(), f.Name+".yaml")

			for {
				_, err := generateAndValidateFlow(f, outPath)
				if err == nil {
					break
				}
				fmt.Printf("\n✗ %s — %v\n", outPath, err)
				choice := promptChoice(scanner, os.Stdout, "What next?", []string{
					"Edit the file manually, then re-validate",
					"Restart the wizard from scratch",
					"Exit (file stays on disk, but invalid)",
				}, 0)
				switch choice {
				case 0:
					promptLine(scanner, os.Stdout, "Press Enter once you've fixed the file: ")
				case 1:
					f = runInitWizard(scanner, os.Stdout)
					outPath = filepath.Join(flowsDir(), f.Name+".yaml")
				default:
					return fmt.Errorf("flow.yaml left invalid at %s: %w", outPath, err)
				}
			}

			if err := ensureGitignoreEntry(".", ".afm/secrets.env"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
			}

			fmt.Printf("\n✓ Created: %s\n✓ Validated: %d stage(s), valid\n", outPath, len(f.Stages))
			fmt.Printf("\nRun it with:\n  afm run %s\n", outPath)
			return nil
		},
	}
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}

// ensureGitignoreEntry дописывает entry в .gitignore в repoDir, если его там ещё
// нет. Создаёт .gitignore при отсутствии. Используется afm-init, чтобы
// секреты recipe (.afm/secrets.env) не попали в VCS.
func ensureGitignoreEntry(repoDir, entry string) error {
	giPath := filepath.Join(repoDir, ".gitignore")
	data, _ := os.ReadFile(giPath)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil // уже есть
		}
	}
	content := entry + "\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		content = "\n" + content
	}
	f, err := os.OpenFile(giPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// runInitWizard runs the full interactive flow: archetype selection,
// flow-level name/description, and per-stage questions. Returns the
// fully built Flow — nothing is written to disk yet.
func runInitWizard(scanner *bufio.Scanner, w io.Writer) *flow.Flow {
	name := promptLine(scanner, w, "Flow name (e.g. my-feature): ")
	description := promptLine(scanner, w, "Flow description: ")

	archetype := promptChoice(scanner, w, "\nWhat kind of flow are you building?", archetypeOptions, archetypeSingleChange)
	stages := buildArchetypeStages(archetype, scanner, w)

	return &flow.Flow{Name: name, Description: description, Stages: stages}
}
