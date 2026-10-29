package orchestrator

import (
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// helperActivitySink returns the OnAction of one helper turn, nil for an unknown task. Root stage and turn are
// read once, not per action, to avoid a Store snapshot per parsed line. The executor redacts (tool, detail)
// before OnAction, so only safe text is published.
func (o *Orchestrator) helperActivitySink(taskID assistants.TaskID) func(tool, detail string) {
	t, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]
	if !ok {
		return nil
	}
	rootStage, turn, participant := t.RootStageID, t.Turn, string(o.taskMeshParticipant(taskID))
	return func(tool, detail string) {
		// The caps bound one notice, so a page of 200 entries stays a few MiB and a saved line always fits the
		// history reader. The full text remains in the helper's own log.
		tool, toolCut := capUTF8(tool, stagefiles.MaxActivityToolBytes)
		detail, detailCut := capUTF8(detail, stagefiles.MaxActivityDetailBytes)
		o.publishNotice(bus.Event{Type: bus.EventAssistantActivity, StageID: rootStage, Data: stagefiles.ActivityData{
			EventID:       "act-" + randomOpSuffix(),
			TaskID:        string(taskID),
			ParticipantID: participant,
			Turn:          turn,
			OccurredAt:    o.clk().Now().UTC().Format(time.RFC3339Nano),
			Tool:          tool,
			Detail:        detail,
			Truncated:     toolCut || detailCut,
		}})
	}
}

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
