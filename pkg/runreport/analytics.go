package runreport

import (
	"fmt"
	"time"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/pkg/usageview"
)

// Analytics is the agent-job view of the run, counted in jobs. It is the same projection the dashboard's
// Models & agents shows, without paging or truncation.
type Analytics struct {
	Health          usageview.Health       `json:"health"`
	Completeness    usageview.Completeness `json:"completeness"`
	Totals          usageview.Metrics      `json:"totals"`
	OutsideRunTotal *usageview.Metrics     `json:"outside_run_total,omitempty"`
	Catalog         usageview.Catalog      `json:"catalog"`
	Models          []usageview.Row        `json:"models"`
	Agents          []usageview.Row        `json:"agents"`
	Jobs            []Job                  `json:"jobs"`
	Timeline        usageview.Timeline     `json:"timeline"`
}

// Liveness values of a job that has no recorded end.
const livenessUnknown = "unknown"

// Job is a job of the shared projection. A job without a finished record keeps the status the facts gave it;
// EndRecorded and Liveness say that nothing offline can tell whether it is still alive.
type Job struct {
	usageview.Job
	EndRecorded *bool  `json:"end_recorded,omitempty"`
	Liveness    string `json:"liveness,omitempty"`
}

func buildAnalytics(src *Sources, mode usageview.Mode, stageID string) (*Analytics, []Diagnostic) {
	facts := buildFacts(src)
	filter := accounting.Filter{Scope: accounting.FilterAll}
	if stageID != "" {
		filter = accounting.Filter{Scope: accounting.FilterStage, StageID: stageID}
	}
	sel := facts.Select(filter, src.AsOf)

	items := sel.Jobs(accounting.SortStarted, false)
	sortKey := rowSortKey(mode)
	out := &Analytics{
		Health:       usageview.NewHealth(facts.Health()).ForMode(mode),
		Completeness: usageview.NewCompleteness(sel.Completeness()).ForMode(mode),
		Totals:       usageview.NewMetrics(sel.Totals()).ForMode(mode),
		Catalog:      usageview.NewCatalog(facts.Catalog()),
		Models:       usageview.RowsForMode(usageview.NewRows(sel.Rows(accounting.GroupModel, sortKey, true)), mode),
		Agents:       usageview.RowsForMode(usageview.NewRows(sel.Rows(accounting.GroupAgent, sortKey, true)), mode),
		Timeline:     usageview.NewTimeline(sel.Timeline(max(len(items), 1))),
	}
	if outside := sel.Outside(); outside != nil {
		m := usageview.NewMetrics(*outside).ForMode(mode)
		out.OutsideRunTotal = &m
	}

	jobs := make([]usageview.Job, len(items))
	for i, item := range items {
		jobs[i] = usageview.NewJob(item)
	}
	openJobs := 0
	for _, job := range usageview.JobsForMode(jobs, mode) {
		item := Job{Job: job}
		if job.Status == usageview.JobInterrupted && job.DurationMS == nil {
			recorded := false
			item.EndRecorded, item.Liveness = &recorded, livenessUnknown
			openJobs++
		}
		out.Jobs = append(out.Jobs, item)
	}
	if out.Jobs == nil {
		out.Jobs = []Job{}
	}

	var diags []Diagnostic
	if openJobs > 0 {
		diags = append(diags, Diagnostic{
			Code: "open_jobs_without_end",
			Message: fmt.Sprintf("%d job(s) have no recorded end at %s: the run may still be working or was interrupted; their time is unknown and not summed",
				openJobs, src.AsOf.Format(time.RFC3339)),
		})
	}
	return out, diags
}

// rowSortKey orders rows by the most telling metric the mode has, as the dashboard does.
func rowSortKey(mode usageview.Mode) accounting.SortKey {
	switch {
	case mode.HasMoney():
		return accounting.SortCost
	case mode.HasTokens():
		return accounting.SortTokens
	default:
		return accounting.SortTime
	}
}
