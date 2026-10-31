package server

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var updateUsageAnalyticsGolden = flag.Bool("update-usage-analytics", false, "rewrite pkg/server/testdata/usage-analytics/*.json")

const usageAnalyticsGoldenDir = "testdata/usage-analytics"

// One shared run: five agents, three models, one running job, one interrupted job and one Assistant job.
// Numbers are small and hand-checked; TestUsageAnalyticsFixturesReconcile keeps rows and totals consistent.

var usageAsOf = time.Date(2026, 10, 31, 11, 12, 0, 0, time.UTC)

var (
	modelOpus = UsageModelRef{Key: "anthropic/claude-opus-4-8", Channel: "anthropic", Model: "claude-opus-4-8", ColorOrdinal: new(0)}
	modelGPT  = UsageModelRef{Key: "codex/gpt-5.6-sol", Channel: "codex", Model: "gpt-5.6-sol", ColorOrdinal: new(1)}
	modelGLM  = UsageModelRef{Key: "anthropic/glm-5.3", Channel: "anthropic", Model: "glm-5.3", ColorOrdinal: new(2)}
)

var (
	agentBuild     = UsageAgent{ID: "stage:build", Kind: UsageAgentStage, Title: "Build"}
	agentReviewer  = UsageAgent{ID: "helper:t-1", Kind: UsageAgentHelper, Title: "Reviewer"}
	agentVerify    = UsageAgent{ID: "verify:review#0", Kind: UsageAgentVerify, Title: "Verify · review"}
	agentReview    = UsageAgent{ID: "stage:review", Kind: UsageAgentStage, Title: "Review"}
	agentAssistant = UsageAgent{ID: "side:run-1", Kind: UsageAgentAssistant, Title: "Assistant", OutsideRunTotal: true}
)

func fixtureCatalog() UsageCatalog {
	return UsageCatalog{
		Stages: []UsageStageRef{{ID: "build", Title: "Build"}, {ID: "review", Title: "Review"}},
		Agents: []UsageAgent{agentBuild, agentReviewer, agentVerify, agentReview, agentAssistant},
		Models: []UsageModelRef{modelOpus, modelGPT, modelGLM},
		Scopes: UsageScopes{RunWide: true, Unattributed: false},
	}
}

func fixtureTokens(c UsageTokenCounts, metered, unmetered, pending int) *UsageTokens {
	c.Total = c.UncachedInput + c.CacheRead + c.CacheWrite5m + c.CacheWrite1h + c.CacheWriteOther + c.Output
	return &UsageTokens{Counts: &c, MeteredJobs: metered, UnmeteredJobs: unmetered, PendingJobs: pending}
}

// unknownTokens is a group whose jobs reported no usage: the counts are unknown, not zero.
func unknownTokens(unmetered, pending int) *UsageTokens {
	return &UsageTokens{UnmeteredJobs: unmetered, PendingJobs: pending}
}

func fixtureJobs() map[string]UsageJob {
	return map[string]UsageJob{
		"j1": {
			ID: "job-1", Title: "Build · planning", Agent: agentBuild, StageID: "build", Model: &modelOpus, ModelSource: UsageSourceObserved,
			Status: UsageJobOK, StartedAt: time.Date(2026, 10, 31, 11, 0, 0, 0, time.UTC), RunOffsetMS: new(int64(0)), DurationMS: new(int64(120000)),
			UsageSpend: UsageSpend{
				Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 1000, CacheRead: 5000, CacheWrite5m: 400, Output: 600}, 1, 0, 0),
				Money:  &UsageMoney{EstimatedCostUSD: new(0.42), PricedJobs: 1},
			},
		},
		"j2": {
			ID: "job-2", Title: "Build · implementation", Agent: agentBuild, StageID: "build", Model: &modelOpus, ModelSource: UsageSourceObserved,
			Status: UsageJobOK, StartedAt: time.Date(2026, 10, 31, 11, 2, 10, 0, time.UTC), RunOffsetMS: new(int64(130000)), DurationMS: new(int64(300000)),
			UsageSpend: UsageSpend{
				Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 3000, CacheRead: 20000, CacheWrite5m: 1000, Output: 2000}, 1, 0, 0),
				Money:  &UsageMoney{EstimatedCostUSD: new(1.35), PricedJobs: 1},
			},
		},
		"j3": {
			ID: "job-3", Title: "Review the diff", Agent: agentReviewer, StageID: "build", Model: &modelGPT, ModelSource: UsageSourceObserved,
			Status: UsageJobOK, StartedAt: time.Date(2026, 10, 31, 11, 3, 20, 0, time.UTC), RunOffsetMS: new(int64(200000)), DurationMS: new(int64(90000)),
			UsageSpend: UsageSpend{
				Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 4000, Output: 1000, ReasoningOutput: 300}, 1, 0, 0),
				Money:  &UsageMoney{UnpricedJobs: 1},
			},
		},
		"j4": {
			ID: "job-4", Title: "Verify · step 1", Agent: agentVerify, StageID: "review", Model: &modelGPT, ModelSource: UsageSourceObserved,
			Status: UsageJobOK, StartedAt: time.Date(2026, 10, 31, 11, 6, 40, 0, time.UTC), RunOffsetMS: new(int64(400000)), DurationMS: new(int64(60000)),
			UsageSpend: UsageSpend{
				Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 2000, Output: 500}, 1, 0, 0),
				Money:  &UsageMoney{EstimatedCostUSD: new(0.15), PricedJobs: 1},
			},
		},
		// Running: duration is the elapsed time at as_of, usage is still pending.
		"j5": {
			ID: "job-5", Title: "Review · implementation", Agent: agentReview, StageID: "review", Model: &modelGLM, ModelSource: UsageSourceHint,
			Status: UsageJobRunning, StartedAt: time.Date(2026, 10, 31, 11, 8, 0, 0, time.UTC), RunOffsetMS: new(int64(480000)), DurationMS: new(int64(240000)),
			UsageSpend: UsageSpend{Tokens: unknownTokens(0, 1), Money: &UsageMoney{}},
		},
		// Assistant jobs are outside the run total and get no place on the run timeline.
		"j6": {
			ID: "job-6", Title: "Assistant · job 1", Agent: agentAssistant, Status: UsageJobOK,
			StartedAt: time.Date(2026, 10, 31, 11, 9, 0, 0, time.UTC), DurationMS: new(int64(15000)),
			UsageSpend: UsageSpend{Tokens: unknownTokens(1, 0), Money: &UsageMoney{}},
		},
		// Interrupted by a crash: the end is unknown, so no duration and no place on the timeline.
		"j7": {
			ID: "job-7", Title: "Build · implementation", Agent: agentBuild, StageID: "build", Model: &modelOpus, ModelSource: UsageSourceObserved,
			Status: UsageJobInterrupted, StartedAt: time.Date(2026, 10, 31, 11, 10, 0, 0, time.UTC),
			UsageSpend: UsageSpend{Tokens: unknownTokens(1, 0), Money: &UsageMoney{}},
		},
	}
}

func fixtureJob(id string) UsageJob { return fixtureJobs()[id] }

func timeMetric(durationMS int64, measured, unknown int) UsageTime {
	return UsageTime{DurationMS: &durationMS, MeasuredJobs: measured, UnknownJobs: unknown}
}

var (
	// metrics of the jobs run for every agent / model, matching fixtureJobs above.
	metricsBuild = UsageMetrics{
		Jobs: 3, AgentTime: timeMetric(420000, 2, 1),
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 4000, CacheRead: 25000, CacheWrite5m: 1400, Output: 2600}, 2, 1, 0),
			Money:  &UsageMoney{EstimatedCostUSD: new(1.77), PricedJobs: 2},
		},
	}
	metricsReviewer = UsageMetrics{
		Jobs: 1, AgentTime: timeMetric(90000, 1, 0),
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 4000, Output: 1000, ReasoningOutput: 300}, 1, 0, 0),
			Money:  &UsageMoney{UnpricedJobs: 1},
		},
	}
	metricsVerify = UsageMetrics{
		Jobs: 1, AgentTime: timeMetric(60000, 1, 0),
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 2000, Output: 500}, 1, 0, 0),
			Money:  &UsageMoney{EstimatedCostUSD: new(0.15), PricedJobs: 1},
		},
	}
	metricsReview = UsageMetrics{
		Jobs: 1, ActiveJobs: 1, AgentTime: timeMetric(240000, 1, 0),
		UsageSpend: UsageSpend{Tokens: unknownTokens(0, 1), Money: &UsageMoney{}},
	}
	metricsAssistant = UsageMetrics{
		Jobs: 1, AgentTime: timeMetric(15000, 1, 0),
		UsageSpend: UsageSpend{Tokens: unknownTokens(1, 0), Money: &UsageMoney{}},
	}
	// opus is the build agent alone; gpt is reviewer + verify.
	metricsGPT = UsageMetrics{
		Jobs: 2, AgentTime: timeMetric(150000, 2, 0),
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 6000, Output: 1500, ReasoningOutput: 300}, 2, 0, 0),
			Money:  &UsageMoney{EstimatedCostUSD: new(0.15), PricedJobs: 1, UnpricedJobs: 1},
		},
	}
	metricsRunTotal = UsageMetrics{
		Jobs: 6, ActiveJobs: 1, AgentTime: timeMetric(810000, 5, 1),
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 10000, CacheRead: 25000, CacheWrite5m: 1400, Output: 4100, ReasoningOutput: 300}, 4, 1, 1),
			Money:  &UsageMoney{EstimatedCostUSD: new(1.92), PricedJobs: 3, UnpricedJobs: 1},
		},
	}
)

func modelRow(ref UsageModelRef, source string, agents int, m UsageMetrics) UsageRow {
	return UsageRow{Key: ref.Key, Label: ref.Model, Model: &ref, ModelSource: source, Agents: agents, UsageMetrics: m}
}

func agentRow(a UsageAgent, ref UsageModelRef, source string, m UsageMetrics) UsageRow {
	return UsageRow{Key: a.ID, Label: a.Title, Agent: &a, Model: &ref, ModelState: UsageModelSingle, ModelSource: source, UsageMetrics: m}
}

// Rows are sorted by agent time, high to low; the Assistant row stays last.
func fixtureModelRows() []UsageRow {
	return []UsageRow{
		modelRow(modelOpus, UsageSourceObserved, 1, metricsBuild),
		modelRow(modelGLM, UsageSourceHint, 1, metricsReview),
		modelRow(modelGPT, UsageSourceObserved, 2, metricsGPT),
	}
}

func fixtureAgentRows() []UsageRow {
	assistant := UsageRow{Key: agentAssistant.ID, Label: agentAssistant.Title, Agent: &agentAssistant, ModelState: UsageModelUnknown, UsageMetrics: metricsAssistant}
	return []UsageRow{
		agentRow(agentBuild, modelOpus, UsageSourceObserved, metricsBuild),
		agentRow(agentReview, modelGLM, UsageSourceHint, metricsReview),
		agentRow(agentReviewer, modelGPT, UsageSourceObserved, metricsReviewer),
		agentRow(agentVerify, modelGPT, UsageSourceObserved, metricsVerify),
		assistant,
	}
}

func fullCompleteness() UsageCompleteness {
	return UsageCompleteness{
		Time:   UsageCoverage{Status: UsageCoveragePartial, Known: 5, Unknown: 1, Reasons: []string{"interrupted_job"}},
		Tokens: &UsageCoverage{Status: UsageCoveragePartial, Known: 4, Unknown: 2, Reasons: []string{"unmetered_job", "pending_usage"}},
		Price:  &UsageCoverage{Status: UsageCoveragePartial, Known: 3, Unknown: 3, Reasons: []string{"unmetered_job", "pending_usage", "unpriced_model"}},
	}
}

func fixtureBase(view UsageView) UsageAnalytics {
	return UsageAnalytics{
		View:         view,
		AsOf:         usageAsOf,
		Revision:     "u7.j12.m3",
		Mode:         UsageModeFull,
		Metrics:      UsageModeFull.Metrics(),
		Health:       UsageHealth{Usage: UsageStoreOK, Jobs: UsageStoreOK},
		Completeness: fullCompleteness(),
		Query:        UsageQuery{View: view, Group: UsageGroupModel, Scope: UsageScopeAll, Sort: UsageSortTime, Dir: UsageDirDesc},
		Totals:       metricsRunTotal,
		// Assistant jobs are summed apart from the totals so that the totals equal the run total.
		OutsideRunTotal: &metricsAssistant,
		Catalog:         fixtureCatalog(),
	}
}

func fixtureFullBreakdown() UsageAnalytics {
	r := fixtureBase(UsageViewBreakdown)
	r.Query.Group = UsageGroupAgent
	r.Breakdown = &UsageBreakdown{Group: UsageGroupAgent, Rows: fixtureAgentRows()}
	return r
}

func fixtureFullJobs() UsageAnalytics {
	r := fixtureBase(UsageViewJobs)
	r.Query.Sort = UsageSortStarted
	r.Query.Limit = 4
	r.Jobs = &UsageJobsPage{
		Items:      []UsageJob{fixtureJob("j7"), fixtureJob("j6"), fixtureJob("j5"), fixtureJob("j4")},
		Total:      7,
		Limit:      4,
		NextCursor: "c.4.u7j12m3",
	}
	return r
}

func fixtureFullCharts() UsageAnalytics {
	r := fixtureBase(UsageViewCharts)
	r.Charts = &UsageCharts{
		Group:      UsageGroupModel,
		Comparison: fixtureModelRows(),
		Timeline: UsageTimeline{
			RangeStartMS: 0,
			RangeEndMS:   720000,
			Limit:        2000,
			Total:        5,
			Unplaced:     2,
			Intervals: []UsageInterval{
				{JobID: "job-1", Title: "Build · planning", AgentID: agentBuild.ID, ModelKey: modelOpus.Key, StageID: "build", Status: UsageJobOK, RunOffsetMS: 0, DurationMS: 120000},
				{JobID: "job-2", Title: "Build · implementation", AgentID: agentBuild.ID, ModelKey: modelOpus.Key, StageID: "build", Status: UsageJobOK, RunOffsetMS: 130000, DurationMS: 300000},
				{JobID: "job-3", Title: "Review the diff", AgentID: agentReviewer.ID, ModelKey: modelGPT.Key, StageID: "build", Status: UsageJobOK, RunOffsetMS: 200000, DurationMS: 90000},
				{JobID: "job-4", Title: "Verify · step 1", AgentID: agentVerify.ID, ModelKey: modelGPT.Key, StageID: "review", Status: UsageJobOK, RunOffsetMS: 400000, DurationMS: 60000},
				{JobID: "job-5", Title: "Review · implementation", AgentID: agentReview.ID, ModelKey: modelGLM.Key, StageID: "review", Status: UsageJobRunning, RunOffsetMS: 480000, DurationMS: 240000},
			},
		},
	}
	return r
}

// The three jobs of the build stage, sorted by start time, newest first.
func buildStageJobsResponse() UsageAnalytics {
	r := fixtureBase(UsageViewJobs)
	r.Query.Scope = UsageScopeStage
	r.Query.StageID = "build"
	r.Query.Sort = UsageSortStarted
	r.Query.Limit = 10
	r.Totals = metricsBuild
	r.OutsideRunTotal = nil
	r.Completeness = UsageCompleteness{
		Time:   UsageCoverage{Status: UsageCoveragePartial, Known: 2, Unknown: 1, Reasons: []string{"interrupted_job"}},
		Tokens: &UsageCoverage{Status: UsageCoveragePartial, Known: 2, Unknown: 1, Reasons: []string{"unmetered_job"}},
		Price:  &UsageCoverage{Status: UsageCoveragePartial, Known: 2, Unknown: 1, Reasons: []string{"unmetered_job"}},
	}
	r.Jobs = &UsageJobsPage{Items: []UsageJob{fixtureJob("j7"), fixtureJob("j2"), fixtureJob("j1")}, Total: 3, Limit: 10}
	return r
}

func fixturePartialJobs() UsageAnalytics {
	r := buildStageJobsResponse()
	r.Health.StartFailedJobs = 1
	r.Completeness.Time.Reasons = []string{"interrupted_job", "start_failed"}
	return r
}

func fixtureFocusFilteredOut() UsageAnalytics {
	r := buildStageJobsResponse()
	r.Query.Scope = UsageScopeAll
	r.Query.StageID = ""
	r.Query.Agent = agentBuild.ID
	r.Jobs.Focus = &UsageFocus{Status: UsageFocusFilteredOut, JobID: "job-4"}
	return r
}

// A run recorded before agent-jobs.jsonl existed: tokens and prices are known from usage.jsonl,
// agent time is unknown (not zero) and legacy usage has no job.
func fixtureLegacyBreakdown() UsageAnalytics {
	build := UsageMetrics{
		AgentTime: UsageTime{},
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 5000, CacheRead: 10000, Output: 2000}, 2, 0, 0),
			Money:  &UsageMoney{EstimatedCostUSD: new(0.8), PricedJobs: 2},
		},
	}
	review := UsageMetrics{
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 2000, Output: 500}, 1, 0, 0),
			Money:  &UsageMoney{EstimatedCostUSD: new(0.2), PricedJobs: 1},
		},
	}
	unattributed := UsageMetrics{UsageSpend: UsageSpend{Tokens: unknownTokens(1, 0), Money: &UsageMoney{}}}
	total := UsageMetrics{
		UsageSpend: UsageSpend{
			Tokens: fixtureTokens(UsageTokenCounts{UncachedInput: 7000, CacheRead: 10000, Output: 2500}, 3, 1, 0),
			Money:  &UsageMoney{EstimatedCostUSD: new(1.0), PricedJobs: 3},
		},
	}
	unknownAgent := UsageAgent{ID: "unattributed", Kind: UsageAgentUnknown, Title: "Unattributed"}
	rows := []UsageRow{
		agentRow(agentBuild, modelOpus, UsageSourceLegacy, build),
		agentRow(agentReview, modelGLM, UsageSourceLegacy, review),
		{Key: unknownAgent.ID, Label: unknownAgent.Title, Agent: &unknownAgent, ModelState: UsageModelUnknown, UsageMetrics: unattributed},
	}
	r := fixtureBase(UsageViewBreakdown)
	r.Health = UsageHealth{Usage: UsageStoreOK, Jobs: UsageStoreAbsent}
	r.Completeness = UsageCompleteness{
		Time:   UsageCoverage{Status: UsageCoverageUnknown, Reasons: []string{"legacy_journal"}},
		Tokens: &UsageCoverage{Status: UsageCoveragePartial, Known: 3, Unknown: 1, Reasons: []string{"unmetered_job"}},
		Price:  &UsageCoverage{Status: UsageCoveragePartial, Known: 3, Unknown: 1, Reasons: []string{"unmetered_job"}},
	}
	r.Query.Group = UsageGroupAgent
	r.Query.Sort = UsageSortTokens
	r.Totals = total
	r.OutsideRunTotal = nil
	r.Catalog = UsageCatalog{
		Stages: []UsageStageRef{{ID: "build", Title: "Build"}, {ID: "review", Title: "Review"}},
		Agents: []UsageAgent{agentBuild, agentReview, unknownAgent},
		Models: []UsageModelRef{modelOpus, modelGLM},
		Scopes: UsageScopes{Unattributed: true},
	}
	r.Breakdown = &UsageBreakdown{Group: UsageGroupAgent, Rows: rows}
	return r
}

// goldenFiles maps each golden file to its value. Modes are derived from the full response, so the
// no-money and time-only files prove that ForMode removes exactly the money and token data.
func goldenFiles() map[string]any {
	files := map[string]any{
		"full-breakdown":       fixtureFullBreakdown(),
		"full-jobs":            fixtureFullJobs(),
		"full-charts":          fixtureFullCharts(),
		"legacy-breakdown":     fixtureLegacyBreakdown(),
		"partial-jobs":         fixturePartialJobs(),
		"focus-filtered-out":   fixtureFocusFilteredOut(),
		"error-stale-cursor":   UsageAnalyticsError{Code: UsageErrorStaleCursor, Message: "cursor was issued for another revision, filter, sort or mode"},
		"status-analytics":     UsageAnalyticsStatus{Revision: "u7.j12.m3", Mode: UsageModeNoMoney, Models: 3, Agents: 4},
		"status-analytics-off": UsageAnalyticsStatus{Revision: "j12.m3", Mode: UsageModeTimeOnly, Models: 3, Agents: 4},
	}
	for name, full := range map[string]UsageAnalytics{
		"breakdown": fixtureFullBreakdown(),
		"jobs":      fixtureFullJobs(),
		"charts":    fixtureFullCharts(),
	} {
		files["no-money-"+name] = full.ForMode(UsageModeNoMoney)
		files["time-only-"+name] = full.ForMode(UsageModeTimeOnly)
	}
	return files
}

func marshalGolden(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return buf.Bytes()
}

func TestUsageAnalyticsGolden(t *testing.T) {
	files := goldenFiles()
	for name, value := range files {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(usageAnalyticsGoldenDir, name+".json")
			got := marshalGolden(t, value)
			if *updateUsageAnalyticsGolden {
				if err := os.MkdirAll(usageAnalyticsGoldenDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update-usage-analytics)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the DTO output; run with -update-usage-analytics and review the diff\n got: %s", path, got)
			}
		})
	}

	// A golden file nobody checks would silently rot, and the dashboard client parses every one of them.
	onDisk, err := filepath.Glob(filepath.Join(usageAnalyticsGoldenDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range onDisk {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		if _, ok := files[name]; !ok {
			t.Errorf("%s is not produced by goldenFiles", path)
		}
	}
}

// Every golden file must decode into the DTO without unknown fields and re-encode to the same bytes:
// proves the files and the types describe the same contract.
func TestUsageAnalyticsGoldenRoundTrip(t *testing.T) {
	for name, value := range goldenFiles() {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(usageAnalyticsGoldenDir, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var target any
			switch value.(type) {
			case UsageAnalytics:
				target = new(UsageAnalytics)
			case UsageAnalyticsError:
				target = new(UsageAnalyticsError)
			default:
				target = new(UsageAnalyticsStatus)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(target); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if again := marshalGolden(t, target); !bytes.Equal(again, raw) {
				t.Errorf("round trip changed the document:\n%s", again)
			}
		})
	}
}

var (
	moneyWords = []string{"money", "cost", "price", "priced", "usd", "currency", "$"}
	tokenWords = []string{"token", "cache", "uncached", "output", "reasoning", "metered", "pending"}
)

// leakedWords returns a path for every JSON key or string value, on any depth, that contains one of words.
func leakedWords(node any, path string, words []string) []string {
	var found []string
	hit := func(text string) bool {
		lower := strings.ToLower(text)
		return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(lower, w) })
	}
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if hit(key) {
				found = append(found, path+"/"+key)
			}
			found = append(found, leakedWords(child, path+"/"+key, words)...)
		}
	case []any:
		for i, child := range v {
			found = append(found, leakedWords(child, path+"/"+strconv.Itoa(i), words)...)
		}
	case string:
		if hit(v) {
			found = append(found, path+"="+v)
		}
	default:
		// numbers, booleans and null carry no words
	}
	return found
}

func decodeGeneric(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(usageAnalyticsGoldenDir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestUsageAnalyticsModesRemoveData(t *testing.T) {
	// The mode value names itself ("no_money"), so it is checked first and left out of the word search.
	takeMode := func(doc map[string]any) any {
		mode := doc["mode"]
		delete(doc, "mode")
		return mode
	}
	for _, view := range []string{"breakdown", "jobs", "charts"} {
		t.Run(view, func(t *testing.T) {
			full, noMoney, timeOnly := decodeGeneric(t, "full-"+view), decodeGeneric(t, "no-money-"+view), decodeGeneric(t, "time-only-"+view)
			if modes := []any{takeMode(full), takeMode(noMoney), takeMode(timeOnly)}; !slices.Equal(modes, []any{"full", "no_money", "time_only"}) {
				t.Errorf("modes = %v", modes)
			}
			if len(leakedWords(full, "", moneyWords)) == 0 || len(leakedWords(full, "", tokenWords)) == 0 {
				t.Fatal("the full fixture has no money or token data, so the checks below prove nothing")
			}
			if leaks := leakedWords(noMoney, "", moneyWords); len(leaks) > 0 {
				t.Errorf("no_money response contains money data: %v", leaks)
			}
			if len(leakedWords(noMoney, "", tokenWords)) == 0 {
				t.Error("no_money response lost its token data")
			}
			if leaks := leakedWords(timeOnly, "", slices.Concat(moneyWords, tokenWords)); len(leaks) > 0 {
				t.Errorf("time_only response contains money or token data: %v", leaks)
			}
		})
	}
}

// The status carries the counts of the Cost overview so that the overview needs no analytics request.
func TestUsageAnalyticsStatusKeepsRevisionModeAndCounts(t *testing.T) {
	for _, name := range []string{"status-analytics", "status-analytics-off"} {
		doc := decodeGeneric(t, name)
		if len(doc) != 4 || doc["revision"] == nil || doc["mode"] == nil || doc["models"] != float64(3) || doc["agents"] != float64(4) {
			t.Errorf("%s: want revision, mode, models 3 and agents 4, got %v", name, doc)
		}
	}
}

// The agent count of a model row is not money or tokens: every mode keeps it.
func TestUsageAnalyticsModelRowKeepsItsAgentCountInEveryMode(t *testing.T) {
	want := []float64{1, 1, 2}
	for _, name := range []string{"full-charts", "no-money-charts", "time-only-charts"} {
		comparison := decodeGeneric(t, name)["charts"].(map[string]any)["comparison"].([]any)
		var got []float64
		for _, row := range comparison {
			got = append(got, row.(map[string]any)["agents"].(float64))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: agents per model row = %v, want %v", name, got, want)
		}
	}
	for _, row := range decodeGeneric(t, "full-breakdown")["breakdown"].(map[string]any)["rows"].([]any) {
		if _, present := row.(map[string]any)["agents"]; present {
			t.Errorf("agent row %v carries an agent count", row.(map[string]any)["key"])
		}
	}
}

func TestForModeDoesNotModifyItsInput(t *testing.T) {
	original := fixtureFullJobs()
	before := marshalGolden(t, original)
	_ = original.ForMode(UsageModeTimeOnly)
	_ = fixtureFullCharts().ForMode(UsageModeNoMoney)
	if after := marshalGolden(t, original); !bytes.Equal(before, after) {
		t.Errorf("ForMode changed the receiver:\n%s", after)
	}
}

func TestUsageModeMetricsAndSorts(t *testing.T) {
	cases := []struct {
		mode    UsageAnalyticsMode
		metrics []UsageMetric
		cost    bool
		tokens  bool
	}{
		{UsageModeFull, []UsageMetric{UsageMetricCost, UsageMetricAgentTime, UsageMetricTokens}, true, true},
		{UsageModeNoMoney, []UsageMetric{UsageMetricAgentTime, UsageMetricTokens}, false, true},
		{UsageModeTimeOnly, []UsageMetric{UsageMetricAgentTime}, false, false},
	}
	for _, tc := range cases {
		if got := tc.mode.Metrics(); !slices.Equal(got, tc.metrics) {
			t.Errorf("%s metrics = %v, want %v", tc.mode, got, tc.metrics)
		}
		if got := tc.mode.AllowsSort(UsageSortCost); got != tc.cost {
			t.Errorf("%s allows cost sort = %v, want %v", tc.mode, got, tc.cost)
		}
		if got := tc.mode.AllowsSort(UsageSortTokens); got != tc.tokens {
			t.Errorf("%s allows tokens sort = %v, want %v", tc.mode, got, tc.tokens)
		}
		for _, sort := range []UsageSort{UsageSortTime, UsageSortJobs, UsageSortName, UsageSortModel, UsageSortStarted} {
			if !tc.mode.AllowsSort(sort) {
				t.Errorf("%s must allow %s sort", tc.mode, sort)
			}
		}
	}
}

// Unknown is null, a measured zero is 0: the client tells them apart by the key value.
func TestUsageAnalyticsNullIsNotZero(t *testing.T) {
	zero, err := json.Marshal(UsageTime{DurationMS: new(int64(0)), MeasuredJobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"duration_ms":0,"measured_jobs":1,"unknown_jobs":0}`; string(zero) != want {
		t.Errorf("measured zero = %s, want %s", zero, want)
	}
	unknown, err := json.Marshal(UsageTime{UnknownJobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"duration_ms":null,"measured_jobs":0,"unknown_jobs":1}`; string(unknown) != want {
		t.Errorf("unknown time = %s, want %s", unknown, want)
	}

	jobs := decodeGeneric(t, "full-jobs")["jobs"].(map[string]any)["items"].([]any)
	interrupted := jobs[0].(map[string]any)
	if interrupted["status"] != "interrupted" || interrupted["duration_ms"] != nil || interrupted["run_offset_ms"] != nil {
		t.Errorf("interrupted job must keep duration_ms and run_offset_ms as null: %v", interrupted)
	}
	if _, present := interrupted["duration_ms"]; !present {
		t.Error("duration_ms key is missing from the interrupted job instead of being null")
	}
	tokens := interrupted["tokens"].(map[string]any)
	if counts, present := tokens["counts"]; !present || counts != nil {
		t.Errorf("unmetered job: tokens.counts = %v, want null", counts)
	}
	money := interrupted["money"].(map[string]any)
	if cost, present := money["estimated_cost_usd"]; !present || cost != nil {
		t.Errorf("unpriced job: estimated_cost_usd = %v, want null", cost)
	}
}

func sumMetrics(rows []UsageRow) UsageMetrics {
	var total UsageMetrics
	var counts UsageTokenCounts
	var tokens UsageTokens
	var cost float64
	var money UsageMoney
	var duration int64
	for _, row := range rows {
		if row.Agent != nil && row.Agent.OutsideRunTotal {
			continue
		}
		total.Jobs += row.Jobs
		total.ActiveJobs += row.ActiveJobs
		total.AgentTime.MeasuredJobs += row.AgentTime.MeasuredJobs
		total.AgentTime.UnknownJobs += row.AgentTime.UnknownJobs
		if row.AgentTime.DurationMS != nil {
			duration += *row.AgentTime.DurationMS
		}
		tokens.MeteredJobs += row.Tokens.MeteredJobs
		tokens.UnmeteredJobs += row.Tokens.UnmeteredJobs
		tokens.PendingJobs += row.Tokens.PendingJobs
		if c := row.Tokens.Counts; c != nil {
			counts.UncachedInput += c.UncachedInput
			counts.CacheRead += c.CacheRead
			counts.CacheWrite5m += c.CacheWrite5m
			counts.CacheWrite1h += c.CacheWrite1h
			counts.CacheWriteOther += c.CacheWriteOther
			counts.Output += c.Output
			counts.ReasoningOutput += c.ReasoningOutput
			counts.Total += c.Total
		}
		money.PricedJobs += row.Money.PricedJobs
		money.UnpricedJobs += row.Money.UnpricedJobs
		if row.Money.EstimatedCostUSD != nil {
			cost += *row.Money.EstimatedCostUSD
		}
	}
	total.AgentTime.DurationMS = &duration
	tokens.Counts = &counts
	money.EstimatedCostUSD = &cost
	total.Tokens = &tokens
	total.Money = &money
	return total
}

// The breakdown rows of every full fixture add up to the totals, with the Assistant counted apart.
func TestUsageAnalyticsFixturesReconcile(t *testing.T) {
	cases := map[string][]UsageRow{
		"by agent": fixtureFullBreakdown().Breakdown.Rows,
		"by model": fixtureFullCharts().Charts.Comparison,
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			got, want := sumMetrics(rows), metricsRunTotal
			if got.Jobs != want.Jobs || got.ActiveJobs != want.ActiveJobs {
				t.Errorf("jobs/active: got %d/%d, want %d/%d", got.Jobs, got.ActiveJobs, want.Jobs, want.ActiveJobs)
			}
			if *got.AgentTime.DurationMS != *want.AgentTime.DurationMS ||
				got.AgentTime.MeasuredJobs != want.AgentTime.MeasuredJobs || got.AgentTime.UnknownJobs != want.AgentTime.UnknownJobs {
				t.Errorf("agent time: got %+v, want %+v", got.AgentTime, want.AgentTime)
			}
			if *got.Tokens.Counts != *want.Tokens.Counts {
				t.Errorf("token counts: got %+v, want %+v", *got.Tokens.Counts, *want.Tokens.Counts)
			}
			if got.Tokens.MeteredJobs != want.Tokens.MeteredJobs || got.Tokens.UnmeteredJobs != want.Tokens.UnmeteredJobs || got.Tokens.PendingJobs != want.Tokens.PendingJobs {
				t.Errorf("token job counts: got %+v, want %+v", *got.Tokens, *want.Tokens)
			}
			if math.Abs(*got.Money.EstimatedCostUSD-*want.Money.EstimatedCostUSD) > 1e-9 ||
				got.Money.PricedJobs != want.Money.PricedJobs || got.Money.UnpricedJobs != want.Money.UnpricedJobs {
				t.Errorf("money: got %+v, want %+v", *got.Money, *want.Money)
			}
		})
	}
}

// Token total is uncached input + cache read + cache writes + output; reasoning is part of output.
func TestUsageAnalyticsTokenTotals(t *testing.T) {
	for id, job := range fixtureJobs() {
		c := job.Tokens.Counts
		if c == nil {
			continue
		}
		if want := c.UncachedInput + c.CacheRead + c.CacheWrite5m + c.CacheWrite1h + c.CacheWriteOther + c.Output; c.Total != want {
			t.Errorf("%s: total = %d, want %d", id, c.Total, want)
		}
		if c.ReasoningOutput > c.Output {
			t.Errorf("%s: reasoning %d exceeds output %d", id, c.ReasoningOutput, c.Output)
		}
	}
}

func TestUsageAnalyticsErrorCodes(t *testing.T) {
	for _, code := range []UsageErrorCode{UsageErrorStaleCursor, UsageErrorInvalidParam} {
		if code == "" || strings.ToLower(string(code)) != string(code) {
			t.Errorf("error code %q must be non-empty lower snake case", code)
		}
	}
	if UsageErrorStaleCursor.HTTPStatus() != 409 || UsageErrorInvalidParam.HTTPStatus() != 400 {
		t.Errorf("statuses: stale_cursor=%d invalid_param=%d", UsageErrorStaleCursor.HTTPStatus(), UsageErrorInvalidParam.HTTPStatus())
	}
}
