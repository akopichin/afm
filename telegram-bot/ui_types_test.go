package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUIIntentContract(t *testing.T) {
	updated := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	want := Intent{
		Kind: IntentAnswerQuestion, RunKey: RunKey("run-1"), StageID: "stage-1",
		ThreadID: "thread-1", InReplyTo: "message-1", QuestionID: "question-1",
		QuestionPhase: "planning", QuestionDigest: "question-digest", ProfileID: "profile-1",
		Scope: "stage", Mode: "required", Filter: "all", PlanDigest: "plan-digest",
		ExpectedStatus: "awaiting_approval", StageUpdatedAt: updated,
		RecipientIDs: []string{"agent-1", "agent-2"}, OptionValue: "save-draft",
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Intent
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("intent lost its exact action context: got %#v, want %#v", got, want)
	}
	if CallbackDataLimit != 64 {
		t.Fatalf("Telegram callback data is limited to 64 bytes, got %d", CallbackDataLimit)
	}
	if len(encoded) <= CallbackDataLimit {
		t.Fatal("test intent must require a short persisted callback ID")
	}
}

func TestUICardContract(t *testing.T) {
	updated := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	run := RunSummary{Key: "run-1", Label: "Checkout", DashboardPort: 9876, State: DataReady, StageName: "Implement", Status: "running", LastSeenAt: updated, PendingCount: 2, Completed: false}
	home := HomeCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, StageName: run.StageName, Status: run.Status, PendingCount: run.PendingCount, AgentCount: 3, HasQuestion: true, HasPlan: true, CanMessage: true}
	runs := RunCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, Runs: []RunSummary{run}}
	inbox := InboxCard{State: DataReady, Filter: "important", Items: []InboxItem{{RunKey: run.Key, RunLabel: run.Label, StageID: "stage-1", StageName: "Implement", Kind: "question", Summary: "Save draft?", Actionable: true}}}
	roster := RosterCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, Agents: []AgentCard{{ID: "agent-1", Alias: "frontend", Name: "Frontend", Scope: "stage", Reachable: true, Running: true, CanInterrupt: true}}, SelectedRecipientIDs: []string{"agent-1"}}
	thread := ThreadCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, ThreadID: "thread-1", Title: "Validation", Messages: []ThreadMessage{{ID: "message-1", SenderID: "agent-1", SenderName: "Frontend", Text: "Please check", InReplyTo: "message-0"}}, HasMore: true, NextCursor: 5}
	question := QuestionCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, StageID: "stage-1", Prompt: "Save draft?", QuestionPhase: "planning", QuestionID: "question-1", QuestionDigest: "digest", StageUpdatedAt: updated, Options: []QuestionOption{{Label: "Yes", Value: "save-draft"}}, AllowCustom: true, CanAnswer: true}
	plan := PlanCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, StageID: "stage-1", Plan: strings.Repeat("full source\n", 500), PlanDigest: "digest", ExpectedStatus: "awaiting_approval", StageUpdatedAt: updated, CanApprove: true, CanRevise: true, CanPause: true}
	add := AddAgentCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, StageID: "stage-1", Profiles: []AgentProfile{{ID: "profile-1", Name: "QA", Available: true}}, ProfileID: "profile-1", Scope: "stage", Mode: "required", Instruction: "Review", FirstMessage: "Start", CanAdd: true}
	receipt := ReceiptCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, Text: "Please check", Status: ReceiptAccepted, Recipients: []RecipientReceipt{{ID: "agent-1", Name: "Frontend", Status: ReceiptReplied, OutboundMessageID: "message-1"}}, ThreadID: "thread-1"}
	composer := ComposerCard{State: DataReady, RunKey: run.Key, RunLabel: run.Label, Mode: "message", StageID: "stage-1", ThreadID: "thread-1", InReplyTo: "message-1", MessageKind: "reply", Interrupt: true, RecipientIDs: []string{"agent-1"}, Draft: "Reply", QuestionPhase: "planning", QuestionID: "question-1", QuestionDigest: "digest", StageUpdatedAt: updated}
	if question.Options[0].Value == question.Options[0].Label {
		t.Fatal("an answer must carry the displayed option value, not its index or label")
	}
	if len(plan.Plan) <= 4096 {
		t.Fatal("plan DTO must retain the complete source for multipart rendering")
	}
	if !composer.Interrupt || composer.MessageKind != "reply" || composer.InReplyTo != thread.Messages[0].ID {
		t.Fatal("reply and interrupt context was lost")
	}
	if home.RunKey != runs.RunKey || inbox.Items[0].RunKey != roster.RunKey || add.ProfileID != add.Profiles[0].ID || receipt.Recipients[0].OutboundMessageID == "" {
		t.Fatal("card context was lost")
	}
	if DataReady != "ready" || DataLoading != "loading" || DataOffline != "offline" || DataEmpty != "empty" || DataError != "error" {
		t.Fatal("unexpected data state contract")
	}
}

func TestUIViewAndRendererSignatures(t *testing.T) {
	view := View{Parts: []ViewPart{{Text: "part one", Buttons: [][]Button{{{Label: "Answer", Intent: Intent{Kind: IntentAnswerQuestion, OptionValue: "yes"}}}}}, {Text: "part two"}}}
	if len(view.Parts) != 2 || view.Parts[0].Buttons[0][0].Intent.OptionValue != "yes" {
		t.Fatal("multipart view lost button intent")
	}
	var _ RenderHomeFunc = func(HomeCard) View { return View{} }
	var _ RenderRunsFunc = func(RunCard) View { return View{} }
	var _ RenderInboxFunc = func(InboxCard) View { return View{} }
	var _ RenderRosterFunc = func(RosterCard) View { return View{} }
	var _ RenderThreadFunc = func(ThreadCard) View { return View{} }
	var _ RenderQuestionFunc = func(QuestionCard) View { return View{} }
	var _ RenderPlanFunc = func(PlanCard) View { return View{} }
	var _ RenderAddAgentFunc = func(AddAgentCard) View { return View{} }
	var _ RenderReceiptFunc = func(ReceiptCard) View { return View{} }
	var _ RenderComposerFunc = func(ComposerCard) View { return View{} }
}
