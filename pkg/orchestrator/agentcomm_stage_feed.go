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

// StageMessagePage is what changed in a stage feed's conversations in the journal range (cursor, NextCursor]:
// the messages committed there, in journal order, and the feed's threads opened or closed there. A reader that
// keeps the threads it has seen and merges each page's threads by id sees a status change (a conversation
// closed after its last message) without waiting for a message in it, and a quiet poll returns nothing.
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
// after cursor, in journal order, at most limit of them (defaultMeshPageLimit when limit <= 0), and the feed's
// threads changed in the same range. A page never splits the messages of one operation (they share a seq), so
// a cursor-based reader never skips one. The last page also covers threads changed after its last message, and
// its NextCursor moves past them, so the next poll does not return them again. It is polled every few seconds,
// so it reads the live projection (Store.View) instead of a full copy, and finds each thread's new messages by
// binary search over its seq-ordered message list: an empty poll costs a pass over the threads' ids, not over
// the run's whole history, and copies nothing.
func (o *Orchestrator) StageMessages(stageID string, cursor uint64, limit int) StageMessagePage {
	if limit <= 0 {
		limit = defaultMeshPageLimit
	}
	page := StageMessagePage{NextCursor: cursor}
	var msgs []agentcomm.Message
	var changed []changedThread
	o.opts.Store.View(func(rs *state.RunState) {
		mesh := rs.Assistants.Mesh
		for _, id := range mesh.ThreadOrder {
			th := mesh.Threads[id]
			if th == nil || !threadInStageFeed(*rs, th, stageID) {
				continue
			}
			if seq := mesh.ThreadChangedSeq[id]; seq > cursor {
				thread := *th
				thread.Members = slices.Clone(th.Members)
				changed = append(changed, changedThread{thread, seq})
			}
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
	lastMessage := page.NextCursor
	for _, c := range changed {
		if page.HasMore && c.seq > lastMessage {
			continue // a later page covers it
		}
		page.Threads = append(page.Threads, c.thread)
		page.NextCursor = max(page.NextCursor, c.seq)
	}
	return page
}

// changedThread is a copy of a thread with the seq of the record that last changed it.
type changedThread struct {
	thread agentcomm.Thread
	seq    uint64
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
