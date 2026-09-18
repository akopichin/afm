package lifecyclehooks

import "testing"

func hook(id string) Hook { return Hook{ID: id, Events: EventSelector{All: true}, Command: "true"} }

func TestCombine_LayersAppend(t *testing.T) {
	out := Combine(
		Layer{Hooks: []Hook{hook("a"), hook("b")}},
		Layer{Hooks: []Hook{hook("c")}},
	)
	if len(out) != 3 || out[0].Hook.ID != "a" || out[1].Hook.ID != "b" || out[2].Hook.ID != "c" {
		t.Fatalf("got %+v", out)
	}
	if out[0].StageID != "" || out[2].StageID != "" {
		t.Fatalf("no stage scope expected: %+v", out)
	}
}

func TestCombine_LaterLayerOverridesByID(t *testing.T) {
	replacement := Hook{ID: "a", Events: EventSelector{Events: []EventType{EventFlowFailed}}, Command: "other"}
	out := Combine(
		Layer{Hooks: []Hook{hook("a"), hook("b")}},
		Layer{Hooks: []Hook{replacement}},
	)
	if len(out) != 2 {
		t.Fatalf("len: %+v", out)
	}
	if out[0].Hook.Command != "other" || out[0].Hook.Events.All {
		t.Fatalf("override not applied: %+v", out[0])
	}
	if out[1].Hook.ID != "b" {
		t.Fatalf("order broken: %+v", out)
	}
}

func TestCombine_StageScopeOverridesGlobal(t *testing.T) {
	out := Combine(
		Layer{Hooks: []Hook{hook("a")}},                    // global
		Layer{StageID: "deploy", Hooks: []Hook{hook("a")}}, // stage заменяет global целиком
	)
	if len(out) != 1 {
		t.Fatalf("len: %+v", out)
	}
	if out[0].StageID != "deploy" {
		t.Fatalf("stage replacement must change scope: %+v", out[0])
	}
}

func TestCombine_Empty(t *testing.T) {
	if out := Combine(); out != nil {
		t.Fatalf("got %+v", out)
	}
	if out := Combine(Layer{}); out != nil {
		t.Fatalf("got %+v", out)
	}
}
