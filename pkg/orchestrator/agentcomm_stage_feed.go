package orchestrator

import (
	"cmp"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// StageMessagePage is one page of a stage feed's conversation messages, in journal order, with the threads
// they belong to (each once).
type StageMessagePage struct {
	Messages   []agentcomm.Message
	Threads    []agentcomm.Thread
	NextCursor uint64
	HasMore    bool
}

// threadInStageFeed is THE rule for which conversations a stage's feed shows: the stage opened the thread
// (origin_stage), or its agent or one of its helpers is a member. A stage's discussion threads always have its
// agent as a member, so they are included too. A thread journaled before origin_stage existed shows by
// membership alone. A run agent belongs to no stage: its conversations show where they were opened from and
// in the feeds of the stage members it talks to.
func threadInStageFeed(rs state.RunState, th *agentcomm.Thread, stageID string) bool {
	if stageID == "" {
		return false
	}
	if th.OriginStage == stageID {
		return true
	}
	return slices.ContainsFunc(th.Members, func(m agentcomm.ParticipantID) bool {
		if m == meshStageParticipantID(stageID) {
			return true
		}
		taskID, isHelper := strings.CutPrefix(string(m), meshHelperPrefix)
		t := rs.Assistants.Tasks[assistants.TaskID(taskID)]
		return isHelper && t != nil && t.Scope != assistants.ScopeRun && t.RootStageID == stageID
	})
}

// StageMessages returns the conversation messages stage stageID's feed shows (threadInStageFeed) committed
// after cursor, in journal order, at most limit of them (defaultMeshPageLimit when limit <= 0). A page never
// splits the messages of one operation (they share a seq), so a cursor-based reader never skips one.
func (o *Orchestrator) StageMessages(stageID string, cursor uint64, limit int) StageMessagePage {
	if limit <= 0 {
		limit = defaultMeshPageLimit
	}
	rs := o.opts.Store.Snapshot()
	mesh := rs.Assistants.Mesh
	var msgs []agentcomm.Message
	for _, id := range mesh.ThreadOrder {
		th := mesh.Threads[id]
		if th == nil || !threadInStageFeed(rs, th, stageID) {
			continue
		}
		for _, mid := range mesh.MessagesByThread[id] {
			if m := mesh.Messages[mid]; m != nil && m.Seq > cursor {
				msgs = append(msgs, *m)
			}
		}
	}
	slices.SortStableFunc(msgs, func(a, b agentcomm.Message) int { return cmp.Compare(a.Seq, b.Seq) })

	page := StageMessagePage{NextCursor: cursor}
	for _, m := range msgs {
		if len(page.Messages) >= limit && m.Seq != page.NextCursor {
			page.HasMore = true
			break
		}
		page.Messages = append(page.Messages, m)
		page.NextCursor = m.Seq
		if !slices.ContainsFunc(page.Threads, func(th agentcomm.Thread) bool { return th.ID == m.ThreadID }) {
			page.Threads = append(page.Threads, *mesh.Threads[m.ThreadID])
		}
	}
	return page
}
