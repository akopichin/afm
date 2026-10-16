package orchestrator

import (
	"context"
	"fmt"
	"log"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// attachDeclaredAssistants auto-attaches the helper profiles a stage declared in flow.yaml (assistants:),
// the same way a manual dashboard Attach click would. Called from spawnKind — the choke point of every
// stage-agent (re)spawn EXCEPT review-pause's resumeOwner (reviewpause.go), which calls spawnAgentLeased
// directly — strictly BEFORE the agent itself is spawned: a stage that raced ahead to done/failed in between
// would have its Attach rejected (assistants.go's terminal-status check), and spawnKind's body runs
// synchronously in the caller's own goroutine, so there is no window for that race. resumeOwner's omission is
// harmless: a review-pause resume never bumps RootGenerations, so even calling this there would just replay
// the same operation id as the original attach — a no-op, not a second task.
//
// Idempotency is a pure function of the operation id, not new state: Store.ApplyRunOperation already
// deduplicates by Operation.ID durably, so the SAME id (same stage, profile, and root generation) always
// replays the first attempt instead of creating a second task. The root generation is read HERE, from the
// current snapshot, rather than passed in — Revise bumps it immediately before calling spawnKind, so a new
// execution attempt always sees its own fresh generation without a separate code path.
func (o *Orchestrator) attachDeclaredAssistants(ctx context.Context, s flow.Stage, kind string) {
	if len(s.Assistants) == 0 || kind == kindPlanning || !o.opts.AssistantsEnabled {
		return
	}
	gen := o.opts.Store.Snapshot().Assistants.RootGenerations[s.ID]
	for _, a := range s.Assistants {
		_, err := o.Attach(ctx, AttachRequest{
			OperationID: fmt.Sprintf("stage-assistants:%s:%s:%d", s.ID, a.Profile, gen),
			StageID:     s.ID,
			ProfileID:   assistants.ProfileID(a.Profile),
			Title:       a.Title,
			Instruction: a.Instruction,
			Mode:        a.Mode,
			Trigger:     a.Trigger,
			Actor:       state.Actor{Kind: "system", ID: "flow"},
		})
		if err != nil {
			// Best-effort: an unknown profile (preflight bypassed) or a disabled/misconfigured registry must
			// not block the stage itself from running — only a genuine journal-write failure is fatal, and
			// handleTaskOperationErr is what distinguishes the two.
			o.handleTaskOperationErr(err)
			log.Printf("stage %s: auto-attach assistant %q: %v", s.ID, a.Profile, err)
		}
	}
}
