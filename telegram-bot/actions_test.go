package main

import (
	"context"
	"encoding/json"
	"fmt"
	afmsdk "github.com/akopichin/afm/sdk"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type afmFixture struct {
	mu                    sync.Mutex
	started               string
	stageStatus           string
	isScript              bool
	participantsCode      int
	pausePosts            int
	dialogEntries         []map[string]any
	answerBodies          []map[string]any
	options               []string
	plan                  string
	answerPosts           int
	approvalPosts         int
	sendIDs               []string
	sendBodies            []map[string]any
	discussionPosts       []map[string]any
	attachPosts           []map[string]any
	attachMessageID       string
	revisePosts           int
	approveCode           int
	discussionReplyAuthor string
	discussionReplyParent string
	sendCodes             []int
	threads               []string
	replies               []string
}

func newAFMFixture(t *testing.T) (*afmFixture, *httptest.Server, int) {
	t.Helper()
	f := &afmFixture{started: "2026-10-20T06:00:00Z", stageStatus: "awaiting_approval", options: []string{"A", "B"}, plan: "complete plan", threads: []string{"mt-other"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			_, _ = fmt.Fprintf(w, `{"flow_name":"demo","started_at":%q,"run_status":"running","capabilities":{"collaboration":true},"stages":[{"id":"impl","name":"Implementation","status":%q,"updated_at":"2026-10-20T06:01:00Z","show_plan":true,"show_dialog":true,"is_script":%t}]}`, f.started, f.stageStatus, f.isScript)
		case "/api/stages/impl/dialog":
			entries := f.dialogEntries
			if entries == nil {
				entries = []map[string]any{{"id": "q-1", "phase": "execute", "question": "Pick one", "options": f.options, "allow_custom": true}}
			}
			b, _ := json.Marshal(entries)
			_, _ = w.Write(b)
		case "/api/stages/impl/dialog/answer":
			f.answerPosts++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.answerBodies = append(f.answerBodies, body)
			_, _ = w.Write([]byte(`{"status":"answered"}`))
		case "/api/stages/impl/plan":
			_, _ = w.Write([]byte(f.plan))
		case "/api/stages/impl/approve":
			f.approvalPosts++
			if f.approveCode != 0 {
				w.WriteHeader(f.approveCode)
			}
			_, _ = w.Write([]byte(`{"status":"approved"}`))
		case "/api/stages/impl/revise":
			f.revisePosts++
			_, _ = w.Write([]byte(`{"status":"revising"}`))
		case "/api/stages/impl/pause":
			f.pausePosts++
			_, _ = w.Write([]byte(`{"status":"paused"}`))
		case "/api/assistant-profiles":
			_, _ = w.Write([]byte(`{"profiles":[{"id":"reviewer","title":"Reviewer","available":true}]}`))
		case "/api/mesh/attach-and-send":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.attachPosts = append(f.attachPosts, body)
			if f.attachMessageID != "" {
				_, _ = fmt.Fprintf(w, `{"task_id":"t1","participant_id":"a3","thread_id":"mt-new","message_id":%q}`, f.attachMessageID)
			} else {
				_, _ = w.Write([]byte(`{"task_id":"t1","participant_id":"a3","thread_id":"mt-new"}`))
			}
		case "/api/mesh/participants":
			if f.participantsCode != 0 {
				w.WriteHeader(f.participantsCode)
				return
			}
			_, _ = w.Write([]byte(`{"participants":[{"id":"a1","alias":"one","kind":"stage_agent","stage_id":"impl","activity":"running","reachable":true},{"id":"a2","alias":"two","reachable":true}]}`))
		case "/api/mesh/messages", "/api/mesh/threads/mt-other/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.sendIDs = append(f.sendIDs, fmt.Sprint(body["request_id"]))
			f.sendBodies = append(f.sendBodies, body)
			code := 200
			if len(f.sendCodes) > 0 {
				code = f.sendCodes[0]
				f.sendCodes = f.sendCodes[1:]
			}
			w.WriteHeader(code)
			if code == 200 {
				_, _ = w.Write([]byte(`{"message_id":"m-out","thread_id":"mt-other"}`))
			}
		case "/api/mesh/inbox":
			_, _ = w.Write([]byte(`{"messages":[],"next_cursor":0}`))
		case "/api/mesh/threads":
			_, _ = w.Write([]byte(`{"threads":[{"id":"mt-other","scope":"run","status":"open","members":["user","a1","a2"]}]}`))
		case "/api/mesh/threads/mt-other":
			_, _ = w.Write([]byte(`{"messages":[{"id":"m-reply","thread_id":"mt-other","sender_id":"a2","recipient_ids":["user"],"kind":"reply","in_reply_to":"m-out","body":"done","seq":5}],"next_cursor":5}`))
		case "/api/stages/impl/discussion":
			if r.Method == http.MethodPost {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.discussionPosts = append(f.discussionPosts, body)
				_, _ = w.Write([]byte(`{"message_id":"d-out"}`))
			} else {
				if f.discussionReplyAuthor != "" {
					_, _ = fmt.Fprintf(w, `{"messages":[{"id":"d-reply","author_kind":"assistant","author_id":%q,"parent_message_id":%q,"body":"done","seq":5}],"next_cursor":5,"has_more":false}`, f.discussionReplyAuthor, f.discussionReplyParent)
				} else {
					_, _ = w.Write([]byte(`{"messages":[],"next_cursor":0,"has_more":false}`))
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return f, srv, srv.Listener.Addr().(*net.TCPAddr).Port
}
func testService(t *testing.T, port int) (*Service, *Store, *httptest.Server) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true,"result":true}`)) }))
	tg := NewTelegramTransport("test-token")
	tg.base = telegram.URL
	service := NewService(NewRegistry(store), tg, map[int64]bool{17: true})
	b, err := service.Registry.Register(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.setUser(17, func(u *UserSession) { u.Selected = b.Key }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { telegram.Close(); _ = store.Close() })
	return service, store, telegram
}
func TestActionQuestionRejectsReorderedOptions(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, _, _ := testService(t, port)
	key := service.selected(17)
	stamp := time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC)
	q := questionIntent(QuestionCard{RunKey: key, StageID: "impl", QuestionID: "q-1", QuestionPhase: "execute", QuestionDigest: questionDigestFrom("Pick one", []string{"A", "B"}, true), StageUpdatedAt: stamp}, IntentAnswerQuestion)
	q.OptionValue = "B"
	f.mu.Lock()
	f.options = []string{"B", "A"}
	f.mu.Unlock()
	if err := service.HandleIntent(context.Background(), 17, q); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := f.answerPosts
	f.mu.Unlock()
	if got != 0 {
		t.Fatalf("stale answer posted %d times", got)
	}
}
func questionDigestFrom(text string, options []string, custom bool) string {
	return questionDigest(dialogEntry(text, options, custom))
}
func dialogEntry(text string, options []string, custom bool) afmsdk.DialogEntry {
	return afmsdk.DialogEntry{Question: text, Options: options, AllowCustom: custom}
}
func TestActionPlanChangedBlocksApproval(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, _, _ := testService(t, port)
	in := Intent{Kind: IntentApprovePlan, RunKey: service.selected(17), StageID: "impl", ExpectedStatus: "awaiting_approval", StageUpdatedAt: time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC), PlanDigest: planDigest([]byte("complete plan"))}
	f.mu.Lock()
	f.plan = "changed final byte"
	f.mu.Unlock()
	if err := service.HandleIntent(context.Background(), 17, in); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := f.approvalPosts
	f.mu.Unlock()
	if got != 0 {
		t.Fatalf("stale plan approved %d times", got)
	}
}
func TestDeliveryRetriesWithSameIDAndStopsOnRunChange(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	f.mu.Lock()
	f.sendCodes = []int{500, 200}
	f.mu.Unlock()
	out := Outbound{ID: "local-1", RunKey: key, Kind: "mesh", RequestID: "stable-1", Text: "hello", RecipientIDs: []string{"a1"}, Status: ReceiptQueued, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptQueued}}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.Deliver(context.Background(), out.ID); err != nil {
			t.Fatal(err)
		}
	}
	got := store.Snapshot().Outbox[out.ID]
	if got.Status != ReceiptAccepted || got.AFMMessageID != "m-out" {
		t.Fatalf("receipt %+v", got)
	}
	f.mu.Lock()
	ids := append([]string(nil), f.sendIDs...)
	f.started = "2026-10-20T07:00:00Z"
	f.mu.Unlock()
	if strings.Join(ids, ",") != "stable-1,stable-1" {
		t.Fatalf("IDs %v", ids)
	}
	out.ID = "local-2"
	out.RequestID = "stable-2"
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := service.Deliver(context.Background(), out.ID); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Outbox[out.ID]; got.Status != ReceiptFailed {
		t.Fatalf("reused port: %+v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendIDs) != 2 {
		t.Fatalf("sent to new run: %v", f.sendIDs)
	}
}
func TestSyncDiscoversThreadAndMatchesOnlyReplier(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	out := Outbound{ID: "o", RunKey: key, Kind: "mesh", RequestID: "r", Text: "hello", Status: ReceiptAccepted, AFMMessageID: "m-out", RecipientIDs: []string{"a1", "a2"}, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptAccepted, "a2": ReceiptAccepted}}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncRun(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot()
	receipt := got.Outbox["o"]
	if receipt.RecipientStatus["a1"] != ReceiptAccepted || receipt.RecipientStatus["a2"] != ReceiptReplied {
		t.Fatalf("recipient receipts %+v", receipt)
	}
	if got.Cursors["thread:"+string(key)+":mt-other"] != 5 {
		t.Fatalf("thread cursor %+v", got.Cursors)
	}
}

func TestActionQuestionAcceptsDisplayedOption(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, _, _ := testService(t, port)
	in := Intent{Kind: IntentAnswerQuestion, RunKey: service.selected(17), StageID: "impl", QuestionID: "q-1", QuestionPhase: "execute", QuestionDigest: questionDigestFrom("Pick one", []string{"A", "B"}, true), StageUpdatedAt: time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC), OptionValue: "B"}
	if err := service.HandleIntent(context.Background(), 17, in); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.answerPosts != 1 {
		t.Fatalf("answer posts %d", f.answerPosts)
	}
}
func TestComposerReplyAndInterruptSurviveRestart(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	reply := Intent{Kind: IntentReply, RunKey: key, ThreadID: "mt-other", InReplyTo: "m-agent", RecipientIDs: []string{"a1"}}
	if err := service.HandleIntent(context.Background(), 17, reply); err != nil {
		t.Fatal(err)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	service.Store = again
	service.Registry.Store = again
	if err := service.handleText(context.Background(), 17, "reply text"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.sendBodies) != 1 || f.sendBodies[0]["kind"] != "reply" || f.sendBodies[0]["in_reply_to"] != "m-agent" {
		t.Fatalf("reply body %+v", f.sendBodies)
	}
	f.mu.Unlock()
	interrupt := Intent{Kind: IntentInterrupt, RunKey: key, StageID: "impl", RecipientIDs: []string{"a1"}}
	f.mu.Lock()
	f.stageStatus = "running"
	f.mu.Unlock()
	if err := service.HandleIntent(context.Background(), 17, interrupt); err != nil {
		t.Fatal(err)
	}
	if err := service.handleText(context.Background(), 17, "interrupt text"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.discussionPosts) != 1 || f.discussionPosts[0]["interrupt"] != true || f.discussionPosts[0]["body"] != "interrupt text" {
		t.Fatalf("discussion %+v", f.discussionPosts)
	}
}
func TestActionRevisionNeedsConfirmationAndDuplicateCallbackDoesNotRetry(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	stamp := time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC)
	revision := Intent{Kind: IntentRevisePlan, RunKey: key, StageID: "impl", PlanDigest: planDigest([]byte("complete plan")), ExpectedStatus: "awaiting_approval", StageUpdatedAt: stamp}
	if err := service.HandleIntent(context.Background(), 17, revision); err != nil {
		t.Fatal(err)
	}
	if err := service.handleText(context.Background(), 17, "Please fix test"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	before := f.revisePosts
	f.mu.Unlock()
	if before != 0 {
		t.Fatalf("revision posted before confirmation: %d", before)
	}
	revision.Kind = IntentConfirmRevision
	if err := service.HandleIntent(context.Background(), 17, revision); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	after := f.revisePosts
	f.mu.Unlock()
	if after != 1 {
		t.Fatalf("revision posts %d", after)
	}
	approve := Intent{Kind: IntentApprovePlan, RunKey: key, StageID: "impl", PlanDigest: planDigest([]byte("complete plan")), ExpectedStatus: "awaiting_approval", StageUpdatedAt: stamp}
	f.mu.Lock()
	f.approveCode = 500
	f.mu.Unlock()
	if err := store.Update(func(st *State) error {
		st.Callbacks["short-id"] = CallbackRecord{UserID: 17, Intent: approve}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	callback := TelegramUpdate{Callback: &TelegramCallback{ID: "tg-1", From: TelegramUser{ID: 17}, Message: &TelegramMessage{Chat: TelegramChat{ID: 17, Type: "private"}}, Data: "short-id"}}
	for i := 0; i < 2; i++ {
		if err := service.HandleUpdate(context.Background(), callback); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	posts := f.approvalPosts
	f.mu.Unlock()
	if posts != 1 {
		t.Fatalf("ambiguous approve retried %d times", posts)
	}
}
func TestActionAddAgentChecksRealProfileAndStableRequest(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	comp := &ComposerContext{Mode: "agent_ready", RunKey: key, StageID: "impl", ProfileID: "reviewer", Scope: "stage", HelperMode: "advisory", Instruction: "Review code", FirstMessage: "Hello"}
	if err := service.setUser(17, func(u *UserSession) { u.Composer = comp }); err != nil {
		t.Fatal(err)
	}
	in := Intent{Kind: IntentConfirmAddAgent, RunKey: key, StageID: "impl", ProfileID: "reviewer", Scope: "stage", Mode: "advisory", ComposerDigest: composerDigest("Review code", "Hello")}
	if err := service.HandleIntent(context.Background(), 17, in); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	posts := append([]map[string]any(nil), f.attachPosts...)
	f.mu.Unlock()
	if len(posts) != 1 || posts[0]["profile_id"] != "reviewer" || posts[0]["scope"] != "stage" || posts[0]["mode"] != "advisory" || posts[0]["first_message"] != "Hello" || posts[0]["request_id"] == "" {
		t.Fatalf("attach body %+v", posts)
	}
	var outbound Outbound
	for _, o := range store.Snapshot().Outbox {
		outbound = o
	}
	if outbound.Status != ReceiptAccepted || outbound.RequestID != posts[0]["request_id"] {
		t.Fatalf("outbound %+v", outbound)
	}
}
func TestComposerResolvesAliasOnlyInSelectedRun(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	if err := service.setUser(17, func(u *UserSession) { u.Composer = &ComposerContext{Mode: "message", RunKey: key} }); err != nil {
		t.Fatal(err)
	}
	if err := service.handleText(context.Background(), 17, "@one please review"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	bodies := append([]map[string]any(nil), f.sendBodies...)
	f.mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("sends %+v", bodies)
	}
	recipients, ok := bodies[0]["recipient_ids"].([]any)
	if !ok || len(recipients) != 1 || recipients[0] != "a1" {
		t.Fatalf("recipients %+v", bodies[0])
	}
	if err := service.setUser(17, func(u *UserSession) { u.Composer = &ComposerContext{Mode: "message", RunKey: key} }); err != nil {
		t.Fatal(err)
	}
	if err := service.handleText(context.Background(), 17, "@missing hello"); err != nil {
		t.Fatal(err)
	}
	if service.user(17).Composer.Draft != "@missing hello" {
		t.Fatal("unknown alias draft lost")
	}
	if len(store.Snapshot().Outbox) != 1 {
		t.Fatal("unknown alias queued")
	}
}

func TestOfflineCachedHistoryRemainsReadable(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	service, store, _ := testService(t, port)
	key := service.selected(17)
	if err := service.showPlan(context.Background(), 17, key, "impl", "", false); err != nil {
		t.Fatal(err)
	}
	if err := service.showThread(context.Background(), 17, key, "mt-other", 0); err != nil {
		t.Fatal(err)
	}
	if store.Snapshot().PlanCache[planCacheKey(key, "impl")] != "complete plan" {
		t.Fatal("plan not cached")
	}
	var mu sync.Mutex
	var sent []string
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body.Text)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer telegram.Close()
	service.Telegram.base = telegram.URL
	srv.Close()
	if err := service.showPlan(context.Background(), 17, key, "impl", "", false); err != nil {
		t.Fatal(err)
	}
	if err := service.showThread(context.Background(), 17, key, "mt-other", 0); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || !strings.Contains(sent[0], "complete plan") || !strings.Contains(sent[1], "done") || !strings.Contains(sent[1], "Нет связи") {
		t.Fatalf("offline views %v", sent)
	}
}

func TestPlanDeliveryFailureNeverExposesApproval(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	f.mu.Lock()
	f.plan = strings.Repeat("complete plan line\n", 600)
	f.mu.Unlock()
	service, _, _ := testService(t, port)
	var mu sync.Mutex
	var sent []map[string]any
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body)
		n := len(sent)
		mu.Unlock()
		if n == 2 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer telegram.Close()
	service.Telegram.base = telegram.URL
	if err := service.showPlan(context.Background(), 17, service.selected(17), "impl", "", false); err == nil {
		t.Fatal("expected second Telegram part to fail")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent parts %d", len(sent))
	}
	for _, body := range sent {
		raw, _ := json.Marshal(body["reply_markup"])
		if strings.Contains(string(raw), "Одобрить план") {
			t.Fatalf("approval exposed before final part: %s", raw)
		}
	}
}

func TestDeliveryPermanentFailureRetainsTextAndRestartDoesNotResendAccepted(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	f.mu.Lock()
	f.sendCodes = []int{400, 200}
	f.mu.Unlock()
	makeOut := func(id string) Outbound {
		return Outbound{ID: id, RunKey: key, Kind: "mesh", RequestID: id, Text: "keep this text", RecipientIDs: []string{"a1"}, Status: ReceiptQueued, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptQueued}}
	}
	if err := store.Update(func(st *State) error { st.Outbox["bad"] = makeOut("bad"); st.Outbox["ok"] = makeOut("ok"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := service.Deliver(context.Background(), "bad"); err != nil {
		t.Fatal(err)
	}
	if err := service.Deliver(context.Background(), "ok"); err != nil {
		t.Fatal(err)
	}
	bad := store.Snapshot().Outbox["bad"]
	if bad.Status != ReceiptFailed || bad.Text != "keep this text" {
		t.Fatalf("failed item %+v", bad)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	service.Store = again
	service.Registry.Store = again
	if err := service.Deliver(context.Background(), "ok"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendIDs) != 2 {
		t.Fatalf("accepted item resent: %v", f.sendIDs)
	}
}

func TestSyncQuestionAndReplyNotifiedOnceAcrossRestart(t *testing.T) {
	_, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	var mu sync.Mutex
	var sent int
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			mu.Lock()
			sent++
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer telegram.Close()
	service.Telegram.base = telegram.URL
	if err := service.SyncRun(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := sent
	mu.Unlock()
	if first < 2 {
		t.Fatalf("expected question and reply notifications, got %d", first)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	service.Store = again
	service.Registry.Store = again
	if err := service.SyncRun(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	after := sent
	mu.Unlock()
	if after != first {
		t.Fatalf("duplicate notifications after restart: before=%d after=%d", first, after)
	}
}
func TestSyncDiscussionReplyMatchesOnlyAuthorAndParent(t *testing.T) {
	f, srv, port := newAFMFixture(t)
	defer srv.Close()
	service, store, _ := testService(t, port)
	key := service.selected(17)
	f.mu.Lock()
	f.discussionReplyAuthor = "a2"
	f.discussionReplyParent = "d-out"
	f.mu.Unlock()
	out := Outbound{ID: "d", RunKey: key, Kind: "discussion", RequestID: "r", Text: "hello", Status: ReceiptAccepted, AFMMessageID: "d-out", RecipientIDs: []string{"a1", "a2"}, RecipientStatus: map[string]ReceiptStatus{"a1": ReceiptAccepted, "a2": ReceiptAccepted}}
	if err := store.Update(func(st *State) error { st.Outbox[out.ID] = out; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncRun(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot().Outbox["d"]
	if got.RecipientStatus["a1"] != ReceiptAccepted || got.RecipientStatus["a2"] != ReceiptReplied || got.Status != ReceiptAccepted {
		t.Fatalf("discussion receipts %+v", got)
	}
}

func TestCommandConnectCopiesAndRunSwitchDropsOldComposer(t *testing.T) {
	f1, srv1, port1 := newAFMFixture(t)
	defer srv1.Close()
	_, srv2, port2 := newAFMFixture(t)
	defer srv2.Close()
	service, _, _ := testService(t, port1)
	old := service.selected(17)
	var mu sync.Mutex
	var messages []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		messages = append(messages, body.Text)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tg.Close()
	service.Telegram.base = tg.URL
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	badPort := closed.Listener.Addr().(*net.TCPAddr).Port
	closed.Close()
	if err := service.command(context.Background(), 17, fmt.Sprintf("/connect %d", badPort)); err != nil {
		t.Fatal(err)
	}
	if err := service.command(context.Background(), 17, fmt.Sprintf("/connect %d", port2)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	texts := append([]string(nil), messages...)
	mu.Unlock()
	if len(texts) < 2 || !strings.Contains(texts[0], fmt.Sprintf("Не удалось подключить AFM на порту %d", badPort)) || !strings.Contains(texts[1], fmt.Sprintf("dashboard :%d", port2)) {
		t.Fatalf("connect texts %v", texts)
	}
	newKey := service.selected(17)
	if err := service.setUser(17, func(u *UserSession) {
		u.Selected = old
		u.Composer = &ComposerContext{Mode: "question", RunKey: old, Draft: "keep"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleIntent(context.Background(), 17, Intent{Kind: IntentSelectRun, RunKey: newKey}); err != nil {
		t.Fatal(err)
	}
	if service.user(17).Composer != nil {
		t.Fatal("composer kept after selection")
	}
	stale := Intent{Kind: IntentAnswerQuestion, RunKey: old, StageID: "impl", QuestionID: "q-1", QuestionPhase: "execute", QuestionDigest: questionDigestFrom("Pick one", []string{"A", "B"}, true), StageUpdatedAt: time.Date(2026, 10, 20, 6, 1, 0, 0, time.UTC), OptionValue: "A"}
	if err := service.HandleIntent(context.Background(), 17, stale); err != nil {
		t.Fatal(err)
	}
	f1.mu.Lock()
	posts := f1.answerPosts
	f1.mu.Unlock()
	if posts != 0 {
		t.Fatalf("old-run action posted %d", posts)
	}
}
