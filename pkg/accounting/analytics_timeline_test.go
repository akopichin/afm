package accounting

import (
	"slices"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentjobs"
)

func TestAnalyticsTimeline_PlacedAndUnplacedJobs(t *testing.T) {
	tl := BuildFacts(anFullRun()).Select(Filter{}, anAsOf()).Timeline(2000)

	var ids []string
	for _, iv := range tl.Intervals {
		ids = append(ids, iv.JobID)
	}
	if !slices.Equal(ids, []string{"j1", "j2", "j3", "j4", "j5"}) {
		t.Fatalf("intervals = %v", ids)
	}
	// j7 has no duration, the Assistant job no offset: both are counted, neither is drawn.
	if tl.Total != 5 || tl.Unplaced != 2 || tl.Truncated || tl.Limit != 2000 {
		t.Fatalf("timeline = %+v", tl)
	}
	if tl.RangeStartMS != 0 || tl.RangeEndMS != 720000 {
		t.Fatalf("range = %d..%d", tl.RangeStartMS, tl.RangeEndMS)
	}
	j5 := tl.Intervals[4]
	if j5.Status != "running" || j5.DurationMS != 240000 || j5.RunOffsetMS != 480000 || j5.ModelKey != "anthropic/glm-5.3" || j5.StageID != "review" || j5.AgentID != "stage:review" {
		t.Fatalf("running interval = %+v", j5)
	}
	if tl.Intervals[2].ModelKey != "codex/gpt-5.6-sol" || tl.Intervals[2].Title != "Review the diff" {
		t.Fatalf("helper interval = %+v", tl.Intervals[2])
	}
}

func TestAnalyticsTimeline_IsBoundedAndSaysSo(t *testing.T) {
	tl := BuildFacts(anFullRun()).Select(Filter{}, anAsOf()).Timeline(2)

	if len(tl.Intervals) != 2 || tl.Total != 5 || !tl.Truncated || tl.Limit != 2 {
		t.Fatalf("timeline = %+v", tl)
	}
	if tl.Intervals[0].JobID != "j1" || tl.Intervals[1].JobID != "j2" {
		t.Fatalf("a cut keeps the earliest intervals: %+v", tl.Intervals)
	}
	if tl.RangeEndMS != 430000 {
		t.Fatalf("range covers only what is returned: %d", tl.RangeEndMS)
	}
}

func TestAnalyticsTimeline_FollowsTheFilter(t *testing.T) {
	tl := BuildFacts(anFullRun()).Select(Filter{Scope: FilterStage, StageID: "review"}, anAsOf()).Timeline(2000)

	if len(tl.Intervals) != 2 || tl.Intervals[0].JobID != "j4" || tl.RangeStartMS != 400000 {
		t.Fatalf("timeline = %+v", tl)
	}
}

func TestAnalyticsTimeline_EmptyRunHasEmptyRange(t *testing.T) {
	tl := BuildFacts(AnalyticsInput{UsageState: UsageStateOK, Jobs: newAnJournal(anT0).snapshot(), Meta: anMeta()}).
		Select(Filter{}, anAsOf()).Timeline(2000)

	if len(tl.Intervals) != 0 || tl.Total != 0 || tl.RangeStartMS != 0 || tl.RangeEndMS != 0 || tl.Unplaced != 0 {
		t.Fatalf("timeline = %+v", tl)
	}
	if tl.Intervals == nil {
		t.Fatal("intervals must encode as [], not null")
	}
}

func TestAnalyticsTimeline_JobOverlappingTheNextProcessSegmentIsNotPlaced(t *testing.T) {
	// First process: a long job and a short one. The run resumed at active time 300000, so a job whose
	// recorded end passes that point belongs to a clock the later segment replaced.
	j := newAnJournal(anT0)
	meta := func(id string) agentjobs.JobMeta {
		return agentjobs.JobMeta{JobID: id, AgentID: "stage:build", AgentKind: AgentKindStage, StageID: "build", RootStageID: "build", Title: id}
	}
	j.startInSegment(meta("long"), anT0.Add(100*time.Second), 0, 100000)
	j.finish("long", agentjobs.OutcomeOK, anT0.Add(30*time.Minute), 500*time.Second) // finished after the crash
	j.startInSegment(meta("short"), anT0, 0, 0)
	j.finish("short", agentjobs.OutcomeOK, anT0.Add(200*time.Second), 200*time.Second)
	j.startInSegment(meta("resumed"), anT0.Add(time.Hour), 300000, 310000)
	j.finish("resumed", agentjobs.OutcomeOK, anT0.Add(time.Hour+60*time.Second), 60*time.Second)

	in := AnalyticsInput{UsageState: UsageStateOK, Jobs: j.snapshot(), Meta: anMeta()}
	in.Meta.LiveSince = anT0.Add(time.Hour)
	sel := BuildFacts(in).Select(Filter{}, anT0.Add(2*time.Hour))

	items := sel.Jobs(SortStarted, false)
	long := anJob(t, items, "long")
	if long.RunOffsetMS != nil {
		t.Fatalf("long overlaps the resumed segment, its offset must be dropped: %+v", long)
	}
	if long.DurationMS == nil || *long.DurationMS != 500000 || !long.StartedAt.Equal(anT0.Add(100*time.Second)) {
		t.Fatalf("UTC facts and duration stay: %+v", long)
	}
	if short := anJob(t, items, "short"); short.RunOffsetMS == nil || *short.RunOffsetMS != 0 {
		t.Fatalf("short = %+v", short)
	}
	if resumed := anJob(t, items, "resumed"); resumed.RunOffsetMS == nil || *resumed.RunOffsetMS != 310000 {
		t.Fatalf("resumed = %+v", resumed)
	}
	tl := sel.Timeline(2000)
	if tl.Total != 2 || tl.Unplaced != 1 {
		t.Fatalf("timeline = %+v", tl)
	}
	// Agent time still counts the dropped job: the duration is measured, only the placement is lost.
	if got := sel.Totals().Time; got.Measured != 3 || got.DurationMS == nil || *got.DurationMS != 760000 {
		t.Fatalf("time = %+v", got)
	}
}

func TestAnalyticsTimeline_JobsWithoutSegmentHaveNoOffset(t *testing.T) {
	j := newAnJournal(anT0)
	j.start(agentjobs.JobMeta{JobID: "a", AgentID: "side:r", AgentKind: AgentKindAssistant, Title: "a"}, anT0, -1)
	j.finish("a", agentjobs.OutcomeOK, anT0.Add(time.Second), time.Second)
	sel := BuildFacts(AnalyticsInput{UsageState: UsageStateOK, Jobs: j.snapshot(), Meta: anMeta()}).Select(Filter{}, anAsOf())

	if got := sel.Jobs(SortStarted, true)[0]; got.RunOffsetMS != nil {
		t.Fatalf("job = %+v", got)
	}
}
