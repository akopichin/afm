package state

import (
	"errors"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// MeshBoardEntry is the projection's board entry. A key belongs to its first publisher: only that author
// publishes its next revisions (see applyMeshBoardPublished).
type MeshBoardEntry struct {
	agentcomm.BoardEntry
}

// ErrBoardStaleRevision — a mesh_board_published record's ExpectedVersion does not match the key's current
// Version (or the key has no prior publish and ExpectedVersion != 0) — the expected-version CAS that guards
// the lost-update race, the same role Preconditions.StageGeneration/TaskStatus play for ApplyRunOperation
// (operation.go), at the projection level since no caller wires a Preconditions check for the board yet.
var ErrBoardStaleRevision = errors.New("mesh: board publish is based on a stale revision")

// ErrBoardNotAuthorized — a mesh_board_published record's publisher is not the key's author.
var ErrBoardNotAuthorized = errors.New("mesh: board publisher is not authorized for this key")

// ErrBoardArtifactRefOutsideRun — an ArtifactRef does not start with boardArtifactRefPrefix.
var ErrBoardArtifactRefOutsideRun = errors.New("mesh: board artifact ref is outside the run's artifact namespace")

// boardRejected is a typed, fixable board rejection (4xx with the reason) that still errors.Is its sentinel.
func boardRejected(code assistants.ErrorCode, sentinel error, format string, args ...any) error {
	return &OperationRejectedError{Code: code, Reason: fmt.Sprintf(format, args...), Cause: sentinel}
}

// boardArtifactRefPrefix is the only prefix mesh_board_published's ArtifactRefs may start with. pkg/state has
// no notion of "the run's artifact store" (that lives in pkg/executor/pkg/server) — this is a cheap
// projection-level sanity check, not a filesystem check: it only catches a ref shaped like something that
// could never be one of this run's own artifacts (an absolute path, a URL, ...), mirroring the existing
// feed-image convention of naming run artifacts by a path relative to the run's own artifacts directory
// (AGENTS.md's "[AFM image: <name>]").
const boardArtifactRefPrefix = "artifacts/"

// MeshBoardPublishedPayload is the mesh_board_published control record's payload — decoded here by
// applyMeshBoardPublished. The key is Target.BoardKey; the publishing author is Target.ParticipantID (reusing
// the field T1.1/T1.2 already use for "the participant this record is about").
type MeshBoardPublishedPayload struct {
	Status          string   `json:"status"`
	ArtifactRefs    []string `json:"artifact_refs,omitempty"`
	ExpectedVersion int      `json:"expected_version"`
	// Notify is the publish request's own list of participants to tell about the entry, kept so a retry of the
	// same request is compared with it in full. The notices themselves are separate effects of the same
	// operation; the projection does not read this field. A new record always carries it, an empty list
	// included; nil means a record written before the field existed, whose notify list is unknown.
	Notify *[]string `json:"notify,omitempty"`
}

// applyMeshBoardPublished projects one mesh_board_published control record.
//
// Versioning: the first publish of a key is always Version 1 and must declare ExpectedVersion 0 ("I am not
// superseding anything"); every later publish must declare ExpectedVersion equal to the key's CURRENT
// Version, exactly the lost-update guard a plain "if changed since I last read, reject" CAS gives, without
// this package needing to track per-reader read timestamps.
//
// Ownership: a key belongs to its author — every later revision must come from that same author. Anyone
// else reports under a key of their own (e.g. "<task key>/<alias>" for a report on a task the human posted).
// The rule applies to NEW publishes only: older journals may hold a revision by a participant the author
// granted it to through `authorized_by`, and such a journal must still replay.
func applyMeshBoardPublished(as *AssistantsState, c *Control, dryRun bool) error {
	key := c.Target.BoardKey
	if key == "" {
		return errors.New("mesh_board_published: empty target board_key")
	}
	author := agentcomm.ParticipantID(c.Target.ParticipantID)
	if author == "" {
		return errors.New("mesh_board_published: empty target participant_id")
	}

	var p MeshBoardPublishedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_board_published payload: %w", err)
	}
	for _, ref := range p.ArtifactRefs {
		if !strings.HasPrefix(ref, boardArtifactRefPrefix) {
			return boardRejected(assistants.CodeInvalidRequest, ErrBoardArtifactRefOutsideRun, "artifact ref %q of board key %s is outside this run's artifacts", ref, key)
		}
	}

	existing, exists := as.Mesh.Board[key]
	version := 1
	if !exists {
		if p.ExpectedVersion != 0 {
			return boardRejected(assistants.CodeStaleRevision, ErrBoardStaleRevision, "board key %s is not published yet: expected_version must be 0, got %d", key, p.ExpectedVersion)
		}
	} else {
		if p.ExpectedVersion != existing.Version {
			return boardRejected(assistants.CodeStaleRevision, ErrBoardStaleRevision, "board key %s is at version %d, not %d — read it again (afm agent board get)", key, existing.Version, p.ExpectedVersion)
		}
		if author != existing.AuthorID && as.admittingNewRecord(dryRun) {
			return boardRejected(assistants.CodeActorNotAllowed, ErrBoardNotAuthorized, "%s may not update board key %s (published by %s) — publish your own key, e.g. %s/<your alias>", author, key, existing.AuthorID, key)
		}
		version = existing.Version + 1
	}

	if dryRun {
		return nil
	}

	entry := &MeshBoardEntry{BoardEntry: agentcomm.BoardEntry{
		AuthorID: author, Key: key, Version: version, Status: p.Status, ArtifactRefs: p.ArtifactRefs,
		Seq: c.Seq, Ts: c.Time,
	}}
	as.Mesh.Board[key] = entry

	// Resolve every PENDING WaitOnBoardKey wait this publish satisfies in the SAME commit — decided here,
	// against the live Mesh state, not by the orchestrator from a pre-publish snapshot. See
	// resolvePendingWaits's doc comment (pkg/state/mesh_waits.go) for why.
	as.resolvePendingWaits(agentcomm.WaitReady, func(w *MeshWait) bool {
		return as.meshWaitSatisfied(w)
	})
	return nil
}
