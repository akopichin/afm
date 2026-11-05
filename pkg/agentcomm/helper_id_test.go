package agentcomm

import "testing"

func TestHelperParticipantID_RoundTrip(t *testing.T) {
	id := HelperParticipantID("t-1")
	if id != "helper:t-1" {
		t.Fatalf("HelperParticipantID(t-1) = %q, want helper:t-1", id)
	}
	taskID, ok := HelperTaskID(id)
	if !ok || taskID != "t-1" {
		t.Fatalf("HelperTaskID(%q) = (%q, %v), want (t-1, true)", id, taskID, ok)
	}
}

// Only a "helper:<task>" id names a helper task: a run agent keyed by the same task id, another kind of
// participant or a bare prefix does not.
func TestHelperTaskID_RejectsOtherParticipants(t *testing.T) {
	for _, id := range []ParticipantID{"run:t-1", "stage:impl", UserParticipantID, "side:run1", "helper:", "t-1", ""} {
		if taskID, ok := HelperTaskID(id); ok {
			t.Errorf("HelperTaskID(%q) = (%q, true), want not a helper", id, taskID)
		}
	}
}
