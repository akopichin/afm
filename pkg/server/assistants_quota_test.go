package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/state"
)

// Задача помощника, ждущая сброса usage-limit, отдаёт дашборду свою quota (until/provider/источник/причину паузы),
// а задача без парка поле не отдаёт вовсе.
func TestList_ParkedHelperTaskCarriesItsQuota(t *testing.T) {
	until := time.Date(2026, 10, 23, 23, 51, 0, 0, time.UTC)
	reader := &fakeAssistantsReader{tree: []AssistantTaskView{
		{ID: "t-1", ProfileID: "reviewer", Status: "queued", StatusReason: "usage limit: waiting until 2026-10-23T23:51:00Z", Quota: NewQuotaView(&state.QuotaWait{
			Provider: "claude", Window: "five_hour", Source: state.QuotaSourceStructured, Until: until,
		})},
		{ID: "t-2", ProfileID: "reviewer", Status: "queued", Quota: NewQuotaView(&state.QuotaWait{Provider: "codex", PauseReason: state.QuotaPauseNoReset})},
		{ID: "t-3", ProfileID: "reviewer", Status: "running"},
	}}
	srv := newTestServer(t, Config{AssistantsReader: reader})
	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/assistants", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}

	var raw struct {
		Tasks []map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var first map[string]any
	if err := json.Unmarshal(raw.Tasks[0]["quota"], &first); err != nil {
		t.Fatalf("task quota: %v (%s)", err, raw.Tasks[0]["quota"])
	}
	if first["provider"] != "claude" || first["until"] != "2026-10-23T23:51:00Z" || first["window"] != "five_hour" || first["source"] != "structured" {
		t.Fatalf("quota = %v", first)
	}
	var second map[string]any
	if err := json.Unmarshal(raw.Tasks[1]["quota"], &second); err != nil || second["pause_reason"] != "no_reset" {
		t.Fatalf("paused quota = %v, %v", second, err)
	}
	if _, ok := raw.Tasks[2]["quota"]; ok {
		t.Fatalf("a task without a park must not carry quota: %s", raw.Tasks[2]["quota"])
	}
}
