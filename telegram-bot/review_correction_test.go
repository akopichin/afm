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

const correctionStamp = "2026-10-20T06:01:00Z"

func captureTelegram(t *testing.T, s *Service) (*[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	texts := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			texts = append(texts, body.Text)
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(srv.Close)
	s.Telegram.base = srv.URL
	return &texts, &mu
}

func TestDeliveryReplaysCommittedRequestAfterRecipientFinishes(t *testing.T) {
	var mu sync.Mutex
	posts := []string{}
	committed := map[string]bool{}
	finished := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			state := "running"
			if finished {
				state = "finished"
			}
			_, _ = fmt.Fprintf(w, `{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":%q,"capabilities":{"collaboration":true}}`, state)
		case "/api/mesh/participants":
			reachable := !finished
			_, _ = fmt.Fprintf(w, `{"participants":[{"id":"a1","reachable":%t}]}`, reachable)
		case "/api/mesh/messages":
			var body struct {
				RequestID string `json:"request_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			posts = append(posts, body.RequestID)
			if !committed[body.RequestID] {
				committed[body.RequestID] = true
				finished = true
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close() // AFM accepted the request but the response was lost.
				return
			}
			_, _ = w.Write([]byte(`{"message_id":"m-once","thread_id":"thread"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	service, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	key := service.selected(17)
	out := Outbound{ID: "local", OwnerUserID: 17, RunKey: key, Kind: "mesh", RequestID: "stable", Text: "hello", RecipientIDs: []string{"a1"}, Status: ReceiptQueued, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptQueued}}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := service.Deliver(context.Background(), out.ID); err != nil {
		t.Fatal(err)
	}
	first := store.Snapshot().Outbox[out.ID]
	if first.Status != ReceiptUnknown || !first.Attempted {
		t.Fatalf("lost response must be uncertain: %+v", first)
	}
	if err := service.Deliver(context.Background(), out.ID); err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot().Outbox[out.ID]
	if got.Status != ReceiptAccepted || got.AFMMessageID != "m-once" {
		t.Fatalf("replayed receipt: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 2 || posts[0] != "stable" || posts[1] != "stable" || len(committed) != 1 {
		t.Fatalf("posts=%v commits=%v", posts, committed)
	}
}

func TestLostResponseRequestIDReplaySurvivesRestartForEveryOutboundKind(t *testing.T) {
	for _, kind := range []string{"mesh", "discussion", "attach"} {
		t.Run(kind, func(t *testing.T) {
			var mu sync.Mutex
			posts := []string{}
			commits := map[string]bool{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/status":
					_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","capabilities":{"collaboration":true}}`))
				case "/api/mesh/participants":
					_, _ = w.Write([]byte(`{"participants":[{"id":"a1","reachable":true}]}`))
				case "/api/mesh/inbox":
					_, _ = w.Write([]byte(`{"messages":[],"next_cursor":0}`))
				case "/api/mesh/threads":
					_, _ = w.Write([]byte(`{"threads":[]}`))
				case "/api/mesh/messages", "/api/stages/impl/discussion", "/api/mesh/attach-and-send":
					var body struct {
						RequestID string `json:"request_id"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					posts = append(posts, body.RequestID)
					first := !commits[body.RequestID]
					commits[body.RequestID] = true
					mu.Unlock()
					if first {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					if kind == "attach" {
						_, _ = w.Write([]byte(`{"task_id":"task","participant_id":"a2","thread_id":"thread","message_id":"accepted"}`))
					} else {
						_, _ = w.Write([]byte(`{"message_id":"accepted","thread_id":"thread"}`))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			service, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
			out := Outbound{ID: "local", RunKey: service.selected(17), Kind: kind, RequestID: "stable", Text: "hello", StageID: "impl", RecipientIDs: []string{"a1"}, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptQueued}, ProfileID: "reviewer", Scope: "run", Instruction: "review", FirstMessage: "hello", Status: ReceiptQueued}
			if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
				t.Fatal(err)
			}
			if err := service.Deliver(context.Background(), out.ID); err != nil {
				t.Fatal(err)
			}
			if store.Snapshot().Outbox[out.ID].Status != ReceiptUnknown {
				t.Fatal("lost response was not marked unknown")
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
			service.Store = reopened
			service.Registry = NewRegistry(reopened)
			if err := service.SyncOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := reopened.Snapshot().Outbox[out.ID]
			if got.Status != ReceiptAccepted || got.AFMMessageID != "accepted" || got.RequestID != "stable" {
				t.Fatalf("receipt: %+v", got)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(posts) != 2 || posts[0] != "stable" || posts[1] != "stable" || len(commits) != 1 {
				t.Fatalf("posts=%v effects=%v", posts, commits)
			}
		})
	}
}

func TestPausedStageWithoutPlanCanContinue(t *testing.T) {
	continued := 0
	paused := 0
	stageStatus := "paused"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = fmt.Fprintf(w, `{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","stages":[{"id":"impl","name":"Script","status":%q,"updated_at":"%s","show_plan":true}]}`, stageStatus, correctionStamp)
		case "/api/stages/impl/plan":
			http.NotFound(w, r)
		case "/api/stages/impl/continue":
			continued++
			_, _ = w.Write([]byte(`{"status":"continued"}`))
		case "/api/stages/impl/pause":
			paused++
			_, _ = w.Write([]byte(`{"status":"paused"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	service, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	key := service.selected(17)
	if err := service.showPlan(context.Background(), 17, key, "impl", "", false); err != nil {
		t.Fatal(err)
	}
	var action Intent
	for _, callback := range store.Snapshot().Callbacks {
		if callback.Intent.Kind == IntentContinue {
			action = callback.Intent
		}
	}
	if action.Kind != IntentContinue || action.ExpectedStatus != "paused" || action.StageUpdatedAt.IsZero() {
		t.Fatalf("continue button missing: %+v", action)
	}
	if err := service.HandleIntent(context.Background(), 17, action); err != nil {
		t.Fatal(err)
	}
	if continued != 1 {
		t.Fatalf("continue posts=%d", continued)
	}
	if err := service.HandleIntent(context.Background(), 17, Intent{Kind: IntentPause, RunKey: key, StageID: "impl", ExpectedStatus: "paused", StageUpdatedAt: action.StageUpdatedAt}); err != nil {
		t.Fatal(err)
	}
	if paused != 0 {
		t.Fatalf("ineligible pause posted %d times", paused)
	}
	stageStatus = "running"
	if err := service.showPlan(context.Background(), 17, key, "impl", "", false); err != nil {
		t.Fatal(err)
	}
	var pauseIntent Intent
	for _, callback := range store.Snapshot().Callbacks {
		if callback.Intent.Kind == IntentPause {
			pauseIntent = callback.Intent
		}
	}
	if pauseIntent.Kind != IntentPause || pauseIntent.ExpectedStatus != "running" {
		t.Fatalf("pause button missing: %+v", pauseIntent)
	}
	if err := service.HandleIntent(context.Background(), 17, pauseIntent); err != nil {
		t.Fatal(err)
	}
	if paused != 1 {
		t.Fatalf("eligible pause posts=%d", paused)
	}
}

func TestTelegramTextReplayDoesNotDuplicateOutboundOrAgentInstruction(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	f.mu.Lock()
	f.sendCodes = []int{500}
	f.mu.Unlock()
	if err := service.setUser(17, func(u *UserSession) {
		u.Composer = &ComposerContext{Mode: "message", RunKey: key, RecipientIDs: []string{"a1"}}
	}); err != nil {
		t.Fatal(err)
	}
	update := TelegramUpdate{UpdateID: 42, Message: &TelegramMessage{From: TelegramUser{ID: 17}, Chat: TelegramChat{ID: 17, Type: "private"}, Text: "send once"}}
	if err := service.HandleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	// The outbox and replay marker have been saved, but polling has not yet
	// advanced UpdateOffset. Reopen the store as if the bot crashed here.
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = reopened
	service.Store = reopened
	service.Registry = NewRegistry(reopened)
	if err := service.HandleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot().Outbox) != 1 {
		t.Fatalf("replayed update created %d outbounds", len(store.Snapshot().Outbox))
	}
	if err := service.setUser(17, func(u *UserSession) { u.Composer = &ComposerContext{Mode: "agent_instruction", RunKey: key} }); err != nil {
		t.Fatal(err)
	}
	update.UpdateID = 43
	update.Message.Text = "check code"
	if err := service.HandleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	comp := service.user(17).Composer
	if comp == nil || comp.Mode != "agent_ready" || comp.Instruction != "check code" || comp.FirstMessage != "" {
		t.Fatalf("agent input was reinterpreted on replay: %+v", comp)
	}
}

func TestReceiptChangesNotifyOriginatingUser(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	_ = f
	defer srv.Close()
	service, store, _ := testService(t, port)
	texts, mu := captureTelegram(t, service)
	key := service.selected(17)
	out := Outbound{ID: "out", OwnerUserID: 17, RunKey: key, Kind: "mesh", RequestID: "out", Text: "saved text", RecipientIDs: []string{"a1"}, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptQueued}, Status: ReceiptQueued}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := service.notifyReceiptChanges(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.setOutbound(out.ID, func(o *Outbound) { o.Status = ReceiptAccepted; o.RecipientStatus["a1"] = ReceiptAccepted }); err != nil {
		t.Fatal(err)
	}
	if err := service.notifyReceiptChanges(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.setOutbound(out.ID, func(o *Outbound) {
		o.Status = ReceiptFailed
		o.FailureReason = "recipient unavailable"
		o.RecipientStatus["a1"] = ReceiptFailed
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.notifyReceiptChanges(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), (*texts)...)
	mu.Unlock()
	if len(got) != 3 || !strings.Contains(got[0], "В очереди") || !strings.Contains(got[1], "Принято AFM") || !strings.Contains(got[2], "Не доставлено") || !strings.Contains(got[2], "saved text") {
		t.Fatalf("receipt notifications: %v", got)
	}
	if service.Store.Snapshot().Outbox[out.ID].NotifiedState == "" {
		t.Fatal("notification state not durable")
	}
}

func TestQueuedDeliveryHandlesOutageTerminalAndVanishedRecipient(t *testing.T) {
	for _, scenario := range []string{"outage", "terminal", "vanished"} {
		t.Run(scenario, func(t *testing.T) {
			runState := "running"
			reachable := true
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/status":
					_, _ = fmt.Fprintf(w, `{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":%q,"capabilities":{"collaboration":true}}`, runState)
				case "/api/mesh/participants":
					_, _ = fmt.Fprintf(w, `{"participants":[{"id":"a1","reachable":%t}]}`, reachable)
				case "/api/mesh/messages":
					posts++
					_, _ = w.Write([]byte(`{"message_id":"m"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			service, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
			out := Outbound{ID: "out", RunKey: service.selected(17), Kind: "mesh", RequestID: "stable", Text: "keep me", RecipientIDs: []string{"a1"}, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptQueued}, Status: ReceiptQueued}
			if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "outage":
				srv.Close()
			case "terminal":
				runState = "finished"
				defer srv.Close()
			case "vanished":
				reachable = false
				defer srv.Close()
			}
			if err := service.Deliver(context.Background(), out.ID); err != nil {
				t.Fatal(err)
			}
			got := store.Snapshot().Outbox[out.ID]
			if scenario == "outage" {
				if got.Status != ReceiptQueued {
					t.Fatalf("transient outage should remain queued: %+v", got)
				}
			} else if got.Status != ReceiptFailed || got.RecipientStatus["a1"] != ReceiptFailed || got.Text != "keep me" {
				t.Fatalf("permanent failure should retain text and recipient state: %+v", got)
			}
			if posts != 0 {
				t.Fatalf("ineligible delivery posted %d times", posts)
			}
		})
	}
}

func TestQuestionLostResponseIsReconciledWithoutRepost(t *testing.T) {
	answered := false
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","stages":[{"id":"impl","name":"Implementation","status":"awaiting_user_input","updated_at":"` + correctionStamp + `","show_dialog":true}]}`))
		case "/api/stages/impl/dialog":
			if answered {
				_, _ = w.Write([]byte(`[{"id":"q-1","phase":"execute","question":"Pick one","options":["A"],"allow_custom":true,"answer":"A"}]`))
			} else {
				_, _ = w.Write([]byte(`[{"id":"q-1","phase":"execute","question":"Pick one","options":["A"],"allow_custom":true}]`))
			}
		case "/api/stages/impl/dialog/answer":
			posts++
			answered = true
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
	service, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	texts, mu := captureTelegram(t, service)
	key := service.selected(17)
	stamp, _ := time.Parse(time.RFC3339, correctionStamp)
	in := Intent{Kind: IntentAnswerQuestion, RunKey: key, StageID: "impl", QuestionID: "q-1", QuestionPhase: "execute", QuestionDigest: questionDigestFrom("Pick one", []string{"A"}, true), StageUpdatedAt: stamp, OptionValue: "A"}
	if err := service.HandleIntent(context.Background(), 17, in); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot().PendingActions) != 1 {
		t.Fatal("unknown action was not persisted")
	}
	if err := service.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || len(store.Snapshot().PendingActions) != 0 {
		t.Fatalf("posts=%d pending=%v", posts, store.Snapshot().PendingActions)
	}
	mu.Lock()
	got := strings.Join(*texts, "\n")
	mu.Unlock()
	if !strings.Contains(got, "Результат неизвестен") || !strings.Contains(got, "AFM показывает принятый ответ") {
		t.Fatalf("missing uncertain/reconciled notices: %s", got)
	}
}

func TestReusedQuestionIDAndConflictKeepCustomDraft(t *testing.T) {
	question := "New prompt"
	phase := "execute"
	answerCode := 200
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","stages":[{"id":"impl","status":"awaiting_user_input","updated_at":"` + correctionStamp + `","show_dialog":true}]}`))
		case "/api/stages/impl/dialog":
			_, _ = fmt.Fprintf(w, `[{"id":"q-1","phase":%q,"question":%q,"options":["A"],"allow_custom":true}]`, phase, question)
		case "/api/stages/impl/dialog/answer":
			posts++
			w.WriteHeader(answerCode)
			_, _ = w.Write([]byte(`{"status":"answered"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	service, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	texts, mu := captureTelegram(t, service)
	key := service.selected(17)
	stamp, _ := time.Parse(time.RFC3339, correctionStamp)
	old := Intent{Kind: IntentAnswerQuestion, RunKey: key, StageID: "impl", QuestionID: "q-1", QuestionPhase: "execute", QuestionDigest: questionDigestFrom("Old prompt", []string{"A"}, true), StageUpdatedAt: stamp, OptionValue: "A"}
	if err := service.HandleIntent(context.Background(), 17, old); err != nil {
		t.Fatal(err)
	}
	old.Kind = IntentCustomAnswer
	if err := service.HandleIntent(context.Background(), 17, old); err != nil {
		t.Fatal(err)
	}
	if err := service.handleText(context.Background(), 17, "my draft"); err != nil {
		t.Fatal(err)
	}
	if posts != 0 || service.user(17).Composer.Draft != "my draft" {
		t.Fatalf("stale reused ID posted=%d composer=%+v", posts, service.user(17).Composer)
	}
	mu.Lock()
	shown := strings.Join(*texts, "\n")
	mu.Unlock()
	if !strings.Contains(shown, "New prompt") {
		t.Fatalf("stale question was not refreshed: %s", shown)
	}
	foundFresh := false
	for _, callback := range store.Snapshot().Callbacks {
		if callback.Intent.Kind == IntentAnswerQuestion && callback.Intent.QuestionDigest == questionDigestFrom("New prompt", []string{"A"}, true) {
			foundFresh = true
		}
	}
	if !foundFresh {
		t.Fatal("refreshed question did not get fresh callback")
	}
	question = "Old prompt"
	phase = "later"
	if err := service.HandleIntent(context.Background(), 17, Intent{Kind: IntentAnswerQuestion, RunKey: key, StageID: "impl", QuestionID: "q-1", QuestionPhase: "execute", QuestionDigest: old.QuestionDigest, StageUpdatedAt: stamp, OptionValue: "A"}); err != nil {
		t.Fatal(err)
	}
	if posts != 0 {
		t.Fatalf("changed phase posted %d times", posts)
	}
	phase = "execute"
	answerCode = http.StatusConflict
	if err := service.HandleIntent(context.Background(), 17, old); err != nil {
		t.Fatal(err)
	}
	if err := service.handleText(context.Background(), 17, "retry draft"); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || service.user(17).Composer.Draft != "retry draft" {
		t.Fatalf("conflict posted=%d composer=%+v", posts, service.user(17).Composer)
	}
	mu.Lock()
	shown = strings.Join(*texts, "\n")
	mu.Unlock()
	if !strings.Contains(shown, "Old prompt") {
		t.Fatalf("conflict question was not refreshed: %s", shown)
	}
}

func TestStageActionLostResponseIsReportedAfterRestartWithoutRepost(t *testing.T) {
	posts := 0
	stageStatus := "awaiting_approval"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = fmt.Fprintf(w, `{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"running","stages":[{"id":"impl","status":%q,"updated_at":"%s","show_plan":true}]}`, stageStatus, correctionStamp)
		case "/api/stages/impl/plan":
			_, _ = w.Write([]byte("plan"))
		case "/api/stages/impl/approve":
			posts++
			stageStatus = "running"
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
	service, store, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
	texts, mu := captureTelegram(t, service)
	stamp, _ := time.Parse(time.RFC3339, correctionStamp)
	in := Intent{Kind: IntentApprovePlan, RunKey: service.selected(17), StageID: "impl", PlanDigest: planDigest([]byte("plan")), ExpectedStatus: "awaiting_approval", StageUpdatedAt: stamp}
	if err := service.HandleIntent(context.Background(), 17, in); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot().PendingActions) != 1 {
		t.Fatal("unknown action not persisted")
	}
	// Reopen the same durable state to exercise reconciliation after a restart.
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service.Store = reopened
	service.Registry = NewRegistry(reopened)
	if err := service.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || len(reopened.Snapshot().PendingActions) != 0 {
		t.Fatalf("posts=%d pending=%v", posts, reopened.Snapshot().PendingActions)
	}
	mu.Lock()
	got := strings.Join(*texts, "\n")
	mu.Unlock()
	if !strings.Contains(got, "Результат неизвестен") || !strings.Contains(got, "AFM показывает") {
		t.Fatalf("missing unknown/reconciled notice: %s", got)
	}
}

func TestInboxShowsOfflineAndFailedRuns(t *testing.T) {
	t.Run("offline", func(t *testing.T) {
		_, srv, port := newAFMFixture(t)
		service, _, _ := testService(t, port)
		texts, mu := captureTelegram(t, service)
		srv.Close()
		if err := service.showInbox(context.Background(), 17); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		got := strings.Join(*texts, "\n")
		mu.Unlock()
		if !strings.Contains(got, "Нет связи с AFM") || strings.Contains(got, "Сейчас ничего не требует решения") {
			t.Fatalf("offline inbox: %s", got)
		}
	})
	t.Run("failed stage", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				_, _ = w.Write([]byte(`{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","run_status":"failed","stages":[{"id":"bad","name":"Build","status":"failed","updated_at":"` + correctionStamp + `"},{"id":"later","name":"Later","status":"pending"}]}`))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		service, _, _ := testService(t, srv.Listener.Addr().(*net.TCPAddr).Port)
		texts, mu := captureTelegram(t, service)
		if err := service.showInbox(context.Background(), 17); err != nil {
			t.Fatal(err)
		}
		if err := service.showHome(context.Background(), 17, service.selected(17)); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		got := strings.Join(*texts, "\n")
		mu.Unlock()
		if !strings.Contains(got, "Ошибка стадии: Build") || !strings.Contains(got, "Состояние: Ошибка") {
			t.Fatalf("failed stage summary: %s", got)
		}
	})
}

func TestSyncContinuesPastAFMReadFailureInAnotherRun(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"bad","started_at":"2026-10-20T06:00:00Z","run_status":"running","capabilities":{"collaboration":true}}`))
		case "/api/mesh/participants":
			_, _ = w.Write([]byte(`{"participants":[]}`))
		case "/api/mesh/inbox":
			_, _ = w.Write([]byte(`{"messages":[],"next_cursor":0}`))
		case "/api/mesh/threads":
			w.WriteHeader(500)
		default:
			http.NotFound(w, r)
		}
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"good","started_at":"2026-10-20T07:00:00Z","run_status":"running","stages":[{"id":"impl","name":"Implementation","status":"awaiting_user_input","updated_at":"` + correctionStamp + `","show_dialog":true}]}`))
		case "/api/stages/impl/dialog":
			_, _ = w.Write([]byte(`[{"id":"q","phase":"execute","question":"Needs reply","options":["A"]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer second.Close()
	service, store, _ := testService(t, first.Listener.Addr().(*net.TCPAddr).Port)
	good, err := service.Registry.Register(context.Background(), second.Listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	bad := service.selected(17)
	if err := store.Update(func(st *State) error {
		b := st.Runs[bad]
		delete(st.Runs, bad)
		b.Key = "a"
		st.Runs["a"] = b
		g := st.Runs[good.Key]
		delete(st.Runs, good.Key)
		g.Key = "z"
		st.Runs["z"] = g
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected first run read error")
	}
	seen := false
	for marker := range store.Snapshot().Seen {
		if strings.Contains(marker, "question:z:impl") {
			seen = true
		}
	}
	if !seen {
		t.Fatal("healthy run was not synchronized after first run failed")
	}
}
