package sideagent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func alwaysKnown(string) bool { return true }

// TestActions_QuestionAboutRoleCreatesNothing — plain prose (even one mentioning a role) with no
// action_drafts fence at all must extract zero drafts: asking what a role does is not a request to do
// anything (dev-plan.md T4.4 acceptance).
func TestActions_QuestionAboutRoleCreatesNothing(t *testing.T) {
	text := "@qaEngineer reviews test coverage and flags missing edge cases before a release."
	if got := extractActionDrafts(text, alwaysKnown); got != nil {
		t.Fatalf("extractActionDrafts = %+v, want nil", got)
	}
}

// TestActions_NotParsedFromQuotedText — a fenced action_drafts block shown only as an illustrative example
// inside a markdown blockquote (every line prefixed with "> ") must not be executed. The fence regex anchors
// on the literal start of a line, so a blockquoted fence never matches to begin with.
func TestActions_NotParsedFromQuotedText(t *testing.T) {
	text := "Here's the format, for reference:\n" +
		"> ```action_drafts\n" +
		`> [{"type":"attach_assistant","expected_revision":"1"}]` + "\n" +
		"> ```\n" +
		"I am not proposing this myself right now."
	if got := extractActionDrafts(text, alwaysKnown); got != nil {
		t.Fatalf("extractActionDrafts = %+v, want nil (fence was quoted)", got)
	}
}

// TestActions_UnknownTypeRejected — a draft whose type the caller's predicate does not recognize is dropped;
// a recognized sibling in the same batch still survives.
func TestActions_UnknownTypeRejected(t *testing.T) {
	known := func(typ string) bool { return typ == "attach_assistant" }
	text := "```action_drafts\n" +
		`[{"type":"delete_everything"},{"type":"attach_assistant","expected_revision":"2"}]` + "\n" +
		"```"
	got := extractActionDrafts(text, known)
	if len(got) != 1 || got[0].Type != "attach_assistant" {
		t.Fatalf("extractActionDrafts = %+v, want exactly one attach_assistant draft", got)
	}
}

// TestActions_LimitPerTurn — one turn cannot propose more than maxActionDraftsPerTurn drafts at once, even
// if every one of them is individually well-formed and recognized.
func TestActions_LimitPerTurn(t *testing.T) {
	var items []string
	for i := 0; i < maxActionDraftsPerTurn+3; i++ {
		items = append(items, fmt.Sprintf(`{"type":"attach_assistant","expected_revision":"%d"}`, i))
	}
	text := "```action_drafts\n[" + strings.Join(items, ",") + "]\n```"
	got := extractActionDrafts(text, alwaysKnown)
	if len(got) != maxActionDraftsPerTurn {
		t.Fatalf("len(extractActionDrafts) = %d, want %d (the per-turn cap)", len(got), maxActionDraftsPerTurn)
	}
}

// TestActions_SizeLimits — a single draft whose Target+Payload exceeds maxActionDraftBytes is dropped, but a
// properly-sized sibling in the same batch survives.
func TestActions_SizeLimits(t *testing.T) {
	huge, _ := json.Marshal(map[string]string{"blob": strings.Repeat("x", maxActionDraftBytes+1)})
	text := fmt.Sprintf("```action_drafts\n[{\"type\":\"attach_assistant\",\"payload\":%s},{\"type\":\"attach_assistant\",\"expected_revision\":\"1\"}]\n```", huge)
	got := extractActionDrafts(text, alwaysKnown)
	if len(got) != 1 || got[0].ExpectedRevision != "1" {
		t.Fatalf("extractActionDrafts = %+v, want exactly the one properly-sized draft", got)
	}
}

// TestActions_LastFenceWins — mirrors the project-wide "last text block is the answer" convention (also used
// by RunHelperAgent/RunVerifyAgent): if the narration contains more than one action_drafts fence (e.g. a
// draft then a correction), only the LAST one is honored.
func TestActions_LastFenceWins(t *testing.T) {
	text := "```action_drafts\n" + `[{"type":"attach_assistant","expected_revision":"1"}]` + "\n```\n" +
		"Actually, let me revise that:\n" +
		"```action_drafts\n" + `[{"type":"attach_assistant","expected_revision":"2"}]` + "\n```"
	got := extractActionDrafts(text, alwaysKnown)
	if len(got) != 1 || got[0].ExpectedRevision != "2" {
		t.Fatalf("extractActionDrafts = %+v, want exactly the LAST fence's draft (expected_revision=2)", got)
	}
}

// TestActions_MalformedJSONIsNotAnError — invalid JSON inside the fence must never panic or propagate an
// error up to the turn: it simply means zero drafts.
func TestActions_MalformedJSONIsNotAnError(t *testing.T) {
	text := "```action_drafts\nnot valid json at all\n```"
	if got := extractActionDrafts(text, alwaysKnown); got != nil {
		t.Fatalf("extractActionDrafts = %+v, want nil for malformed JSON", got)
	}
}

// TestActions_NilKnownPredicateRejectsEverything — a nil predicate (e.g. the capability providing it is off)
// must fail CLOSED, like MentionResolve's nil convention, not silently accept every type.
func TestActions_NilKnownPredicateRejectsEverything(t *testing.T) {
	text := "```action_drafts\n" + `[{"type":"attach_assistant","expected_revision":"1"}]` + "\n```"
	if got := extractActionDrafts(text, nil); got != nil {
		t.Fatalf("extractActionDrafts with nil predicate = %+v, want nil", got)
	}
}

// TestActions_NotParsedFromToolOutput — a manager-level (not pure-function) regression test: a well-formed
// action_drafts fence appearing in a TOOL action's detail (e.g. a Bash command's output, or the command text
// itself) must never be executed — only the turn's FINAL assistant_text content is ever handed to
// extractActionDrafts (Manager.runTurn's lastText, overwritten by each new text block and read only once, at
// successful completion). This locks that discipline in so a future refactor that widens the scan surface
// gets caught here, not by the stricter pure-function fence-matching tests above.
func TestActions_NotParsedFromToolOutput(t *testing.T) {
	toolDetailWithFence := "```action_drafts\n" + `[{"type":"attach_assistant","expected_revision":"1"}]` + "\n```"
	ff := &fakeFactory{fakes: []*fakeRunner{{actions: []fakeAction{
		{"Bash", toolDetailWithFence},
		{"text", "Here is my answer. I am not proposing any action right now."},
	}}}}
	store := newTestStore(t)
	m := New(Config{
		Store:           store,
		Command:         "claude",
		Context:         ContextBuilder{},
		NewExecutor:     ff.make,
		KnownActionType: alwaysKnown,
	})

	if _, err := m.Send("c1", "please check this", TurnTarget{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain")
	}

	history := store.History()
	var found bool
	for _, e := range history {
		if e.Type != EventTurnCompleted {
			continue
		}
		found = true
		var d TurnCompletedData
		mustDecode(t, e, &d)
		if d.ActionDrafts != nil {
			t.Fatalf("ActionDrafts = %+v, want nil — the fence was in tool output, not the final narration", d.ActionDrafts)
		}
	}
	if !found {
		t.Fatal("no turn_completed event recorded")
	}
}

// TestActions_RecordedFromFinalNarration is the positive counterpart of TestActions_NotParsedFromToolOutput:
// a well-formed action_drafts fence in the LAST assistant_text block of a successful turn must be persisted
// on turn_completed, going through the real KnownActionType predicate injected via Config (not a test-only
// shortcut).
func TestActions_RecordedFromFinalNarration(t *testing.T) {
	finalText := "I'll bring in QA for this.\n\n```action_drafts\n" +
		`[{"type":"attach_assistant","target":{"profile_id":"qaEngineer"},"expected_revision":"1"}]` + "\n```"
	ff := &fakeFactory{fakes: []*fakeRunner{{actions: []fakeAction{{"text", finalText}}}}}
	store := newTestStore(t)
	m := New(Config{
		Store:           store,
		Command:         "claude",
		Context:         ContextBuilder{},
		NewExecutor:     ff.make,
		KnownActionType: func(typ string) bool { return typ == "attach_assistant" },
	})

	if _, err := m.Send("c1", "please bring in qa", TurnTarget{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain")
	}

	var got *TurnCompletedData
	for _, e := range store.History() {
		if e.Type != EventTurnCompleted {
			continue
		}
		var d TurnCompletedData
		mustDecode(t, e, &d)
		got = &d
	}
	if got == nil {
		t.Fatal("no turn_completed event recorded")
	}
	if len(got.ActionDrafts) != 1 || got.ActionDrafts[0].Type != "attach_assistant" {
		t.Fatalf("ActionDrafts = %+v, want exactly one attach_assistant draft", got.ActionDrafts)
	}
}
