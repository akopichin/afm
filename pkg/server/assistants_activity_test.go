package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

func getActivity(t *testing.T, srv *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func errCodeOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Error
}

func TestAssistantActivity_404WhenReaderOff(t *testing.T) {
	srv := newTestServer(t, Config{})
	if w := getActivity(t, srv, "/api/assistants/t-1/activity"); w.Code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404", w.Code)
	}
}

func TestAssistantActivity_InvalidTaskID(t *testing.T) {
	srv := newTestServer(t, Config{AssistantsReader: &fakeAssistantsReader{}})
	w := getActivity(t, srv, "/api/assistants/bad%20id/activity")
	if w.Code != http.StatusBadRequest || errCodeOf(t, w) != "invalid_task_id" {
		t.Fatalf("HTTP %d %s, want 400 invalid_task_id", w.Code, w.Body.String())
	}
}

func TestAssistantActivity_ForwardsQueryAndAnswersNoStore(t *testing.T) {
	cursor := uint64(120)
	cases := []struct {
		target string
		want   stagefiles.ActivityQuery
	}{
		{"/api/assistants/t-1/activity", stagefiles.ActivityQuery{Limit: stagefiles.DefaultActivityLimit}},
		{"/api/assistants/t-1/activity?limit=7", stagefiles.ActivityQuery{Limit: 7}},
		{"/api/assistants/t-1/activity?limit=100000", stagefiles.ActivityQuery{Limit: stagefiles.MaxActivityLimit}},
		{"/api/assistants/t-1/activity?after=120&limit=3", stagefiles.ActivityQuery{Limit: 3, After: &cursor}},
		{"/api/assistants/t-1/activity?before=120", stagefiles.ActivityQuery{Limit: stagefiles.DefaultActivityLimit, Before: &cursor}},
	}
	for _, c := range cases {
		reader := &fakeAssistantsReader{}
		w := getActivity(t, newTestServer(t, Config{AssistantsReader: reader}), c.target)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", c.target, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", c.target, got)
		}
		q := reader.activityQuery
		same := func(a, b *uint64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
		if reader.activityTask != "t-1" || q.Limit != c.want.Limit || !same(q.After, c.want.After) || !same(q.Before, c.want.Before) {
			t.Errorf("%s: reader got %s %+v, want t-1 %+v", c.target, reader.activityTask, q, c.want)
		}
	}
}

func TestAssistantActivity_RejectsMalformedQuery(t *testing.T) {
	cases := map[string]string{
		"after=abc":                  "invalid_cursor",
		"after=-1":                   "invalid_cursor",
		"after=":                     "invalid_cursor",
		"before=1.5":                 "invalid_cursor",
		"after=1&after=2":            "invalid_cursor",
		"after=0&before=0":           "invalid_cursor",
		"after=99999999999999999999": "invalid_cursor",
		"limit=abc":                  "invalid_limit",
		"limit=0":                    "invalid_limit",
		"limit=-5":                   "invalid_limit",
		"limit=":                     "invalid_limit",
		"limit=1&limit=2":            "invalid_limit",
	}
	for query, wantCode := range cases {
		reader := &fakeAssistantsReader{}
		w := getActivity(t, newTestServer(t, Config{AssistantsReader: reader}), "/api/assistants/t-1/activity?"+query)
		if w.Code != http.StatusBadRequest || errCodeOf(t, w) != wantCode {
			t.Errorf("%s: HTTP %d %s, want 400 %s", query, w.Code, w.Body.String(), wantCode)
		}
		if reader.activityTask != "" {
			t.Errorf("%s: a malformed query must not reach the reader", query)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: error Cache-Control = %q, want no-store", query, got)
		}
	}
}

func TestAssistantActivity_MapsReaderErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"unknown task", &state.OperationRejectedError{Code: assistants.CodeNotFound}, http.StatusNotFound, "not_found"},
		{"bad cursor", fmt.Errorf("read: %w", stagefiles.ErrInvalidCursor), http.StatusBadRequest, "invalid_cursor"},
		{"unavailable", ErrAssistantsUnavailable, http.StatusServiceUnavailable, "service_unavailable"},
		{"unexpected", errors.New("disk exploded"), http.StatusInternalServerError, errCodeInternal},
	}
	for _, c := range cases {
		srv := newTestServer(t, Config{AssistantsReader: &fakeAssistantsReader{activityErr: c.err}})
		w := getActivity(t, srv, "/api/assistants/t-1/activity")
		if w.Code != c.wantCode || errCodeOf(t, w) != c.wantBody {
			t.Errorf("%s: HTTP %d %s, want %d %s", c.name, w.Code, w.Body.String(), c.wantCode, c.wantBody)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", c.name, got)
		}
	}
}

func TestAssistantActivity_EmptyHistoryEncodesEmptyArray(t *testing.T) {
	reader := &fakeAssistantsReader{activity: stagefiles.ActivityPage{Entries: []stagefiles.ActivityEntry{}}}
	w := getActivity(t, newTestServer(t, Config{AssistantsReader: reader}), "/api/assistants/t-1/activity")
	if !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Fatalf("body = %s, want an empty entries array, not null", w.Body.String())
	}
}

// fileActivityReader serves the real notices.jsonl reader for the tasks it knows, like the production adapter.
type fileActivityReader struct {
	fakeAssistantsReader
	runDir string
	tasks  map[string]bool
}

func (f *fileActivityReader) TaskActivity(taskID string, q stagefiles.ActivityQuery) (stagefiles.ActivityPage, error) {
	if !f.tasks[taskID] {
		return stagefiles.ActivityPage{}, &state.OperationRejectedError{Code: assistants.CodeNotFound}
	}
	return stagefiles.ReadTaskActivity(f.runDir, taskID, q)
}

func TestAssistantActivity_PagesThroughARealNoticesFile(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		stagefiles.AppendNotice(dir, "", "assistant_activity", stagefiles.ActivityData{EventID: fmt.Sprint("a", i), TaskID: "t-1", Detail: fmt.Sprint("a", i)})
		stagefiles.AppendNotice(dir, "", "assistant_activity", stagefiles.ActivityData{EventID: fmt.Sprint("b", i), TaskID: "t-2", Detail: "other"})
	}
	srv := newTestServer(t, Config{AssistantsReader: &fileActivityReader{runDir: dir, tasks: map[string]bool{"t-1": true, "t-2": true}}})

	page := func(target string) stagefiles.ActivityPage {
		t.Helper()
		w := getActivity(t, srv, target)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", target, w.Code, w.Body.String())
		}
		var p stagefiles.ActivityPage
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ids := func(p stagefiles.ActivityPage) string {
		var out []string
		for _, e := range p.Entries {
			out = append(out, e.EventID)
		}
		return strings.Join(out, ",")
	}

	tail := page("/api/assistants/t-1/activity?limit=2")
	if ids(tail) != "a3,a4" || !tail.HasOlder {
		t.Fatalf("tail = %s older=%v", ids(tail), tail.HasOlder)
	}
	older := page(fmt.Sprintf("/api/assistants/t-1/activity?limit=2&before=%d", tail.Before))
	if ids(older) != "a1,a2" || !older.HasOlder {
		t.Fatalf("older = %s older=%v", ids(older), older.HasOlder)
	}
	stagefiles.AppendNotice(dir, "", "assistant_activity", stagefiles.ActivityData{EventID: "a5", TaskID: "t-1"})
	fresh := page(fmt.Sprintf("/api/assistants/t-1/activity?after=%d", tail.After))
	if ids(fresh) != "a5" {
		t.Fatalf("after = %s, want only the entry written since the tail", ids(fresh))
	}
	if w := getActivity(t, srv, "/api/assistants/t-9/activity"); w.Code != http.StatusNotFound {
		t.Errorf("unknown task: HTTP %d, want 404", w.Code)
	}
	if w := getActivity(t, srv, "/api/assistants/t-1/activity?after=3"); w.Code != http.StatusBadRequest || errCodeOf(t, w) != "invalid_cursor" {
		t.Errorf("mid-line cursor: HTTP %d %s, want 400 invalid_cursor", w.Code, w.Body.String())
	}
}

func TestAssistantActivity_CursorsAreJSONStrings(t *testing.T) {
	dir := t.TempDir()
	stagefiles.AppendNotice(dir, "", "assistant_activity", stagefiles.ActivityData{EventID: "a1", TaskID: "t-1"})
	srv := newTestServer(t, Config{AssistantsReader: &fileActivityReader{runDir: dir, tasks: map[string]bool{"t-1": true}}})
	w := getActivity(t, srv, "/api/assistants/t-1/activity")
	var raw struct {
		Entries []map[string]json.RawMessage `json:"entries"`
		After   json.RawMessage              `json:"after"`
		Before  json.RawMessage              `json:"before"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw.Entries) != 1 {
		t.Fatalf("body %s: %v", w.Body.String(), err)
	}
	for name, v := range map[string]json.RawMessage{"entry cursor": raw.Entries[0]["cursor"], "after": raw.After, "before": raw.Before} {
		if len(v) == 0 || v[0] != '"' {
			t.Errorf("%s = %s, want a JSON string", name, v)
		}
	}
}

func TestAssistantActivity_ResponseSizeIsBoundedByTheLimitCap(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 16*1024)
	for i := 0; i < 3*stagefiles.MaxActivityLimit; i++ {
		stagefiles.AppendNotice(dir, "", "assistant_activity", stagefiles.ActivityData{EventID: fmt.Sprint("e", i), TaskID: "t-1", Detail: big})
	}
	srv := newTestServer(t, Config{AssistantsReader: &fileActivityReader{runDir: dir, tasks: map[string]bool{"t-1": true}}})
	w := getActivity(t, srv, "/api/assistants/t-1/activity?limit=1000")
	var p stagefiles.ActivityPage
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != stagefiles.MaxActivityLimit || w.Body.Len() > 4<<20 {
		t.Fatalf("entries = %d, body = %d bytes; want %d entries within ~3.3 MiB", len(p.Entries), w.Body.Len(), stagefiles.MaxActivityLimit)
	}
}
