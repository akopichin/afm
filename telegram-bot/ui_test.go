package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func findIntent(v View, kind IntentKind) (Intent, bool) {
	for _, p := range v.Parts {
		for _, row := range p.Buttons {
			for _, b := range row {
				if b.Intent.Kind == kind {
					return b.Intent, true
				}
			}
		}
	}
	return Intent{}, false
}

func viewText(v View) string {
	var parts []string
	for _, p := range v.Parts {
		parts = append(parts, p.Text)
	}
	return strings.Join(parts, "\n")
}

func joinedPartText(v View) string {
	var text strings.Builder
	for _, part := range v.Parts {
		text.WriteString(part.Text)
	}
	return text.String()
}

func assertTextParts(t *testing.T, view View, want string) {
	t.Helper()
	if len(view.Parts) < 2 {
		t.Fatalf("expected multipart text, got %d part(s)", len(view.Parts))
	}
	for i, part := range view.Parts {
		if textUnits(part.Text) > telegramTextLimit {
			t.Fatalf("part %d exceeds Telegram limit", i)
		}
	}
	if got := joinedPartText(view); got != want {
		t.Fatalf("text lost or changed: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func buttonPart(view View, kind IntentKind) (int, Intent, bool) {
	for i, part := range view.Parts {
		for _, row := range part.Buttons {
			for _, button := range row {
				if button.Intent.Kind == kind {
					return i, button.Intent, true
				}
			}
		}
	}
	return 0, Intent{}, false
}

func TestRenderRunsLongUnicodeAndInvalidated(t *testing.T) {
	name := strings.Repeat("Запуск🛰️", 650) + " конец имени"
	view := RenderRuns(RunCard{State: DataReady, Runs: []RunSummary{{Key: "old", Label: name, DashboardPort: 9876, Status: "Запуск заменён · подключите новый: /connect 9876", Completed: true}}})
	want := "Подключённые запуски:\n" + name + " · Запуск заменён · подключите новый: /connect 9876 · dashboard :9876"
	assertTextParts(t, view, want)
	i, intent, ok := buttonPart(view, IntentSelectRun)
	if !ok || intent.RunKey != "old" || i != len(view.Parts)-1 {
		t.Fatalf("run action detached from final part: %+v", view)
	}
	if strings.Contains(view.Parts[i].Buttons[0][0].Label, "Завершён") {
		t.Fatal("invalidated run shown as completed")
	}
}

func TestRenderRosterAndFailedReceiptPreserveLongText(t *testing.T) {
	name := strings.Repeat("Агент😀", 650) + " конец имени"
	roster := RenderRoster(RosterCard{State: DataReady, RunKey: "r", Agents: []AgentCard{{ID: "a", Name: name, Reachable: true}}})
	assertTextParts(t, roster, "Агенты · r\n"+name+" · Доступен")
	i, in, ok := buttonPart(roster, IntentToggleRecipient)
	if !ok || i != len(roster.Parts)-1 || len(in.RecipientIDs) != 1 || in.RecipientIDs[0] != "a" {
		t.Fatalf("roster action detached from final part: %+v", roster)
	}
	message := strings.Repeat("Неотправлено📨", 550) + " конец исходного сообщения"
	receipt := RenderReceipt(ReceiptCard{State: DataReady, RunKey: "r", Text: message, Status: ReceiptFailed, FailureReason: "адресат недоступен", ThreadID: "t"})
	assertTextParts(t, receipt, "Отправка · r\nНе доставлено\nСообщение: "+message+"\nПричина: адресат недоступен")
	i, in, ok = buttonPart(receipt, IntentThread)
	if !ok || i != len(receipt.Parts)-1 || in.ThreadID != "t" {
		t.Fatalf("receipt action detached from final part: %+v", receipt)
	}
}

func TestRenderRunsAndRosterAttachButtonsToTheirOwnLongItems(t *testing.T) {
	first := strings.Repeat("Первый😀", 600) + " конец первого"
	second := strings.Repeat("Второй🛰️", 600) + " конец второго"
	runs := RenderRuns(RunCard{State: DataReady, Runs: []RunSummary{{Key: "a", Label: first}, {Key: "b", Label: second}}})
	roster := RenderRoster(RosterCard{State: DataReady, RunKey: "r", Agents: []AgentCard{{ID: "a", Name: first, Reachable: true}, {ID: "b", Name: second, Reachable: true}}})
	for _, tc := range []struct {
		name string
		view View
		kind IntentKind
		key  func(Intent) string
	}{
		{"runs", runs, IntentSelectRun, func(in Intent) string { return string(in.RunKey) }},
		{"roster", roster, IntentToggleRecipient, func(in Intent) string { return in.RecipientIDs[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, part := range tc.view.Parts {
				if textUnits(part.Text) > telegramTextLimit {
					t.Fatal("part exceeds Telegram limit")
				}
				for _, row := range part.Buttons {
					for _, button := range row {
						if button.Intent.Kind != tc.kind {
							continue
						}
						key := tc.key(button.Intent)
						marker := map[string]string{"a": "конец первого", "b": "конец второго"}[key]
						if marker == "" || !strings.Contains(part.Text, marker) {
							t.Fatalf("%s button is detached from item %q", tc.name, key)
						}
						seen[key] = true
					}
				}
			}
			if !seen["a"] || !seen["b"] {
				t.Fatalf("missing item buttons: %v", seen)
			}
		})
	}
}

func TestRenderInboxErrorDoesNotClaimEmptySuccess(t *testing.T) {
	for _, filter := range []string{"important", "all"} {
		view := RenderInbox(InboxCard{State: DataError, Filter: filter, Error: "чтение не удалось"})
		got := joinedPartText(view)
		if !strings.Contains(got, "Ошибка: чтение не удалось") || strings.Contains(got, "Сейчас ничего не требует решения") || strings.Contains(got, "Пока нет сообщений") {
			t.Fatalf("error rendered as empty success: %q", got)
		}
	}
}

func TestRenderReceiptAcceptedDoesNotPromiseReply(t *testing.T) {
	view := RenderReceipt(ReceiptCard{State: DataReady, RunKey: "r", Status: ReceiptAccepted, Recipients: []RecipientReceipt{{Name: "Помощник", Status: ReceiptAccepted}}})
	got := joinedPartText(view)
	if strings.Count(got, "Принято AFM") != 2 || strings.Contains(got, "ожидаем ответ") {
		t.Fatalf("accepted receipt promises a reply without message ID: %q", got)
	}
}

func TestRenderHomeRunsAndOffline(t *testing.T) {
	empty := viewText(RenderRuns(RunCard{State: DataEmpty}))
	if !strings.Contains(empty, "Запуски не подключены") || !strings.Contains(empty, "/connect 9876") {
		t.Fatal(empty)
	}
	runs := RenderRuns(RunCard{State: DataReady, RunKey: "a", Runs: []RunSummary{{Key: "a", Label: "Alpha", State: DataReady}, {Key: "b", Label: "Beta", State: DataOffline, LastSeenAt: time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)}}})
	if len(runs.Parts[0].Buttons) != 2 || runs.Parts[0].Buttons[1][0].Intent.RunKey != "b" || !strings.Contains(viewText(runs), "Нет связи") {
		t.Fatalf("multi-run selector: %+v", runs)
	}
	home := RenderHome(HomeCard{State: DataReady, RunKey: "a", RunLabel: "Alpha", StageID: "s", HasPlan: true, HasQuestion: true, CanMessage: true})
	for _, kind := range []IntentKind{IntentRuns, IntentInbox, IntentPlan, IntentRoster} {
		if in, ok := findIntent(home, kind); !ok || (kind == IntentPlan && in.RunKey != "a") {
			t.Fatalf("missing scoped %s: %+v", kind, home)
		}
	}
	completed := RenderHome(HomeCard{State: DataReady, RunKey: "a", Completed: true, HasQuestion: true, HasPlan: true, CanMessage: true})
	if _, ok := findIntent(completed, IntentCompose); ok {
		t.Fatal("completed run exposes composer")
	}
}

func TestRenderRunsDuplicateNamesHaveDistinctButtons(t *testing.T) {
	name := strings.Repeat("Одинаковый запуск", 6)
	view := RenderRuns(RunCard{State: DataReady, RunKey: "12345678aaaaaaaaaaaaaaaaaaaaaaaa", Runs: []RunSummary{
		{Key: "12345678aaaaaaaaaaaaaaaaaaaaaaaa", Label: name, DashboardPort: 9876, Status: "Работает"},
		{Key: "12345678bbbbbbbbbbbbbbbbbbbbbbbb", Label: name, DashboardPort: 9876, Status: "Работает"},
	}})
	labels := map[RunKey]string{}
	for _, part := range view.Parts {
		for _, row := range part.Buttons {
			for _, button := range row {
				if button.Intent.Kind == IntentSelectRun {
					labels[button.Intent.RunKey] = button.Label
				}
			}
		}
	}
	if len(labels) != 2 || labels["12345678aaaaaaaaaaaaaaaaaaaaaaaa"] == labels["12345678bbbbbbbbbbbbbbbbbbbbbbbb"] {
		t.Fatalf("run buttons indistinguishable: %+v", labels)
	}
	for key, label := range labels {
		if !strings.Contains(label, ":9876") || !strings.Contains(label, "#"+string(key)[:9]) || textUnits(label) > 64 {
			t.Fatalf("run %s lacks stable port/key within button limit: %q", key, label)
		}
	}
}

func TestRenderOfflineCardsDoNotClaimConfirmedEmpty(t *testing.T) {
	cases := []struct {
		name string
		view View
		bad  string
	}{
		{"runs", RenderRuns(RunCard{State: DataOffline}), "Запуски не подключены"},
		{"roster", RenderRoster(RosterCard{State: DataOffline, RunKey: "r"}), "Агенты пока не найдены"},
		{"thread", RenderThread(ThreadCard{State: DataOffline, RunKey: "r", ThreadID: "t"}), "Пока нет сообщений"},
		{"add agent", RenderAddAgent(AddAgentCard{State: DataOffline, RunKey: "r"}), "Доступных профилей нет"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joinedPartText(tc.view)
			if strings.Contains(got, tc.bad) || !strings.Contains(got, "Нет связи") {
				t.Fatalf("offline data presented as confirmed empty: %q", got)
			}
		})
	}
}

func TestRenderEmptyThreadContinuationHasEndAndNavigation(t *testing.T) {
	view := RenderThread(ThreadCard{State: DataReady, RunKey: "r", ThreadID: "t", Filter: "all"})
	if !strings.Contains(joinedPartText(view), "Конец") {
		t.Fatalf("empty final page does not state end: %+v", view)
	}
	if in, ok := findIntent(view, IntentInbox); !ok || in.Filter != "all" {
		t.Fatalf("empty final page loses inbox navigation: %+v", view)
	}
	if in, ok := findIntent(view, IntentThread); !ok || in.RunKey != "r" || in.ThreadID != "t" {
		t.Fatalf("empty final page loses thread restart: %+v", view)
	}
	more := RenderThread(ThreadCard{State: DataReady, RunKey: "r", ThreadID: "t", HasMore: true})
	if strings.Contains(joinedPartText(more), "Конец") {
		t.Fatal("thread with more pages shown as ended")
	}
	if _, ok := findIntent(more, IntentLoadMore); !ok {
		t.Fatal("empty page with more history lost pagination")
	}
}

func TestRenderQuestionComposerNamesTarget(t *testing.T) {
	card := ComposerCard{State: DataReady, RunKey: "r", RunLabel: "Run", Mode: "question", StageID: "stage-1", QuestionID: "question-2", QuestionPhase: "execute", QuestionPrompt: "Which approach?"}
	got := joinedPartText(RenderComposer(card))
	for _, want := range []string{"Ответ на вопрос", "stage-1", "question-2", "Which approach?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("question composer missing %q: %q", want, got)
		}
	}
	card.StageName = "Implementation"
	if got := joinedPartText(RenderComposer(card)); !strings.Contains(got, "Implementation") {
		t.Fatalf("question composer ignores stage name: %q", got)
	}
}

func TestRenderInboxAndThread(t *testing.T) {
	empty := RenderInbox(InboxCard{State: DataEmpty, Filter: "important"})
	if !strings.Contains(viewText(empty), "Сейчас ничего не требует решения") {
		t.Fatal(viewText(empty))
	}
	inbox := RenderInbox(InboxCard{State: DataReady, Filter: "important", Items: []InboxItem{{RunKey: "a", RunLabel: "A", Kind: "question", StageID: "s", Summary: "Choose"}, {RunKey: "b", RunLabel: "B", Kind: "thread", ThreadID: "t", Summary: "Reply"}}})
	if in, ok := findIntent(inbox, IntentThread); !ok || in.RunKey != "b" || in.ThreadID != "t" {
		t.Fatalf("cross-run thread intent: %+v", inbox)
	}
	if in, ok := findIntent(inbox, IntentFilter); !ok || in.Filter != "all" {
		t.Fatalf("filter intent: %+v", inbox)
	}
	thread := RenderThread(ThreadCard{State: DataReady, RunKey: "a", ThreadID: "t", Messages: []ThreadMessage{{ID: "m", SenderID: "agent", SenderName: "Agent", Text: "Hello"}}, HasMore: true, NextCursor: 11})
	if in, ok := findIntent(thread, IntentReply); !ok || in.InReplyTo != "m" || in.RecipientIDs[0] != "agent" || in.ThreadID != "t" {
		t.Fatalf("reply context: %+v", thread)
	}
	if in, ok := findIntent(thread, IntentLoadMore); !ok || in.ThreadID != "t" {
		t.Fatalf("pagination: %+v", thread)
	}
	buttons := thread.Parts[0].Buttons
	if got := buttons[len(buttons)-1][0].Label; got != "Ещё сообщения" {
		t.Fatalf("forward pagination label = %q", got)
	}
}

func TestRenderThreadReplyAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     DataState
		completed bool
		wantReply bool
	}{
		{name: "active", state: DataReady, wantReply: true},
		{name: "completed", state: DataReady, completed: true},
		{name: "offline", state: DataOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thread := RenderThread(ThreadCard{
				State: tc.state, Completed: tc.completed, RunKey: "run-1", ThreadID: "thread-1",
				Messages: []ThreadMessage{{ID: "message-1", SenderID: "agent-1", SenderName: "Agent", Text: "History"}},
				HasMore:  true,
			})
			if _, ok := findIntent(thread, IntentReply); ok != tc.wantReply {
				t.Fatalf("reply available = %t, want %t: %+v", ok, tc.wantReply, thread)
			}
			if !strings.Contains(viewText(thread), "Agent: History") {
				t.Fatalf("thread history missing: %s", viewText(thread))
			}
			if in, ok := findIntent(thread, IntentLoadMore); !ok || in.RunKey != "run-1" || in.ThreadID != "thread-1" {
				t.Fatalf("thread pagination missing: %+v", thread)
			}
		})
	}
}

func TestRenderInboxPartsPreserveItemsAndButtons(t *testing.T) {
	long := strings.Repeat("📨данные", 700) + " конец первого элемента"
	items := []InboxItem{
		{RunKey: "a", RunLabel: "A", Kind: "thread", ThreadID: "t1", Summary: long},
		{RunKey: "b", RunLabel: "B", Kind: "plan", StageID: "s2", Summary: "второй элемент"},
	}
	view := RenderInbox(InboxCard{State: DataReady, Filter: "important", Items: items})
	want := "Входящие · Важное\nA · " + long + "\nB · второй элемент"
	if len(view.Parts) < 2 {
		t.Fatalf("long inbox has %d parts", len(view.Parts))
	}
	var rendered strings.Builder
	seen := map[IntentKind]bool{}
	for i, part := range view.Parts {
		if len(utf16.Encode([]rune(part.Text))) > telegramTextLimit {
			t.Fatalf("part %d exceeds Telegram limit", i)
		}
		rendered.WriteString(part.Text)
		for _, row := range part.Buttons {
			for _, button := range row {
				seen[button.Intent.Kind] = true
				switch button.Intent.Kind {
				case IntentThread:
					if button.Intent.ThreadID != "t1" || !strings.Contains(part.Text, "конец первого элемента") {
						t.Fatalf("thread button is detached from its item in part %d", i)
					}
				case IntentPlan:
					if button.Intent.StageID != "s2" || !strings.Contains(part.Text, "второй элемент") {
						t.Fatalf("plan button is detached from its item in part %d", i)
					}
				case IntentFilter:
					if !strings.Contains(part.Text, "Входящие · Важное") {
						t.Fatalf("filter button is detached from heading in part %d", i)
					}
				}
			}
		}
	}
	if rendered.String() != want || !seen[IntentThread] || !seen[IntentPlan] || !seen[IntentFilter] {
		t.Fatalf("inbox text or actions were lost: actions=%v", seen)
	}
}

func TestRenderThreadPartsPreserveMessagesAndButtons(t *testing.T) {
	long := strings.Repeat("😀message", 600) + " конец первого сообщения"
	messages := []ThreadMessage{
		{ID: "m1", SenderID: "agent-1", SenderName: "One", Text: long},
		{ID: "m2", SenderID: "agent-2", SenderName: "Two", Text: "второе сообщение"},
	}
	view := RenderThread(ThreadCard{State: DataReady, RunKey: "r", ThreadID: "t", Title: "Тема", Messages: messages, HasMore: true})
	if len(view.Parts) < 2 {
		t.Fatalf("long thread has %d parts", len(view.Parts))
	}
	var rendered strings.Builder
	seen := map[string]bool{}
	for i, part := range view.Parts {
		if len(utf16.Encode([]rune(part.Text))) > telegramTextLimit {
			t.Fatalf("part %d exceeds Telegram limit", i)
		}
		rendered.WriteString(part.Text)
		for _, row := range part.Buttons {
			for _, button := range row {
				in := button.Intent
				if in.Kind == IntentReply {
					marker := map[string]string{"m1": "конец первого сообщения", "m2": "второе сообщение"}[in.InReplyTo]
					if marker == "" || !strings.Contains(part.Text, marker) {
						t.Fatalf("reply %q is detached from message in part %d", in.InReplyTo, i)
					}
					seen[in.InReplyTo] = true
				}
				if in.Kind == IntentLoadMore && !strings.Contains(part.Text, "Ещё сообщения") {
					t.Fatalf("navigation is detached from its label in part %d", i)
				}
			}
		}
	}
	want := "Беседа · r\nТема\nOne: " + long + "\nTwo: второе сообщение\nЕщё сообщения"
	if rendered.String() != want || !seen["m1"] || !seen["m2"] {
		t.Fatalf("thread text or reply actions missing: parts=%d, replies=%v", len(view.Parts), seen)
	}
}

func TestRenderThreadSkipsUserSelfReply(t *testing.T) {
	view := RenderThread(ThreadCard{State: DataReady, RunKey: "r", ThreadID: "t", Messages: []ThreadMessage{{ID: "own", SenderID: "user", Text: "mine"}, {ID: "agent", SenderID: "a", Text: "theirs"}}})
	for _, part := range view.Parts {
		for _, row := range part.Buttons {
			for _, button := range row {
				if button.Intent.Kind == IntentReply && button.Intent.InReplyTo == "own" {
					t.Fatal("user message has a self-reply action")
				}
			}
		}
	}
	if in, ok := findIntent(view, IntentReply); !ok || in.InReplyTo != "agent" {
		t.Fatalf("agent reply action missing: %+v", view)
	}
}

func TestRenderComposerInterruptToggle(t *testing.T) {
	card := ComposerCard{State: DataReady, RunKey: "r", StageID: "s", CanInterrupt: true, RecipientIDs: []string{"agent"}}
	for _, tc := range []struct {
		interrupt bool
		mode      string
		label     string
	}{
		{false, "", "Прервать для ответа"},
		{true, "off", "Не прерывать"},
	} {
		card.Interrupt = tc.interrupt
		view := RenderComposer(card)
		in, ok := findIntent(view, IntentInterrupt)
		if !ok || in.Mode != tc.mode || in.StageID != "s" {
			t.Fatalf("interrupt=%t intent: %+v", tc.interrupt, view)
		}
		found := false
		for _, row := range view.Parts[0].Buttons {
			for _, button := range row {
				found = found || button.Intent.Kind == IntentInterrupt && button.Label == tc.label
			}
		}
		if !found {
			t.Fatalf("interrupt=%t label missing: %+v", tc.interrupt, view)
		}
	}
}

func TestRenderRosterReceiptComposer(t *testing.T) {
	roster := RenderRoster(RosterCard{State: DataReady, RunKey: "a", Agents: []AgentCard{{ID: "one", Alias: "one", StageID: "s", Reachable: true, Running: true, CanInterrupt: true}, {ID: "two", Alias: "two", Reachable: false}}, SelectedRecipientIDs: []string{"one"}})
	if in, ok := findIntent(roster, IntentToggleRecipient); !ok || in.RunKey != "a" || in.RecipientIDs[0] != "one" {
		t.Fatalf("selection: %+v", roster)
	}
	if strings.Contains(viewText(roster), "@two · доступен") {
		t.Fatal("inaccessible agent shown as reachable")
	}
	if in, ok := findIntent(roster, IntentInterrupt); !ok || in.StageID != "s" || in.RecipientIDs[0] != "one" {
		t.Fatalf("running stage agent interrupt: %+v", roster)
	}
	quiet := RenderRoster(RosterCard{State: DataReady, RunKey: "a", Agents: []AgentCard{{ID: "one", StageID: "s", Reachable: true, CanInterrupt: true}}})
	if _, ok := findIntent(quiet, IntentInterrupt); ok {
		t.Fatal("idle agent has interrupt control")
	}
	receipt := RenderReceipt(ReceiptCard{State: DataReady, RunKey: "a", Text: "hi", Status: ReceiptQueued, Recipients: []RecipientReceipt{{Name: "One", Status: ReceiptAccepted}, {Name: "Two", Status: ReceiptReplied}}})
	for _, phrase := range []string{"В очереди", "Принято AFM", "Агент ответил"} {
		if !strings.Contains(viewText(receipt), phrase) {
			t.Fatalf("missing %s: %s", phrase, viewText(receipt))
		}
	}
	for _, tc := range []struct {
		status ReceiptStatus
		want   string
	}{{ReceiptFailed, "Не доставлено"}, {ReceiptUnknown, "Результат неизвестен"}} {
		got := viewText(RenderReceipt(ReceiptCard{Status: tc.status, Text: "сохранённый текст", FailureReason: "адресат недоступен"}))
		if !strings.Contains(got, tc.want) || !strings.Contains(got, "сохранённый текст") || !strings.Contains(got, "адресат недоступен") {
			t.Fatalf("receipt %s lost status or context: %s", tc.status, got)
		}
	}
	composer := RenderComposer(ComposerCard{State: DataReady, RunKey: "a", RunLabel: "A", Mode: "message", StageID: "s", ThreadID: "t", InReplyTo: "m", ReplyAuthor: "One", ReplyText: "Hello", RecipientIDs: []string{"one"}, Draft: "reply", CanSend: true, CanInterrupt: true})
	if !strings.Contains(viewText(composer), "One") || !strings.Contains(viewText(composer), "Hello") {
		t.Fatal(viewText(composer))
	}
	if in, ok := findIntent(composer, IntentCancel); !ok || in.RunKey != "a" {
		t.Fatalf("cancel: %+v", composer)
	}
	if in, ok := findIntent(composer, IntentInterrupt); !ok || in.StageID != "s" || in.RecipientIDs[0] != "one" {
		t.Fatalf("interrupt: %+v", composer)
	}
}

func TestRenderRosterAddAgentEntry(t *testing.T) {
	ready := RenderRoster(RosterCard{State: DataReady, RunKey: "run-1", StageID: "stage-1"})
	in, ok := findIntent(ready, IntentAddAgent)
	if !ok || in.RunKey != "run-1" || in.StageID != "stage-1" {
		t.Fatalf("ready roster needs an add-agent entry with the current stage: %+v", ready)
	}
	addAgent := RenderAddAgent(AddAgentCard{State: DataReady, RunKey: in.RunKey, StageID: in.StageID, CanAdd: true})
	stageScopeFound := false
	for _, row := range addAgent.Parts[0].Buttons {
		for _, button := range row {
			stage := button.Intent
			if stage.Kind == IntentSelectScope && stage.Scope == "stage" && stage.StageID == "stage-1" {
				stageScopeFound = true
			}
		}
	}
	if !stageScopeFound {
		t.Fatalf("stage scope loses the current stage: %+v", addAgent)
	}

	for _, tc := range []struct {
		name      string
		state     DataState
		completed bool
	}{
		{"loading", DataLoading, false},
		{"offline", DataOffline, false},
		{"error", DataError, false},
		{"empty", DataEmpty, false},
		{"completed", DataReady, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := RenderRoster(RosterCard{State: tc.state, RunKey: "run-1", StageID: "stage-1", Completed: tc.completed,
				Agents:               []AgentCard{{ID: "agent-1", Alias: "agent", StageID: "stage-1", Reachable: true, Running: true, CanInterrupt: true}},
				SelectedRecipientIDs: []string{"agent-1"},
			})
			for _, kind := range []IntentKind{IntentAddAgent, IntentToggleRecipient, IntentCompose, IntentInterrupt} {
				if _, ok := findIntent(view, kind); ok {
					t.Fatalf("%s roster exposes %s: %+v", tc.name, kind, view)
				}
			}
		})
	}
	if _, ok := findIntent(RenderRoster(RosterCard{State: DataReady}), IntentAddAgent); ok {
		t.Fatal("roster without a run key exposes add-agent action")
	}
}
