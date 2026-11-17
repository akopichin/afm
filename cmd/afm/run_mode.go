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

// mcpFlags — значения --mcp, --mcp-off и --mcp-allow как их ввёл пользователь; allowSet отличает явное пустое
// значение --mcp-allow (только чтение) от отсутствия флага (все группы).
type mcpFlags struct {
	on       bool
	off      bool
	allowSet bool
	allow    string
}

// parseMCPSettings проверяет флаги MCP до создания рана. При HTTP API /mcp монтируется по умолчанию со всеми
// группами; без API по умолчанию молча выключен, а явный запрос MCP или прав — ошибка, чтобы не обещать
// несуществующее.
func parseMCPSettings(in mcpFlags, cfg config.Config) (mcpSettings, error) {
	hasAPI := cfg.Server.GetPort() > 0
	if in.off {
		if in.on || in.allowSet {
			return mcpSettings{}, errors.New("--mcp-off cannot be combined with --mcp or --mcp-allow")
		}
		return mcpSettings{}, nil
	}
	if !hasAPI {
		switch {
		case in.on:
			return mcpSettings{}, errors.New("--mcp needs the HTTP API: set a positive --port or server.port; 0 disables it")
		case in.allowSet:
			return mcpSettings{}, errors.New("--mcp-allow needs the HTTP API: set a positive --port or server.port; 0 disables it")
		}
		return mcpSettings{}, nil
	}
	if !in.allowSet {
		return mcpSettings{enabled: true, groups: mcpserver.AllGroups()}, nil
	}
	groups, err := mcpserver.ParseGroups(in.allow)
	if err != nil {
		return mcpSettings{}, fmt.Errorf("--mcp-allow: %w", err)
	}
	return mcpSettings{enabled: true, groups: groups}, nil
}
