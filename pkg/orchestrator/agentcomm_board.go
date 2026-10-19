package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.4: Publish/ReadBoard. Publish resolves every currently-pending WaitOnBoardKey wait
// that this exact publish satisfies in the SAME ApplyRunOperation as the publish itself — not a separate
// pass, and not something a restart needs to re-derive: once committed, the wait's own stored status is
// already Ready, so replay reproduces it with no second pass over notices.jsonl (tmp/agent-mesh.md §6.5's
// "после рестарта уже выполненное условие замечается без повторной публикации события в live-bus").

// Publish implements Service.Publish. Any PENDING WaitOnBoardKey wait this publish satisfies is resolved by
// applyMeshBoardPublished itself (pkg/state/mesh_board.go), in the SAME commit as the publish — not
// precomputed here from a snapshot, which could race a concurrent Wait registration or break idempotent
// replay. beforeReady only tells notifyAndResumeNewlyReadyWaits which waits to skip because they were already
// ready before this call.
//
// The notices to req.Notify are messages committed in the SAME operation as the revision, so nothing — a
// run finishing in between included — can keep the revision and lose its notices: FinalizeIf sees them
// unread. A recipient that cannot take a message now (finished, unknown, inbox or conversation full) is
// skipped, never a reason to fail the publish; if that changes before the commit, the notices are planned
// again from the new state.
//
// The returned entry is the revision THIS operation committed — rebuilt from the committed operation, never
// read back from the board, which may already hold a newer revision.
func (o *Orchestrator) Publish(ctx context.Context, req PublishRequest) (agentcomm.BoardEntry, error) {
	if req.OperationID == "" {
		return agentcomm.BoardEntry{}, errors.New("agentcomm: operation id is required")
	}
	if req.Key == "" || req.Author == "" {
		return agentcomm.BoardEntry{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "a board entry needs a key"}
	}
	if len(req.Notify) > agentcomm.MaxBoardNotify {
		return agentcomm.BoardEntry{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest,
			Reason: fmt.Sprintf("a board publish may notify at most %d participants, got %d", agentcomm.MaxBoardNotify, len(req.Notify))}
	}
	notify := make([]string, len(req.Notify))
	for i, n := range req.Notify {
		notify[i] = string(n)
	}
	status := o.RedactSecrets(req.Status)
	publishEff, err := marshalEffect("mesh_board_published",
		state.Target{BoardKey: req.Key, ParticipantID: string(req.Author)},
		state.MeshBoardPublishedPayload{Status: status, ArtifactRefs: req.ArtifactRefs, ExpectedVersion: req.ExpectedVersion, Notify: &notify})
	if err != nil {
		return agentcomm.BoardEntry{}, err
	}
	runCtx := o.runContext(ctx) // a resumed waiter must not die with the HTTP request.

	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		if entry, replayed, err := o.replayedPublish(req, notify); replayed {
			return entry, err
		}
		rs := o.opts.Store.Snapshot()
		notices, err := o.planBoardNotices(rs, req, status)
		if err != nil {
			return agentcomm.BoardEntry{}, err
		}
		effects := []state.Effect{publishEff}
		var recipients []agentcomm.ParticipantID
		for _, n := range notices {
			effects = append(effects, n.effects...)
			recipients = append(recipients, n.send.Recipients...)
		}
		beforeReady := readyWaitSet(rs)
		op := state.Operation{ID: req.OperationID, Kind: opKindMeshPublish, Actor: req.Actor, Effects: effects, Expect: state.Preconditions{
			Check: func(cur *state.RunState) error { return rejectUnreachableRecipients(*cur, recipients) },
		}}
		res, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
			o.publishNotice(bus.Event{Type: bus.EventMeshBoard, Data: map[string]any{"key": req.Key, "author_id": string(req.Author), "status": status}})
			for _, n := range notices {
				o.announceMessage(n.send, n.messageID, n.openedThread)
			}
			o.notifyAndResumeNewlyReadyWaits(runCtx, beforeReady)
			o.wakeRecipients(runCtx, recipients)
		})
		if err == nil {
			return agentcomm.BoardEntry{
				AuthorID: req.Author, Key: req.Key, Version: req.ExpectedVersion + 1, Status: status, ArtifactRefs: req.ArtifactRefs,
				Seq: res.Seq, Ts: res.Time,
			}, nil
		}
		if attempt == maxAttempts || !o.worthAnotherPublishAttempt(req, status, notices, err) {
			return agentcomm.BoardEntry{}, err
		}
	}
}

// worthAnotherPublishAttempt reports whether a rejected publish is tried again: the same request id committed
// meanwhile (a concurrent retry — the next attempt answers from that commit), or the notices planned from the
// current state differ from the rejected ones (a recipient finished, a conversation filled or closed). A
// rejection the notices did not cause is the publish's own answer.
func (o *Orchestrator) worthAnotherPublishAttempt(req PublishRequest, status string, rejectedNotices []boardNotice, err error) bool {
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) {
		return false
	}
	rs := o.opts.Store.Snapshot()
	if _, committed := rs.Assistants.OperationIndex[req.OperationID]; committed {
		return true
	}
	again, perr := o.planBoardNotices(rs, req, status)
	return perr == nil && !reflect.DeepEqual(again, rejectedNotices)
}

// replayedPublish answers a publish whose request id already committed with the revision that operation
// committed; another key, author, status, artifacts, expected version or notify list under that id is a
// conflict. A record written before the notify list was stored has none to compare: its retry is answered
// from the record whatever notify list it carries. replayed=false: nothing committed.
func (o *Orchestrator) replayedPublish(req PublishRequest, notify []string) (agentcomm.BoardEntry, bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindMeshPublish)
	if err != nil || !committed {
		return agentcomm.BoardEntry{}, err != nil, err // a storage error answers the request too
	}
	var p state.MeshBoardPublishedPayload
	target, found, err := op.Effect("mesh_board_published", &p)
	if err != nil {
		return agentcomm.BoardEntry{}, true, err
	}
	if !found || target.BoardKey != req.Key || target.ParticipantID != string(req.Author) || !o.sameRedactedText(p.Status, req.Status) ||
		!slices.Equal(p.ArtifactRefs, req.ArtifactRefs) || p.ExpectedVersion != req.ExpectedVersion || (p.Notify != nil && !slices.Equal(*p.Notify, notify)) {
		return agentcomm.BoardEntry{}, true, state.OperationIDConflictError(req.OperationID)
	}
	return agentcomm.BoardEntry{
		AuthorID: req.Author, Key: req.Key, Version: p.ExpectedVersion + 1, Status: p.Status, ArtifactRefs: p.ArtifactRefs,
		Seq: op.Result.Seq, Ts: op.Result.Time,
	}, true, nil
}

// boardNoticeStatusLimit and boardNoticeKeyLimit bound how much of an entry's status and key a notice quotes;
// the board holds the rest. Together with agentcomm.MaxBoardNotify they keep a publish's journal record small.
const (
	boardNoticeStatusLimit = 1000
	boardNoticeKeyLimit    = 200
)

// clipForNotice returns s cut to at most limit bytes (on a UTF-8 boundary), marked with "…" when cut.
func clipForNotice(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "") + "…"
}

// boardNotice is one planned notice of a publish: a message from the author to one recipient, in their open
// conversation or in a new one opened by the same operation.
type boardNotice struct {
	send         SendRequest
	messageID    agentcomm.MessageID
	openedThread bool
	effects      []state.Effect
}

// planBoardNotices plans one message from the author per reachable participant in req.Notify — one per
// recipient, so no recipient or conversation-size limit can refuse the notices as a whole. A name that cannot
// receive a message now is skipped with a log line. Each message id is derived from the publish's request id
// and its recipient.
func (o *Orchestrator) planBoardNotices(rs state.RunState, req PublishRequest, status string) ([]boardNotice, error) {
	if len(req.Notify) == 0 {
		return nil, nil
	}
	key := clipForNotice(req.Key, boardNoticeKeyLimit)
	body := fmt.Sprintf("New task on the board: %s — %s. Report under %s/<your alias>.", key, clipForNotice(status, boardNoticeStatusLimit), key)
	roster := o.liveRoster(rs)
	var notices []boardNotice
	seen := map[agentcomm.ParticipantID]bool{req.Author: true}
	for _, name := range req.Notify {
		ids, err := resolveRecipients(roster, []agentcomm.ParticipantID{name})
		if err != nil {
			log.Printf("WARN: mesh: board key %s: not notifying %q: %v", req.Key, name, err)
			continue
		}
		recipient := ids[0]
		if seen[recipient] {
			continue
		}
		seen[recipient] = true
		if p, ok := resolveParticipant(rs, recipient); !ok || !p.Reachable {
			continue
		}
		if err := rs.Assistants.Mesh.CheckInboxCapacity([]agentcomm.ParticipantID{recipient}); err != nil {
			log.Printf("WARN: mesh: board key %s: not notifying %s: %v", req.Key, recipient, err)
			continue
		}
		n := boardNotice{
			send: SendRequest{
				Sender: req.Author, Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindRequest, Body: body,
			},
			messageID: deriveMeshMessageID(req.OperationID + ":notify:" + string(recipient)),
		}
		members := []agentcomm.ParticipantID{req.Author, recipient}
		if th := openThreadWithMembers(rs, members); th != "" {
			if rs.Assistants.Mesh.ThreadFull(th) {
				log.Printf("WARN: mesh: board key %s: not notifying %s: conversation %s is full", req.Key, recipient, th)
				continue
			}
			n.send.ThreadID = th
		} else {
			n.send.ThreadID = deriveMeshThreadID(conversationOpenID(rs, members))
			open, err := threadOpenEffects(rs, n.send.ThreadID, "", agentcomm.ScopeRun, sortedMemberSet(members), 0)
			if err != nil {
				return nil, err
			}
			n.effects, n.openedThread = open, true
		}
		msg, err := messageSentEffect(n.send, n.messageID, body)
		if err != nil {
			return nil, err
		}
		n.effects = append(n.effects, msg)
		if mirror, ok, err := discussionMirrorOfReply(rs, n.send, n.messageID, body); err != nil {
			return nil, err
		} else if ok {
			n.effects = append(n.effects, mirror)
		}
		notices = append(notices, n)
	}
	return notices, nil
}

// ReadBoard implements Service.ReadBoard. An empty Key returns every entry, sorted for a deterministic
// result (the board has no commit-order index of its own, unlike threads/inboxes).
func (o *Orchestrator) ReadBoard(ctx context.Context, req ReadBoardRequest) (BoardPage, error) {
	rs := o.opts.Store.Snapshot()
	if req.Key != "" {
		e, ok := rs.Assistants.Mesh.Board[req.Key]
		if !ok {
			return BoardPage{}, nil
		}
		return BoardPage{Entries: []agentcomm.BoardEntry{e.BoardEntry}}, nil
	}
	keys := make([]string, 0, len(rs.Assistants.Mesh.Board))
	for k := range rs.Assistants.Mesh.Board {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	page := BoardPage{Entries: make([]agentcomm.BoardEntry, 0, len(keys))}
	for _, k := range keys {
		page.Entries = append(page.Entries, rs.Assistants.Mesh.Board[k].BoardEntry)
	}
	return page, nil
}
