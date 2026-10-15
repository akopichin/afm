package assistantstest

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock никогда не ждёт по-настоящему: Sleep возвращается немедленно,
// учитывая только уже наступившую отмену контекста. Используется ВСЕМИ
// тестами этого файла, чтобы ни один из них не зависел от wall-clock, даже
// если сценарий просит многочасовую задержку.
type fakeClock struct{}

func (fakeClock) Sleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

type recordedAction struct{ tool, detail string }

func TestScriptedRunner_EmitsActionsInOrder(t *testing.T) {
	r := NewScriptedRunner(
		Step{Action: &Action{Tool: "Bash", Detail: "ls -la"}},
		Step{Action: &Action{Tool: "Read", Detail: "main.go"}},
		Step{Result: "done"},
	)
	r.Clock = fakeClock{}

	var got []recordedAction
	r.OnAction = func(tool, detail string) {
		got = append(got, recordedAction{tool, detail})
	}

	if err := r.RunAgent(context.Background(), "claude", "stage-1", "prompt", "log.txt"); err != nil {
		t.Fatalf("RunAgent err = %v", err)
	}

	want := []recordedAction{
		{"Bash", "ls -la"},
		{"Read", "main.go"},
		{toolText, "done"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d actions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("action[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestScriptedRunner_RespectsContextCancel(t *testing.T) {
	r := NewScriptedRunner(
		Step{Action: &Action{Tool: "Bash", Detail: "step-1"}},
		Step{Action: &Action{Tool: "Bash", Detail: "step-2"}},
	)
	r.Clock = fakeClock{}

	ctx, cancel := context.WithCancel(context.Background())
	var got []recordedAction
	r.OnAction = func(tool, detail string) {
		got = append(got, recordedAction{tool, detail})
		cancel() // отменяем между первым и вторым шагом
	}

	err := r.RunAgent(ctx, "claude", "stage-1", "prompt", "log.txt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAgent err = %v, want context.Canceled", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d actions, want 1 (tail must not be published): %+v", len(got), got)
	}
	if got[0].detail != "step-1" {
		t.Fatalf("got[0] = %+v, want step-1", got[0])
	}
}

func TestScriptedRunner_DeterministicAcrossRuns(t *testing.T) {
	steps := []Step{
		{Delay: time.Hour, Action: &Action{Tool: "Bash", Detail: "slow-step"}},
		{Result: "final answer"},
	}

	run := func() ([]recordedAction, error) {
		r := NewScriptedRunner(steps...)
		r.Clock = fakeClock{}
		var got []recordedAction
		r.OnAction = func(tool, detail string) {
			got = append(got, recordedAction{tool, detail})
		}
		err := r.RunAgent(context.Background(), "claude", "stage-1", "prompt", "log.txt")
		return got, err
	}

	start := time.Now()
	got1, err1 := run()
	got2, err2 := run()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("run took %s, want near-instant (fake clock must not sleep for real)", elapsed)
	}
	if err1 != nil || err2 != nil {
		t.Fatalf("RunAgent errs = %v, %v, want nil, nil", err1, err2)
	}
	if len(got1) != len(got2) {
		t.Fatalf("run1 = %+v, run2 = %+v: different lengths", got1, got2)
	}
	for i := range got1 {
		if got1[i] != got2[i] {
			t.Fatalf("run1[%d] = %+v, run2[%d] = %+v: diverged", i, got1[i], i, got2[i])
		}
	}
}

func TestScriptedRunner_Fails(t *testing.T) {
	sentinel := errors.New("boom")
	r := NewScriptedRunner(
		Step{Action: &Action{Tool: "Bash", Detail: "step-1"}},
		Step{Fail: sentinel},
		Step{Action: &Action{Tool: "Bash", Detail: "never-reached"}},
	)
	r.Clock = fakeClock{}

	var got []recordedAction
	r.OnAction = func(tool, detail string) {
		got = append(got, recordedAction{tool, detail})
	}

	err := r.RunAgent(context.Background(), "claude", "stage-1", "prompt", "log.txt")
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunAgent err = %v, want sentinel %v", err, sentinel)
	}
	if len(got) != 1 {
		t.Fatalf("got %d actions, want 1 (step after Fail must not be published): %+v", len(got), got)
	}
}
