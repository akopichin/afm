package main

import (
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/config"
)

// accountingDisplayEnabled resolves whether cost/usage display is on for the
// read-only CLI commands (`afm check`, `afm report`). It mirrors the dashboard
// gate in run.go: priority env AFM_ACCOUNTING > config > default true, resolved
// by config.AccountingConfig.IsEnabled.
//
// Best-effort by design: these are status commands that must never fail just
// because a config file is missing or malformed, so any load error falls back
// to the zero-value AccountingConfig, which still honors AFM_ACCOUNTING and
// defaults to enabled.
func accountingDisplayEnabled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.AccountingConfig{}.IsEnabled()
	}
	cfg, err := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
	if err != nil {
		return config.AccountingConfig{}.IsEnabled()
	}
	return cfg.Accounting.IsEnabled()
}
