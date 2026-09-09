package main

import "github.com/spf13/cobra"

// newMemoryCmd — родительская команда для обслуживания agent-памяти
// (`afm memory rebuild`, ...).
func newMemoryCmd() *cobra.Command {
	c := &cobra.Command{Use: "memory", Short: "Agent-memory maintenance commands"}
	c.AddCommand(newMemoryRebuildCmd())
	return c
}
