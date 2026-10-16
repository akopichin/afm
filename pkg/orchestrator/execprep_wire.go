package orchestrator

import (
	"fmt"

	"github.com/akopichin/afm/pkg/execprep"
	"github.com/akopichin/afm/pkg/flow"
)

// buildPreparedCommandSet wires pkg/execprep (T8.5) into the ONE thing Этап 9 needs it for: telling a
// dynamically-added stage's draft command apart from one that only exists because config.yaml/flow.yaml
// changed on disk AFTER this run started. Scope decision (recorded in dev-plan.md's "Review decisions"):
// T8.5 built execprep as general infra but never wired assistant-profile/docker-mount resolution into it —
// doing that properly is its own, separable integration. A dynamically added stage only ever needs to reuse
// a command this run has ALREADY proven it can run (wrappers generated, credentials resolved, docker mounts
// done) by virtue of an existing stage already using it — so the prepared set here is exactly the distinct
// set of commands among the stages THIS run started with (stages []flow.Stage is read once, at Orchestrator
// construction, before any dynamic addition could exist). resolve always returns ok=true: these commands
// already passed whatever preflight let the run start in the first place.
func buildPreparedCommandSet(stages []flow.Stage, defaultCommand string) *execprep.Set {
	seen := make(map[string]bool, len(stages)+1)
	var required []execprep.CommandRef
	addCmd := func(cmd, ref string) {
		if cmd == "" || seen[cmd] {
			return
		}
		seen[cmd] = true
		required = append(required, execprep.CommandRef{Command: cmd, ConfigRef: ref})
	}
	addCmd(defaultCommand, "client.command")
	for _, s := range stages {
		addCmd(s.Command, fmt.Sprintf("flow.stages[%s].command", s.ID))
	}
	set, err := execprep.Prepare(required, nil, func(string) (bool, string) { return true, "" })
	if err != nil {
		// Unreachable: resolve above always returns ok=true.
		return nil
	}
	return set
}

// IsCommandPrepared reports whether cmd is in this run's pre-prepared command set (T8.5/T9.1) — the gate a
// dynamically-added stage's draft.Command must pass before PlanInsertBefore/PlanAppend will accept it.
func (o *Orchestrator) IsCommandPrepared(cmd string) bool {
	return o.preparedCommands.IsPrepared(cmd)
}
