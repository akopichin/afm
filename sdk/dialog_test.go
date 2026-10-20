package afmsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDialogDiscussionAndProfiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/stages/impl/dialog":
			_, _ = fmt.Fprint(w, `[{"id":"q-1","phase":"execute","question":"Choose","options":["A","B"],"allow_custom":false}]`)
		case "/api/stages/impl/dialog/answer":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["phase"] != "execute" || body["id"] != "q-1" || body["answer"] != "B" || body["from_options"] != bool(true) {
				t.Errorf("answer body=%v", body)
			}
			_, _ = fmt.Fprint(w, `{"status":"answered"}`)
		case "/api/stages/impl/discussion":
			if r.Method == http.MethodGet {
				if r.URL.Query().Get("cursor") != "7" || r.URL.Query().Get("limit") != "4" {
					t.Errorf("discussion paging=%s", r.URL.RawQuery)
				}
				_, _ = fmt.Fprint(w, `{"messages":[{"id":"d-1","author_kind":"assistant","author_id":"a-1","parent_message_id":"d-0","body":"reply","seq":8}],"next_cursor":8,"has_more":true}`)
			} else {
				if r.Header.Get("Origin") != "http://"+r.Host {
					t.Errorf("Origin=%q", r.Header.Get("Origin"))
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["request_id"] != "stable" || body["interrupt"] != bool(true) {
					t.Errorf("discussion body=%v", body)
				}
				_, _ = fmt.Fprint(w, `{"message_id":"d-2"}`)
			}
		case "/api/assistant-profiles":
			_, _ = fmt.Fprint(w, `{"profiles":[{"id":"reviewer","title":"Reviewer","command":"codex","available":true},{"id":"other","available":false,"unavailable_reason":"missing"}],"max_depth":2,"max_depth_source":"flow"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	ctx := context.Background()
	entries, err := run.Dialog(ctx, "impl")
	if err != nil || len(entries) != 1 || entries[0].Phase != "execute" || entries[0].AllowCustom || len(entries[0].Options) != 2 {
		t.Fatalf("dialog=%+v err=%v", entries, err)
	}
	if err := run.Answer(ctx, "impl", DialogAnswer{Phase: "execute", ID: "q-1", Answer: "B", FromOptions: true}); err != nil {
		t.Fatal(err)
	}
	page, err := run.Discussion(ctx, "impl", 7, 4)
	if err != nil || page.NextCursor != 8 || !page.HasMore || page.Messages[0].ParentMessageID != "d-0" {
		t.Fatalf("discussion=%+v err=%v", page, err)
	}
	receipt, err := run.PostDiscussion(ctx, "impl", DiscussionPost{RequestID: "stable", Body: "hi", Interrupt: true})
	if err != nil || receipt.MessageID != "d-2" {
		t.Fatalf("post discussion=%+v err=%v", receipt, err)
	}
	profiles, err := run.AssistantProfiles(ctx)
	if err != nil || len(profiles.Profiles) != 2 || !profiles.Profiles[0].Available || profiles.Profiles[1].UnavailableReason != "missing" {
		t.Fatalf("profiles=%+v err=%v", profiles, err)
	}
}
