package watch

import (
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/executor"
)

// DedupKey returns the stable idempotency key for one watcher checkpoint pass — checkpointID + taskID +
// generation (dev-plan.md T5.5's TestDedup_StableKey). Same inputs always give the same key; a different
// generation (e.g. after the task's whole attempt lineage was reset — the same "generation" concept
// assistants.Task.TaskGeneration already tracks for the broader collaboration feature) always gives a
// different one, even for the identical checkpointID — a stale pass from a superseded generation must never
// be mistaken for a dedup of the current one.
func DedupKey(checkpointID, taskID string, generation uint64) string {
	return fmt.Sprintf("%s|%s|%d", checkpointID, taskID, generation)
}

// CheckpointID derives a stable, human-auditable checkpoint identifier from the bundle it was built from —
// used as the dedup key's first component and as the durable checkpoint file's own name (see
// CheckpointStore.checkpointPath, which is keyed on UpToSeq directly; CheckpointID is the richer, log-friendly
// form surfaced in notices/UI).
func CheckpointID(executionID string, upToSeq int) string {
	return fmt.Sprintf("%s@%06d", executionID, upToSeq)
}

// RenderBundle turns a fired Bundle's observations into the durable checkpoint context package's text — a
// plain, readable transcript of exactly what the watcher is being shown, nothing more. Every entry here has
// already passed Collector.Observe's Complete/Filter gate.
func RenderBundle(b Bundle) string {
	var out strings.Builder
	for _, o := range b.Observations {
		switch o.Type {
		case executor.ObservationAssistantMessage:
			fmt.Fprintf(&out, "### message (seq %d)\n\n%s\n\n", o.Seq, o.Content)
		case executor.ObservationToolAction:
			fmt.Fprintf(&out, "### tool_action: %s (seq %d)\n\n%s\n\n", o.Tool, o.Seq, o.Content)
		default:
			fmt.Fprintf(&out, "### %s (seq %d)\n\n%s\n\n", o.Type, o.Seq, o.Content)
		}
	}
	return out.String()
}

// IsMainAgentObservation reports whether o plausibly originates from the stage's OWN main/author agent, as
// opposed to a helper/watcher agent's own output being fed back in by mistake (dev-plan.md T5.5's
// TestLoop_AssistantMessageIsNotMainAgentOutput / TestLoop_SummaryDoesNotTriggerItself). In this codebase the
// real guarantee is structural, not a runtime check: only pkg/orchestrator's main-agent runners
// (RunPlanning/RunAgent via runner_factory.go) ever wire executor.Config.OnObservation at all — RunHelperAgent
// (the watcher's own turn) and RunVerifyAgent never do, so a helper/watcher's own output never reaches a
// Collector in the first place. This predicate exists as an explicit, independently-testable second line of
// defense: Actor is always "<stageID>/<phase>" for a main-agent observation (see
// pkg/executor/observation.go's NewExecutionID/observationMeta) and never carries a task id — a value shaped
// like a watcher's own actor (containing "task:" or a profile id marker) is rejected outright.
func IsMainAgentObservation(o executor.Observation) bool {
	return !strings.Contains(o.Actor, "task:")
}
