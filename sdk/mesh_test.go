package afmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestMeshReadsAndWrites(t *testing.T) {
	var writes []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mesh/participants":
			_, _ = fmt.Fprint(w, `{"participants":[{"id":"stage:impl","alias":"impl","kind":"stage_agent","scope":"stage","reachable":true}]}`)
		case "/api/mesh/threads":
			_, _ = fmt.Fprint(w, `{"threads":[{"id":"mt-1","scope":"run","status":"open","members":["user","stage:impl"]}]}`)
		case "/api/mesh/inbox", "/api/mesh/threads/mt-1":
			if r.URL.Query().Get("cursor") != "2" || r.URL.Query().Get("limit") != "5" {
				t.Errorf("paging: %s", r.URL.RawQuery)
			}
			_, _ = fmt.Fprint(w, `{"messages":[{"id":"m-1","thread_id":"mt-1","sender_id":"stage:impl","recipient_ids":["user"],"kind":"reply","in_reply_to":"m-0","body":"done","seq":3}],"next_cursor":3}`)
		case meshMessagesPath, "/api/mesh/attach-and-send":
			if r.Header.Get("Origin") != "http://"+r.Host {
				t.Errorf("Origin=%q host=%q", r.Header.Get("Origin"), r.Host)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			writes = append(writes, body)
			if r.URL.Path == meshMessagesPath {
				_, _ = fmt.Fprint(w, `{"message_id":"m-2","thread_id":"mt-1","seq":4}`)
			} else {
				_, _ = fmt.Fprint(w, `{"task_id":"t-1","participant_id":"run:t-1","thread_id":"mt-2","message_id":"m-3"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	ctx := context.Background()
	participants, err := run.Participants(ctx)
	if err != nil || len(participants) != 1 || participants[0].Alias != "impl" || !participants[0].Reachable {
		t.Fatalf("participants=%+v err=%v", participants, err)
	}
	threads, err := run.Threads(ctx)
	if err != nil || len(threads) != 1 || threads[0].ID != "mt-1" {
		t.Fatalf("threads=%+v err=%v", threads, err)
	}
	for _, read := range []func(context.Context, uint64, int) (MessagePage, error){run.Inbox, func(ctx context.Context, cursor uint64, limit int) (MessagePage, error) {
		return run.Thread(ctx, "mt-1", cursor, limit)
	}} {
		page, err := read(ctx, 2, 5)
		if err != nil || page.NextCursor != 3 || len(page.Messages) != 1 || page.Messages[0].InReplyTo != "m-0" {
			t.Fatalf("page=%+v err=%v", page, err)
		}
	}
	receipt, err := run.Send(ctx, MeshSendRequest{RequestID: "req-fixed", Recipients: []string{"stage:impl"}, Kind: "reply", InReplyTo: "m-0", Body: "hello"})
	if err != nil || receipt.MessageID != "m-2" {
		t.Fatalf("send=%+v err=%v", receipt, err)
	}
	first := "brief"
	attached, err := run.AttachAndSend(ctx, AttachAndSendRequest{RequestID: "req-attach", ProfileID: "reviewer", Scope: "run", Mode: "required", Instruction: "check", FirstMessage: &first})
	if err != nil || attached.ParticipantID != "run:t-1" || attached.MessageID == nil || *attached.MessageID != "m-3" {
		t.Fatalf("attach=%+v err=%v", attached, err)
	}
	if len(writes) != 2 || writes[0]["request_id"] != "req-fixed" || writes[0]["kind"] != "reply" || writes[0]["in_reply_to"] != "m-0" || writes[1]["request_id"] != "req-attach" || writes[1]["first_message"] != first {
		t.Fatalf("writes=%v", writes)
	}
}

func TestMeshHTTPConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"error":"stale"}`)
	}))
	defer srv.Close()
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := run.Send(context.Background(), MeshSendRequest{RequestID: "req", Body: "hi"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("err=%v", err)
	}
}

func TestThreadSendUsesThreadPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mesh/threads/mt-1/messages" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"message_id":"m"}`)
	}))
	defer srv.Close()
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := run.Send(context.Background(), MeshSendRequest{RequestID: "req", ThreadID: "mt-1", Body: "hi"})
	if !reflect.DeepEqual(err, error(nil)) {
		t.Fatal(err)
	}
}
