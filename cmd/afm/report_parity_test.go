package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentjobs"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/pkg/runreport"
	"github.com/akopichin/afm/pkg/usageview"
)

// The report and the dashboard are two readers of one run: on a finished run every figure they share must be
// equal. The run is real (afm child, scripted agent, no LLM): two stages, a stage with two attempts, priced,
// unpriced and unmetered usage, and a stage added at runtime.

const reportParityConfig = `server:
  open_browser: false
accounting:
  show_money: true
pricing:
  models:
    report-priced:
      input: 3
      output: 15
`

// newReportParityFixture holds every stage until the test releases it, so the runtime stage can be added first.
func newReportParityFixture(t *testing.T) *modeFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no global config layer
	t.Setenv("AFM_ACCOUNTING", "")
	t.Setenv("AFM_ACCOUNTING_SHOW_MONEY", "")
	fx := newModeFixture(t, modeCore, "report-parity.yaml", "")
	agent := testdataAbs(t, "agents/report_agent.sh")
	fx.agentPath = agent
	writeFile(t, fx.flowPath, strings.ReplaceAll(readTestdata(t, "flows/report-parity.yaml"), "__REPORT_AGENT__", agent))
	writeFile(t, filepath.Join(fx.root, ".afm", "config.yaml"), reportParityConfig)
	fx.gateImplementation()
	return fx
}

// finishParityRun adds the runtime stage while build is held, releases every stage and waits for the end.
func finishParityRun(t *testing.T, fx *modeFixture) string {
	t.Helper()
	child := fx.start()
	runID := fx.waitReady(child)
	waitFor(t, "the second build attempt", func() bool { return slices.Contains(fx.agentRuns(), "build:2") })
	fx.commitExtraStage()
	fx.waitStage("extra", "pending")
	for _, stage := range []string{"build", "ship", "extra"} {
		fx.release(stage)
	}
	fx.waitRunStatus("finished")
	t.Cleanup(func() {
		// The core holds the API after the result; Ctrl+C is its clean exit.
		child.waitOutput(t, holdingFinalState)
		if code := child.interrupt(t); code != 0 {
			t.Errorf("core exit code = %d\n%s", code, child.out.String())
		}
	})
	return runID
}

// fixedReportNow is the as_of of every report test, so documents do not depend on the clock.
func fixedReportNow() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }

// runReportCmd runs `afm report` in-process against root and returns its streams and error.
func runReportCmd(t *testing.T, root string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	prevRoot, prevNow := rootDir, reportNow
	rootDir, reportNow = root, fixedReportNow
	t.Cleanup(func() { rootDir, reportNow = prevRoot, prevNow })
	stdout, stderr = captureStreams(t, func() {
		cmd := newReportCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	return stdout, stderr, err
}

// captureStreams redirects os.Stdout and os.Stderr to files, so a document larger than a pipe buffer cannot
// block the command while nobody reads.
func captureStreams(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	outFile, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		fn()
	}()
	for _, f := range []*os.File{outFile, errFile} {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return read("stdout"), read("stderr")
}

func reportDoc(t *testing.T, root string, args ...string) map[string]any {
	t.Helper()
	stdout, stderr, err := runReportCmd(t, root, append([]string{"--format", "json"}, args...)...)
	if err != nil {
		t.Fatalf("report %v: %v\nstderr: %s", args, err, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	return doc
}

func getJSON(t *testing.T, fx *modeFixture, path string) map[string]any {
	t.Helper()
	code, body, _ := fx.call(http.MethodGet, path, "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

func sub(t *testing.T, doc map[string]any, keys ...string) any {
	t.Helper()
	var cur any = doc
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%v: %v is not an object", keys, cur)
		}
		cur = m[k]
	}
	return cur
}

func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("%s differs\nreport:\n%s\ndashboard:\n%s", what, g, w)
	}
}

// byKey indexes breakdown rows by their key.
func byKey(t *testing.T, rows any) map[string]any {
	t.Helper()
	out := map[string]any{}
	list, _ := rows.([]any)
	for _, r := range list {
		row := r.(map[string]any)
		out[row["key"].(string)] = row
	}
	return out
}

func TestReportParityWithDashboardCost(t *testing.T) {
	fx := newReportParityFixture(t)
	runID := finishParityRun(t, fx)

	// Cost: /api/status against the ledger block.
	status := getJSON(t, fx, "/api/status")
	doc := reportDoc(t, fx.root, runID)
	cost := runreportCost(t, doc)
	assertCostLine(t, "total", cost.Total, status["run_cost"])
	assertCostLine(t, "overhead", cost.Overhead, status["run_overhead_cost"])
	stages, _ := status["stages"].([]any)
	seen := 0
	for _, s := range stages {
		stage := s.(map[string]any)
		if stage["cost"] == nil {
			continue
		}
		seen++
		line, ok := costStage(cost, stage["id"].(string))
		if !ok {
			t.Fatalf("stage %v has dashboard cost but no report row", stage["id"])
		}
		assertCostLine(t, "stage "+stage["id"].(string), &line, stage["cost"])
	}
	if seen != len(cost.Stages) || seen < 3 {
		t.Errorf("stages with cost: dashboard %d, report %d, want the same and at least build, ship, extra", seen, len(cost.Stages))
	}
	// Stage time: the report keeps the JSON of the dashboard's stage timing view, so a drift fails here.
	reportStages := map[string]any{}
	for _, rs := range sub(t, doc, "stages").([]any) {
		reportStages[rs.(map[string]any)["id"].(string)] = rs.(map[string]any)["timing"]
	}
	for _, s := range stages {
		stage := s.(map[string]any)
		sameJSON(t, "timing of "+stage["id"].(string), reportStages[stage["id"].(string)], stage["timing"])
	}

	issues, _ := json.Marshal(cost.Issues)
	var reportIssues, dashIssues []any
	_ = json.Unmarshal(issues, &reportIssues)
	dashIssues, _ = status["coverage_issues"].([]any)
	sameJSON(t, "coverage issues", reportIssues, dashIssues)

	// Analytics: the dashboard views of the same facts.
	models := getJSON(t, fx, "/api/usage/analytics?view=breakdown&group=model")
	agents := getJSON(t, fx, "/api/usage/analytics?view=breakdown&group=agent")
	charts := getJSON(t, fx, "/api/usage/analytics?view=charts")
	for _, key := range []string{"totals", "completeness", "health", "outside_run_total"} {
		sameJSON(t, "analytics "+key, sub(t, doc, "analytics", key), models[key])
	}
	sameJSON(t, "model rows", byKey(t, sub(t, doc, "analytics", "models")), byKey(t, sub(t, models, "breakdown", "rows")))
	sameJSON(t, "agent rows", byKey(t, sub(t, doc, "analytics", "agents")), byKey(t, sub(t, agents, "breakdown", "rows")))

	timeline := sub(t, doc, "analytics", "timeline").(map[string]any)
	liveTimeline := sub(t, charts, "charts", "timeline").(map[string]any)
	delete(timeline, "limit")
	delete(liveTimeline, "limit")
	sameJSON(t, "timeline", timeline, liveTimeline)

	sameJSON(t, "jobs", sub(t, doc, "analytics", "jobs"), allDashboardJobs(t, fx, ""))
	if jobs, _ := sub(t, doc, "analytics", "jobs").([]any); len(jobs) < 4 {
		t.Errorf("report has %d jobs, want the attempts of build, ship and extra", len(jobs))
	}

	// Stage scope: one stage, every attempt.
	stageDoc := reportDoc(t, fx.root, runID, "--stage", "build")
	scoped := getJSON(t, fx, "/api/usage/analytics?view=jobs&scope=stage&stage_id=build&limit=100")
	for _, key := range []string{"totals", "completeness"} {
		sameJSON(t, "stage analytics "+key, sub(t, stageDoc, "analytics", key), scoped[key])
	}
	sameJSON(t, "stage jobs", sub(t, stageDoc, "analytics", "jobs"), allDashboardJobs(t, fx, "&scope=stage&stage_id=build"))
}

// allDashboardJobs pages through the whole job list in start order.
func allDashboardJobs(t *testing.T, fx *modeFixture, filter string) []any {
	t.Helper()
	var all []any
	cursor := ""
	for page := 0; page < 20; page++ {
		path := "/api/usage/analytics?view=jobs&sort=started&dir=asc&limit=2" + filter
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := getJSON(t, fx, path)
		jobs := sub(t, resp, "jobs").(map[string]any)
		items, _ := jobs["items"].([]any)
		all = append(all, items...)
		next, _ := jobs["next_cursor"].(string)
		if next == "" {
			return all
		}
		cursor = next
	}
	t.Fatal("the job pages did not end")
	return nil
}

func runreportCost(t *testing.T, doc map[string]any) runreport.Cost {
	t.Helper()
	raw, err := json.Marshal(doc["cost"])
	if err != nil {
		t.Fatal(err)
	}
	var cost runreport.Cost
	if err := json.Unmarshal(raw, &cost); err != nil {
		t.Fatalf("decode cost: %v", err)
	}
	return cost
}

func costStage(c runreport.Cost, id string) (runreport.CostLine, bool) {
	for _, s := range c.Stages {
		if s.StageID == id {
			return s.CostLine, true
		}
	}
	return runreport.CostLine{}, false
}

// assertCostLine compares a report cost line with the CostView the dashboard shows for the same group.
func assertCostLine(t *testing.T, what string, got *runreport.CostLine, dashboard any) {
	t.Helper()
	if dashboard == nil {
		if got != nil {
			t.Errorf("%s: the report has a line the dashboard does not", what)
		}
		return
	}
	if got == nil {
		t.Errorf("%s: the dashboard has a line the report does not", what)
		return
	}
	raw, _ := json.Marshal(dashboard)
	var want accounting.CostView
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("%s: decode dashboard cost: %v", what, err)
	}
	wantModels := slices.Sorted(slices.Values(want.Models))
	checks := []struct {
		name      string
		got, want any
	}{
		{"invocations", got.Invocations, want.Metered + want.Unmetered},
		{"metered", got.Metered, want.Metered},
		{"unmetered", got.Unmetered, want.Unmetered},
		{"unpriced", got.Unpriced, want.Unpriced},
		{"models", got.Models, wantModels},
		{"phases", got.Phases, want.Phases},
		{"tokens.total", got.Tokens.Total, want.TotalTokens},
		{"tokens.uncached_input", got.Tokens.UncachedInput, want.UncachedInput},
		{"tokens.output", got.Tokens.Output, want.Output},
		{"tokens.cache_read", got.Tokens.CacheRead, want.CacheRead},
		{"tokens.cache_write_total", got.Tokens.CacheWriteTotal, want.CacheWriteTotal},
		{"money.display_cost", got.Money.DisplayCost, want.DisplayCost},
		{"money.coverage", got.Money.Coverage, want.Coverage},
		{"money.estimated_cost_usd", got.Money.EstimatedCostUSD, want.EstimatedCostUSD},
		{"money.priced_invocations", got.Money.PricedInvocations, want.PricedInvocations},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s %s = %v, dashboard %v", what, c.name, c.got, c.want)
		}
	}
}

// The offline meta repeats four rules of the engine, so a drift must fail here: an interrupted run with a
// runtime stage is read both ways, without starting anything the second time.
func TestReportMetaMatchesEngineAnalyticsMeta(t *testing.T) {
	fx := newReportParityFixture(t)
	child := fx.start()
	runID := fx.waitReady(child)
	waitFor(t, "the second build attempt", func() bool { return slices.Contains(fx.agentRuns(), "build:2") })
	fx.commitExtraStage()
	fx.waitStage("extra", "pending")
	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d\n%s", code, interruptedExitCode, child.out.String())
	}

	seedAssistantTasks(t, fx.runDir(runID))

	src, err := runreport.Load(fx.runDir(runID), runreport.LoadOptions{Now: fixedReportNow})
	if err != nil {
		t.Fatal(err)
	}
	offline := runreport.OfflineMeta(src)
	for _, id := range []string{"helper:t-helper", "helper:t-nested", "run:t-run", "run:ops"} {
		if !slices.ContainsFunc(offline.Agents, func(a accounting.AgentMeta) bool { return a.ID == id }) {
			t.Fatalf("offline agents %+v lack %s", offline.Agents, id)
		}
	}
	if slices.ContainsFunc(offline.Agents, func(a accounting.AgentMeta) bool { return a.ID == "run:t-legacy" }) {
		t.Fatal("the legacy run agent must keep its run:<profile> identity")
	}
	if !slices.ContainsFunc(offline.Stages, func(s accounting.StageMeta) bool { return s.ID == "extra" }) {
		t.Fatalf("offline stages %+v lack the runtime stage", offline.Stages)
	}

	prev := rootDir
	rootDir = fx.root
	t.Cleanup(func() { rootDir = prev })
	f, err := flow.ParseFile(fx.flowPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProductConfig(filepath.Join(os.Getenv("HOME"), config.AfmDir), fmDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := engine.Open(context.Background(), engine.Spec{Flow: f, Config: cfg.Execution()}, engine.Options{
		Paths:    enginePaths(""),
		RunName:  f.Name,
		Policies: engine.Policies{HumanControl: true, AssistantAvailable: true},
		Stdout:   io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })
	live := run.Read().AnalyticsMeta()

	// LiveSince and Revision describe the process, not the run.
	live.LiveSince, live.Revision = time.Time{}, 0
	offline.LiveSince, offline.Revision = time.Time{}, 0
	if !reflect.DeepEqual(offline, live) {
		t.Errorf("offline meta differs from the engine's\noffline: %+v\nlive:    %+v", offline, live)
	}
	assertStageScopeKeepsHelpers(t, fx.runDir(runID))
}

// seedAssistantTasks writes what the engine would have recorded for a stage helper, a nested helper and a run
// agent of each generation (own id, and the legacy run:<profile> name), plus a finished job for each.
func seedAssistantTasks(t *testing.T, runDir string) {
	t.Helper()
	path := filepath.Join(runDir, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var last struct {
		Seq uint64    `json:"seq"`
		At  time.Time `json:"time"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}

	type record = map[string]any
	task := func(id, stage, profile, title, scope, parent string) record {
		payload := record{"profile_id": profile, "title": title, "scope": scope, "created_by": "user"}
		if parent != "" {
			payload["parent_task_id"] = parent
		}
		target := record{"task_id": id}
		if stage != "" {
			target["stage_id"] = stage
		}
		return record{"kind": "task_created", "target": target, "payload": payload}
	}
	participant := func(id string) record {
		return record{
			"kind": "mesh_participant_added", "target": record{"participant_id": id},
			"payload": record{"kind": "run_agent", "scope": "run", "display_name": id},
		}
	}
	records := []record{
		task("t-helper", "build", "reviewer", "Review", "stage", ""),
		task("t-nested", "build", "tester", "", "stage", "t-helper"),
		task("t-run", "", "qa", "Standing QA", "run", ""),
		participant("run:t-run"),
		task("t-legacy", "", "ops", "Ops", "run", ""),
		participant("run:ops"),
	}
	var out strings.Builder
	out.WriteString(strings.Join(lines, "\n") + "\n")
	for i, r := range records {
		r["seq"], r["time"] = last.Seq+uint64(i)+1, last.At.Add(time.Duration(i+1)*time.Millisecond)
		r["schema_version"], r["actor"] = 1, record{"kind": "system"}
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(line, '\n'))
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := agentjobs.Open(runDir, agentjobs.Header{RecordingStartedAt: last.At})
	if err != nil {
		t.Fatal(err)
	}
	jobs := []agentjobs.JobMeta{
		{JobID: "seed-helper", AgentID: "helper:t-helper", AgentKind: accounting.AgentKindHelper, StageID: "build", TaskID: "t-helper"},
		{JobID: "seed-nested", AgentID: "helper:t-nested", AgentKind: accounting.AgentKindHelper, StageID: "build", TaskID: "t-nested", ParentTaskID: "t-helper"},
		{JobID: "seed-run", AgentID: "run:t-run", AgentKind: accounting.AgentKindRunAgent, TaskID: "t-run"},
		{JobID: "seed-legacy", AgentID: "run:ops", AgentKind: accounting.AgentKindRunAgent, TaskID: "t-legacy"},
	}
	for i, meta := range jobs {
		at := last.At.Add(time.Duration(i+1) * time.Second)
		if err := store.Append(agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: at}); err != nil {
			t.Fatal(err)
		}
		fin := agentjobs.Record{Type: agentjobs.TypeFinished, JobMeta: agentjobs.JobMeta{JobID: meta.JobID}, At: at.Add(time.Second), Outcome: agentjobs.OutcomeOK, DurationMS: 1000}
		if err := store.Append(fin); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// assertStageScopeKeepsHelpers checks that a stage report keeps the stage's helpers and leaves run agents out.
func assertStageScopeKeepsHelpers(t *testing.T, runDir string) {
	t.Helper()
	src, err := runreport.Load(runDir, runreport.LoadOptions{Now: fixedReportNow})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := runreport.Build(src, runreport.Options{Mode: usageview.ModeFull, StageID: "build"})
	if err != nil {
		t.Fatal(err)
	}
	var jobIDs []string
	for _, job := range rep.Analytics.Jobs {
		jobIDs = append(jobIDs, job.ID)
	}
	for _, want := range []string{"seed-helper", "seed-nested"} {
		if !slices.Contains(jobIDs, want) {
			t.Errorf("stage jobs %v lack %s", jobIDs, want)
		}
	}
	for _, unwanted := range []string{"seed-run", "seed-legacy"} {
		if slices.Contains(jobIDs, unwanted) {
			t.Errorf("stage jobs %v include run-wide %s", jobIDs, unwanted)
		}
	}
}
