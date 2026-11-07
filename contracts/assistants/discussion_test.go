package assistants

import (
	"errors"
	"testing"
	"time"
)

func TestRound_LimitEnforced(t *testing.T) {
	var usage RoundUsage
	if err := usage.Allow(RoundIndependent); err != nil {
		t.Fatalf("first independent round should be allowed: %v", err)
	}
	usage.IndependentUsed = true
	if err := usage.Allow(RoundIndependent); !errors.Is(err, ErrRoundLimitExceeded) {
		t.Fatalf("a second independent round must be rejected, got %v", err)
	}
	if err := usage.Allow(RoundDiscussion); err != nil {
		t.Fatalf("the one discussion round should still be allowed: %v", err)
	}
	usage.DiscussionUsed = true
	if err := usage.Allow(RoundDiscussion); !errors.Is(err, ErrRoundLimitExceeded) {
		t.Fatalf("a second discussion round must be rejected, got %v", err)
	}
	// No third discussion-shaped round exists at all — anything beyond independent+discussion is unknown.
	if err := usage.Allow(4); err == nil {
		t.Fatal("round 4 is not a recognized round number and must error")
	}
}

func TestRound_MessageCarriesAuthorRoleModelRoundEvidence(t *testing.T) {
	msg := DiscussionMessage{
		ID:              "m-1",
		Author:          MessageAuthor{Kind: AuthorAssistant, ID: "qaEngineer"},
		Role:            "reviewer",
		Executor:        "codex-as-claude",
		Round:           RoundIndependent,
		ParentMessageID: "",
		ContextVersion:  "ctx-v1",
		Evidence:        []string{"f-1", "f-2"},
		Body:            "looks fine except the retry loop",
		CreatedAt:       time.Now(),
	}
	if msg.Author.Kind != AuthorAssistant || msg.Author.ID != "qaEngineer" {
		t.Fatalf("message must carry its author (kind+id): %+v", msg.Author)
	}
	if msg.Role == "" {
		t.Fatal("message must carry a role")
	}
	if msg.Executor == "" {
		t.Fatal("message must carry the actual executor that produced it")
	}
	if msg.Round != RoundIndependent {
		t.Fatal("message must carry its round")
	}
	if msg.ContextVersion == "" {
		t.Fatal("message must carry the context version it was computed against")
	}
	if len(msg.Evidence) == 0 {
		t.Fatal("message must carry evidence references")
	}
}

func TestRound_SilentParticipantNotShownAsAgreeing(t *testing.T) {
	messages := []DiscussionMessage{
		{ID: "m-1", Round: RoundIndependent, Author: MessageAuthor{Kind: AuthorAssistant, ID: "qa"}, Result: &Result{Verdict: VerdictPass}},
		{ID: "m-2", Round: RoundIndependent, Author: MessageAuthor{Kind: AuthorAssistant, ID: "sec"}, Result: &Result{Verdict: VerdictPass}},
		// "perf" was invited but never replied in round 1 — must not appear as having agreed.
	}
	verdicts := CollectRoundVerdicts(messages, RoundIndependent)
	if _, ok := verdicts["perf"]; ok {
		t.Fatalf("a silent participant must not appear in collected verdicts at all: %+v", verdicts)
	}
	if len(verdicts) != 2 {
		t.Fatalf("expected exactly the two responders, got %+v", verdicts)
	}
}

func TestRound_OrderRestoredFromIdsNotTimestamps(t *testing.T) {
	now := time.Now()
	// Second message committed AFTER the first (higher sequence / append position) but — simulating clock
	// skew — carries an EARLIER wall-clock CreatedAt than the first. Order restoration must follow commit/id
	// order, never re-sort by CreatedAt.
	first := DiscussionMessage{ID: "m-1", CreatedAt: now}
	second := DiscussionMessage{ID: "m-2", CreatedAt: now.Add(-time.Hour)}

	ordered := RestoreOrder([]string{"m-1", "m-2"}, map[string]*DiscussionMessage{
		"m-1": &first,
		"m-2": &second,
	})
	if len(ordered) != 2 || ordered[0].ID != "m-1" || ordered[1].ID != "m-2" {
		t.Fatalf("order must follow the id list, not timestamps: %+v", ordered)
	}
}

func TestRound_SynthesisCountedInLimits(t *testing.T) {
	var usage RoundUsage
	if err := usage.Allow(RoundSynthesis); err != nil {
		t.Fatalf("first synthesis invocation should be allowed: %v", err)
	}
	usage.SynthesisUsed = true
	if err := usage.Allow(RoundSynthesis); !errors.Is(err, ErrRoundLimitExceeded) {
		t.Fatalf("synthesis is a separate billable invocation but is still bounded to exactly one: %v", err)
	}
	// Synthesis uses its own slot, independent from the two discussion rounds.
	usage = RoundUsage{IndependentUsed: true, DiscussionUsed: true}
	if err := usage.Allow(RoundSynthesis); err != nil {
		t.Fatalf("synthesis must still be allowed after both discussion rounds are used: %v", err)
	}
}

func TestDisagreement_IsAValidOutcome(t *testing.T) {
	verdicts := map[string]Verdict{"qa": VerdictPass, "sec": VerdictNeedsChanges}
	out := Disagree(verdicts)
	if out.Agreed {
		t.Fatal("expected disagreement, got agreed=true")
	}
	if !out.RequiresSynthesis() {
		t.Fatal("disagreement must require synthesis, not trigger an endless consensus search")
	}
}

func TestDisagreement_MajorityIsNotProof(t *testing.T) {
	// 3 say pass, 1 says needs_changes — a clear numeric majority.
	verdicts := map[string]Verdict{"a": VerdictPass, "b": VerdictPass, "c": VerdictPass, "d": VerdictNeedsChanges}
	out := Disagree(verdicts)
	if out.Agreed {
		t.Fatal("a numeric majority must not be reported as agreement")
	}
	if !out.RequiresSynthesis() {
		t.Fatal("majority must not short-circuit synthesis — majority is not proof of correctness")
	}
}

func TestFindings_LinkedByStableIds(t *testing.T) {
	messages := []DiscussionMessage{
		{ID: "m-1", Evidence: []string{"f-1"}, Body: "the retry loop is broken"},
		{ID: "m-2", Evidence: []string{"f-1"}, Body: "agreed, same root cause"},
		{ID: "m-3", Evidence: []string{"f-2"}, Body: "the retry loop is broken"}, // same TEXT, different finding id
	}
	groups := GroupByFindingID(messages)
	if len(groups) != 2 {
		t.Fatalf("expected 2 distinct finding groups by id, got %d: %+v", len(groups), groups)
	}
	var f1 *FindingGroup
	for i := range groups {
		if groups[i].FindingID == "f-1" {
			f1 = &groups[i]
		}
	}
	if f1 == nil {
		t.Fatal("expected a group for f-1")
	}
	if len(f1.MessageIDs) != 2 {
		t.Fatalf("f-1 must group m-1 and m-2 by id, not by text, got %+v", f1.MessageIDs)
	}
}
