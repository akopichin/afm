package lifecyclehooks

import "testing"

func TestValidateLayer(t *testing.T) {
	valid := Hook{ID: "n", Events: EventSelector{Events: []EventType{EventStageFailed}}, Command: "true"}
	cases := []struct {
		name    string
		defs    []Hook
		stage   bool
		wantErr string
	}{
		{"ok", []Hook{valid}, false, ""},
		{"ok stage-scoped", []Hook{{ID: "s", Events: EventSelector{All: true}, Command: "true"}}, true, ""},
		{"empty id", []Hook{{ID: "", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"empty command", []Hook{{ID: "x", Events: EventSelector{All: true}, Command: ""}}, false, "command"},
		{"no events", []Hook{{ID: "x", Command: "true"}}, false, "events"},
		{"unknown event", []Hook{{ID: "x", Events: EventSelector{Events: []EventType{"stage_strted"}}, Command: "true"}}, false, "unknown"},
		{"unknown skip", []Hook{{ID: "x", Events: EventSelector{All: true}, SkipEvents: []EventType{"nope"}, Command: "true"}}, false, "unknown"},
		{"dup id in layer", []Hook{valid, {ID: "n", Events: EventSelector{All: true}, Command: "true"}}, false, "duplicate"},
		{"flow event in stage hook", []Hook{{ID: "x", Events: EventSelector{Events: []EventType{EventFlowFinished}}, Command: "true"}}, true, "flow-level"},
		{"flow event ok in flow layer", []Hook{{ID: "x", Events: EventSelector{Events: []EventType{EventFlowFinished}}, Command: "true"}}, false, ""},
		{"negative retries", []Hook{{ID: "x", Events: EventSelector{All: true}, Command: "true", Retries: -1}}, false, "retries"},
		{"negative timeout", []Hook{{ID: "x", Events: EventSelector{All: true}, Command: "true", Timeout: -1}}, false, "timeout"},
		// id становится именем файла лога — path traversal запрещён (codex CRIT#1):
		{"id traversal", []Hook{{ID: "../events.jsonl", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"id with slash", []Hook{{ID: "a/b", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"id dot", []Hook{{ID: ".", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"id dotdot", []Hook{{ID: "..", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLayer(tc.defs, tc.stage)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestMatches(t *testing.T) {
	all := Hook{ID: "a", Events: EventSelector{All: true}, Command: "true"}
	allSkip := Hook{ID: "b", Events: EventSelector{All: true}, SkipEvents: []EventType{EventStageQuestionAnswered}, Command: "true"}
	list := Hook{ID: "c", Events: EventSelector{Events: []EventType{EventStageFailed, EventFlowFinished}}, Command: "true"}
	empty := Hook{ID: "d", Command: "true"}

	if !all.Matches(EventStageFailed) || !all.Matches(EventFlowStarted) {
		t.Error("all must match everything")
	}
	if allSkip.Matches(EventStageQuestionAnswered) {
		t.Error("skip_events must exclude")
	}
	if !allSkip.Matches(EventStageFailed) {
		t.Error("skip must not exclude others")
	}
	if list.Matches(EventStageFinished) {
		t.Error("list must not match unlisted")
	}
	if !list.Matches(EventFlowFinished) || !list.Matches(EventStageFailed) {
		t.Error("list must match listed")
	}
	if empty.Matches(EventStageFailed) {
		t.Error("empty selector matches nothing")
	}
}

func TestRegisteredHookScope(t *testing.T) {
	global := RegisteredHook{Hook: Hook{Events: EventSelector{All: true}}, StageID: ""}
	scoped := RegisteredHook{Hook: Hook{Events: EventSelector{All: true}}, StageID: "deploy"}
	evOther := Event{Type: EventStageFailed, StageID: "build"}
	evOwn := Event{Type: EventStageFailed, StageID: "deploy"}

	if !global.MatchesEvent(evOther) || !global.MatchesEvent(evOwn) {
		t.Error("global scope matches all stages")
	}
	if scoped.MatchesEvent(evOther) {
		t.Error("stage scope must not match other stages")
	}
	if !scoped.MatchesEvent(evOwn) {
		t.Error("stage scope must match own stage")
	}
}
