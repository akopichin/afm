package mcpserver

import (
	"fmt"
	"slices"
	"strings"
)

// Group is a set of MCP tools a client may use. Reading is always on; the others are opted into one by one.
type Group string

const (
	GroupReply  Group = "reply"
	GroupStages Group = "stages"
	// GroupApprove allows approving the exact plan revision a client read; it never falls back to legacy approve.
	GroupApprove Group = "approve"
	GroupBudget  Group = "budget"
)

var knownGroups = []Group{GroupReply, GroupStages, GroupApprove, GroupBudget}

// Groups is the set of enabled write groups. The zero value is read-only.
type Groups struct {
	enabled map[Group]bool
}

// ParseGroups reads a comma-separated list such as "reply,stages". There is deliberately no "all": every
// permission has to be named. An empty list means read-only.
func ParseGroups(csv string) (Groups, error) {
	g := Groups{enabled: map[Group]bool{}}
	for _, raw := range strings.Split(csv, ",") {
		name := Group(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if !slices.Contains(knownGroups, name) {
			return Groups{}, fmt.Errorf("unknown MCP permission group %q (allowed: %s)", name, joinGroups(knownGroups))
		}
		g.enabled[name] = true
	}
	return g, nil
}

// Has reports whether the group is enabled.
func (g Groups) Has(name Group) bool { return g.enabled[name] }

// Names lists the enabled groups in a stable order.
func (g Groups) Names() []Group {
	var names []Group
	for _, name := range knownGroups {
		if g.enabled[name] {
			names = append(names, name)
		}
	}
	return names
}

func joinGroups(groups []Group) string {
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = string(g)
	}
	return strings.Join(names, ", ")
}
