package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available flow files in .afm/flows/",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := filepath.Join(fmDir(), "flows")
			entries, err := os.ReadDir(dir)
			if err != nil {
				fmt.Println("No flows found (create one with `afm init`)")
				return nil
			}
			fmt.Println("Available flows:")
			for _, e := range entries {
				if !e.IsDir() && (filepath.Ext(e.Name()) == ".yaml" || filepath.Ext(e.Name()) == ".yml") {
					fmt.Printf("  %s\n", filepath.Join(dir, e.Name()))
				}
			}
			return nil
		},
	}
}
