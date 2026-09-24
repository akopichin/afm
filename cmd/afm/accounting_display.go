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
// because a config file is missing or malformed, so a load error is ignored.
// Crucially we still read the RETURNED config's switch on error — LoadFrom
// carries every layer merged before the failure, so an already-merged
// `accounting.enabled: false` opt-out is honored even if an unrelated part of
// the config (e.g. a negative pricing rate) fails validation, rather than
// failing open to "enabled". env AFM_ACCOUNTING wins regardless via IsEnabled.
func accountingDisplayEnabled() bool {
	home, _ := os.UserHomeDir() // "" on error → the global layer just isn't found
	cfg, _ := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
	return cfg.Accounting.IsEnabled()
}

// accountingShowMoney resolves whether monetary ($) figures are shown by the
// read-only CLI commands (`afm check`, `afm report`). Orthogonal to
// accountingDisplayEnabled: accounting can be on (tokens collected and shown)
// while money is hidden. Priority env AFM_ACCOUNTING_SHOW_MONEY > config
// accounting.show_money > default false, resolved by
// config.AccountingConfig.ShowMoneyEnabled.
//
// Best-effort like accountingDisplayEnabled: a config load error is ignored,
// and the returned (partially merged) config's switch is still read.
func accountingShowMoney() bool {
	home, _ := os.UserHomeDir() // "" on error → the global layer just isn't found
	cfg, _ := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
	return cfg.Accounting.ShowMoneyEnabled()
}
