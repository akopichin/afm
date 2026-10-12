package main

import (
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/config"
)

// accountingDisplayEnabled resolves whether cost/usage display is on for the read-only CLI commands. env
// AFM_ACCOUNTING wins regardless via IsEnabled.
func accountingDisplayEnabled() bool {
	home, _ := os.UserHomeDir() // "" on error → the global layer just isn't found
	cfg, _ := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
	return cfg.Accounting.IsEnabled()
}

// accountingShowMoney resolves whether monetary figures are shown by the read-only CLI commands.
func accountingShowMoney() bool {
	home, _ := os.UserHomeDir() // "" on error → the global layer just isn't found
	cfg, _ := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
	return cfg.Accounting.ShowMoneyEnabled()
}
