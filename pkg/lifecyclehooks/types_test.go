package lifecyclehooks

import (
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
	if len(got) != 27 {
		t.Errorf("catalog size: got %d want 27 (%v)", len(got), AllEvents())
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
