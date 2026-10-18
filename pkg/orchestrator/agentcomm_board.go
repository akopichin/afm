package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
func (o *Orchestrator) Publish(ctx context.Context, req PublishRequest) (agentcomm.BoardEntry, error) {
	if req.OperationID == "" {
		return agentcomm.BoardEntry{}, errors.New("agentcomm: operation id is required")
	}
	if req.Key == "" || req.Author == "" {
		return agentcomm.BoardEntry{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "a board entry needs a key"}
	}

	authorizedBy := make([]string, len(req.AuthorizedBy))
	for i, a := range req.AuthorizedBy {
		authorizedBy[i] = string(a)
	}
	publishEff, err := marshalEffect("mesh_board_published",
		state.Target{BoardKey: req.Key, ParticipantID: string(req.Author)},
		state.MeshBoardPublishedPayload{
			Status: req.Status, ArtifactRefs: req.ArtifactRefs, ExpectedVersion: req.ExpectedVersion, AuthorizedBy: authorizedBy,
		})
	if err != nil {
		return agentcomm.BoardEntry{}, err
	}

	beforeReady := readyWaitSet(o.opts.Store.Snapshot())
	runCtx := o.runContext(ctx) // a resumed waiter must not die with the HTTP request.

	op := state.Operation{ID: req.OperationID, Kind: "mesh_publish", Actor: req.Actor, Effects: []state.Effect{publishEff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshBoard, Data: map[string]any{"key": req.Key, "author_id": string(req.Author), "status": req.Status}})
		o.notifyAndResumeNewlyReadyWaits(runCtx, beforeReady)
	})
	if err != nil {
		return agentcomm.BoardEntry{}, err
	}

	after := o.opts.Store.Snapshot()
	entry, ok := after.Assistants.Mesh.Board[req.Key]
	if !ok {
		return agentcomm.BoardEntry{}, fmt.Errorf("agentcomm: board entry %q missing immediately after publish", req.Key)
	}
	return entry.BoardEntry, nil
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
