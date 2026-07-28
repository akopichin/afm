package flow

import (
	"slices"
	"testing"
)

func TestIsValidPhase(t *testing.T) {
	for _, ok := range []string{"planning", "implementation", "review", "autonomous_execution"} {
		if !IsValidPhase(ok) {
			t.Errorf("IsValidPhase(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "commit-changes", "Planning", "autonomous", "plan"} {
		if IsValidPhase(bad) {
			t.Errorf("IsValidPhase(%q) = true, want false", bad)
		}
	}
}

func TestPhaseValues(t *testing.T) {
	// Значения фаз — часть контракта имён файлов на диске; зафиксировать.
	if PhasePlanning != "planning" || PhaseImplementation != "implementation" ||
		PhaseReview != "review" || PhaseAutonomous != "autonomous_execution" {
		t.Fatalf("phase constant values changed: %q %q %q %q",
			PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous)
	}
	if got := Phases(); !slices.Equal(got, []Phase{PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous}) {
		t.Fatalf("Phases() = %v", got)
	}
}

func TestPhaseJSONL(t *testing.T) {
	cases := map[Phase]string{
		PhasePlanning:       "planning.jsonl",
		PhaseImplementation: "implementation.jsonl",
		PhaseReview:         "review.jsonl",
		PhaseAutonomous:     "autonomous.jsonl", // НЕ autonomous_execution.jsonl
	}
	for p, want := range cases {
		if got := PhaseJSONL(p); got != want {
			t.Errorf("PhaseJSONL(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestPhaseStreamLogs(t *testing.T) {
	if got := PhaseStreamLogs(PhasePlanning); !slices.Equal(got,
		[]string{"planning.jsonl", "planning-reprompt.jsonl", "planning-revision.jsonl"}) {
		t.Errorf("PhaseStreamLogs(planning) = %v", got)
	}
	if got := PhaseStreamLogs(PhaseAutonomous); !slices.Equal(got, []string{"autonomous.jsonl"}) {
		t.Errorf("PhaseStreamLogs(autonomous) = %v", got)
	}
}

func TestPhaseLogFile(t *testing.T) {
	cases := map[Phase]string{
		PhasePlanning:       "planning.log",
		PhaseImplementation: "implementation.log",
		PhaseReview:         "review.log",
		PhaseAutonomous:     "autonomous.log", // НЕ autonomous_execution.log
	}
	for p, want := range cases {
		if got := PhaseLogFile(p); got != want {
			t.Errorf("PhaseLogFile(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestPhaseLogFiles(t *testing.T) {
	cases := map[Phase][]string{
		PhasePlanning:       {"planning.log", "planning-reprompt.log", "planning-revision.log"},
		PhaseImplementation: {"implementation.log", "implementation-feedback.log"},
		PhaseReview:         {"review.log", "review-feedback.log"},
		PhaseAutonomous:     {"autonomous.log", "autonomous-feedback.log"},
	}
	for p, want := range cases {
		if got := PhaseLogFiles(p); !slices.Equal(got, want) {
			t.Errorf("PhaseLogFiles(%q) = %v, want %v", p, got, want)
		}
	}
}

// AgentType (YAML-агенты) НЕ должен включать autonomous_execution.
func TestAgentTypeExcludesAutonomous(t *testing.T) {
	if AgentType("autonomous_execution") == AgentPlanning ||
		AgentType("autonomous_execution") == AgentImplementation ||
		AgentType("autonomous_execution") == AgentReview {
		t.Fatal("autonomous_execution must not equal any YAML AgentType")
	}
}
