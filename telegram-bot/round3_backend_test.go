package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMultiplePendingQuestionsRemainIndependentlyActionable(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	f.mu.Lock()
	f.stageStatus = "awaiting_user_input"
	f.dialogEntries = []map[string]any{
		{"id": "first", "phase": "execute", "question": "First question", "options": []string{"A"}, "allow_custom": true},
		{"id": "second", "phase": "execute", "question": "Second question", "options": []string{"B"}, "allow_custom": true},
	}
	f.mu.Unlock()
	s, store, _ := testService(t, port)
	texts, mu := captureTelegram(t, s)
	key := s.selected(17)
	if err := s.showInbox(context.Background(), 17); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	inbox := strings.Join(*texts, "\n")
	mu.Unlock()
	if !strings.Contains(inbox, "First question") || !strings.Contains(inbox, "Second question") {
		t.Fatalf("inbox omitted pending question: %s", inbox)
	}
	if err := s.SyncRun(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	seenQuestions := 0
	for marker := range store.Snapshot().Seen {
		if strings.HasPrefix(marker, "question:"+string(key)+":impl:") {
			seenQuestions++
		}
	}
	if seenQuestions != 2 {
		t.Fatalf("sync notified %d questions, want 2", seenQuestions)
	}
	var firstCustom Intent
	for _, record := range store.Snapshot().Callbacks {
		if record.Intent.Kind == IntentCustomAnswer && record.Intent.QuestionID == "first" {
			firstCustom = record.Intent
		}
	}
	if firstCustom.QuestionID == "" {
		t.Fatal("first question has no custom-answer action")
	}
	if err := s.HandleIntent(context.Background(), 17, firstCustom); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	composer := (*texts)[len(*texts)-1]
	mu.Unlock()
	if !strings.Contains(composer, "First question") || !strings.Contains(composer, "Стадия: Implementation") {
		t.Fatalf("first question composer lost context: %s", composer)
	}
	var firstRefresh Intent
	for _, record := range store.Snapshot().Callbacks {
		if record.Intent.Kind == IntentRefresh && record.Intent.QuestionID == "first" {
			firstRefresh = record.Intent
		}
	}
	if firstRefresh.QuestionID == "" {
		t.Fatal("first question has no opening action")
	}
	if err := s.HandleIntent(context.Background(), 17, firstRefresh); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	opened := (*texts)[len(*texts)-1]
	mu.Unlock()
	if !strings.Contains(opened, "First question") {
		t.Fatalf("wrong question opened: %s", opened)
	}
	stamp := time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC)
	for _, tc := range []struct{ id, prompt, option string }{{"first", "First question", "A"}, {"second", "Second question", "B"}} {
		in := Intent{Kind: IntentAnswerQuestion, RunKey: key, StageID: "impl", QuestionID: tc.id, QuestionPhase: "execute", QuestionDigest: questionDigestFrom(tc.prompt, []string{tc.option}, true), StageUpdatedAt: stamp, OptionValue: tc.option}
		if err := s.HandleIntent(context.Background(), 17, in); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.answerBodies) != 2 || f.answerBodies[0]["id"] != "first" || f.answerBodies[1]["id"] != "second" {
		t.Fatalf("answer targets: %+v", f.answerBodies)
	}
}

func TestOldAddAgentConfirmationCannotUseNewFirstMessage(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, _, _ := testService(t, port)
	key := s.selected(17)
	comp := &ComposerContext{Mode: "agent_ready", RunKey: key, StageID: "impl", ProfileID: "reviewer", Scope: "stage", HelperMode: "advisory", Instruction: "Review code", FirstMessage: "A"}
	if err := s.setUser(17, func(u *UserSession) { u.Composer = comp }); err != nil {
		t.Fatal(err)
	}
	old := Intent{Kind: IntentConfirmAddAgent, RunKey: key, StageID: "impl", ProfileID: "reviewer", Scope: "stage", Mode: "advisory", ComposerDigest: composerDigest("Review code", "A")}
	if err := s.setUser(17, func(u *UserSession) { u.Composer.FirstMessage = "B" }); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleIntent(context.Background(), 17, old); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	posts := len(f.attachPosts)
	f.mu.Unlock()
	if posts != 0 {
		t.Fatalf("stale confirmation attached %d agents", posts)
	}
	old.ComposerDigest = composerDigest("Review code", "B")
	if err := s.HandleIntent(context.Background(), 17, old); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.attachPosts) != 1 || f.attachPosts[0]["first_message"] != "B" {
		t.Fatalf("fresh confirmation: %+v", f.attachPosts)
	}
}

func TestRefreshedQuestionSendsFreshInlineButton(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	f.mu.Lock()
	f.dialogEntries = []map[string]any{{"id": "q-1", "phase": "execute", "question": "Old prompt", "options": []string{"A"}, "allow_custom": true}}
	f.mu.Unlock()
	s, store, _ := testService(t, port)
	type sentCard struct {
		Text        string `json:"text"`
		ReplyMarkup struct {
			InlineKeyboard [][]TelegramButton `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	var cards []sentCard
	var cardsMu sync.Mutex
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var card sentCard
			_ = json.NewDecoder(r.Body).Decode(&card)
			cardsMu.Lock()
			cards = append(cards, card)
			cardsMu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer telegram.Close()
	s.Telegram.base = telegram.URL
	key := s.selected(17)
	if err := s.showQuestion(context.Background(), 17, key, "impl"); err != nil {
		t.Fatal(err)
	}
	old := Intent{Kind: IntentAnswerQuestion, RunKey: key, StageID: "impl", QuestionID: "q-1", QuestionPhase: "execute", QuestionDigest: questionDigestFrom("Old prompt", []string{"A"}, true), StageUpdatedAt: time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC), OptionValue: "A"}
	f.mu.Lock()
	f.dialogEntries[0]["question"] = "New prompt"
	f.mu.Unlock()
	if err := s.HandleIntent(context.Background(), 17, old); err != nil {
		t.Fatal(err)
	}
	fresh := questionDigestFrom("New prompt", []string{"A"}, true)
	found := false
	cardsMu.Lock()
	defer cardsMu.Unlock()
	for _, card := range cards {
		if !strings.Contains(card.Text, "New prompt") {
			continue
		}
		for _, row := range card.ReplyMarkup.InlineKeyboard {
			for _, button := range row {
				callback, ok := store.Snapshot().Callbacks[button.CallbackData]
				if ok && callback.Intent.Kind == IntentAnswerQuestion && callback.Intent.QuestionDigest == fresh && callback.Intent.QuestionPhase == "execute" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("refreshed question lacked a fresh inline answer: %+v", cards)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.answerPosts != 0 {
		t.Fatalf("old answer posted %d times", f.answerPosts)
	}
}

func TestInterruptRequiresLiveRunningStageAgent(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, store, _ := testService(t, port)
	key := s.selected(17)
	comp := ComposerContext{Mode: "message", RunKey: key, StageID: "impl", Interrupt: true}
	for _, status := range []string{"awaiting_approval", "paused"} {
		f.mu.Lock()
		f.stageStatus = status
		f.mu.Unlock()
		if err := s.showComposer(context.Background(), 17, comp); err != nil {
			t.Fatal(err)
		}
		for _, record := range store.Snapshot().Callbacks {
			if record.Intent.Kind == IntentInterrupt && record.Intent.Mode != "off" {
				t.Fatalf("interrupt offered while stage %s", status)
			}
		}
		if err := s.setUser(17, func(u *UserSession) { u.Composer = &comp }); err != nil {
			t.Fatal(err)
		}
		if err := s.handleText(context.Background(), 17, "urgent"); err != nil {
			t.Fatal(err)
		}
		if got := s.user(17).Composer.Draft; got != "urgent" {
			t.Fatalf("draft lost while %s: %q", status, got)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.discussionPosts) != 0 {
		t.Fatalf("interrupt posted while unavailable: %+v", f.discussionPosts)
	}
}

func TestRunningScriptDoesNotOfferOrAcceptPause(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	f.mu.Lock()
	f.stageStatus, f.isScript = "running", true
	f.mu.Unlock()
	s, store, _ := testService(t, port)
	key := s.selected(17)
	if err := s.showPlan(context.Background(), 17, key, "impl", "", false); err != nil {
		t.Fatal(err)
	}
	for _, record := range store.Snapshot().Callbacks {
		if record.Intent.Kind == IntentPause {
			t.Fatal("running script offered pause")
		}
	}
	in := Intent{Kind: IntentPause, RunKey: key, StageID: "impl", ExpectedStatus: "running", StageUpdatedAt: time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC)}
	if err := s.HandleIntent(context.Background(), 17, in); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pausePosts != 0 {
		t.Fatalf("running script pause posted %d times", f.pausePosts)
	}
}

func TestFailedParticipantLookupKeepsTextAfterRestart(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	f.mu.Lock()
	f.participantsCode = http.StatusInternalServerError
	f.mu.Unlock()
	s, store, _ := testService(t, port)
	key := s.selected(17)
	if err := s.setUser(17, func(u *UserSession) { u.Composer = &ComposerContext{Mode: "message", RunKey: key} }); err != nil {
		t.Fatal(err)
	}
	if err := s.handleText(context.Background(), 17, "check @one"); err != nil {
		t.Fatal(err)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Snapshot().Users[17].Composer.Draft; got != "check @one" {
		t.Fatalf("draft lost after participants failure: %q", got)
	}
}

func TestThreadPagingCursorSurvivesSecondPageFailureAndRestart(t *testing.T) {
	secondPageFails := true
	var cursors []string
	var cursorsMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","capabilities":{"collaboration":true},"stages":[]}`))
		case "/api/mesh/inbox":
			_, _ = w.Write([]byte(`{"messages":[],"next_cursor":0}`))
		case "/api/mesh/threads":
			_, _ = w.Write([]byte(`{"threads":[{"id":"t1","scope":"run","status":"open"}]}`))
		case "/api/mesh/threads/t1":
			cursor := r.URL.Query().Get("cursor")
			cursorsMu.Lock()
			cursors = append(cursors, cursor)
			cursorsMu.Unlock()
			if cursor == "100" && secondPageFails {
				secondPageFails = false
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			start, end := 1, 100
			if cursor == "100" {
				start, end = 101, 105
			} else if cursor == "105" {
				start, end = 106, 105
			}
			messages := make([]map[string]any, 0, end-start+1)
			for i := start; i <= end; i++ {
				messages = append(messages, map[string]any{"id": fmt.Sprintf("m%d", i), "thread_id": "t1", "sender_id": "agent", "body": fmt.Sprintf("body %d", i), "seq": i})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": messages, "next_cursor": end})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	texts, mu := captureTelegram(t, s)
	key := s.selected(17)
	if err := s.setUser(17, func(u *UserSession) { u.Filter = "all" }); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncRun(context.Background(), key); err == nil {
		t.Fatal("second-page failure was hidden")
	}
	cursorKey := "thread:" + string(key) + ":t1"
	if got := store.Snapshot().Cursors[cursorKey]; got != 100 {
		t.Fatalf("first page cursor %d", got)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.Store, s.Registry.Store = reopened, reopened
	if err := s.SyncRun(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot().Cursors[cursorKey]; got != 105 {
		t.Fatalf("final cursor %d", got)
	}
	if got := len(reopened.Snapshot().ThreadCache[threadCacheKey(key, "t1")]); got != 105 {
		t.Fatalf("cached %d of 105 messages", got)
	}
	cursorsMu.Lock()
	if len(cursors) < 3 || cursors[0] != "0" || cursors[1] != "100" || cursors[2] != "100" {
		t.Fatalf("paged reads after restart: %v", cursors)
	}
	cursorsMu.Unlock()
	mu.Lock()
	before := len(*texts)
	mu.Unlock()
	if before != 105 {
		t.Fatalf("notifications %d, want 105", before)
	}
	if err := s.SyncRun(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*texts) != before {
		t.Fatalf("duplicate notifications: before=%d after=%d", before, len(*texts))
	}
}

func TestReceiptShowsDiscoveredAttachAndDiscussionRecipients(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, store, _ := testService(t, port)
	texts, mu := captureTelegram(t, s)
	key := s.selected(17)
	for _, out := range []Outbound{
		{ID: "attach", Kind: "attach", RunKey: key, Status: ReceiptAccepted, Text: "First message", AFMMessageID: "m1", RecipientStatus: map[string]ReceiptStatus{"a3": ReceiptAccepted}},
		{ID: "discussion", Kind: "discussion", RunKey: key, Status: ReceiptAccepted, Text: "Stage message", AFMMessageID: "m2", RecipientStatus: map[string]ReceiptStatus{"stage:impl": ReceiptAccepted}},
	} {
		if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
			t.Fatal(err)
		}
		if err := s.sendReceipt(context.Background(), 17, out); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	joined := strings.Join(*texts, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "a3") || !strings.Contains(joined, "stage:impl") {
		t.Fatalf("receipt omitted resolved recipients: %s", joined)
	}
}
