package agentjobs

import (
	"reflect"
	"testing"
	"time"
)

func snapshotWith(status HealthStatus, reasons ...string) Snapshot {
	return Snapshot{
		Header: Header{Type: TypeHeader, RecordVersion: RecordVersion, RecordingStartedAt: testStart},
		Health: Health{Status: status, Reasons: reasons},
	}
}

func TestCoverageGaps(t *testing.T) {
	afterStart := testStart.Add(time.Minute)
	beforeStart := testStart.Add(-time.Minute)

	tests := []struct {
		name         string
		snap         Snapshot
		stageStart   time.Time
		usageHealthy bool
		want         []string
	}{
		{"fresh stage on healthy stores is proven", snapshotWith(HealthOK), afterStart, true, nil},
		{"stage starting exactly at recording start is proven", snapshotWith(HealthOK), testStart, true, nil},
		{"stage that never started has nothing to disprove", snapshotWith(HealthOK), time.Time{}, true, nil},
		{"run without journal is legacy", snapshotWith(HealthUnavailable, ReasonNoJournal), afterStart, true, []string{GapLegacyJournal}},
		{"stage started before recording", snapshotWith(HealthOK), beforeStart, true, []string{GapStartedBeforeRecording}},
		{"unavailable jobs store", snapshotWith(HealthUnavailable, ReasonIOFailed), afterStart, true, []string{GapJobsStoreUnavailable}},
		{"degraded jobs store is not proof", snapshotWith(HealthDegraded, ReasonCorruptLine), afterStart, true, []string{GapJobsStoreUnavailable}},
		{"unavailable usage store", snapshotWith(HealthOK), afterStart, false, []string{GapUsageStoreUnavailable}},
		{
			"all gaps together keep a stable order",
			snapshotWith(HealthDegraded, ReasonConflictingRecord), beforeStart, false,
			[]string{GapJobsStoreUnavailable, GapStartedBeforeRecording, GapUsageStoreUnavailable},
		},
		{
			"legacy run does not also claim started before recording",
			snapshotWith(HealthUnavailable, ReasonNoJournal), beforeStart, false,
			[]string{GapLegacyJournal, GapUsageStoreUnavailable},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CoverageGaps(tt.snap, tt.stageStart, tt.usageHealthy)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("CoverageGaps = %v, want %v", got, tt.want)
			}
		})
	}
}
