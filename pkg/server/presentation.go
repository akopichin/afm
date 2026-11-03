package server

import (
	"maps"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/config"
)

// verifyKindShell is the kind of a verify step whose command is the check itself rather than an agent.
const verifyKindShell = "shell"

// meshParticipantView is a roster entry for the dashboard: the participant plus its executor's display name.
// The agent-facing roster stays without it.
type meshParticipantView struct {
	agentcomm.Participant
	CommandName string `json:"command_name,omitempty"`
}

// commandName is the dashboard name of an agent command. Responses keep the raw command next to it for tooltips.
func (s *Server) commandName(command string) string {
	return config.ResolveCommandName(s.commandNames, command)
}

func (s *Server) meshParticipantView(p agentcomm.Participant) meshParticipantView {
	return meshParticipantView{Participant: p, CommandName: s.commandName(p.Command)}
}

// verifyCommandName names the executor of an AI verify step; a shell step has no executor to name.
func verifyCommandName(kind, command string, commandName func(string) string) string {
	if kind == verifyKindShell {
		return ""
	}
	return commandName(command)
}

// presentVerifyData adds command_name to a copy of a verify event's data, so the live stream and the history
// replay show the same name while the bus event and notices.jsonl keep only the raw command.
func presentVerifyData(eventType string, data any, commandName func(string) string) any {
	fields, ok := data.(map[string]any)
	if !ok || !strings.HasPrefix(eventType, "verify_") {
		return data
	}
	kind, _ := fields["kind"].(string)
	command, _ := fields["command"].(string)
	name := verifyCommandName(kind, command, commandName)
	if name == "" {
		return data
	}
	presented := maps.Clone(fields)
	presented["command_name"] = name
	return presented
}
