package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
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
// The returned entry is the revision THIS operation committed — rebuilt from the request and the commit's own
// seq/time, never read back from the board, which may already hold a newer revision (or, on a replay, does).
func (o *Orchestrator) Publish(ctx context.Context, req PublishRequest) (agentcomm.BoardEntry, error) {
	if req.OperationID == "" {
		return agentcomm.BoardEntry{}, errors.New("agentcomm: operation id is required")
	}
	if req.Key == "" || req.Author == "" {
		return agentcomm.BoardEntry{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "a board entry needs a key"}
	}

	status := redactMeshText(req.Status)
	publishEff, err := marshalEffect("mesh_board_published",
		state.Target{BoardKey: req.Key, ParticipantID: string(req.Author)},
		state.MeshBoardPublishedPayload{Status: status, ArtifactRefs: req.ArtifactRefs, ExpectedVersion: req.ExpectedVersion})
	if err != nil {
		return agentcomm.BoardEntry{}, err
	}

	beforeReady := readyWaitSet(o.opts.Store.Snapshot())
	runCtx := o.runContext(ctx) // a resumed waiter must not die with the HTTP request.

	op := state.Operation{ID: req.OperationID, Kind: "mesh_publish", Actor: req.Actor, Effects: []state.Effect{publishEff}}
	res, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshBoard, Data: map[string]any{"key": req.Key, "author_id": string(req.Author), "status": status}})
		o.notifyAndResumeNewlyReadyWaits(runCtx, beforeReady)
	})
	if err != nil {
		return agentcomm.BoardEntry{}, err
	}
	entry := agentcomm.BoardEntry{
		AuthorID: req.Author, Key: req.Key, Version: req.ExpectedVersion + 1, Status: status, ArtifactRefs: req.ArtifactRefs,
		Seq: res.Seq, Ts: res.Time,
	}
	o.notifyAboutBoardEntry(ctx, req, entry)
	return entry, nil
}

// boardNoticeStatusLimit bounds how much of an entry's status a notice quotes; the board holds the rest.
const boardNoticeStatusLimit = 1000

// notifyAboutBoardEntry tells every reachable participant in req.Notify about the just-published entry, with
// one message from its author per recipient — so no recipient or conversation-size limit can refuse the notice
// as a whole, and one finished or unknown recipient does not keep the others from hearing about it. The publish
// is already committed: a notice that cannot be sent is logged, never turned into a failed publish. Each
// message's request id is derived from the publish's and its recipient, so a retried publish replays the same
// messages instead of sending them twice.
func (o *Orchestrator) notifyAboutBoardEntry(ctx context.Context, req PublishRequest, entry agentcomm.BoardEntry) {
	if len(req.Notify) == 0 {
		return
	}
	status := entry.Status
	if len(status) > boardNoticeStatusLimit {
		status = strings.ToValidUTF8(status[:boardNoticeStatusLimit], "") + "…"
	}
	body := fmt.Sprintf("New task on the board: %s — %s. Report under %s/<your alias>.", entry.Key, status, entry.Key)
	rs := o.opts.Store.Snapshot()
	roster := o.liveRoster(rs)
	for _, name := range req.Notify {
		ids, err := resolveRecipients(roster, []agentcomm.ParticipantID{name})
		if err != nil {
			log.Printf("WARN: mesh: board key %s: not notifying %q: %v", entry.Key, name, err)
			continue
		}
		recipient := ids[0]
		if p, ok := resolveParticipant(rs, recipient); recipient == req.Author || !ok || !p.Reachable {
			continue
		}
		if _, err := o.SendFromAgent(ctx, SendRequest{
			OperationID: req.OperationID + ":notify:" + string(recipient), Sender: req.Author, Recipients: []agentcomm.ParticipantID{recipient},
			Kind: agentcomm.MessageKindRequest, Body: body, Actor: req.Actor,
		}); err != nil {
			log.Printf("WARN: mesh: board key %s: notify %s: %v", entry.Key, recipient, err)
		}
	}
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
