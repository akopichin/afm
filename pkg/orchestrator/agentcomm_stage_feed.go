package orchestrator

import (
	"cmp"
	"slices"
	"sort"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// StageMessagePage is one page of a stage feed's conversation messages, in journal order, and every thread the
// feed shows — on every page, even one with no new message, so a reader sees a thread's status change (a
// conversation closed after its last message) without waiting for a message in it.
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
// after cursor, in journal order, at most limit of them (defaultMeshPageLimit when limit <= 0), with every
// thread of the feed. A page never splits the messages of one operation (they share a seq), so a cursor-based
// reader never skips one. It is polled every few seconds, so it reads the live projection (Store.View) instead
// of a full copy, and finds each thread's new messages by binary search over its seq-ordered message list: an
// empty poll costs a pass over the threads, not over the run's whole history.
func (o *Orchestrator) StageMessages(stageID string, cursor uint64, limit int) StageMessagePage {
	if limit <= 0 {
		limit = defaultMeshPageLimit
	}
	page := StageMessagePage{NextCursor: cursor}
	var msgs []agentcomm.Message
	o.opts.Store.View(func(rs *state.RunState) {
		mesh := rs.Assistants.Mesh
		for _, id := range mesh.ThreadOrder {
			th := mesh.Threads[id]
			if th == nil || !threadInStageFeed(*rs, th, stageID) {
				continue
			}
			thread := *th
			thread.Members = slices.Clone(th.Members)
			page.Threads = append(page.Threads, thread)
			msgs = append(msgs, messagesAfter(mesh, id, cursor, limit)...)
		}
	})
	slices.SortStableFunc(msgs, func(a, b agentcomm.Message) int { return cmp.Compare(a.Seq, b.Seq) })

	for _, m := range msgs {
		if len(page.Messages) >= limit && m.Seq != page.NextCursor {
			page.HasMore = true
			break
		}
		page.Messages = append(page.Messages, m)
		page.NextCursor = m.Seq
	}
	return page
}

// messagesAfter copies thread's messages committed after cursor — at most limit of them plus the rest of the
// last one's seq, enough for one page. A thread's message list is in commit order, so its seq never decreases
// and the first new message is found by binary search.
func messagesAfter(mesh state.MeshState, thread agentcomm.ThreadID, cursor uint64, limit int) []agentcomm.Message {
	ids := mesh.MessagesByThread[thread]
	seq := func(i int) uint64 {
		if m := mesh.Messages[ids[i]]; m != nil {
			return m.Seq
		}
		return 0
	}
	var out []agentcomm.Message
	for i := sort.Search(len(ids), func(i int) bool { return seq(i) > cursor }); i < len(ids); i++ {
		m := mesh.Messages[ids[i]]
		if m == nil {
			continue
		}
		if len(out) >= limit && m.Seq != out[len(out)-1].Seq {
			break
		}
		out = append(out, *m)
	}
	return out
}
