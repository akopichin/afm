package agentcomm

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProtocolVersion_IsOne(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", ProtocolVersion)
	}
}

// TestTypes_ZeroValuesAreInvalid documents, type by type, what "zero value is invalid" means for
// every Validate-able type in this package. T0.2's ValidateMessage and the pkg/state projection
// both lean on these Valid() methods never accepting a zero/unset value by accident.
func TestTypes_ZeroValuesAreInvalid(t *testing.T) {
	if (ParticipantKind("")).Valid() {
		t.Error("zero ParticipantKind must be invalid")
	}
	if (Scope("")).Valid() {
		t.Error("zero Scope must be invalid")
	}
	if (Participant{}).Valid() {
		t.Error("zero Participant must be invalid")
	}
	if (ThreadStatus("")).Valid() {
		t.Error("zero ThreadStatus must be invalid")
	}
	if (Thread{}).Valid() {
		t.Error("zero Thread must be invalid")
	}
	if (MessageKind("")).Valid() {
		t.Error("zero MessageKind must be invalid")
	}
	if (Message{}).Valid() {
		t.Error("zero Message must be invalid")
	}
	if (DeliveryStatus("")).Valid() {
		t.Error("zero DeliveryStatus must be invalid")
	}
	if (WaitConditionKind("")).Valid() {
		t.Error("zero WaitConditionKind must be invalid")
	}
	if (WaitCondition{}).Valid() {
		t.Error("zero WaitCondition must be invalid")
	}
	if (WaitStatus("")).Valid() {
		t.Error("zero WaitStatus must be invalid")
	}
	if (Wait{}).Valid() {
		t.Error("zero Wait must be invalid")
	}
	if (BoardEntry{}).Valid() {
		t.Error("zero BoardEntry must be invalid")
	}

	// Sanity: a fully-populated value of each type IS valid, so Valid() isn't just "always false".
	if !KindUser.Valid() {
		t.Error("KindUser must be valid")
	}
	if !ScopeRun.Valid() {
		t.Error("ScopeRun must be valid")
	}
	validParticipant := Participant{ID: "user", Kind: KindUser, Scope: ScopeRun}
	if !validParticipant.Valid() {
		t.Error("populated Participant must be valid")
	}
	validThread := Thread{ID: "t1", Members: []ParticipantID{"user"}, Status: ThreadOpen}
	if !validThread.Valid() {
		t.Error("populated Thread must be valid")
	}
	validMessage := Message{
		ID:         "m1",
		ThreadID:   "t1",
		Sender:     "user",
		Recipients: []ParticipantID{"stage:build"},
		Kind:       MessageKindMessage,
	}
	if !validMessage.Valid() {
		t.Error("populated Message must be valid")
	}
	validCondition := WaitCondition{Kind: WaitOnReplyTo, MessageID: "m1"}
	if !validCondition.Valid() {
		t.Error("populated WaitCondition must be valid")
	}
	validWait := Wait{ID: "w1", Waiter: "user", Condition: validCondition, Status: WaitPending}
	if !validWait.Valid() {
		t.Error("populated Wait must be valid")
	}
	validBoardEntry := BoardEntry{AuthorID: "user", Key: "qa.tests", Version: 1}
	if !validBoardEntry.Valid() {
		t.Error("populated BoardEntry must be valid")
	}
}

func TestMessage_RoundTripsJSON(t *testing.T) {
	inReplyTo := MessageID("m0")
	want := Message{
		ID:           "m1",
		ThreadID:     "t1",
		Sender:       "user",
		Recipients:   []ParticipantID{"stage:build", "helper:task-1"},
		Kind:         MessageKindReply,
		InReplyTo:    &inReplyTo,
		Body:         "please re-check the failing test",
		ArtifactRefs: []string{"artifacts/log.txt"},
		Seq:          42,
		CreatedAt:    time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Message
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	got.CreatedAt = want.CreatedAt // time.Time's zone representation can differ after round-trip
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got  = %+v\n want = %+v", got, want)
	}
}

// TestNoForbiddenImports fixes an architectural boundary: pkg/agentcomm is a neutral dictionary
// that pkg/state and pkg/orchestrator both depend on. If it started importing any of the
// packages that depend on it — directly or through a subpackage — that would be either a build
// cycle or a hidden coupling that's easy to miss in a later refactor. Mirrors
// pkg/assistants/types_test.go's TestNoForbiddenImports, extended with pkg/server and
// pkg/executor per this package's own contract.
func TestNoForbiddenImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/akopichin/afm/pkg/agentcomm").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	forbiddenRoots := []string{
		"github.com/akopichin/afm/pkg/state",
		"github.com/akopichin/afm/pkg/orchestrator",
		"github.com/akopichin/afm/pkg/server",
		"github.com/akopichin/afm/pkg/executor",
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, root := range forbiddenRoots {
			if line == root || strings.HasPrefix(line, root+"/") {
				t.Fatalf("pkg/agentcomm must not depend on %s (found %q in go list -deps)", root, line)
			}
		}
	}
}
