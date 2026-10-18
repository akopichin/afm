package state

import (
	"encoding/json"
	"errors"
	"testing"
)

// ---- T1.4: board ----

func meshBoardPublishedControl(key, author, status string, expectedVersion int, artifactRefs []string) *Control {
	payload, _ := json.Marshal(MeshBoardPublishedPayload{Status: status, ArtifactRefs: artifactRefs, ExpectedVersion: expectedVersion})
	return &Control{
		Kind: kindMeshBoardPublished, SchemaVersion: ControlSchemaVersion,
		Target: Target{BoardKey: key, ParticipantID: author}, Payload: payload,
	}
}

func TestMesh_BoardFirstPublishGetsVersion1(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshBoardPublishedControl("design-doc", "a", "draft", 0, nil))

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
	mustApply(t, &as, meshBoardPublishedControl("k", "a", "draft", 0, nil)) // -> version 1

	// Publishing again with expected_version 0 is now stale: the key's current version is 1.
	c := meshBoardPublishedControl("k", "a", "final", 0, nil)
	if err := dispatchControlKind(&as, c, false); !errors.Is(err, ErrBoardStaleRevision) {
		t.Fatalf("err = %v, want ErrBoardStaleRevision", err)
	}
	if as.Mesh.Board["k"].Version != 1 || as.Mesh.Board["k"].Status != "draft" {
		t.Fatalf("entry mutated by a rejected stale-revision publish: %+v", as.Mesh.Board["k"])
	}
}

// TestMesh_BoardNonAuthorizedRepublishRejected: a key belongs to its author — only that author publishes its
// next revisions. A record naming someone else in a legacy authorized_by list grants nothing.
func TestMesh_BoardNonAuthorizedRepublishRejected(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshBoardPublishedControl("k", "a", "draft", 0, nil)) // version 1, author a

	c := meshBoardPublishedControl("k", "stranger", "final", 1, nil)
	if err := dispatchControlKind(&as, c, false); !errors.Is(err, ErrBoardNotAuthorized) {
		t.Fatalf("err = %v, want ErrBoardNotAuthorized", err)
	}
	if as.Mesh.Board["k"].Version != 1 {
		t.Fatalf("Version mutated by a rejected unauthorized publish: %d", as.Mesh.Board["k"].Version)
	}

	legacy := &Control{
		Kind: kindMeshBoardPublished, SchemaVersion: ControlSchemaVersion, Target: Target{BoardKey: "k2", ParticipantID: "a"},
		Payload: json.RawMessage(`{"status":"draft","expected_version":0,"authorized_by":["b"]}`),
	}
	mustApply(t, &as, legacy)
	if err := dispatchControlKind(&as, meshBoardPublishedControl("k2", "b", "final", 1, nil), false); !errors.Is(err, ErrBoardNotAuthorized) {
		t.Fatalf("publish by a participant named in authorized_by: err = %v, want ErrBoardNotAuthorized", err)
	}

	// The key's own author may always republish.
	mustApply(t, &as, meshBoardPublishedControl("k2", "a", "final", 1, nil))
	if as.Mesh.Board["k2"].Version != 2 || as.Mesh.Board["k2"].AuthorID != "a" {
		t.Fatalf("author republish not accepted: %+v", as.Mesh.Board["k2"])
	}
}

func TestMesh_BoardArtifactRefOutsidePrefixRejected(t *testing.T) {
	as := newAssistantsState()
	c := meshBoardPublishedControl("k", "a", "draft", 0, []string{"/etc/passwd"})
	if err := dispatchControlKind(&as, c, false); !errors.Is(err, ErrBoardArtifactRefOutsideRun) {
		t.Fatalf("err = %v, want ErrBoardArtifactRefOutsideRun", err)
	}
	if _, ok := as.Mesh.Board["k"]; ok {
		t.Fatal("board entry must not be projected when an artifact ref fails the allowed-prefix check")
	}

	mustApply(t, &as, meshBoardPublishedControl("k2", "a", "draft", 0, []string{"artifacts/out.txt"}))
	if got := as.Mesh.Board["k2"].ArtifactRefs; len(got) != 1 || got[0] != "artifacts/out.txt" {
		t.Fatalf("valid artifact ref (within artifacts/ prefix) rejected: %+v", got)
	}
}
