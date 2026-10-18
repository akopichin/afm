package state

import (
	"encoding/json"
	"errors"
	"testing"
)

// ---- T1.4: board ----

func meshBoardPublishedControl(key, author, status string, expectedVersion int, artifactRefs, authorizedBy []string) *Control {
	payload, _ := json.Marshal(MeshBoardPublishedPayload{
		Status: status, ArtifactRefs: artifactRefs, ExpectedVersion: expectedVersion, AuthorizedBy: authorizedBy,
	})
	return &Control{
		Kind: kindMeshBoardPublished, SchemaVersion: ControlSchemaVersion,
		Target: Target{BoardKey: key, ParticipantID: author}, Payload: payload,
	}
}

func TestMesh_BoardFirstPublishGetsVersion1(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshBoardPublishedControl("design-doc", "a", "draft", 0, nil, nil))

	e, ok := as.Mesh.Board["design-doc"]
	if !ok {
		t.Fatal("board entry not projected")
	}
	if e.Version != 1 {
		t.Fatalf("Version = %d, want 1 (first publish of any key)", e.Version)
	}
	if e.AuthorID != "a" || e.Status != "draft" {
		t.Fatalf("entry = %+v, want author=a status=draft", e)
	}
}

func TestMesh_BoardStaleExpectedVersionRejected(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshBoardPublishedControl("k", "a", "draft", 0, nil, nil)) // -> version 1

	// Publishing again with expected_version 0 is now stale: the key's current version is 1.
	c := meshBoardPublishedControl("k", "a", "final", 0, nil, nil)
	if err := dispatchControlKind(&as, c, false); !errors.Is(err, ErrBoardStaleRevision) {
		t.Fatalf("err = %v, want ErrBoardStaleRevision", err)
	}
	if as.Mesh.Board["k"].Version != 1 || as.Mesh.Board["k"].Status != "draft" {
		t.Fatalf("entry mutated by a rejected stale-revision publish: %+v", as.Mesh.Board["k"])
	}
}

// TestMesh_BoardNonAuthorizedRepublishRejected tests the exact authorization rule applyMeshBoardPublished
// implements: a publish is accepted iff its author is either the key's CURRENT AuthorID, or was named in
// that entry's AuthorizedBy list by whoever published it. A third participant, named by nobody, is rejected.
func TestMesh_BoardNonAuthorizedRepublishRejected(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshBoardPublishedControl("k", "a", "draft", 0, nil, nil)) // version 1, author a, no AuthorizedBy

	c := meshBoardPublishedControl("k", "stranger", "final", 1, nil, nil)
	if err := dispatchControlKind(&as, c, false); !errors.Is(err, ErrBoardNotAuthorized) {
		t.Fatalf("err = %v, want ErrBoardNotAuthorized", err)
	}
	if as.Mesh.Board["k"].Version != 1 {
		t.Fatalf("Version mutated by a rejected unauthorized publish: %d", as.Mesh.Board["k"].Version)
	}

	// A participant explicitly named in AuthorizedBy by the CURRENT author IS allowed to republish.
	mustApply(t, &as, meshBoardPublishedControl("k2", "a", "draft", 0, nil, []string{"b"}))
	mustApply(t, &as, meshBoardPublishedControl("k2", "b", "final", 1, nil, nil))
	if as.Mesh.Board["k2"].Version != 2 || as.Mesh.Board["k2"].AuthorID != "b" {
		t.Fatalf("authorized republish not accepted: %+v", as.Mesh.Board["k2"])
	}

	// The key's own current author may always republish, with no AuthorizedBy entry needed.
	mustApply(t, &as, meshBoardPublishedControl("k2", "b", "final-2", 2, nil, nil))
	if as.Mesh.Board["k2"].Version != 3 {
		t.Fatalf("current-author republish not accepted: %+v", as.Mesh.Board["k2"])
	}
}

func TestMesh_BoardArtifactRefOutsidePrefixRejected(t *testing.T) {
	as := newAssistantsState()
	c := meshBoardPublishedControl("k", "a", "draft", 0, []string{"/etc/passwd"}, nil)
	if err := dispatchControlKind(&as, c, false); !errors.Is(err, ErrBoardArtifactRefOutsideRun) {
		t.Fatalf("err = %v, want ErrBoardArtifactRefOutsideRun", err)
	}
	if _, ok := as.Mesh.Board["k"]; ok {
		t.Fatal("board entry must not be projected when an artifact ref fails the allowed-prefix check")
	}

	mustApply(t, &as, meshBoardPublishedControl("k2", "a", "draft", 0, []string{"artifacts/out.txt"}, nil))
	if got := as.Mesh.Board["k2"].ArtifactRefs; len(got) != 1 || got[0] != "artifacts/out.txt" {
		t.Fatalf("valid artifact ref (within artifacts/ prefix) rejected: %+v", got)
	}
}
