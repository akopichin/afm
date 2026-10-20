package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	afmsdk "github.com/akopichin/afm/sdk"
)

func TestRunScopedAttachOmitsStageAndTracksActualParticipant(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	f.attachMessageID = "attach-message"
	s, store, _ := testService(t, port)
	key := s.selected(17)
	out := Outbound{ID: "attach", OwnerUserID: 17, RunKey: key, Kind: "attach", RequestID: "attach", StageID: "impl", Scope: "run", ProfileID: "reviewer", Instruction: "review", FirstMessage: "hello", Status: ReceiptQueued}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(context.Background(), out.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	posts := append([]map[string]any(nil), f.attachPosts...)
	f.mu.Unlock()
	if len(posts) != 1 || posts[0]["stage_id"] != nil || posts[0]["origin_stage"] != "impl" {
		t.Fatalf("run-scoped attach request: %+v", posts)
	}
	got := store.Snapshot().Outbox[out.ID]
	if got.AFMMessageID != "attach-message" || got.RecipientStatus["a3"] != ReceiptAccepted {
		t.Fatalf("receipt before reply: %+v", got)
	}
	if err := s.processMeshMessage(context.Background(), key, afmsdk.Message{ID: "reply", SenderID: "a3", Kind: "reply", InReplyTo: "attach-message"}); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Outbox[out.ID]; got.Status != ReceiptReplied || got.RecipientStatus["a3"] != ReceiptReplied {
		t.Fatalf("receipt after reply: %+v", got)
	}
}

func TestAttachWithoutFirstMessageHasNoPendingReplyTarget(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, store, _ := testService(t, port)
	out := Outbound{ID: "attach", RunKey: s.selected(17), Kind: "attach", RequestID: "attach", Scope: "run", ProfileID: "reviewer", Instruction: "review", Status: ReceiptQueued}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(context.Background(), out.ID); err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot().Outbox[out.ID]
	if got.Status != ReceiptAccepted || got.AFMMessageID != "" || len(got.RecipientStatus) != 0 {
		t.Fatalf("attach without message: %+v", got)
	}
}

func TestDiscussionImplicitStageRecipientAndMirroredNoticeOnce(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, store, _ := testService(t, port)
	texts, mu := captureTelegram(t, s)
	key := s.selected(17)
	out := Outbound{ID: "discussion", RunKey: key, Kind: "discussion", RequestID: "discussion", StageID: "impl", Text: "question", Status: ReceiptQueued}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(context.Background(), out.ID); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Outbox[out.ID]; got.RecipientStatus["stage:impl"] != ReceiptAccepted {
		t.Fatalf("implicit stage target missing: %+v", got)
	}
	f.mu.Lock()
	f.discussionReplyAuthor, f.discussionReplyParent = "stage:impl", "d-out"
	f.mu.Unlock()
	if err := s.syncDiscussion(context.Background(), mustRun(t, port), key, "impl", "discussion:"+string(key)+":impl"); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Outbox[out.ID]; got.Status != ReceiptReplied {
		t.Fatalf("stage reply not counted: %+v", got)
	}
	if err := s.processMeshMessage(context.Background(), key, afmsdk.Message{ID: "d-reply", SenderID: "stage:impl", Kind: "reply", InReplyTo: "d-out", Body: "done"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := 0
	for _, text := range *texts {
		if strings.Contains(text, "done") {
			count++
		}
	}
	mu.Unlock()
	if count != 1 {
		t.Fatalf("mirrored reply notified %d times", count)
	}
}

func mustRun(t *testing.T, port int) *afmsdk.Run {
	t.Helper()
	run, err := afmsdk.Connect(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestReconciliationSkipsRevokedUser(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, store, _ := testService(t, port)
	texts, mu := captureTelegram(t, s)
	a := PendingAction{ID: "pending", OwnerUserID: 17, RunKey: s.selected(17), StageID: "impl", Kind: IntentApprovePlan}
	if err := store.Update(func(st *State) error { st.PendingActions[a.ID] = a; return nil }); err != nil {
		t.Fatal(err)
	}
	s.Allowed = map[int64]bool{}
	if err := s.reconcilePendingActions(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sent := len(*texts)
	mu.Unlock()
	if sent != 0 || len(store.Snapshot().PendingActions) != 1 {
		t.Fatalf("revoked user got %d messages, pending=%v", sent, store.Snapshot().PendingActions)
	}
}

func TestInboxReportsPartialReadErrors(t *testing.T) {
	for _, failedPath := range []string{"/api/stages/impl/dialog", "/api/mesh/threads"} {
		t.Run(failedPath, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == failedPath {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				switch r.URL.Path {
				case "/api/status":
					_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","capabilities":{"collaboration":true},"stages":[{"id":"impl","status":"awaiting_user_input","show_dialog":true}]}`))
				case "/api/stages/impl/dialog":
					_, _ = w.Write([]byte(`[]`))
				case "/api/mesh/threads":
					_, _ = w.Write([]byte(`{"threads":[]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			s, _, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
			if err := s.setUser(17, func(u *UserSession) { u.Filter = "all" }); err != nil {
				t.Fatal(err)
			}
			texts, mu := captureTelegram(t, s)
			if err := s.showInbox(context.Background(), 17); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			body := strings.Join(*texts, "\n")
			mu.Unlock()
			if !strings.Contains(body, "Не удалось проверить вопросы или беседы") || strings.Contains(body, "Пока нет сообщений") {
				t.Fatalf("misleading inbox: %s", body)
			}
		})
	}
}

func TestReconcileReusedQuestionDoesNotClaimOldAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running"}`))
		case "/api/stages/impl/dialog":
			_, _ = w.Write([]byte(`[{"id":"q","phase":"execute","question":"old","answer":"yes"},{"id":"q","phase":"execute","question":"new","answer":null}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	texts, mu := captureTelegram(t, s)
	a := PendingAction{ID: "pending", OwnerUserID: 17, RunKey: s.selected(17), StageID: "impl", Kind: IntentAnswerQuestion, QuestionID: "q", QuestionPhase: "execute", QuestionDigest: questionDigestFrom("new", nil, false), Answer: "yes"}
	if err := store.Update(func(st *State) error {
		st.PendingActions[a.ID] = a
		st.Users[17] = UserSession{Selected: a.RunKey, Composer: &ComposerContext{Mode: "question", RunKey: a.RunKey, QuestionID: "q", QuestionPhase: "execute", Draft: "yes"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePendingActions(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	body := strings.Join(*texts, "\n")
	mu.Unlock()
	if strings.Contains(body, "принятый ответ") || !strings.Contains(body, "Не удалось подтвердить") {
		t.Fatalf("false confirmation: %s", body)
	}
	if c := s.user(17).Composer; c == nil || c.Draft != "yes" {
		t.Fatalf("draft lost: %+v", c)
	}
}

func TestQueuedMessageRetainsOriginalRunAfterSwitch(t *testing.T) {
	fa, sa, pa := newAFMFixture(t)
	defer sa.Close()
	fb, sb, pb := newAFMFixture(t)
	defer sb.Close()
	s, store, _ := testService(t, pa)
	a := s.selected(17)
	b, err := s.Registry.Register(context.Background(), pb)
	if err != nil {
		t.Fatal(err)
	}
	out := Outbound{ID: "queued", RunKey: a, Kind: "mesh", RequestID: "stable", ThreadID: "mt-other", InReplyTo: "m-original", MessageKind: "reply", RecipientIDs: []string{"a1"}, Text: "original text", Status: ReceiptQueued, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptQueued}}
	if err := store.Update(func(st *State) error {
		st.Outbox[out.ID] = out
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fa.mu.Lock()
	fa.sendCodes = []int{http.StatusInternalServerError}
	fa.mu.Unlock()
	if err := s.Deliver(context.Background(), out.ID); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Outbox[out.ID]; got.Status != ReceiptUnknown || !got.Attempted {
		t.Fatalf("temporary outage did not preserve queue: %+v", got)
	}
	if err := s.setUser(17, func(u *UserSession) { u.Selected = b.Key }); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	fa.mu.Lock()
	postsA := append([]map[string]any(nil), fa.sendBodies...)
	fa.mu.Unlock()
	fb.mu.Lock()
	postsB := len(fb.sendBodies)
	fb.mu.Unlock()
	if len(postsA) != 2 || postsB != 0 || postsA[0]["request_id"] != "stable" || postsA[1]["request_id"] != "stable" || postsA[1]["thread_id"] != "mt-other" || postsA[1]["in_reply_to"] != "m-original" || postsA[1]["body"] != "original text" {
		t.Fatalf("retargeted queue: A=%v B=%d", postsA, postsB)
	}
	if got := store.Snapshot().Outbox[out.ID]; got.Status != ReceiptAccepted || got.RunKey != a || got.Text != "original text" {
		t.Fatalf("queued item changed: %+v", got)
	}
}

func TestClosedStoreRejectsWritesAndReplayedTextPersists(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, store, _ := testService(t, port)
	key := s.selected(17)
	if err := s.setUser(17, func(u *UserSession) { u.Composer = &ComposerContext{Mode: "agent_instruction", RunKey: key} }); err != nil {
		t.Fatal(err)
	}
	update := TelegramUpdate{UpdateID: 44, Message: &TelegramMessage{From: TelegramUser{ID: 17}, Chat: TelegramChat{ID: 17, Type: "private"}, Text: "review this"}}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleUpdate(context.Background(), update); err == nil {
		t.Fatal("closed store acknowledged text")
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.Store = reopened
	s.Registry = NewRegistry(reopened)
	if err := s.HandleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if c := s.user(17).Composer; c == nil || c.Instruction != "review this" || c.Mode != "agent_ready" {
		t.Fatalf("replay failed: %+v", c)
	}
	if !reopened.Snapshot().ProcessedText[44] {
		t.Fatal("text update not durable")
	}
}

func TestConfirmAgentRejectsOldStageCard(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	defer srv.Close()
	s, store, _ := testService(t, port)
	key := s.selected(17)
	if err := s.setUser(17, func(u *UserSession) {
		u.Composer = &ComposerContext{Mode: "agent_ready", RunKey: key, StageID: "stage-b", ProfileID: "reviewer", Scope: "stage", HelperMode: "advisory", Instruction: "review"}
	}); err != nil {
		t.Fatal(err)
	}
	in := Intent{Kind: IntentConfirmAddAgent, RunKey: key, StageID: "stage-a", ProfileID: "reviewer", Scope: "stage", Mode: "advisory"}
	if err := s.HandleIntent(context.Background(), 17, in); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot().Outbox) != 0 {
		t.Fatal("stale stage card queued an attach")
	}
}

func TestRegisterRequestDoesNotWriteAfterStoreClose(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *State) error { st.UpdateOffset = time.Now().Unix(); return nil }); err == nil {
		t.Fatal("closed store accepted write")
	}
}

func TestCallbackConsumedAcrossRestartAfterLostActionResponse(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","stages":[{"id":"impl","status":"awaiting_approval","updated_at":"2026-10-20T06:01:00Z","show_plan":true}]}`))
		case "/api/stages/impl/plan":
			_, _ = w.Write([]byte("plan"))
		case "/api/stages/impl/approve":
			posts++
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	texts, mu := captureTelegram(t, s)
	stamp, _ := time.Parse(time.RFC3339, correctionStamp)
	intent := Intent{Kind: IntentApprovePlan, RunKey: s.selected(17), StageID: "impl", ExpectedStatus: "awaiting_approval", StageUpdatedAt: stamp, PlanDigest: planDigest([]byte("plan"))}
	if err := store.Update(func(st *State) error { st.Callbacks["cb"] = CallbackRecord{UserID: 17, Intent: intent}; return nil }); err != nil {
		t.Fatal(err)
	}
	update := TelegramUpdate{UpdateID: 55, Callback: &TelegramCallback{ID: "callback", From: TelegramUser{ID: 17}, Message: &TelegramMessage{Chat: TelegramChat{ID: 17, Type: "private"}}, Data: "cb"}}
	if err := s.HandleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Callbacks["cb"]; !got.Consumed {
		t.Fatalf("callback not consumed: %+v", got)
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
	s.Store = reopened
	s.Registry = NewRegistry(reopened)
	if err := s.HandleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	body := strings.Join(*texts, "\n")
	mu.Unlock()
	if posts != 1 || len(reopened.Snapshot().PendingActions) != 1 || !strings.Contains(body, "Результат неизвестен") {
		t.Fatalf("posts=%d pending=%d text=%s", posts, len(reopened.Snapshot().PendingActions), body)
	}
}
