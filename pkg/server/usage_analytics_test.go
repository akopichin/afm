package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentjobs"
)

var usageT0 = time.Date(2026, 10, 31, 11, 0, 0, 0, time.UTC)

// usageSource is a mutable live source: tests change the input and bump a revision like the stores do.
type usageSource struct {
	mu sync.Mutex
	in accounting.AnalyticsInput
}

func (s *usageSource) Facts() *accounting.Facts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return accounting.BuildFacts(s.in)
}

func (s *usageSource) edit(change func(in *accounting.AnalyticsInput)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.in)
}

func usageStarted(id string, meta agentjobs.JobMeta, at time.Time, offsetMS int64) agentjobs.Record {
	meta.JobID = id
	return agentjobs.Record{
		Type: agentjobs.TypeStarted, JobMeta: meta, At: at,
		Segment: agentjobs.Segment{RunStartedAt: usageT0, OffsetMS: offsetMS},
	}
}

func usageFinished(id string, at time.Time, d time.Duration) agentjobs.Record {
	return agentjobs.Record{
		Type: agentjobs.TypeFinished, JobMeta: agentjobs.JobMeta{JobID: id}, At: at,
		Outcome: agentjobs.OutcomeOK, DurationMS: d.Milliseconds(),
	}
}

func usageRecord(jobID, stage, channel, model string, output uint64, cost float64) accounting.UsageRecord {
	return accounting.UsageRecord{
		RecordVersion: 1, StageID: stage, Phase: "implementation", Channel: channel, Model: model,
		JobID: jobID, ModelSource: accounting.ModelSourceObserved, Metered: true, Priced: cost > 0,
		Tokens: accounting.Tokens{UncachedInput: 100, Output: output}, EstimatedCostUSD: cost,
		StartedAt: usageT0.Format(time.RFC3339), FinishedAt: usageT0.Format(time.RFC3339),
	}
}

func stageJobMeta(stage string) agentjobs.JobMeta {
	return agentjobs.JobMeta{AgentID: "stage:" + stage, AgentKind: accounting.AgentKindStage, StageID: stage, RootStageID: stage}
}

// newUsageSource is a small run: two stages, a helper, a verify step, a running job, a job interrupted by
// a crash and an Assistant job outside the run total.
func newUsageSource() *usageSource {
	var records []agentjobs.Record
	records = append(records,
		usageStarted("j1", stageJobMeta("build"), usageT0, 0), usageFinished("j1", usageT0.Add(120*time.Second), 120*time.Second),
		usageStarted("j2", stageJobMeta("build"), usageT0.Add(130*time.Second), 130000), usageFinished("j2", usageT0.Add(430*time.Second), 300*time.Second),
		usageStarted("j3", agentjobs.JobMeta{AgentID: "helper:t-1", AgentKind: accounting.AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: "t-1"},
			usageT0.Add(200*time.Second), 200000), usageFinished("j3", usageT0.Add(290*time.Second), 90*time.Second),
		usageStarted("j4", agentjobs.JobMeta{AgentID: "verify:review#0", AgentKind: accounting.AgentKindVerify, StageID: "review", RootStageID: "review"},
			usageT0.Add(400*time.Second), 400000), usageFinished("j4", usageT0.Add(460*time.Second), 60*time.Second),
		usageStarted("j5", stageJobMeta("review"), usageT0.Add(480*time.Second), 480000),
		usageStarted("j7", stageJobMeta("build"), usageT0.Add(420*time.Second), 420000),
		usageStarted("j6", agentjobs.JobMeta{AgentID: "side:run-1", AgentKind: accounting.AgentKindAssistant}, usageT0.Add(500*time.Second), 0),
		usageFinished("j6", usageT0.Add(515*time.Second), 15*time.Second),
	)
	return &usageSource{in: accounting.AnalyticsInput{
		Usage: []accounting.UsageRecord{
			usageRecord("j1", "build", "anthropic", "claude-opus-4-8", 600, 0.42),
			usageRecord("j2", "build", "anthropic", "claude-opus-4-8", 2000, 1.35),
			usageRecord("j3", "build", "codex", "gpt-5.6-sol", 1000, 0),
			usageRecord("j4", "review", "codex", "gpt-5.6-sol", 500, 0.15),
		},
		UsageRevision: 7,
		UsageState:    accounting.UsageStateOK,
		Jobs: agentjobs.Snapshot{
			Header:   agentjobs.Header{Type: agentjobs.TypeHeader, RecordVersion: 1, RecordingStartedAt: usageT0},
			Records:  records,
			Revision: uint64(len(records)),
			Health:   agentjobs.Health{Status: agentjobs.HealthOK},
		},
		Meta: accounting.AnalyticsMeta{
			Revision:  3,
			LiveSince: usageT0.Add(470 * time.Second),
			Stages: []accounting.StageMeta{
				{ID: "build", Title: "Build", StartedAt: usageT0},
				{ID: "review", Title: "Review", StartedAt: usageT0.Add(400 * time.Second)},
			},
			Agents: []accounting.AgentMeta{{ID: "helper:t-1", Kind: accounting.AgentKindHelper, Title: "Reviewer"}},
		},
	}}
}

// addBulkJobs appends n finished jobs of one stage that share start time and duration, so only the
// tie-breaker orders them.
func (s *usageSource) addBulkJobs(n int) {
	s.edit(func(in *accounting.AnalyticsInput) {
		for i := range n {
			id := fmt.Sprintf("k%03d", i)
			in.Jobs.Records = append(in.Jobs.Records,
				usageStarted(id, stageJobMeta("build"), usageT0.Add(10*time.Minute), 600000),
				usageFinished(id, usageT0.Add(11*time.Minute), time.Minute))
		}
		in.Jobs.Revision = uint64(len(in.Jobs.Records))
	})
}

var usageNowAt = usageT0.Add(12 * time.Minute)

func newUsageServer(t *testing.T, src UsageAnalyticsSource, mode UsageAnalyticsMode) *Server {
	t.Helper()
	srv := newTestServer(t, Config{UsageAnalytics: src, UsageMode: mode})
	srv.usageNow = func() time.Time { return usageNowAt }
	return srv
}

func getUsage(t *testing.T, srv *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/analytics?"+query, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func decodeUsage(t *testing.T, w *httptest.ResponseRecorder) UsageAnalytics {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body)
	}
	var r UsageAnalytics
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body)
	}
	return r
}

func usageError(t *testing.T, w *httptest.ResponseRecorder, status int, code UsageErrorCode) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body %s", w.Code, status, w.Body)
	}
	var e UsageAnalyticsError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != code || e.Message == "" {
		t.Fatalf("error body = %s (%v), want code %q with a message", w.Body, err, code)
	}
}

func jobIDsOf(page *UsageJobsPage) []string {
	ids := make([]string, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	return ids
}

func TestUsageAnalytics_DefaultsPerMode(t *testing.T) {
	cases := []struct {
		mode UsageAnalyticsMode
		sort UsageSort
	}{
		{UsageModeFull, UsageSortCost},
		{UsageModeNoMoney, UsageSortTokens},
		{UsageModeTimeOnly, UsageSortTime},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			srv := newUsageServer(t, newUsageSource(), tc.mode)
			r := decodeUsage(t, getUsage(t, srv, ""))
			want := UsageQuery{View: UsageViewBreakdown, Group: UsageGroupModel, Scope: UsageScopeAll, Sort: tc.sort, Dir: UsageDirDesc}
			if r.Query != want {
				t.Errorf("query = %+v, want %+v", r.Query, want)
			}
			if r.View != UsageViewBreakdown || r.Mode != tc.mode || r.Breakdown == nil || r.Jobs != nil || r.Charts != nil {
				t.Errorf("view/mode/data = %s/%s %v %v %v", r.View, r.Mode, r.Breakdown, r.Jobs, r.Charts)
			}
			if !slices.Equal(r.Metrics, tc.mode.Metrics()) {
				t.Errorf("metrics = %v", r.Metrics)
			}
			if !r.AsOf.Equal(usageNowAt) || r.Revision == "" {
				t.Errorf("as_of %v revision %q", r.AsOf, r.Revision)
			}
		})
	}

	srv := newUsageServer(t, newUsageSource(), UsageModeFull)
	jobs := decodeUsage(t, getUsage(t, srv, "view=jobs"))
	if q := jobs.Query; q.Sort != UsageSortStarted || q.Dir != UsageDirDesc || q.Limit != 10 || jobs.Jobs.Limit != 10 {
		t.Errorf("jobs defaults = %+v, page limit %d", q, jobs.Jobs.Limit)
	}
	if got := decodeUsage(t, getUsage(t, srv, "view=breakdown&sort=name")).Query.Dir; got != UsageDirAsc {
		t.Errorf("name sort defaults to dir %q, want asc", got)
	}
}

func TestUsageAnalytics_BreakdownMapsTheReadModel(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), UsageModeFull)
	r := decodeUsage(t, getUsage(t, srv, "group=agent&sort=name&dir=asc"))

	if r.Totals.Jobs != 6 || r.Totals.ActiveJobs != 1 {
		t.Errorf("totals jobs/active = %d/%d, want 6/1 (Assistant is outside)", r.Totals.Jobs, r.Totals.ActiveJobs)
	}
	if got := *r.Totals.Tokens.Counts; got.Output != 4100 || got.UncachedInput != 400 || got.Total != 4500 {
		t.Errorf("token counts = %+v", got)
	}
	if got := *r.Totals.Money.EstimatedCostUSD; got < 1.919 || got > 1.921 {
		t.Errorf("cost = %v, want 1.92", got)
	}
	if r.OutsideRunTotal == nil || r.OutsideRunTotal.Jobs != 1 {
		t.Errorf("outside run total = %+v, want the Assistant job", r.OutsideRunTotal)
	}

	var keys []string
	var assistant *UsageRow
	for i, row := range r.Breakdown.Rows {
		keys = append(keys, row.Key)
		if row.Agent == nil || row.Agent.ID != row.Key {
			t.Errorf("row %s has agent %+v", row.Key, row.Agent)
		}
		if row.Agent.OutsideRunTotal {
			assistant = &r.Breakdown.Rows[i]
		}
	}
	if want := []string{"side:run-1", "stage:build", "stage:review", "helper:t-1", "verify:review#0"}; !slices.Equal(keys, want) {
		t.Errorf("agent rows (label asc: Assistant, Build, Review, Reviewer, Verify) = %v, want %v", keys, want)
	}
	if assistant == nil || assistant.Key != "side:run-1" {
		t.Errorf("the Assistant row is not flagged outside the run total: %+v", assistant)
	}

	catalog := r.Catalog
	if len(catalog.Stages) != 2 || len(catalog.Agents) != 5 || len(catalog.Models) != 2 {
		t.Errorf("catalog = %d stages, %d agents, %d models", len(catalog.Stages), len(catalog.Agents), len(catalog.Models))
	}
	for i, m := range catalog.Models {
		if m.ColorOrdinal == nil || *m.ColorOrdinal != i {
			t.Errorf("model %s color ordinal = %v, want %d", m.Key, m.ColorOrdinal, i)
		}
	}
	if r.Health.Usage != UsageStoreOK || r.Health.Jobs != UsageStoreOK {
		t.Errorf("health = %+v", r.Health)
	}
	if r.Completeness.Tokens == nil || r.Completeness.Price == nil || r.Completeness.Time.Status != UsageCoveragePartial {
		t.Errorf("completeness = %+v", r.Completeness)
	}
}

// A model row says how many agents ran the model; the count stays in every mode and agent rows have none.
func TestUsageAnalytics_ModelRowsCountTheirAgents(t *testing.T) {
	for _, mode := range []UsageAnalyticsMode{UsageModeFull, UsageModeNoMoney, UsageModeTimeOnly} {
		srv := newUsageServer(t, newUsageSource(), mode)
		byModel := decodeUsage(t, getUsage(t, srv, "group=model&sort=name&dir=asc")).Breakdown.Rows
		got := map[string]int{}
		for _, row := range byModel {
			got[row.Key] = row.Agents
		}
		// gpt ran as the helper and as the verify agent; opus only as the build stage.
		if got["anthropic/claude-opus-4-8"] != 1 || got["codex/gpt-5.6-sol"] != 2 {
			t.Errorf("%s: agents per model row = %v", mode, got)
		}
		charts := decodeUsage(t, getUsage(t, srv, "view=charts&group=model")).Charts.Comparison
		for _, row := range charts {
			if row.Agents != got[row.Key] {
				t.Errorf("%s: comparison row %s has %d agents, breakdown %d", mode, row.Key, row.Agents, got[row.Key])
			}
		}
		for _, row := range decodeUsage(t, getUsage(t, srv, "group=agent")).Breakdown.Rows {
			if row.Agents != 0 {
				t.Errorf("%s: agent row %s has an agent count %d", mode, row.Key, row.Agents)
			}
		}
	}
}

func TestUsageAnalytics_FiltersChangeTotalsButNotTheCatalog(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), UsageModeFull)
	all := decodeUsage(t, getUsage(t, srv, ""))
	byStage := decodeUsage(t, getUsage(t, srv, "scope=stage&stage_id=review"))
	byAgent := decodeUsage(t, getUsage(t, srv, "agent=helper:t-1"))
	byModel := decodeUsage(t, getUsage(t, srv, "model="+url.QueryEscape("codex/gpt-5.6-sol")))

	if byStage.Totals.Jobs != 2 || byAgent.Totals.Jobs != 1 || byModel.Totals.Jobs != 2 {
		t.Errorf("filtered jobs: stage %d, agent %d, model %d", byStage.Totals.Jobs, byAgent.Totals.Jobs, byModel.Totals.Jobs)
	}
	for name, r := range map[string]UsageAnalytics{"stage": byStage, "agent": byAgent, "model": byModel} {
		if !reflect.DeepEqual(r.Catalog, all.Catalog) {
			t.Errorf("%s filter changed the catalog", name)
		}
	}
	if byStage.Query.StageID != "review" || byAgent.Query.Agent != "helper:t-1" || byModel.Query.Model != "codex/gpt-5.6-sol" {
		t.Errorf("filters are not echoed: %+v %+v %+v", byStage.Query, byAgent.Query, byModel.Query)
	}
	if none := decodeUsage(t, getUsage(t, srv, "agent=helper:nobody")); none.Totals.Jobs != 0 || none.Breakdown.Rows == nil || len(none.Breakdown.Rows) != 0 {
		t.Errorf("an agent nobody carries must select nothing: %+v", none.Totals)
	}
}

func TestUsageAnalytics_DynamicStageIsFilterable(t *testing.T) {
	src := newUsageSource()
	srv := newUsageServer(t, src, UsageModeFull)
	before := decodeUsage(t, getUsage(t, srv, "scope=stage&stage_id=extra"))
	if before.Totals.Jobs != 0 {
		t.Fatalf("jobs before the stage exists: %d", before.Totals.Jobs)
	}

	src.edit(func(in *accounting.AnalyticsInput) {
		in.Meta.Stages = append(in.Meta.Stages, accounting.StageMeta{ID: "extra", Title: "Added at run time", StartedAt: usageT0.Add(9 * time.Minute)})
		in.Meta.Revision++
		in.Jobs.Records = append(in.Jobs.Records,
			usageStarted("x1", stageJobMeta("extra"), usageT0.Add(9*time.Minute), 540000),
			usageFinished("x1", usageT0.Add(10*time.Minute), time.Minute))
		in.Jobs.Revision++
	})
	after := decodeUsage(t, getUsage(t, srv, "scope=stage&stage_id=extra&view=jobs"))
	if after.Jobs.Total != 1 || after.Jobs.Items[0].ID != "x1" {
		t.Errorf("dynamic stage jobs = %v", jobIDsOf(after.Jobs))
	}
	if after.Revision == before.Revision {
		t.Error("revision did not change with the new stage and job")
	}
	if !slices.ContainsFunc(after.Catalog.Stages, func(s UsageStageRef) bool { return s.ID == "extra" && s.Title == "Added at run time" }) {
		t.Errorf("catalog stages = %v", after.Catalog.Stages)
	}
}

func TestUsageAnalytics_ModesRemoveData(t *testing.T) {
	for _, view := range []string{"breakdown", "jobs", "charts"} {
		t.Run(view, func(t *testing.T) {
			bodies := map[UsageAnalyticsMode]map[string]any{}
			for _, mode := range []UsageAnalyticsMode{UsageModeFull, UsageModeNoMoney, UsageModeTimeOnly} {
				srv := newUsageServer(t, newUsageSource(), mode)
				sort := "time"
				w := getUsage(t, srv, "view="+view+"&group=agent&sort="+sort)
				decodeUsage(t, w)
				var doc map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				delete(doc, "mode")
				bodies[mode] = doc
			}
			if len(leakedWords(bodies[UsageModeFull], "", moneyWords)) == 0 || len(leakedWords(bodies[UsageModeFull], "", tokenWords)) == 0 {
				t.Fatal("the full response has no money or token data, so the checks below prove nothing")
			}
			if leaks := leakedWords(bodies[UsageModeNoMoney], "", moneyWords); len(leaks) > 0 {
				t.Errorf("no_money leaks money: %v", leaks)
			}
			if len(leakedWords(bodies[UsageModeNoMoney], "", tokenWords)) == 0 {
				t.Error("no_money lost its tokens")
			}
			if leaks := leakedWords(bodies[UsageModeTimeOnly], "", slices.Concat(moneyWords, tokenWords)); len(leaks) > 0 {
				t.Errorf("time_only leaks money or tokens: %v", leaks)
			}
		})
	}
}

func TestUsageAnalytics_SortsTheModeCannotServeAreRejected(t *testing.T) {
	cases := []struct {
		mode   UsageAnalyticsMode
		sort   string
		status int
	}{
		{UsageModeFull, "cost", http.StatusOK},
		{UsageModeNoMoney, "cost", http.StatusBadRequest},
		{UsageModeNoMoney, "tokens", http.StatusOK},
		{UsageModeTimeOnly, "cost", http.StatusBadRequest},
		{UsageModeTimeOnly, "tokens", http.StatusBadRequest},
		{UsageModeTimeOnly, "time", http.StatusOK},
	}
	for _, tc := range cases {
		for _, view := range []string{"breakdown", "jobs", "charts"} {
			srv := newUsageServer(t, newUsageSource(), tc.mode)
			w := getUsage(t, srv, "view="+view+"&sort="+tc.sort)
			if w.Code != tc.status {
				t.Errorf("%s %s sort=%s: status %d, want %d", tc.mode, view, tc.sort, w.Code, tc.status)
			} else if tc.status != http.StatusOK {
				usageError(t, w, tc.status, UsageErrorInvalidParam)
			}
		}
	}
}

func TestUsageAnalytics_InvalidParametersAre400(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), UsageModeFull)
	cases := map[string]string{
		"view":                     "view=pie",
		"group":                    "group=profile",
		"scope":                    "scope=everything",
		"stage scope needs id":     "scope=stage",
		"stage id needs scope":     "stage_id=build",
		"stage id on other scope":  "scope=run_wide&stage_id=build",
		"sort":                     "sort=banana",
		"job-only sort in rows":    "view=breakdown&sort=started",
		"row-only sort in jobs":    "view=jobs&sort=jobs",
		"dir":                      "dir=sideways",
		"limit zero":               "view=jobs&limit=0",
		"limit too big":            "view=jobs&limit=101",
		"limit text":               "view=jobs&limit=ten",
		"limit outside jobs":       "view=breakdown&limit=5",
		"cursor outside jobs":      "view=charts&cursor=x",
		"focus outside jobs":       "view=breakdown&focus_job=j1",
		"cursor with focus":        "view=jobs&cursor=x&focus_job=j1",
		"garbled cursor":           "view=jobs&cursor=not-a-cursor",
		"control char in agent":    "agent=" + url.QueryEscape("a\x00b"),
		"control char in model":    "model=" + url.QueryEscape("a\nb"),
		"overlong agent":           "agent=" + strings.Repeat("a", 300),
		"overlong stage":           "scope=stage&stage_id=" + strings.Repeat("s", 300),
		"overlong focus":           "view=jobs&focus_job=" + strings.Repeat("j", 300),
		"repeated parameter":       "view=jobs&view=charts",
		"unknown parameter":        "color=blue",
		"overlong cursor":          "view=jobs&cursor=" + strings.Repeat("c", 300),
		"control char in focus":    "view=jobs&focus_job=" + url.QueryEscape("j\t1"),
		"empty value is not unset": "view=",
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			usageError(t, getUsage(t, srv, query), http.StatusBadRequest, UsageErrorInvalidParam)
		})
	}
}

func TestUsageAnalytics_IDsAreValuesNotPaths(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), UsageModeFull)
	for _, id := range []string{"../../etc/passwd", "a/b", `..\x`, "%2e%2e"} {
		r := decodeUsage(t, getUsage(t, srv, "scope=stage&stage_id="+url.QueryEscape(id)+"&agent="+url.QueryEscape(id)))
		if r.Totals.Jobs != 0 || r.Query.StageID != id {
			t.Errorf("id %q: jobs %d, echoed %q", id, r.Totals.Jobs, r.Query.StageID)
		}
	}
}

func TestUsageAnalytics_OnlyGETIsServed(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), UsageModeFull)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest(method, "/api/usage/analytics", nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s: status %d, Allow %q", method, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestUsageAnalytics_RouteIsOffWithoutASource(t *testing.T) {
	srv := newTestServer(t, Config{})
	w := getUsage(t, srv, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
	if st := decodeStatus(t, srv); st.Capabilities.UsageAnalytics || st.Analytics != nil {
		t.Errorf("status advertises analytics without a source: %+v", st)
	}
}

func TestUsageAnalytics_JobsPagesAreStableAndTieBroken(t *testing.T) {
	src := newUsageSource()
	src.addBulkJobs(25)
	srv := newUsageServer(t, src, UsageModeFull)

	for _, sort := range []string{"time", "started", "name"} {
		t.Run(sort, func(t *testing.T) {
			full := decodeUsage(t, getUsage(t, srv, "view=jobs&sort="+sort+"&limit=100")).Jobs
			if full.Total != 32 || len(full.Items) != 32 || full.NextCursor != "" || full.PrevCursor != "" {
				t.Fatalf("full list: total %d items %d cursors %q/%q", full.Total, len(full.Items), full.NextCursor, full.PrevCursor)
			}

			var walked []string
			query := "view=jobs&limit=10&sort=" + sort
			var pages int
			for cursor := ""; ; pages++ {
				q := query
				if cursor != "" {
					q += "&cursor=" + url.QueryEscape(cursor)
				}
				page := decodeUsage(t, getUsage(t, srv, q)).Jobs
				walked = append(walked, jobIDsOf(page)...)
				if page.Total != 32 {
					t.Fatalf("page total = %d", page.Total)
				}
				if cursor == "" && page.PrevCursor != "" {
					t.Error("the first page has a prev cursor")
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if pages != 3 {
				t.Errorf("pages = %d, want 3", pages)
			}
			if !slices.Equal(walked, jobIDsOf(full)) {
				t.Errorf("paged order differs from the full order:\n%v\n%v", walked, jobIDsOf(full))
			}
		})
	}

	// Equal sort values fall back to start time and then job ID.
	tied := decodeUsage(t, getUsage(t, srv, "view=jobs&sort=time&dir=desc&limit=100")).Jobs
	var bulk []string
	for _, id := range jobIDsOf(tied) {
		if strings.HasPrefix(id, "k") {
			bulk = append(bulk, id)
		}
	}
	if !slices.IsSorted(bulk) {
		t.Errorf("tied jobs are not in ID order: %v", bulk)
	}
}

func TestUsageAnalytics_PrevCursorReturnsTheEarlierPage(t *testing.T) {
	src := newUsageSource()
	src.addBulkJobs(25)
	srv := newUsageServer(t, src, UsageModeFull)

	first := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=10")).Jobs
	second := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=10&cursor="+url.QueryEscape(first.NextCursor))).Jobs
	if second.PrevCursor == "" {
		t.Fatal("the second page has no prev cursor")
	}
	back := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=10&cursor="+url.QueryEscape(second.PrevCursor))).Jobs
	if !slices.Equal(jobIDsOf(back), jobIDsOf(first)) {
		t.Errorf("prev page = %v, want %v", jobIDsOf(back), jobIDsOf(first))
	}
}

func TestUsageAnalytics_CursorIsBoundToItsRequest(t *testing.T) {
	src := newUsageSource()
	src.addBulkJobs(25)
	srv := newUsageServer(t, src, UsageModeFull)
	cursor := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=10")).Jobs.NextCursor
	use := func(srv *Server, query string) *httptest.ResponseRecorder {
		return getUsage(t, srv, query+"&cursor="+url.QueryEscape(cursor))
	}

	if w := use(srv, "view=jobs&limit=10"); w.Code != http.StatusOK {
		t.Fatalf("the same request must accept its cursor: %d %s", w.Code, w.Body)
	}
	for name, query := range map[string]string{
		"sort":  "view=jobs&limit=10&sort=time",
		"dir":   "view=jobs&limit=10&dir=asc",
		"limit": "view=jobs&limit=5",
		"stage": "view=jobs&limit=10&scope=stage&stage_id=build",
		"agent": "view=jobs&limit=10&agent=stage:build",
		"model": "view=jobs&limit=10&model=unknown",
		"scope": "view=jobs&limit=10&scope=run_wide",
	} {
		t.Run(name, func(t *testing.T) {
			usageError(t, use(srv, query), http.StatusConflict, UsageErrorStaleCursor)
		})
	}

	t.Run("revision", func(t *testing.T) {
		src.edit(func(in *accounting.AnalyticsInput) { in.Jobs.Revision++ })
		usageError(t, use(srv, "view=jobs&limit=10"), http.StatusConflict, UsageErrorStaleCursor)
	})
	t.Run("metadata revision", func(t *testing.T) {
		fresh := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=10")).Jobs.NextCursor
		src.edit(func(in *accounting.AnalyticsInput) { in.Meta.Revision++ })
		usageError(t, getUsage(t, srv, "view=jobs&limit=10&cursor="+url.QueryEscape(fresh)), http.StatusConflict, UsageErrorStaleCursor)
	})
	t.Run("mode", func(t *testing.T) {
		other := newUsageServer(t, src, UsageModeNoMoney)
		usageError(t, use(other, "view=jobs&limit=10"), http.StatusConflict, UsageErrorStaleCursor)
	})
	t.Run("tampered", func(t *testing.T) {
		parts := strings.Split(cursor, ".")
		parts[1] = "20"
		usageError(t, getUsage(t, srv, "view=jobs&limit=10&cursor="+url.QueryEscape(strings.Join(parts, "."))), http.StatusConflict, UsageErrorStaleCursor)
	})
}

// Durations of running jobs grow with time; a later page keeps the as_of of the cursor so the order does
// not shift under the reader.
func TestUsageAnalytics_PaginationSurvivesTickingDurations(t *testing.T) {
	src := newUsageSource()
	src.edit(func(in *accounting.AnalyticsInput) {
		// A running job that is shorter than every finished job at first and longer later.
		in.Jobs.Records = append(in.Jobs.Records, usageStarted("run", stageJobMeta("build"), usageNowAt.Add(-time.Second), 710000))
		in.Jobs.Revision++
	})
	srv := newUsageServer(t, src, UsageModeFull)

	first := decodeUsage(t, getUsage(t, srv, "view=jobs&sort=time&dir=desc&limit=3"))
	reference := decodeUsage(t, getUsage(t, srv, "view=jobs&sort=time&dir=desc&limit=100")).Jobs

	usageNowAt = usageNowAt.Add(time.Hour)
	t.Cleanup(func() { usageNowAt = usageT0.Add(12 * time.Minute) })

	second := decodeUsage(t, getUsage(t, srv, "view=jobs&sort=time&dir=desc&limit=3&cursor="+url.QueryEscape(first.Jobs.NextCursor)))
	if !second.AsOf.Equal(first.AsOf) {
		t.Errorf("second page as_of = %v, want the cursor's %v", second.AsOf, first.AsOf)
	}
	got := append(jobIDsOf(first.Jobs), jobIDsOf(second.Jobs)...)
	if want := jobIDsOf(reference)[:6]; !slices.Equal(got, want) {
		t.Errorf("two pages = %v, want the first six of %v", got, want)
	}
	for _, item := range second.Jobs.Items {
		if item.ID == "run" && (item.DurationMS == nil || *item.DurationMS != 1000) {
			t.Errorf("running job duration on the later page = %v, want 1000 at the pinned as_of", item.DurationMS)
		}
	}
}

func TestUsageAnalytics_FocusJob(t *testing.T) {
	src := newUsageSource()
	src.addBulkJobs(25)
	srv := newUsageServer(t, src, UsageModeFull)
	order := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=100")).Jobs
	index := slices.Index(jobIDsOf(order), "k020")

	t.Run("inside the filter", func(t *testing.T) {
		r := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=10&focus_job=k020"))
		focus := r.Jobs.Focus
		if focus == nil || focus.Status != UsageFocusFound || focus.JobID != "k020" || focus.Index == nil || *focus.Index != index {
			t.Fatalf("focus = %+v, want found at %d", focus, index)
		}
		if !slices.Contains(jobIDsOf(r.Jobs), "k020") || len(r.Jobs.Items) == 0 {
			t.Errorf("page %v does not hold the focused job", jobIDsOf(r.Jobs))
		}
		if wantFirst := jobIDsOf(order)[index/10*10]; r.Jobs.Items[0].ID != wantFirst {
			t.Errorf("page starts at %s, want the page boundary %s", r.Jobs.Items[0].ID, wantFirst)
		}
		if (index/10 > 0) != (r.Jobs.PrevCursor != "") || (index/10 < 3) != (r.Jobs.NextCursor != "") {
			t.Errorf("cursors %q/%q for page %d", r.Jobs.PrevCursor, r.Jobs.NextCursor, index/10)
		}
	})
	t.Run("filtered out", func(t *testing.T) {
		r := decodeUsage(t, getUsage(t, srv, "view=jobs&limit=10&agent=stage:review&focus_job=k020"))
		focus := r.Jobs.Focus
		if focus == nil || focus.Status != UsageFocusFilteredOut || focus.JobID != "k020" || focus.Index != nil {
			t.Errorf("focus = %+v, want filtered_out", focus)
		}
		if r.Jobs.Total != 1 || len(r.Jobs.Items) != 1 {
			t.Errorf("a filtered-out focus must still answer the first page: total %d items %d", r.Jobs.Total, len(r.Jobs.Items))
		}
	})
	t.Run("unknown job", func(t *testing.T) {
		focus := decodeUsage(t, getUsage(t, srv, "view=jobs&focus_job=nope")).Jobs.Focus
		if focus == nil || focus.Status != UsageFocusNotFound || focus.JobID != "nope" {
			t.Errorf("focus = %+v, want not_found", focus)
		}
	})
	t.Run("the Assistant's job is reachable", func(t *testing.T) {
		focus := decodeUsage(t, getUsage(t, srv, "view=jobs&focus_job=j6&limit=100")).Jobs.Focus
		if focus == nil || focus.Status != UsageFocusFound {
			t.Errorf("focus = %+v", focus)
		}
	})
}

func TestUsageAnalytics_ChartsHoldComparisonAndTimeline(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), UsageModeFull)
	r := decodeUsage(t, getUsage(t, srv, "view=charts&group=agent"))
	if r.Charts == nil || r.Charts.Group != UsageGroupAgent || len(r.Charts.Comparison) != 5 {
		t.Fatalf("charts = %+v", r.Charts)
	}
	tl := r.Charts.Timeline
	if tl.Limit != 2000 || tl.Truncated || tl.Total != len(tl.Intervals) || tl.Total == 0 {
		t.Errorf("timeline = %+v", tl)
	}
	if tl.Unplaced != 1 {
		t.Errorf("unplaced = %d, want the interrupted job", tl.Unplaced)
	}
	byModel := decodeUsage(t, getUsage(t, srv, "view=charts&group=model"))
	if byModel.Charts.Group != UsageGroupModel || len(byModel.Charts.Comparison) != 3 {
		t.Errorf("model comparison = %+v", byModel.Charts.Comparison)
	}
}

func TestUsageAnalytics_TimelineIsBounded(t *testing.T) {
	src := newUsageSource()
	src.addBulkJobs(2100)
	srv := newUsageServer(t, src, UsageModeFull)
	tl := decodeUsage(t, getUsage(t, srv, "view=charts")).Charts.Timeline
	if !tl.Truncated || len(tl.Intervals) != 2000 || tl.Total < 2100 || tl.Limit != 2000 {
		t.Errorf("timeline: truncated %v, %d intervals, total %d, limit %d", tl.Truncated, len(tl.Intervals), tl.Total, tl.Limit)
	}
}

func TestUsageAnalytics_LegacyRunKeepsTokensAndHasUnknownTime(t *testing.T) {
	src := &usageSource{in: accounting.AnalyticsInput{
		Usage: []accounting.UsageRecord{
			{RecordVersion: 1, StageID: "build", Phase: "implementation", Channel: "anthropic", Model: "claude-opus-4-8",
				Metered: true, Priced: true, Tokens: accounting.Tokens{UncachedInput: 10, Output: 5}, EstimatedCostUSD: 0.5,
				StartedAt: usageT0.Format(time.RFC3339), FinishedAt: usageT0.Format(time.RFC3339)},
		},
		UsageRevision: 1,
		UsageState:    accounting.UsageStateOK,
		Meta:          accounting.AnalyticsMeta{Revision: 1, Stages: []accounting.StageMeta{{ID: "build", Title: "Build", StartedAt: usageT0}}},
	}}
	srv := newUsageServer(t, src, UsageModeFull)
	r := decodeUsage(t, getUsage(t, srv, ""))
	if r.Health.Jobs != UsageStoreAbsent {
		t.Errorf("jobs health = %q, want absent", r.Health.Jobs)
	}
	if r.Totals.AgentTime.DurationMS != nil || r.Completeness.Time.Status != UsageCoverageUnknown ||
		!slices.Contains(r.Completeness.Time.Reasons, "legacy_journal") {
		t.Errorf("legacy time must be unknown, not zero: %+v %+v", r.Totals.AgentTime, r.Completeness.Time)
	}
	if r.Totals.Tokens.Counts == nil || r.Totals.Tokens.Counts.Output != 5 {
		t.Errorf("legacy tokens lost: %+v", r.Totals.Tokens)
	}
}

func TestUsageAnalytics_ResponseIsIdenticalForTheSameRevision(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), UsageModeNoMoney)
	a := getUsage(t, srv, "view=jobs&group=agent").Body.String()
	b := getUsage(t, srv, "view=jobs&group=agent").Body.String()
	if a != b {
		t.Error("two polls of one revision differ")
	}
}

func TestUsageAnalyticsStatus(t *testing.T) {
	src := newUsageSource()
	srv := newUsageServer(t, src, UsageModeNoMoney)
	st := decodeStatus(t, srv)
	if !st.Capabilities.UsageAnalytics || st.Analytics == nil || st.Analytics.Mode != UsageModeNoMoney {
		t.Fatalf("status = %+v", st)
	}
	r := decodeUsage(t, getUsage(t, srv, ""))
	if st.Analytics.Revision != r.Revision {
		t.Errorf("status revision %q, endpoint revision %q", st.Analytics.Revision, r.Revision)
	}

	src.edit(func(in *accounting.AnalyticsInput) { in.UsageRevision++ })
	if next := decodeStatus(t, srv).Analytics.Revision; next == st.Analytics.Revision {
		t.Error("revision did not follow the usage store")
	}

	// No totals or rows come through the status poll: the revision, the mode and the two counts of the overview.
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	var analytics map[string]json.RawMessage
	if err := json.Unmarshal(doc["analytics"], &analytics); err != nil || len(analytics) != 4 {
		t.Errorf("status analytics = %s", doc["analytics"])
	}
}

// The counts come from the run-wide facts, so they do not depend on the display mode and leave the Assistant out.
func TestUsageAnalyticsStatus_CountsModelsAndAgentsInEveryMode(t *testing.T) {
	src := newUsageSource()
	for _, mode := range []UsageAnalyticsMode{UsageModeFull, UsageModeNoMoney, UsageModeTimeOnly} {
		st := decodeStatus(t, newUsageServer(t, src, mode))
		if st.Analytics.Models != 2 || st.Analytics.Agents != 4 {
			t.Errorf("%s: counts = %d models, %d agents, want 2 and 4 (Assistant excluded)", mode, st.Analytics.Models, st.Analytics.Agents)
		}
	}

	srv := newUsageServer(t, src, UsageModeTimeOnly)
	src.addBulkJobs(1)
	src.edit(func(in *accounting.AnalyticsInput) {
		in.Usage = append(in.Usage, usageRecord("k000", "build", "anthropic", "claude-sonnet-5-5", 10, 0.01))
	})
	if st := decodeStatus(t, srv); st.Analytics.Models != 3 || st.Analytics.Agents != 4 {
		t.Errorf("after a job of a new model: %d models, %d agents, want 3 and 4", st.Analytics.Models, st.Analytics.Agents)
	}
}

// The analytics endpoint does not depend on the display-gated cost provider: time-only runs have none.
func TestUsageAnalyticsStatus_IndependentOfTheCostProvider(t *testing.T) {
	srv := newTestServer(t, Config{UsageAnalytics: newUsageSource(), UsageMode: UsageModeTimeOnly})
	st := decodeStatus(t, srv)
	if srv.accounting != nil || !st.Capabilities.UsageAnalytics || st.Analytics.Mode != UsageModeTimeOnly {
		t.Errorf("accounting=%v status=%+v", srv.accounting, st)
	}
	if st.RunCost != nil || st.Accounting != nil {
		t.Error("the existing cost fields must stay gated by the cost provider")
	}
	if w := getUsage(t, srv, "sort=time"); w.Code != http.StatusOK {
		t.Errorf("direct call in time-only: %d %s", w.Code, w.Body)
	}
}

func TestUsageModeFor(t *testing.T) {
	cases := []struct {
		enabled, showMoney bool
		want               UsageAnalyticsMode
	}{
		{true, true, UsageModeFull},
		{true, false, UsageModeNoMoney},
		{false, true, UsageModeTimeOnly},
		{false, false, UsageModeTimeOnly},
	}
	for _, tc := range cases {
		if got := UsageModeFor(tc.enabled, tc.showMoney); got != tc.want {
			t.Errorf("UsageModeFor(%v, %v) = %q, want %q", tc.enabled, tc.showMoney, got, tc.want)
		}
	}
}

// An unset or unknown mode must hide the most, never expose money by accident.
func TestUsageAnalytics_UnknownModeFailsClosed(t *testing.T) {
	srv := newUsageServer(t, newUsageSource(), "")
	r := decodeUsage(t, getUsage(t, srv, ""))
	if r.Mode != UsageModeTimeOnly {
		t.Errorf("mode = %q, want time_only", r.Mode)
	}
	if st := decodeStatus(t, srv); st.Analytics.Mode != UsageModeTimeOnly {
		t.Errorf("status mode = %q", st.Analytics.Mode)
	}
}
