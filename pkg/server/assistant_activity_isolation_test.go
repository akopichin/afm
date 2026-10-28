package server

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// writeActivity appends n assistant_activity notices for stageID, as the orchestrator's publisher does.
func writeActivity(runDir, stageID string, n int) {
	for i := range n {
		stagefiles.AppendNotice(runDir, stageID, string(bus.EventAssistantActivity), map[string]any{
			"event_id": "act-" + strconv.Itoa(i), "task_id": "t-1", "turn": 1, "tool": "text",
			"detail": strings.Repeat("x", 512),
		})
	}
}

// Activity has its own API: it must neither appear in /api/events nor push real notices out of the replay caps.
func TestHandleEvents_ExcludesAssistantActivityBeforeCaps(t *testing.T) {
	runDir := t.TempDir()
	stagefiles.AppendNotice(runDir, "s1", string(bus.EventAgentCompleted), "planning")
	writeActivity(runDir, "s1", maxStageReplayEvents+100)
	srv := newTestServerForRunDir(t, runDir, []string{"s1"})

	for _, query := range []string{"/api/events", "/api/events?stage=s1"} {
		rr := httptest.NewRecorder()
		srv.handleEvents(rr, httptest.NewRequest("GET", query, nil))
		var got []feedEvent
		mustDecode(t, rr, &got)
		completed := 0
		for _, e := range got {
			if e.Type == string(bus.EventAssistantActivity) {
				t.Fatalf("%s: activity leaked into the replay: %+v", query, e)
			}
			if e.Type == string(bus.EventAgentCompleted) {
				completed++
			}
		}
		if completed != 1 {
			t.Errorf("%s: agent_completed count = %d, want 1 (activity must not evict real notices)", query, completed)
		}
	}
}

// A verify notice is found among high-volume activity, and activity text that merely mentions a verify type is
// never mistaken for one.
func TestLatestVerifyByStage_IgnoresActivityLines(t *testing.T) {
	runDir := t.TempDir()
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyResult), map[string]any{"step": 2, "command": "codex", "verdict": "pass"})
	writeActivity(runDir, "build", 300)
	stagefiles.AppendNotice(runDir, "build", string(bus.EventAssistantActivity), map[string]any{
		"event_id": "act-x", "tool": "text", "detail": `{"type":"verify_result","verdict":"needs_changes"}`,
	})
	stagefiles.AppendNotice(runDir, "other", string(bus.EventAssistantActivity), map[string]any{"event_id": "act-y", "tool": "text", "detail": "verify_started"})

	views := latestVerifyByStage(runDir)
	if v := views["build"]; v == nil || v.Phase != "pass" || v.Step != 2 {
		t.Fatalf("build verify view = %+v, want {2 codex pass}", v)
	}
	if v := views["other"]; v != nil {
		t.Fatalf("other verify view = %+v, want none: activity text is not a verify notice", v)
	}
}

// Only a line whose type is assistant_activity is dropped from the replay: a foreign notice that merely quotes
// the marker in its text is still a real notice.
func TestHandleEvents_KeepsForeignNoticeQuotingActivityMarker(t *testing.T) {
	runDir := t.TempDir()
	stagefiles.AppendNotice(runDir, "s1", string(bus.EventAgentCompleted), "assistant_activity")
	srv := newTestServerForRunDir(t, runDir, []string{"s1"})

	rr := httptest.NewRecorder()
	srv.handleEvents(rr, httptest.NewRequest("GET", "/api/events", nil))
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) != 1 || got[0].Type != string(bus.EventAgentCompleted) {
		t.Fatalf("replay = %+v, want the agent_completed notice kept", got)
	}
}
