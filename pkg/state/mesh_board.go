package state

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// MeshBoardEntry wraps agentcomm.BoardEntry with the minimal republish-authorization bookkeeping this
// projection needs (T1.4): which OTHER participant ids the CURRENT author has explicitly allowed to publish
// the NEXT revision of this key. See applyMeshBoardPublished's doc comment for the authorization rule this
// supports.
type MeshBoardEntry struct {
	agentcomm.BoardEntry
	AuthorizedBy []agentcomm.ParticipantID `json:"authorized_by,omitempty"`
}

// ErrBoardStaleRevision — a mesh_board_published record's ExpectedVersion does not match the key's current
// Version (or the key has no prior publish and ExpectedVersion != 0) — the expected-version CAS that guards
// the lost-update race, the same role Preconditions.StageGeneration/TaskStatus play for ApplyRunOperation
// (operation.go), at the projection level since no caller wires a Preconditions check for the board yet.
var ErrBoardStaleRevision = errors.New("mesh: board publish is based on a stale revision")

// ErrBoardNotAuthorized — a mesh_board_published record's publisher is neither the key's current AuthorID nor
// named in that entry's AuthorizedBy list (see applyMeshBoardPublished's doc comment for the full rule).
var ErrBoardNotAuthorized = errors.New("mesh: board publisher is not authorized for this key")

// ErrBoardArtifactRefOutsideRun — an ArtifactRef does not start with boardArtifactRefPrefix.
var ErrBoardArtifactRefOutsideRun = errors.New("mesh: board artifact ref is outside the run's artifact namespace")

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
	// AuthorizedBy lets this publish name OTHER participants allowed to publish the NEXT revision of this key
	// — see applyMeshBoardPublished's doc comment.
	AuthorizedBy []string `json:"authorized_by,omitempty"`
}

// applyMeshBoardPublished projects one mesh_board_published control record.
//
// Versioning: the first publish of a key is always Version 1 and must declare ExpectedVersion 0 ("I am not
// superseding anything"); every later publish must declare ExpectedVersion equal to the key's CURRENT
// Version, exactly the lost-update guard a plain "if changed since I last read, reject" CAS gives, without
// this package needing to track per-reader read timestamps.
//
// Authorization (Phase 1's minimal model — documented here since agentcomm.BoardEntry has no general ACL of
// its own): a publish is accepted if its author is EITHER the key's current AuthorID, OR was explicitly named
// in the current entry's AuthorizedBy list by whoever published that entry. There is no third way to become
// authorized (no roles, no "anyone in the thread") — a later phase that needs richer sharing can widen this
// without changing the journal format, since AuthorizedBy is already a list.
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
			return fmt.Errorf("mesh_board_published %s: %w: %q", key, ErrBoardArtifactRefOutsideRun, ref)
		}
	}

	existing, exists := as.Mesh.Board[key]
	version := 1
	if !exists {
		if p.ExpectedVersion != 0 {
			return fmt.Errorf("mesh_board_published %s: %w (no prior publish, expected_version must be 0, got %d)", key, ErrBoardStaleRevision, p.ExpectedVersion)
		}
	} else {
		if p.ExpectedVersion != existing.Version {
			return fmt.Errorf("mesh_board_published %s: %w (expected %d, current %d)", key, ErrBoardStaleRevision, p.ExpectedVersion, existing.Version)
		}
		if author != existing.AuthorID && !slices.Contains(existing.AuthorizedBy, author) {
			return fmt.Errorf("mesh_board_published %s: %w: %q", key, ErrBoardNotAuthorized, author)
		}
		version = existing.Version + 1
	}

	if dryRun {
		return nil
	}

	authorizedBy := make([]agentcomm.ParticipantID, 0, len(p.AuthorizedBy))
	for _, a := range p.AuthorizedBy {
		authorizedBy = append(authorizedBy, agentcomm.ParticipantID(a))
	}
	entry := &MeshBoardEntry{
		BoardEntry: agentcomm.BoardEntry{
			AuthorID: author, Key: key, Version: version, Status: p.Status, ArtifactRefs: p.ArtifactRefs,
			Seq: c.Seq, Ts: c.Time,
		},
		AuthorizedBy: authorizedBy,
	}
	as.Mesh.Board[key] = entry

	// Resolve every PENDING WaitOnBoardKey wait this publish satisfies in the SAME commit — decided here,
	// against the live Mesh state, not by the orchestrator from a pre-publish snapshot. See
	// resolvePendingWaits's doc comment (pkg/state/mesh_waits.go) for why.
	as.resolvePendingWaits(agentcomm.WaitReady, func(w *MeshWait) bool {
		return as.meshWaitConditionSatisfied(w.Condition, w.Waiter)
	})
	return nil
}
