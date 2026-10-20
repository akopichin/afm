package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestRenderQuestion(t *testing.T) {
	if got := viewText(RenderQuestion(QuestionCard{State: DataEmpty})); !strings.Contains(got, "Текущего вопроса нет") {
		t.Fatal(got)
	}
	stamp := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	card := QuestionCard{State: DataReady, RunKey: "r", StageID: "s", Prompt: "Save?", QuestionPhase: "planning", QuestionID: "q", QuestionDigest: "digest", StageUpdatedAt: stamp, Options: []QuestionOption{{Label: "Yes", Value: "save-draft"}}, AllowCustom: true, CanAnswer: true}
	v := RenderQuestion(card)
	in, ok := findIntent(v, IntentAnswerQuestion)
	if !ok || in.OptionValue != "save-draft" || in.QuestionDigest != "digest" || in.QuestionPhase != "planning" || in.QuestionID != "q" || in.StageUpdatedAt != stamp || in.RunKey != "r" {
		t.Fatalf("option intent: %+v", v)
	}
	if in, ok := findIntent(v, IntentCustomAnswer); !ok || in.QuestionDigest != "digest" {
		t.Fatalf("custom answer: %+v", v)
	}
	card.Answered = true
	if _, ok := findIntent(RenderQuestion(card), IntentAnswerQuestion); ok {
		t.Fatal("answered question remains actionable")
	}
	card.Answered = false
	card.State = DataOffline
	if _, ok := findIntent(RenderQuestion(card), IntentCustomAnswer); ok {
		t.Fatal("offline question remains actionable")
	}
}

func TestRenderQuestionPreservesLongUnicodeAndDistinctOptions(t *testing.T) {
	prompt := strings.Repeat("Как поступить? 🧭", 330) + " конец вопроса"
	prefix := strings.Repeat("одинаковое значение😀", 220)
	options := []QuestionOption{
		{Label: "Одинаковое длинное имя", Value: prefix + " вариант А"},
		{Label: "Одинаковое длинное имя", Value: prefix + " вариант Б"},
	}
	card := QuestionCard{State: DataReady, RunKey: "r", StageID: "s", Prompt: prompt, QuestionID: "q", QuestionDigest: "d", Options: options, CanAnswer: true, AllowCustom: true}
	view := RenderQuestion(card)
	want := "Вопрос · r\n" + prompt + "\n1. Одинаковое длинное имя\nЗначение: " + options[0].Value + "\n2. Одинаковое длинное имя\nЗначение: " + options[1].Value
	assertTextParts(t, view, want)
	var seen []Button
	for _, part := range view.Parts {
		for _, row := range part.Buttons {
			for _, button := range row {
				if button.Intent.Kind == IntentAnswerQuestion {
					seen = append(seen, button)
				}
			}
		}
	}
	if len(seen) != 2 || seen[0].Intent.OptionValue != options[0].Value || seen[1].Intent.OptionValue != options[1].Value || seen[0].Label == seen[1].Label || !strings.HasPrefix(seen[0].Label, "1.") || !strings.HasPrefix(seen[1].Label, "2.") {
		t.Fatalf("option buttons do not distinguish exact values: %+v", seen)
	}
	for _, kind := range []IntentKind{IntentAnswerQuestion, IntentCustomAnswer, IntentRefresh} {
		i, _, ok := buttonPart(view, kind)
		if !ok || i != len(view.Parts)-1 {
			t.Fatalf("%s action detached from complete question", kind)
		}
	}
}

func TestRenderPlanPartsAndPreconditions(t *testing.T) {
	stamp := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	source := strings.Repeat("abc😀\n", 1500) + "last B"
	card := PlanCard{State: DataReady, RunKey: "r", RunLabel: "Run", StageID: "s", StageName: "Design", Plan: source, PlanDigest: "digest-B", ExpectedStatus: "awaiting_approval", StageUpdatedAt: stamp, CanApprove: true, CanRevise: true, CanPause: true}
	v := RenderPlan(card)
	if len(v.Parts) < 2 {
		t.Fatalf("plan should be split: %d", len(v.Parts))
	}
	var restored strings.Builder
	for i, p := range v.Parts {
		if len(utf16.Encode([]rune(p.Text))) > 4096 {
			t.Fatalf("part %d exceeds Telegram limit", i)
		}
		header := strings.Index(p.Text, "\n\n")
		if header < 0 || !strings.Contains(p.Text[:header], "Часть") {
			t.Fatalf("part %d lacks number", i)
		}
		restored.WriteString(p.Text[header+2:])
		if i < len(v.Parts)-1 {
			if _, ok := findIntent(View{Parts: []ViewPart{p}}, IntentApprovePlan); ok {
				t.Fatal("approval before last part")
			}
		}
	}
	if restored.String() != source {
		t.Fatal("plan source was truncated or modified")
	}
	in, ok := findIntent(View{Parts: v.Parts[len(v.Parts)-1:]}, IntentApprovePlan)
	if !ok || in.PlanDigest != "digest-B" || in.ExpectedStatus != "awaiting_approval" || in.StageUpdatedAt != stamp || in.RunKey != "r" {
		t.Fatalf("approval preconditions: %+v", in)
	}
	card.PlanDigest = "digest-A"
	if in, ok := findIntent(RenderPlan(card), IntentApprovePlan); !ok || in.PlanDigest != "digest-A" {
		t.Fatal("digest taken from stale or partial plan")
	}
}

func TestRenderRevisionAndPause(t *testing.T) {
	card := PlanCard{State: DataReady, RunKey: "r", StageID: "s", Plan: "full", PlanDigest: "d", CanRevise: true, CanContinue: true, RevisionDraft: "Fix a case", RevisionNeedsConfirmation: true}
	v := RenderPlan(card)
	if !strings.Contains(viewText(v), "Fix a case") {
		t.Fatal("revision draft is not shown before confirmation")
	}
	if in, ok := findIntent(v, IntentConfirmRevision); !ok || in.PlanDigest != "d" || in.RunKey != "r" {
		t.Fatalf("revision confirmation: %+v", v)
	}
	if _, ok := findIntent(v, IntentCancel); !ok {
		t.Fatal("no cancel for revision")
	}
	if _, ok := findIntent(v, IntentContinue); !ok {
		t.Fatal("no continue for paused stage")
	}
	card.State = DataOffline
	v = RenderPlan(card)
	for _, kind := range []IntentKind{IntentConfirmRevision, IntentContinue, IntentRevisePlan} {
		if _, ok := findIntent(v, kind); ok {
			t.Fatalf("offline control: %s", kind)
		}
	}
}

func TestRenderPlanWithoutSourceStillOffersStageControl(t *testing.T) {
	stamp := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		kind IntentKind
		card PlanCard
	}{
		{"paused", IntentContinue, PlanCard{CanContinue: true, ExpectedStatus: "paused"}},
		{"running", IntentPause, PlanCard{CanPause: true, ExpectedStatus: "running"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := tc.card
			card.State, card.RunKey, card.StageID, card.StageUpdatedAt = DataReady, "r", "s", stamp
			card.CanApprove, card.CanRevise, card.PlanDigest = true, true, ""
			view := RenderPlan(card)
			in, ok := findIntent(view, tc.kind)
			if !ok || in.RunKey != "r" || in.StageID != "s" || in.ExpectedStatus != card.ExpectedStatus || in.StageUpdatedAt != stamp {
				t.Fatalf("missing stage control with preconditions: %+v", view)
			}
			for _, kind := range []IntentKind{IntentApprovePlan, IntentRevisePlan, IntentConfirmRevision} {
				if _, ok := findIntent(view, kind); ok {
					t.Fatalf("plan action %q without plan bytes", kind)
				}
			}
			card.State = DataOffline
			if _, ok := findIntent(RenderPlan(card), tc.kind); ok {
				t.Fatalf("offline stage exposes %q", tc.kind)
			}
		})
	}
}

func TestRenderAddAgent(t *testing.T) {
	card := AddAgentCard{State: DataReady, RunKey: "r", StageID: "s", Profiles: []AgentProfile{{ID: "p", Name: "QA", Available: true}, {ID: "x", Name: "Gone", Available: false, Reason: "disabled"}}, ProfileID: "p", Scope: "stage", Mode: "required", Instruction: "Review", CanAdd: true}
	v := RenderAddAgent(card)
	if in, ok := findIntent(v, IntentSelectProfile); !ok || in.ProfileID != "p" || in.RunKey != "r" {
		t.Fatalf("profile: %+v", v)
	}
	if in, ok := findIntent(v, IntentConfirmAddAgent); !ok || in.ProfileID != "p" || in.Scope != "stage" || in.Mode != "required" || in.StageID != "s" {
		t.Fatalf("confirmation: %+v", v)
	}
	if !strings.Contains(viewText(v), "Review") || strings.Contains(viewText(v), "Первое сообщение отправлено") {
		t.Fatalf("instruction/first message: %s", viewText(v))
	}
	card.Scope = "run"
	v = RenderAddAgent(card)
	if in, ok := findIntent(v, IntentConfirmAddAgent); !ok || in.Scope != "run" || in.Mode != "" {
		t.Fatalf("run scope: %+v", v)
	}
	card.State = DataOffline
	if _, ok := findIntent(RenderAddAgent(card), IntentConfirmAddAgent); ok {
		t.Fatal("offline add-agent control")
	}
}

func TestRenderAddAgentConfirmationBindsExactComposer(t *testing.T) {
	card := AddAgentCard{State: DataReady, RunKey: "r", StageID: "s", Profiles: []AgentProfile{{ID: "p", Available: true}}, ProfileID: "p", Scope: "stage", Mode: "advisory", Instruction: "Check edge case", FirstMessage: "Start here", CanAdd: true}
	view := RenderAddAgent(card)
	in, ok := findIntent(view, IntentConfirmAddAgent)
	if !ok || in.ComposerDigest != composerDigest(card.Instruction, card.FirstMessage) {
		t.Fatalf("confirmation is not bound to exact composer text: %+v", view)
	}
	for _, kind := range []IntentKind{IntentSelectProfile, IntentSelectScope, IntentSelectMode} {
		if in, ok := findIntent(view, kind); !ok || in.ComposerDigest != "" {
			t.Fatalf("selection intent carries composer digest: %s %+v", kind, in)
		}
	}
}

func TestRenderAddAgentPreservesInstructionAndFirstMessage(t *testing.T) {
	instruction := strings.Repeat("Проверьте 😀", 400) + " конец задачи"
	first := strings.Repeat("Здравствуйте 📨", 400) + " конец первого сообщения"
	view := RenderAddAgent(AddAgentCard{State: DataReady, RunKey: "r", StageID: "s", Profiles: []AgentProfile{{ID: "p", Name: "QA", Available: true}}, ProfileID: "p", Scope: "stage", Mode: "required", Instruction: instruction, FirstMessage: first, CanAdd: true})
	want := "Добавить агента · r\nОбласть: стадия\nРежим помощника: обязательный\nЗадача: " + instruction + "\nПервое сообщение после создания: " + first
	assertTextParts(t, view, want)
	i, in, ok := buttonPart(view, IntentConfirmAddAgent)
	if !ok || i != len(view.Parts)-1 || in.ProfileID != "p" || in.Scope != "stage" || in.Mode != "required" {
		t.Fatalf("confirmation detached from complete instruction: %+v", view)
	}
}
