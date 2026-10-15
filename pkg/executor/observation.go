package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ObservationType classifies a normalized observation of the main stage agent's own output (dev-plan.md
// Этап 5, §8.1). The names are deliberately neutral: afm only ever promises access to the agent's PUBLIC
// output (messages, tool calls, observed results) — never to a model's hidden reasoning.
type ObservationType string

const (
	// ObservationAssistantMessage is a narrative text block from the agent.
	ObservationAssistantMessage ObservationType = "assistant_message"
	// ObservationToolAction is one tool_use call.
	ObservationToolAction ObservationType = "tool_action"
	// ObservationCheckpoint is reserved for a synthetic end-of-invocation marker; never produced by
	// buildObservations itself (no raw stream-json line maps to it) — callers (the orchestrator's collector,
	// T5.3) synthesize it when an invocation ends.
	ObservationCheckpoint ObservationType = "checkpoint"
)

// Observation is a normalized, durable record of one block of the main stage agent's own output. It is built
// directly from the SAME raw stream-json line already durably appended to <phase>.log.jsonl (see
// contentToAction/jf.WriteString above) — independent of bus.UIBus, which intentionally drops events for a
// slow subscriber, and independent of OnAction's display-truncated (tool, detail) pair: Content here is always
// the full, untruncated text.
//
// ID is STABLE across a process restart: it is derived only from (RunID, StageID, ExecutionID, Seq) — never
// from time.Now() or a random value — so the same underlying content always gets the same identity, which is
// what lets a durable cursor (T5.2) resume exactly where it left off instead of reprocessing or renumbering
// everything after a crash.
type Observation struct {
	RunID   string
	StageID string
	// ExecutionID identifies one logical agent-invocation lineage: stable across a --resume restart that
	// reuses the SAME jsonl log file, distinct once the orchestrator genuinely starts a DIFFERENT logFile
	// (e.g. switching from a fresh-start log to a feedback-restart log) — see NewExecutionID.
	ExecutionID string
	// Seq is this observation's 1-based position within ExecutionID — monotonically increasing, and NOT reset
	// to 1 across a restart: it is seeded from the number of observations already derivable from the jsonl
	// file's EXISTING content before this invocation's first new line (see countExistingObservations).
	Seq int
	// ID is the stable, restart-safe identifier (ComputeObservationID(RunID, StageID, ExecutionID, Seq)).
	ID string

	Type ObservationType
	// Complete is false only for a block that could not be fully parsed — a torn trailing line flushed when
	// the subprocess died mid-write (lineReader still delivers it, with no closing '\n', see linereader.go).
	// An incomplete observation is NEVER checkpoint-eligible (TestObservation_IncompleteBlockMarked) — callers
	// must filter on this before treating an observation as a candidate trigger.
	Complete bool
	// Tool is set only for Type==ObservationToolAction (e.g. "Bash"/"Write"/"Edit"); empty otherwise.
	Tool string
	// Content is the FULL, untruncated text — unlike OnAction's `detail`, this is never cut down for display
	// (ObservationToAction re-applies the old truncation rule only for its own display-oriented output).
	Content string
	// Actor names the agent invocation that produced this observation ("<stageID>/<phase>") — a cheap,
	// always-available seed for a future multi-actor causal chain.
	Actor string
	// CausesID is reserved for a future explicit parent-observation link. Never populated at this layer:
	// every Observation built directly from one agent invocation is, from the executor's point of view, a
	// root cause — a watcher's own derived checkpoint (T5.2/T5.3) is what actually fills this in.
	CausesID string
}

// ComputeObservationID builds the stable identity described on Observation.ID.
func ComputeObservationID(runID, stageID, executionID string, seq int) string {
	return fmt.Sprintf("%s|%s|%s|%06d", runID, stageID, executionID, seq)
}

// NewExecutionID derives the stable execution identity for one phase's jsonl log file: the stage id plus the
// log file's own base name without extension (e.g. "implementation", "implementation-feedback"). A --resume
// restart reuses the SAME logFile path (jf is always opened O_APPEND), so this id — and therefore every
// Observation.ID built from it — stays the same across the crash; a genuinely new attempt that switches to a
// DIFFERENT logFile name gets a different id, as it should (it is not a continuation of the same output
// stream).
func NewExecutionID(stageID, logFile string) string {
	phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))
	return stageID + "/" + phase
}

// observationMeta bundles the identity fields constant for one invocation.
type observationMeta struct {
	runID, stageID, executionID, actor string
}

// buildObservationsFromEvent normalizes an ALREADY-PARSED assistant stream event into zero or more
// Observations, advancing *seq for each one — one per content block, mirroring contentToAction's own
// content-block iteration but keeping the FULL, untruncated detail (contentToAction is called with limit=0)
// instead of recomputing the extraction logic a second time.
func buildObservationsFromEvent(meta observationMeta, ev *streamEvent, seq *int) []Observation {
	var out []Observation
	for _, c := range ev.Message.Content {
		tool, detail, ok := contentToAction(c, 0) // limit=0: observations are never truncated
		if !ok {
			continue
		}
		*seq++
		obsType := ObservationToolAction
		if c.Type == contentTypeText {
			obsType = ObservationAssistantMessage
			tool = ""
		}
		out = append(out, Observation{
			RunID: meta.runID, StageID: meta.stageID, ExecutionID: meta.executionID,
			Seq:      *seq,
			ID:       ComputeObservationID(meta.runID, meta.stageID, meta.executionID, *seq),
			Type:     obsType,
			Complete: true,
			Tool:     tool,
			Content:  detail,
			Actor:    meta.actor,
		})
	}
	return out
}

// incompleteObservation reports a torn/unparsable raw line as exactly one incomplete Observation — the trailing
// partial block case described on Observation.Complete. ok is false for a line that fails json.Unmarshal only;
// a VALID JSON line that simply isn't an "assistant" message (system/result/error events) carries no main-agent
// output to normalize and yields no observation at all (handled by the caller, not here).
func incompleteObservation(meta observationMeta, line string, seq *int) (Observation, bool) {
	var raw json.RawMessage
	if json.Unmarshal([]byte(line), &raw) == nil {
		return Observation{}, false
	}
	*seq++
	return Observation{
		RunID: meta.runID, StageID: meta.stageID, ExecutionID: meta.executionID,
		Seq:      *seq,
		ID:       ComputeObservationID(meta.runID, meta.stageID, meta.executionID, *seq),
		Type:     ObservationAssistantMessage,
		Complete: false,
		Content:  line,
		Actor:    meta.actor,
	}, true
}

// countExistingObservations replays a jsonl log file ALREADY on disk (e.g. from before a crash+restart) through
// the exact same normalization this file uses live, returning only the final seq count — the deterministic
// baseline a new invocation that appends to the SAME file must start counting from so Observation IDs never
// collide with (or renumber) ones already handed out before the restart. A missing/unreadable file (first-ever
// invocation) is baseline 0, not an error.
func countExistingObservations(meta observationMeta, jsonlFile string) int {
	f, err := os.Open(jsonlFile)
	if err != nil {
		return 0
	}
	defer f.Close()
	seq := 0
	_ = lineReader(f, func(line string) bool {
		if ev, ok := parseStreamEvent(line); ok {
			buildObservationsFromEvent(meta, ev, &seq)
		} else {
			incompleteObservation(meta, line, &seq) //nolint:errcheck // only advances seq; non-assistant valid JSON advances nothing
		}
		return true
	})
	return seq
}

// ObservationToAction derives the legacy (tool, detail) pair OnAction expects from a normalized Observation —
// the "existing UI publisher becomes an adapter over the new channel" seam (TestObservation_ExistingUIPublisherIsAdapter).
// It reproduces contentToAction's exact per-tool truncation rule (narrative text and the fp-based tools
// Write/Edit/Read/Glob/Grep are never truncated; Bash and any other tool are capped at truncateLimit) applied
// to the Observation's already-full Content, so there is exactly one place or the extraction logic
// (contentToAction) and exactly one place that decides truncation (here) instead of two independent copies.
func ObservationToAction(o Observation, truncateLimit int) (tool, detail string, ok bool) {
	if !o.Complete {
		// An incomplete (torn) block never produced an OnAction call before this feature existed either — the
		// original code only logged it via isErrorLine, which is independent of OnAction.
		return "", "", false
	}
	switch o.Type {
	case ObservationAssistantMessage:
		if o.Content == "" {
			return "", "", false
		}
		return contentTypeText, o.Content, true
	case ObservationToolAction:
		detail = o.Content
		switch o.Tool {
		case toolNameWrite, toolNameEdit, toolNameRead, toolNameGlob, toolNameGrep:
			// never truncated — mirrors contentToAction's dedicated case.
		default:
			if truncateLimit > 0 && len(detail) > truncateLimit {
				detail = detail[:truncateLimit] + "..."
			}
		}
		return o.Tool, detail, true
	default:
		return "", "", false
	}
}
