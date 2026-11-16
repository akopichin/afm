package main

import (
	"errors"
	"fmt"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/pkg/mcpserver"
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

// mcpSettings — что afm run выставляет по MCP. Нулевое значение: /mcp выключен.
type mcpSettings struct {
	enabled bool
	groups  mcpserver.Groups
}

// parseMCPSettings проверяет --mcp и --mcp-allow до создания рана: без HTTP API MCP некуда монтировать, а права
// без --mcp молча ничего бы не значили.
func parseMCPSettings(enabled bool, allow string, cfg config.Config) (mcpSettings, error) {
	if !enabled {
		if allow != "" {
			return mcpSettings{}, errors.New("--mcp-allow needs --mcp")
		}
		return mcpSettings{}, nil
	}
	if cfg.Server.GetPort() <= 0 {
		return mcpSettings{}, errors.New("--mcp needs the HTTP API: set a positive --port or server.port; 0 disables it")
	}
	groups, err := mcpserver.ParseGroups(allow)
	if err != nil {
		return mcpSettings{}, fmt.Errorf("--mcp-allow: %w", err)
	}
	return mcpSettings{enabled: true, groups: groups}, nil
}
