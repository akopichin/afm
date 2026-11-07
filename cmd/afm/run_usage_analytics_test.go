package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm-engine/testkit/engine"
	implorchestrator "github.com/akopichin/afm-engine/testkit/orchestrator"
	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentjobs"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/pkg/server"
)

func appendFinishedJob(t *testing.T, jobs *agentjobs.Store, meta agentjobs.JobMeta, start time.Time) {
	t.Helper()
	records := []agentjobs.Record{
		{Type: agentjobs.TypeStarted, JobMeta: meta, At: start},
		{Type: agentjobs.TypeFinished, JobMeta: meta, At: start.Add(time.Minute), Outcome: agentjobs.OutcomeOK, DurationMS: 60_000},
	}
	for _, rec := range records {
		if err := jobs.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
}

// startAnalyticsDashboard serves a run with one stage job and one Assistant job, wired the way executeFlow wires them.
func startAnalyticsDashboard(t *testing.T, cfg config.Config) string {
	t.Helper()
	return startTestDashboard(t, cfg, func(sc *server.Config, runDir string, store *implstate.Store, orch *implorchestrator.Orchestrator) {
		acct, err := accounting.Open(runDir, accounting.NewResolver(cfg.Pricing))
		if err != nil {
			t.Fatal(err)
		}
		jobs := engine.OpenAgentJobs(runDir, store, io.Discard)
		sideDir := t.TempDir()
		sideAcct, err := accounting.Open(sideDir, accounting.NewResolver(cfg.Pricing))
		if err != nil {
			t.Fatal(err)
		}
		sideJobs := engine.OpenAgentJobs(sideDir, store, io.Discard)
		t.Cleanup(func() { _ = acct.Close(); _ = jobs.Close(); _ = sideAcct.Close(); _ = sideJobs.Close() })

		now := time.Now().Add(-time.Hour)
		appendFinishedJob(t, jobs, agentjobs.JobMeta{JobID: "stage-job", AgentID: "stage:work", AgentKind: accounting.AgentKindStage, StageID: "work", RootStageID: "work", Title: "Work · Autonomous execution"}, now)
		appendFinishedJob(t, sideJobs, agentjobs.JobMeta{JobID: "side-job", AgentID: "side:r1", AgentKind: accounting.AgentKindAssistant, Title: "Assistant · job 1"}, now)

		side := &accounting.AssistantSources{Usage: sideAcct, Jobs: sideJobs}
		sc.UsageAnalytics = newUsageAnalytics(acct, nil, jobs, orch, side)
	})
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// The analytics provider does not depend on the cost display gate: the capability stays on and the mode
// narrows from full to time only.
func TestUsageAnalytics_ModeFollowsAccountingDisplaySettings(t *testing.T) {
	enabled, disabled, money := true, false, true
	tests := []struct {
		name string
		acct config.AccountingConfig
		want server.UsageAnalyticsMode
	}{
		{"money shown", config.AccountingConfig{Enabled: &enabled, ShowMoney: &money}, server.UsageModeFull},
		{"tokens only", config.AccountingConfig{Enabled: &enabled}, server.UsageModeNoMoney},
		{"accounting hidden", config.AccountingConfig{Enabled: &disabled}, server.UsageModeTimeOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_ACCOUNTING", "")
			t.Setenv("AFM_ACCOUNTING_SHOW_MONEY", "")
			cfg := config.Config{Accounting: tc.acct}
			base := startAnalyticsDashboard(t, cfg)

			var status struct {
				Capabilities map[string]bool             `json:"capabilities"`
				Analytics    server.UsageAnalyticsStatus `json:"analytics"`
			}
			code, body := getBody(t, base+"/api/status")
			if code != http.StatusOK || json.Unmarshal([]byte(body), &status) != nil {
				t.Fatalf("status = %d %s", code, body)
			}
			if !status.Capabilities["usage_analytics"] || status.Analytics.Mode != tc.want || status.Analytics.Revision == "" {
				t.Fatalf("status = %+v, want the capability on in mode %s", status, tc.want)
			}
			// One stage job without a model; the Assistant's job counts neither as an agent nor as a model.
			if status.Analytics.Agents != 1 || status.Analytics.Models != 0 {
				t.Fatalf("status counts = %d agents, %d models, want 1 and 0 in mode %s", status.Analytics.Agents, status.Analytics.Models, tc.want)
			}

			code, body = getBody(t, base+"/api/usage/analytics?view=jobs")
			if code != http.StatusOK {
				t.Fatalf("analytics = %d %s", code, body)
			}
			if tc.want != server.UsageModeFull && strings.Contains(body, `"money"`) {
				t.Fatalf("mode %s leaks money: %s", tc.want, body)
			}
			if tc.want == server.UsageModeTimeOnly && strings.Contains(body, `"tokens"`) {
				t.Fatalf("time_only leaks tokens: %s", body)
			}
		})
	}
}

// The Assistant's own journal reaches the endpoint as a job outside the run total.
func TestUsageAnalytics_AssistantJobIsOutsideTheRunTotal(t *testing.T) {
	t.Setenv("AFM_ACCOUNTING", "")
	t.Setenv("AFM_ACCOUNTING_SHOW_MONEY", "")
	base := startAnalyticsDashboard(t, config.Config{})

	code, body := getBody(t, base+"/api/usage/analytics?view=jobs&limit=100")
	if code != http.StatusOK {
		t.Fatalf("analytics = %d %s", code, body)
	}
	var resp server.UsageAnalytics
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Totals.Jobs != 1 || resp.OutsideRunTotal == nil || resp.OutsideRunTotal.Jobs != 1 {
		t.Fatalf("totals = %+v outside = %+v, want 1 run job and 1 outside job", resp.Totals, resp.OutsideRunTotal)
	}
	outside := map[string]bool{}
	for _, job := range resp.Jobs.Items {
		outside[job.ID] = job.Agent.OutsideRunTotal
	}
	if len(outside) != 2 || outside["stage-job"] || !outside["side-job"] {
		t.Fatalf("jobs outside flags = %v", outside)
	}
}

type unavailableLedgerResponse struct {
	Health       server.UsageHealth `json:"health"`
	Completeness completenessBody   `json:"completeness"`
	Totals       totalsBody         `json:"totals"`
}

type completenessBody struct {
	Tokens *tokenReasons `json:"tokens"`
}

type tokenReasons struct {
	Reasons []string `json:"reasons"`
}

type totalsBody struct {
	AgentTime durationBody `json:"agent_time"`
}

type durationBody struct {
	DurationMS *int64 `json:"duration_ms"`
}

// A usage.jsonl that cannot be opened is reported as unavailable, not as accounting switched off, and the
// recorded time stays visible.
func TestUsageAnalytics_UnopenableLedgerIsUnavailableNotDisabled(t *testing.T) {
	t.Setenv("AFM_ACCOUNTING", "")
	t.Setenv("AFM_ACCOUNTING_SHOW_MONEY", "")
	enabled := true
	cfg := config.Config{Accounting: config.AccountingConfig{Enabled: &enabled}}
	base := startTestDashboard(t, cfg, func(sc *server.Config, runDir string, store *implstate.Store, orch *implorchestrator.Orchestrator) {
		// A directory in place of the ledger makes the initial open fail, as an unreadable file would.
		if err := os.Mkdir(filepath.Join(runDir, "usage.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		acct, acctErr := accounting.Open(runDir, accounting.NewResolver(cfg.Pricing))
		if acct != nil || acctErr == nil {
			t.Fatalf("Open = %v, %v, want the failure this test is about", acct, acctErr)
		}
		jobs := engine.OpenAgentJobs(runDir, store, io.Discard)
		t.Cleanup(func() { _ = jobs.Close() })
		appendFinishedJob(t, jobs, agentjobs.JobMeta{JobID: "stage-job", AgentID: "stage:work", AgentKind: accounting.AgentKindStage, StageID: "work", RootStageID: "work", Title: "Work"}, time.Now().Add(-time.Hour))
		sc.UsageAnalytics = newUsageAnalytics(acct, acctErr, jobs, orch, nil)
	})

	code, body := getBody(t, base+"/api/usage/analytics?view=jobs")
	var resp unavailableLedgerResponse
	if code != http.StatusOK || json.Unmarshal([]byte(body), &resp) != nil {
		t.Fatalf("analytics = %d %s", code, body)
	}
	if resp.Health.Usage != "unavailable" || resp.Completeness.Tokens == nil || !slices.Contains(resp.Completeness.Tokens.Reasons, "usage_store_unavailable") {
		t.Fatalf("usage health = %q, token completeness = %+v, want unavailable with usage_store_unavailable: %s", resp.Health.Usage, resp.Completeness.Tokens, body)
	}
	if resp.Totals.AgentTime.DurationMS == nil {
		t.Fatalf("the recorded time must stay reported: %s", body)
	}
}

// newUsageAnalytics builds the read model the way engine.Run.UsageAnalytics does, over a bare orchestrator.
func newUsageAnalytics(acct *accounting.Store, acctErr error, jobs *agentjobs.Store, orch *implorchestrator.Orchestrator, assistant *accounting.AssistantSources) *accounting.Analytics {
	return accounting.NewAnalytics(accounting.AnalyticsSources{
		Usage: acct, UsageOpenFailed: acct == nil && acctErr != nil, Jobs: jobs, Meta: orch.AnalyticsMeta, Assistant: assistant,
	})
}
