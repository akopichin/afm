package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
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

			outPath := ".flowManager/flows/" + name + ".yaml"
			os.MkdirAll(".flowManager/flows", 0755) //nolint:errcheck

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
