package main

import (
	"errors"
	"fmt"

	"github.com/akopichin/afm/contracts/config"
)

// launchMode — как afm run открывает рану внешний мир: dashboard добавляет Web UI к HTTP API, core отдаёт
// только API.
type launchMode int

const (
	modeDashboard = "dashboard"
	modeCore      = "core"
)

const (
	launchDashboard launchMode = iota
	launchCore
)

func (m launchMode) String() string {
	if m == launchCore {
		return modeCore
	}
	return modeDashboard
}

// parseLaunchMode читает значение --mode; пустое значение — прежний режим dashboard.
func parseLaunchMode(value string) (launchMode, error) {
	switch value {
	case "", modeDashboard:
		return launchDashboard, nil
	case modeCore:
		return launchCore, nil
	default:
		return launchDashboard, fmt.Errorf("--mode %q: want dashboard or core", value)
	}
}

// validate отвергает режим, который нельзя запустить с этой конфигурацией: core без HTTP API никому недоступен.
func (m launchMode) validate(cfg config.Config) error {
	if m == launchCore && cfg.Server.GetPort() <= 0 {
		return errors.New("--mode core needs the HTTP API: set a positive --port or server.port; 0 disables it")
	}
	return nil
}
