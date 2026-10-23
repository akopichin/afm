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
	if len(got) != 28 {
		t.Errorf("catalog size: got %d want 28 (%v)", len(got), AllEvents())
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
