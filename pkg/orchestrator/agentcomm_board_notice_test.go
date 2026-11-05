package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

var boardNoticeModes = []assistants.CollaborationMode{assistants.CollaborationModeTeam, assistants.CollaborationModeAssistants}

// messageIDs lists the ids of msgs in order.
func messageIDs(msgs []agentcomm.Message) []agentcomm.MessageID {
	ids := make([]agentcomm.MessageID, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

// visibleTo is ids as a reader in mode gets them: without the notices when the Board is off. It fails the test
// when ids hold no notice, since such a row could not tell the modes apart.
func (r noticedRun) visibleTo(t *testing.T, row string, mode assistants.CollaborationMode, ids []agentcomm.MessageID) []agentcomm.MessageID {
	t.Helper()
	if !slices.ContainsFunc(ids, func(id agentcomm.MessageID) bool { return r.noticeIDs[id] }) {
		t.Fatalf("%s: the fixture puts no Board notice in %v", row, ids)
	}
	if mode == assistants.CollaborationModeTeam {
		return ids
	}
	return slices.DeleteFunc(slices.Clone(ids), func(id agentcomm.MessageID) bool { return r.noticeIDs[id] })
}

// noticeThreads are the conversations holding a Board notice, with their agent members.
func (r noticedRun) noticeThreads(mesh state.MeshState) map[agentcomm.ThreadID][]agentcomm.ParticipantID {
	threads := map[agentcomm.ThreadID][]agentcomm.ParticipantID{}
	for id := range r.noticeIDs {
		thread := mesh.Messages[id].ThreadID
		threads[thread] = slices.DeleteFunc(slices.Clone(mesh.Threads[thread].Members), func(p agentcomm.ParticipantID) bool {
			return p == meshUserParticipantID
		})
	}
	return threads
}

// Every agent-readable read — `afm agent inbox` with and without --all, and a thread — leaves the Board notices
// out in assistants mode and returns them in team mode; ordinary messages come back in both.
func TestBoardNotice_AgentReadsLeaveNoticesOut(t *testing.T) {
	r := newNoticedRun(t)
	ctx := context.Background()
	for _, mode := range boardNoticeModes {
		view := r.replayedView(t, mode)
		mesh := view.opts.Store.Snapshot().Assistants.Mesh
		check := func(row string, got []agentcomm.Message, all []agentcomm.MessageID) {
			t.Helper()
			if want := r.visibleTo(t, row, mode, all); !slices.Equal(messageIDs(got), want) {
				t.Errorf("%s mode, %s = %v, want %v", mode, row, messageIDs(got), want)
			}
			if mode == assistants.CollaborationModeTeam {
				return
			}
			for _, m := range got {
				if strings.Contains(m.Body, "afm agent task") || strings.Contains(m.Body, "afm agent board") {
					t.Errorf("assistants mode, %s returns Board commands in %s: %q", row, m.ID, m.Body)
				}
			}
		}

		for name, p := range r.holders {
			actor := state.Actor{Kind: "agent", ID: string(p)}
			for _, onlyNew := range []bool{true, false} {
				page, err := view.ReadInbox(ctx, ReadInboxRequest{Participant: p, OnlyNew: onlyNew, Actor: actor})
				if err != nil {
					t.Fatalf("ReadInbox: %v", err)
				}
				all := slices.DeleteFunc(slices.Clone(mesh.Inbox[p]), func(id agentcomm.MessageID) bool {
					return onlyNew && mesh.Receipts[id][p] != agentcomm.DeliveryAccepted
				})
				check(fmt.Sprintf("the %s's inbox (only new: %v)", name, onlyNew), page.Messages, all)
			}
		}
		for thread, members := range r.noticeThreads(mesh) {
			for _, p := range members {
				page, err := view.ReadThread(ctx, ReadThreadRequest{ThreadID: thread, Reader: p, Actor: state.Actor{Kind: "agent", ID: string(p)}})
				if err != nil {
					t.Fatalf("ReadThread: %v", err)
				}
				check(fmt.Sprintf("thread %s read by %s", thread, p), page.Messages, mesh.MessagesByThread[thread])
			}
		}
	}
}

// A page holds limit visible messages and its cursor goes on from the last one: a reader that stops on a short page
// (the Assistant's bridge) never takes a notice-filled page for the end of its inbox.
func TestBoardNotice_InboxPagesCountVisibleMessages(t *testing.T) {
	r := newNoticedRun(t)
	const limit = 2
	for _, mode := range boardNoticeModes {
		view := r.replayedView(t, mode)
		mesh := view.opts.Store.Snapshot().Assistants.Mesh
		for name, p := range r.holders {
			var got []agentcomm.MessageID
			cursor := uint64(0)
			for {
				page, err := view.ReadInbox(context.Background(), ReadInboxRequest{Participant: p, Cursor: cursor, Limit: limit})
				if err != nil {
					t.Fatalf("ReadInbox: %v", err)
				}
				got = append(got, messageIDs(page.Messages)...)
				if len(page.Messages) < limit {
					break
				}
				cursor = page.NextCursor
			}
			if want := r.visibleTo(t, name+"'s inbox", mode, mesh.Inbox[p]); !slices.Equal(got, want) {
				t.Errorf("%s mode, the %s's inbox paged by %d = %v, want %v", mode, name, limit, got, want)
			}
		}
	}
	if visible := len(r.notes["run agent"]); visible <= limit {
		t.Fatalf("the run agent has %d ordinary messages, want more than a page of %d", visible, limit)
	}
}

// The human run owner is no agent: the threads and the stage feed show the notices in both modes.
func TestBoardNotice_OwnerReadsKeepNotices(t *testing.T) {
	r := newNoticedRun(t)
	for _, mode := range boardNoticeModes {
		view := r.replayedView(t, mode)
		mesh := view.opts.Store.Snapshot().Assistants.Mesh
		for thread := range r.noticeThreads(mesh) {
			for _, reader := range []agentcomm.ParticipantID{meshUserParticipantID, ""} {
				page, err := view.ReadThread(context.Background(), ReadThreadRequest{ThreadID: thread, Reader: reader})
				if err != nil {
					t.Fatalf("ReadThread: %v", err)
				}
				if got, want := messageIDs(page.Messages), mesh.MessagesByThread[thread]; !slices.Equal(got, want) {
					t.Errorf("%s mode, thread %s read by %q = %v, want %v", mode, thread, reader, got, want)
				}
			}
		}
		feed := messageIDs(view.StageMessages(assistantsTestStageID, 0, 0).Messages)
		for _, id := range r.notices["stage"] {
			if !slices.Contains(feed, id) {
				t.Errorf("%s mode, the stage feed %v misses Board notice %s", mode, feed, id)
			}
		}
	}
}

// The Assistant's own history asks the orchestrator which sources to leave out: only notices, only without the Board.
func TestBoardNotice_HidesFromAgents(t *testing.T) {
	r := newNoticedRun(t)
	for _, mode := range boardNoticeModes {
		view := r.replayedView(t, mode)
		for id := range r.noticeIDs {
			if got, want := view.HidesFromAgents(id), mode == assistants.CollaborationModeAssistants; got != want {
				t.Errorf("%s mode, HidesFromAgents(notice %s) = %v, want %v", mode, id, got, want)
			}
		}
		for name, ids := range r.notes {
			for _, id := range ids {
				if view.HidesFromAgents(id) {
					t.Errorf("%s mode, HidesFromAgents(ordinary message %s to the %s) = true", mode, id, name)
				}
			}
		}
	}
}

// Without the Board the notices are retired once, as handed over: alone they no longer wake an agent, satisfy an
// addressed_message wait or keep an idle run agent busy. A second sweep and a sweep in team mode commit nothing.
func TestBoardNotice_RetiredAsDelivered(t *testing.T) {
	r := newNoticedRun(t)
	ctx := context.Background()
	runTask, _ := participantTask(r.f.store.Snapshot(), r.holders["run agent"])
	for _, mode := range boardNoticeModes {
		view := r.replayedView(t, mode)
		store := view.opts.Store
		mesh := store.Snapshot().Assistants.Mesh
		for _, p := range r.holders {
			for _, id := range mesh.Inbox[p] {
				if !r.noticeIDs[id] {
					if err := view.MarkMessageDelivered(ctx, id, p); err != nil {
						t.Fatalf("deliver ordinary message %s: %v", id, err)
					}
				}
			}
		}
		before := store.Snapshot()
		for name, p := range r.holders {
			if !before.Assistants.Mesh.HasWakingUnreadFor(p) {
				t.Fatalf("%s mode: the %s has no unread notice before the sweep", mode, name)
			}
		}

		for range 2 {
			if err := view.retireHiddenBoardNotices(ctx); err != nil {
				t.Fatalf("%s mode, retireHiddenBoardNotices: %v", mode, err)
			}
		}
		after := store.Snapshot()
		if mode == assistants.CollaborationModeTeam {
			if after.LastSeq != before.LastSeq {
				t.Errorf("team mode: the sweep committed records (LastSeq %d → %d)", before.LastSeq, after.LastSeq)
			}
			continue
		}
		for id := range r.noticeIDs {
			for p, got := range after.Assistants.Mesh.Receipts[id] {
				if got != agentcomm.DeliveryDelivered {
					t.Errorf("assistants mode: notice %s to %s is %s after the sweep, want delivered", id, p, got)
				}
			}
		}
		for name, p := range r.holders {
			if after.Assistants.Mesh.HasWakingUnreadFor(p) {
				t.Errorf("assistants mode: notices alone still wake the %s", name)
			}
			if waitConditionHolds(after, p, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage}) {
				t.Errorf("assistants mode: notices alone satisfy the %s's addressed_message wait", name)
			}
		}
		idle := *after.Assistants.Tasks[runTask]
		idle.Status, idle.StatusReason = assistants.StatusQueued, ""
		if !idleRunAgent(after, &idle) {
			t.Error("assistants mode: notices alone keep a queued run agent from being idle")
		}
		if again := store.Snapshot().LastSeq; again != after.LastSeq {
			t.Errorf("assistants mode: a repeated sweep committed records (LastSeq %d → %d)", after.LastSeq, again)
		}
	}
}

// A run resumed without the Board retires its notices before it schedules anything; one resumed in team mode keeps them.
func TestBoardNotice_RunRetiresNoticesBeforeScheduling(t *testing.T) {
	for _, mode := range boardNoticeModes {
		t.Run(string(mode), func(t *testing.T) {
			runDir := t.TempDir()
			disabled := false
			opts := Options{
				RunDir: runDir, RunID: "notice-run", Config: config.Default(), Prompts: DefaultPrompts(),
				Stages:            []flow.Stage{{ID: "s1", Name: "S1", Script: "true", AutoRun: &disabled}},
				CollaborationMode: assistants.CollaborationModeTeam,
			}
			store, err := state.Open(runDir, []string{"s1"})
			if err != nil {
				t.Fatal(err)
			}
			opts.Store = store
			team := New(opts)
			if _, err := team.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{Command: "codex", Actor: state.Actor{Kind: "system"}}); err != nil {
				t.Fatalf("RegisterSideAgent: %v", err)
			}
			team.SetSideAgentAvailable(true)
			side := team.SideAgentParticipantID()
			if _, err := team.Publish(context.Background(), PublishRequest{OperationID: "publish-notes", Author: meshUserParticipantID, Key: "notes/api",
				Status: "ready", Notify: []agentcomm.ParticipantID{side}, Actor: state.Actor{Kind: "user"}}); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			notice := store.Snapshot().Assistants.Mesh.Inbox[side][0]
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			resumed, err := state.Open(runDir, []string{"s1"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { resumed.Close() })
			opts.Store, opts.CollaborationMode = resumed, mode
			o := New(opts)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { _ = o.Run(ctx); close(done) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("Run did not return within 10s of cancellation")
				}
			})
			// The auto_run gate pauses the stage on its first activation: scheduling has run.
			waitForTrue(t, func() bool { return resumed.Snapshot().Stages["s1"].Status == state.StatusPaused }, 3*time.Second, "s1 was not paused")

			rs := resumed.Snapshot()
			deliveredAt, delivered := rs.Assistants.OperationIndex[meshDeliverOperationID(notice, side)]
			if mode == assistants.CollaborationModeTeam {
				if delivered || rs.Assistants.Mesh.Receipts[notice][side] != agentcomm.DeliveryAccepted {
					t.Errorf("team mode: notice %s is %s, want accepted", notice, rs.Assistants.Mesh.Receipts[notice][side])
				}
				return
			}
			if !delivered || rs.Assistants.Mesh.Receipts[notice][side] != agentcomm.DeliveryDelivered {
				t.Fatalf("assistants mode: notice %s is %s, want delivered", notice, rs.Assistants.Mesh.Receipts[notice][side])
			}
			if pausedAt := rs.Stages["s1"].Seq; deliveredAt >= pausedAt {
				t.Errorf("assistants mode: the notice was retired at seq %d, after scheduling paused s1 at seq %d", deliveredAt, pausedAt)
			}
		})
	}
}
