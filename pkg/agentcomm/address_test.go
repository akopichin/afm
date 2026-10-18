package agentcomm

import (
	"errors"
	"slices"
	"testing"
)

func testRoster() []Participant {
	return []Participant{
		{ID: "user", Kind: KindUser, Scope: ScopeRun, Alias: ""},
		{ID: "stage:build", Kind: KindStageAgent, Scope: ScopeStage, Alias: "build"},
		{ID: "helper:task-1", Kind: KindHelper, Scope: ScopeStage, Alias: "qa"},
		{ID: "helper:task-2", Kind: KindHelper, Scope: ScopeStage, Alias: "QA"}, // second "qa", different case
		{ID: "run:devops", Kind: KindRunAgent, Scope: ScopeRun, Alias: "devops"},
		{ID: "side:run-1", Kind: KindSideAgent, Scope: ScopeRun, Alias: "side"},
	}
}

func TestResolveAlias_CaseInsensitiveSingleMatch(t *testing.T) {
	id, err := ResolveAlias("DevOps", testRoster())
	if err != nil {
		t.Fatalf("ResolveAlias(DevOps) error = %v, want nil", err)
	}
	if id != "run:devops" {
		t.Fatalf("ResolveAlias(DevOps) = %q, want %q", id, "run:devops")
	}
}

func TestResolveAlias_AtPrefixAccepted(t *testing.T) {
	id, err := ResolveAlias("@devops", testRoster())
	if err != nil {
		t.Fatalf("ResolveAlias(@devops) error = %v, want nil", err)
	}
	if id != "run:devops" {
		t.Fatalf("ResolveAlias(@devops) = %q, want %q", id, "run:devops")
	}
}

func TestResolveAlias_ResolvedIsCanonicalID(t *testing.T) {
	// The alias text itself ("DevOps") must never be what's returned/stored — only the canonical
	// ParticipantID ("run:devops") is.
	id, err := ResolveAlias("DEVOPS", testRoster())
	if err != nil {
		t.Fatalf("ResolveAlias error = %v", err)
	}
	if string(id) == "DEVOPS" || string(id) == "devops" {
		t.Fatalf("ResolveAlias returned the alias text, not a canonical id: %q", id)
	}
	if id != ParticipantID("run:devops") {
		t.Fatalf("ResolveAlias = %q, want canonical id %q", id, "run:devops")
	}
}

func TestResolveAlias_AmbiguousRejectedWithCandidates(t *testing.T) {
	_, err := ResolveAlias("qa", testRoster())
	var ambiguous *AmbiguousAliasError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ResolveAlias(qa) error = %v, want *AmbiguousAliasError", err)
	}
	got := make([]string, 0, len(ambiguous.Candidates))
	for _, c := range ambiguous.Candidates {
		got = append(got, string(c))
	}
	slices.Sort(got)
	want := []string{"helper:task-1", "helper:task-2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ambiguous candidates = %v, want %v", got, want)
	}
}

func TestResolveAlias_UnknownRejected(t *testing.T) {
	_, err := ResolveAlias("nobody", testRoster())
	if !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("ResolveAlias(nobody) error = %v, want ErrAliasNotFound", err)
	}
}

func TestResolveAlias_NilRosterIsSafe(t *testing.T) {
	_, err := ResolveAlias("qa", nil)
	if !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("ResolveAlias(nil roster) error = %v, want ErrAliasNotFound", err)
	}
}

func TestResolveAlias_EmptyRosterIsSafe(t *testing.T) {
	_, err := ResolveAlias("qa", []Participant{})
	if !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("ResolveAlias(empty roster) error = %v, want ErrAliasNotFound", err)
	}
}

func TestResolveAlias_ParticipantsWithoutAliasAreSkipped(t *testing.T) {
	// "user" has Alias == "" in testRoster: it must never match an empty-string lookup or
	// accidentally participate in matching.
	_, err := ResolveAlias("", testRoster())
	if !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("ResolveAlias(\"\") error = %v, want ErrAliasNotFound", err)
	}
}

// TestCanAddress_Matrix covers every named pair from agent-mesh.md §5.2: main<->helper,
// helper<->helper (including across different delegation trees, which this package cannot even
// express — it only sees two Participants), main<->main, run-agent<->anyone, side-agent<->anyone,
// and the human<->anyone. Depth limits restrict delegation, never the right to message a
// colleague, so every recognized-kind pair is addressable.
func TestCanAddress_Matrix(t *testing.T) {
	kinds := []ParticipantKind{KindUser, KindStageAgent, KindHelper, KindRunAgent, KindSideAgent}
	for _, from := range kinds {
		for _, to := range kinds {
			fromP := Participant{ID: "a", Kind: from, Scope: ScopeRun}
			toP := Participant{ID: "b", Kind: to, Scope: ScopeRun}
			if !CanAddress(fromP, toP) {
				t.Errorf("CanAddress(%s -> %s) = false, want true", from, to)
			}
		}
	}
}

func TestCanAddress_InvalidKindRejected(t *testing.T) {
	valid := Participant{ID: "a", Kind: KindUser, Scope: ScopeRun}
	zero := Participant{}
	unknown := Participant{ID: "b", Kind: ParticipantKind("bogus"), Scope: ScopeRun}

	if CanAddress(zero, valid) {
		t.Error("CanAddress(zero participant, valid) = true, want false")
	}
	if CanAddress(valid, zero) {
		t.Error("CanAddress(valid, zero participant) = true, want false")
	}
	if CanAddress(unknown, valid) {
		t.Error("CanAddress(unknown kind, valid) = true, want false")
	}
}
