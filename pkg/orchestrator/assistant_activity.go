package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// helperActivitySink returns the OnActivity of one helper turn, nil for an unknown task. Root stage and turn are
// read once, not per event, to avoid a Store snapshot per parsed line. The executor redacts the text before
// OnActivity, so only safe text is published.
func (o *Orchestrator) helperActivitySink(taskID assistants.TaskID) func(executor.ActivityEvent) {
	t, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]
	if !ok {
		return nil
	}
	rootStage, turn, participant := t.RootStageID, t.Turn, string(o.taskMeshParticipant(taskID))
	return func(ev executor.ActivityEvent) {
		data := o.activityData(ev)
		data.TaskID, data.ParticipantID, data.Turn = string(taskID), participant, turn
		out := bus.Event{Type: bus.EventAssistantActivity, StageID: rootStage, Data: data}
		if ev.LiveOnly {
			o.ui.Publish(out) // a snapshot of a message that is still growing: shown now, replaced by the next one
			return
		}
		o.publishNotice(out)
	}
}

// activityData converts one observation to the saved payload. The caps bound one notice, so a page of 200 entries
// stays a few MiB and a saved line always fits the history reader. The full text remains in the helper's own log.
func (o *Orchestrator) activityData(ev executor.ActivityEvent) stagefiles.ActivityData {
	tool := ev.Tool
	if ev.Kind == executor.KindMessage {
		tool = activityTextTool
	}
	tool, toolCut := capUTF8(tool, stagefiles.MaxActivityToolBytes)
	detail, detailCut := capUTF8(ev.Detail, stagefiles.MaxActivityDetailBytes)
	invocation, invocationCut := capID(ev.InvocationID, stagefiles.MaxActivityInvocationBytes)
	item, itemCut := capID(ev.ItemID, stagefiles.MaxActivityItemBytes)
	return stagefiles.ActivityData{
		EventID:      "act-" + randomOpSuffix(),
		OccurredAt:   o.clk().Now().UTC().Format(time.RFC3339Nano),
		Tool:         tool,
		Detail:       detail,
		Truncated:    toolCut || detailCut || invocationCut || itemCut,
		Kind:         string(ev.Kind),
		InvocationID: invocation,
		ItemID:       item,
		Revision:     ev.Revision,
		Partial:      ev.Partial,
		Final:        ev.Final,
		Outcome:      string(ev.Outcome),
		ExitCode:     ev.ExitCode,
		StartedAt:    activityTime(ev.StartedAt),
		FinishedAt:   activityTime(ev.FinishedAt),
	}
}

func activityTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// activityTextTool is the tool name the dashboard renders as an assistant message.
const activityTextTool = "text"

// capUTF8 cuts s to at most limit bytes without splitting a rune.
func capUTF8(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit], true
}

// capID shortens an oversized identifier but keeps a digest of the whole value, so two long ids with a common
// prefix stay different items.
func capID(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	sum := sha256.Sum256([]byte(s))
	suffix := "~" + hex.EncodeToString(sum[:8])
	head, _ := capUTF8(s, limit-len(suffix))
	return head + suffix, true
}
