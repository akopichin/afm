package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const cmdInit = "init"

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   cmdInit,
		Short: "Create a flow.yaml interactively",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanner := bufio.NewScanner(os.Stdin)

			name := prompt(scanner, "Flow name (e.g. my-feature): ")
			description := prompt(scanner, "Flow description: ")

			var stages []stageInput
			for {
				fmt.Println("\nAdd a stage (leave ID empty to finish):")
				id := prompt(scanner, "  Stage ID: ")
				if id == "" {
					break
				}
				stageName := prompt(scanner, "  Stage name: ")
				stageDesc := prompt(scanner, "  Stage description: ")
				depsRaw := prompt(scanner, "  Depends on (comma-separated IDs, or empty): ")
				agentsRaw := prompt(scanner, "  Agents [planning,implementation,review]: ")
				if agentsRaw == "" {
					agentsRaw = "planning,implementation,review"
				}
				stages = append(stages, stageInput{
					id: id, name: stageName, desc: stageDesc,
					deps: splitComma(depsRaw), agents: splitComma(agentsRaw),
				})
			}

			flowsDir := filepath.Join(fmDir(), "flows")
			if err := os.MkdirAll(flowsDir, 0755); err != nil {
				return fmt.Errorf("create flows dir: %w", err)
			}
			outPath := filepath.Join(flowsDir, name+".yaml")

			var sb strings.Builder
			sb.WriteString("name: " + name + "\n")
			sb.WriteString("description: \"" + description + "\"\n\n")
			sb.WriteString("stages:\n")
			for _, s := range stages {
				sb.WriteString("  - id: " + s.id + "\n")
				sb.WriteString("    name: \"" + s.name + "\"\n")
				sb.WriteString("    description: \"" + s.desc + "\"\n")
				sb.WriteString("    agents: [" + strings.Join(s.agents, ", ") + "]\n")
				if len(s.deps) > 0 {
					sb.WriteString("    depends_on: [" + strings.Join(s.deps, ", ") + "]\n")
				}
				sb.WriteString("\n")
			}

			if err := os.WriteFile(outPath, []byte(sb.String()), 0644); err != nil {
				return fmt.Errorf("write flow file: %w", err)
			}
			if err := ensureGitignoreEntry(".", ".afm/secrets.env"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
			}
			fmt.Printf("\nCreated: %s\n", outPath)
			return nil
		},
	}
}

type stageInput struct {
	id, name, desc string
	deps, agents   []string
}

func prompt(scanner *bufio.Scanner, label string) string {
	fmt.Print(label)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
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
