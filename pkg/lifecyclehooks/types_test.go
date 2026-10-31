package lifecyclehooks

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestEventSelectorUnmarshal(t *testing.T) {
	cases := []struct {
		name    string
		yamlIn  string
		want    EventSelector
		wantErr bool
	}{
		{"scalar all", "events: all\n", EventSelector{All: true}, false},
		{"list", "events: [stage_failed, flow_started]\n", EventSelector{Events: []EventType{"stage_failed", "flow_started"}}, false},
		{"scalar not all", "events: everything\n", EventSelector{}, true},
		{"mapping", "events: {a: 1}\n", EventSelector{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s struct {
				Events EventSelector `yaml:"events"`
			}
			err := yaml.Unmarshal([]byte(tc.yamlIn), &s)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", s.Events)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.Events.All != tc.want.All || len(s.Events.Events) != len(tc.want.Events) {
				t.Fatalf("got %+v, want %+v", s.Events, tc.want)
			}
			for i := range tc.want.Events {
				if s.Events.Events[i] != tc.want.Events[i] {
					t.Fatalf("events[%d]: got %q want %q", i, s.Events.Events[i], tc.want.Events[i])
				}
			}
		})
	}
}

func TestHookUnmarshal(t *testing.T) {
	yamlIn := `
id: notify
events: all
skip_events:
  - stage_question_answered
command: "bash /opt/notify.sh"
timeout: 30s
retries: 2
`
	var h Hook
	if err := yaml.Unmarshal([]byte(yamlIn), &h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if h.ID != "notify" || !h.Events.All || h.Command != "bash /opt/notify.sh" {
		t.Fatalf("got %+v", h)
	}
	if h.Timeout != 30_000_000_000 || h.Retries != 2 {
		t.Fatalf("timeout=%v retries=%d", h.Timeout, h.Retries)
	}
	if len(h.SkipEvents) != 1 || h.SkipEvents[0] != EventStageQuestionAnswered {
		t.Fatalf("skip: %+v", h.SkipEvents)
	}
}

func TestAllEventsCatalog(t *testing.T) {
	got := make(map[EventType]bool)
	for _, ev := range AllEvents() {
		if got[ev] {
			t.Errorf("duplicate catalog entry %q", ev)
		}
		got[ev] = true
	}
	if len(got) != 30 {
		t.Errorf("catalog size: got %d want 30 (%v)", len(got), AllEvents())
	}
	for _, ev := range AllEvents() {
		if ev == "" {
			t.Error("empty event name in catalog")
		}
	}
}

func TestIsFlowEvent(t *testing.T) {
	if !IsFlowEvent(EventFlowStarted) {
		t.Error("flow_started must be a flow event")
	}
	if !IsFlowEvent(EventFlowRepeated) {
		t.Error("flow_repeated must be a flow event")
	}
	if IsFlowEvent(EventStageFailed) {
		t.Error("stage_failed must not be a flow event")
	}
}

func TestBuildPayload(t *testing.T) {
	cfg := DispatcherConfig{FlowName: "release", RunID: "run-1", RunDir: "/r", RootDir: "/p", Resumed: false}
	ev := Event{Type: EventStageFailed, StageID: "deploy", StageName: "Deploy", From: "running", To: "failed", Phase: "implementation", Reason: "boom", Seq: 42, Time: time.Unix(0, 0).UTC()}
	p := BuildPayload(cfg, ev, "run-1:transition:42:stage_failed")
	if p.SchemaVersion != 1 || p.Event != EventStageFailed || p.EventID != "run-1:transition:42:stage_failed" {
		t.Fatalf("got %+v", p)
	}
	if p.Flow.RunID != "run-1" || p.Flow.Name != "release" || p.Stage == nil || p.Stage.To != "failed" || p.Reason != "boom" {
		t.Fatalf("got %+v", p)
	}
	// flow-событие: Stage == nil
	fe := Event{Type: EventFlowFinished, Time: time.Unix(0, 0).UTC()}
	if p2 := BuildPayload(cfg, fe, "run-1:flow:flow_finished"); p2.Stage != nil {
		t.Fatalf("flow payload must have no stage, got %+v", p2.Stage)
	}
}

func TestAllEventsCatalog_IncludesFlowRepeated(t *testing.T) {
	for _, ev := range AllEvents() {
		if ev == EventFlowRepeated {
			return
		}
	}
	t.Fatal("flow_repeated missing from the catalog (events: all would skip it)")
}

func TestBuildPayload_RepeatCycles(t *testing.T) {
	maxCycles := func(p Payload) string {
		if p.Flow.MaxCycles == nil {
			return "<nil>"
		}
		return strconv.Itoa(*p.Flow.MaxCycles)
	}
	cases := []struct {
		name         string
		cfg          DispatcherConfig
		wantCycle    int
		wantMax      string
		wantPrevious string
	}{
		{"cycle 1 has no previous run", DispatcherConfig{RunID: "r1", Cycle: 1, MaxCycles: 3}, 1, "3", ""},
		{"cycle 2 links previous run", DispatcherConfig{RunID: "r2", Cycle: 2, MaxCycles: 3, PreviousRunID: "r1"}, 2, "3", "r1"},
		{"infinite repeat reports max_cycles 0", DispatcherConfig{RunID: "r1", Cycle: 1, MaxCycles: 0}, 1, "0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := BuildPayload(tc.cfg, Event{Type: EventFlowRepeated}, "id")
			if p.Flow.Cycle != tc.wantCycle || maxCycles(p) != tc.wantMax || p.Flow.PreviousRunID != tc.wantPrevious {
				t.Fatalf("got cycle=%d max=%s previous=%q", p.Flow.Cycle, maxCycles(p), p.Flow.PreviousRunID)
			}
		})
	}
}

func TestBuildPayload_RepeatJSON(t *testing.T) {
	marshal := func(cfg DispatcherConfig) string {
		raw, err := json.Marshal(BuildPayload(cfg, Event{Type: EventFlowFinished}, "id").Flow)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	t.Run("max_cycles 0 is serialized explicitly", func(t *testing.T) {
		got := marshal(DispatcherConfig{RunID: "r1", Cycle: 1, MaxCycles: 0})
		if !strings.Contains(got, `"cycle":1`) || !strings.Contains(got, `"max_cycles":0`) {
			t.Fatalf("flow json: %s", got)
		}
		if strings.Contains(got, "previous_run_id") {
			t.Fatalf("cycle 1 must not carry previous_run_id: %s", got)
		}
	})
	t.Run("cycle 2 serializes previous_run_id", func(t *testing.T) {
		got := marshal(DispatcherConfig{RunID: "r2", Cycle: 2, MaxCycles: 3, PreviousRunID: "r1"})
		if !strings.Contains(got, `"previous_run_id":"r1"`) {
			t.Fatalf("flow json: %s", got)
		}
	})
	t.Run("outside repeat has no repeat fields", func(t *testing.T) {
		got := marshal(DispatcherConfig{RunID: "r1"})
		for _, key := range []string{"cycle", "max_cycles", "previous_run_id"} {
			if strings.Contains(got, key) {
				t.Fatalf("%s must be absent outside repeat: %s", key, got)
			}
		}
	})
}

// Usage-limit wait: stage events stage_awaiting_quota / stage_quota_reset are public, stage-scoped catalog entries
// (a stage hook may subscribe to them; `events: all` includes them).
func TestAllEventsCatalog_IncludesQuotaEvents(t *testing.T) {
	want := map[EventType]string{
		EventStageAwaitingQuota: "stage_awaiting_quota",
		EventStageQuotaReset:    "stage_quota_reset",
	}
	in := map[EventType]bool{}
	for _, ev := range AllEvents() {
		in[ev] = true
	}
	for ev, name := range want {
		if string(ev) != name {
			t.Errorf("event name = %q, want %q", ev, name)
		}
		if !in[ev] {
			t.Errorf("%s missing from the catalog (events: all would skip it)", name)
		}
		if IsFlowEvent(ev) {
			t.Errorf("%s must be a stage event", name)
		}
	}
}

func sampleAnalytics() StageAnalytics {
	return StageAnalytics{
		Version:       AnalyticsVersion,
		AsOf:          time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		StageEventSeq: 420,
		UsageRevision: 34,
		JobsRevision:  68,
		Scope:         AnalyticsScopeStageCumulative,
		Timing: AnalyticsTiming{
			TotalMS: Known[int64](684000), WorkMS: Known[int64](512000), WaitingMS: Known[int64](112000),
			PausedMS: Known[int64](60000), ApprovalMS: Known[int64](47000), HelpersMS: Known[int64](65000),
			BackoffMS: Known[int64](0), SchedulingMS: Known[int64](0), Complete: true,
		},
		AgentJobs: AnalyticsAgentJobs{
			Count: Known(29), Active: Known(0), Measured: Known(29), UnknownTime: Known(0),
			DurationMS: Known[int64](1272000), Complete: true,
		},
		Usage: AnalyticsUsage{
			Health: "ok", Coverage: "full",
			MeteredJobs: Known(29), UnmeteredJobs: Known(0), PendingJobs: Known(0),
			Tokens: Known(AnalyticsTokens{
				UncachedInput: 210000, CacheRead: 950000, CacheWrite5m: 28000, Output: 52000, Total: 1240000,
			}),
		},
	}
}

func TestBuildPayload_AnalyticsInData(t *testing.T) {
	cfg := DispatcherConfig{FlowName: "release", RunID: "run-1", RunDir: "/r", RootDir: "/p"}
	snap := sampleAnalytics()
	ev := Event{Type: EventStageFinished, StageID: "build", StageName: "Build", From: "running", To: "done", Reason: "ok", Seq: 420, Time: snap.AsOf, Analytics: &snap}

	p := BuildPayload(cfg, ev, "run-1:transition:420:stage_finished")
	if p.SchemaVersion != 1 || p.Event != EventStageFinished || p.Stage == nil || p.Stage.ID != "build" || p.Reason != "ok" {
		t.Fatalf("envelope must stay unchanged next to analytics: %+v", p)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	a := decoded["data"].(map[string]any)["analytics"].(map[string]any)
	if a["version"] != float64(1) || a["scope"] != "stage_cumulative" || a["stage_event_seq"] != float64(420) || a["as_of"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("analytics header: %s", raw)
	}
	timing := a["timing"].(map[string]any)
	complete, _ := timing["complete"].(bool)
	if timing["total_ms"] != float64(684000) || timing["backoff_ms"] != float64(0) || !complete {
		t.Fatalf("timing: %s", raw)
	}
	if _, has := timing["reason"]; has {
		t.Fatalf("complete timing must not carry a reason: %s", raw)
	}
	tokens := a["usage"].(map[string]any)["tokens"].(map[string]any)
	if tokens["total"] != float64(1240000) || tokens["cache_write_1h"] != float64(0) {
		t.Fatalf("tokens: %s", raw)
	}
}

func TestBuildPayload_NoAnalyticsKeepsDataAbsent(t *testing.T) {
	p := BuildPayload(DispatcherConfig{RunID: "run-1"}, Event{Type: EventStageFinished, StageID: "build"}, "id")
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"data"`) {
		t.Fatalf("payload without analytics must not grow a data block: %s", raw)
	}
}

func TestStageAnalytics_UnknownIsNullNotZero(t *testing.T) {
	snap := StageAnalytics{
		Version: AnalyticsVersion,
		Timing:  AnalyticsTiming{Reason: "stage_advanced_before_snapshot"},
		AgentJobs: AnalyticsAgentJobs{
			Count: Known(0), Active: Known(0), Measured: Known(0), UnknownTime: Known(0), DurationMS: Known[int64](0), Complete: true,
		},
		Usage: AnalyticsUsage{Health: "unavailable", Coverage: "none", Reason: "usage_store_unavailable"},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"total_ms":null`, `"work_ms":null`, `"complete":false`, `"reason":"stage_advanced_before_snapshot"`,
		`"count":0`, `"duration_ms":0`, `"tokens":null`, `"metered_jobs":null`, `"reason":"usage_store_unavailable"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// Hook payloads never carry money, whatever accounting.show_money says: the DTO has no place for it.
func TestStageAnalytics_HasNoMoneyKeys(t *testing.T) {
	raw, err := json.Marshal(sampleAnalytics())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		for key, child := range obj {
			for _, banned := range []string{"cost", "usd", "price", "money"} {
				if strings.Contains(strings.ToLower(key), banned) {
					t.Errorf("money key %s.%s in hook analytics", path, key)
				}
			}
			walk(path+"."+key, child)
		}
	}
	walk("analytics", tree)
}
