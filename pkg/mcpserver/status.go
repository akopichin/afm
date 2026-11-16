package mcpserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server"
)

// stageSummary is the compact per-stage line of the status; the dashboard-only fields stay out.
type stageSummary struct {
	ID         string            `json:"id"`
	Name       string            `json:"name,omitempty"`
	Status     state.StageStatus `json:"status"`
	PausedFrom state.StageStatus `json:"paused_from,omitempty"`
	Verify     string            `json:"verify,omitempty"`
	Quota      *server.QuotaView `json:"quota,omitempty"`
	Artifacts  []artifactRef     `json:"artifacts,omitempty"`
}

// statusResult is the body of afm_run_status and of the status resource.
type statusResult struct {
	RunID    string `json:"run_id"`
	FlowName string `json:"flow_name,omitempty"`
	// Cursor is what afm_run_wait takes to block until something changes.
	Cursor string `json:"cursor"`
	// LastSeq is the journal position; it pages afm_events_read and says nothing about attention.
	LastSeq uint64 `json:"last_seq"`
	attentionModel
	Stages []stageSummary `json:"stages"`
	Cost   costSummary    `json:"cost"`
	// Permissions lists the write groups this endpoint was started with; reading is always on.
	Permissions []Group `json:"permissions"`
}

// costSummary reports tokens always and money only when the run shows it. Anything unknown is null, never 0.
type costSummary struct {
	Health   string       `json:"health"`
	HasData  bool         `json:"has_data"`
	Coverage string       `json:"coverage,omitempty"`
	Tokens   *tokenTotals `json:"tokens"`
	// CostUSD is absent unless accounting.show_money is on; then it is null until an invocation was priced. It is
	// an interface so a hidden field (nil) differs from a visible unknown one (a nil *float64, serialized null).
	CostUSD any `json:"estimated_cost_usd,omitempty"`
	// Issues counts coverage problems; they explain a partial or missing figure.
	Issues int `json:"coverage_issues"`
}

type tokenTotals struct {
	Total         uint64 `json:"total"`
	UncachedInput uint64 `json:"uncached_input"`
	CacheRead     uint64 `json:"cache_read"`
	CacheWrite    uint64 `json:"cache_write"`
	Output        uint64 `json:"output"`
}

// buildStatus reads the run once and derives every part of the status from that one snapshot.
func (s *Service) buildStatus(ctx context.Context) statusResult {
	rs := s.cfg.Reader.Snapshot()
	var bundle accounting.CostBundle
	if s.cfg.Cost != nil {
		bundle = s.cfg.Cost.CostSnapshot()
	}
	attention := s.buildAttention(ctx, rs)
	res := statusResult{
		RunID:          s.runID,
		FlowName:       rs.FlowName,
		Cursor:         s.cursorOf(attention),
		LastSeq:        rs.LastSeq,
		attentionModel: attention,
		Stages:         s.stageSummaries(rs, bundle.Stages),
		Cost:           s.costOf(bundle),
		Permissions:    s.cfg.Groups.Names(),
	}
	if res.Permissions == nil {
		res.Permissions = []Group{}
	}
	return res
}

func (s *Service) stageSummaries(rs state.RunState, costs map[string]*accounting.CostView) []stageSummary {
	var configs map[string]server.StageConfig
	if s.cfg.Stages != nil {
		configs = s.cfg.Stages.StageConfigs()
	}
	views := server.BuildStageViews(rs, s.cfg.RunDir, configs, costs)
	out := make([]stageSummary, 0, len(views))
	for _, v := range views {
		sum := stageSummary{ID: v.ID, Name: v.Name, Status: v.Status, PausedFrom: v.PausedFrom, Quota: v.Quota}
		if v.Verify != nil {
			sum.Verify = v.Verify.Phase
		}
		sum.Artifacts = s.stageArtifacts(v.ID, v.Status)
		out = append(out, sum)
	}
	return out
}

func (s *Service) costOf(bundle accounting.CostBundle) costSummary {
	cost := costSummary{Health: string(bundle.Health), HasData: bundle.HasData, Issues: len(bundle.Issues)}
	if s.cfg.ShowMoney {
		cost.CostUSD = (*float64)(nil)
	}
	if s.cfg.Cost == nil {
		cost.Health = string(accounting.HealthUnavailable)
		return cost
	}
	run := bundle.Run
	if run == nil || !bundle.HasData {
		return cost
	}
	cost.Coverage = run.Coverage
	// Usage that no invocation metered is unknown, not zero.
	if run.Metered > 0 {
		cost.Tokens = &tokenTotals{
			Total: run.TotalTokens, UncachedInput: run.UncachedInput, CacheRead: run.CacheRead,
			CacheWrite: run.CacheWriteTotal, Output: run.Output,
		}
	}
	if s.cfg.ShowMoney && run.PricedInvocations > 0 {
		usd := run.EstimatedCostUSD
		cost.CostUSD = &usd
	}
	return cost
}

// costNow is the cost section alone, for the cost resource and for noticing changes.
func (s *Service) costNow() costSummary {
	var bundle accounting.CostBundle
	if s.cfg.Cost != nil {
		bundle = s.cfg.Cost.CostSnapshot()
	}
	return s.costOf(bundle)
}

// escapeSegment percent-encodes everything but RFC 3986 unreserved characters: the SDK matches resource URIs
// against RFC 6570 templates, whose {var} does not accept a bare ':' or '/'.
func escapeSegment(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func statusURI(runID string) string { return "afm://runs/" + escapeSegment(runID) + "/status" }
func costURI(runID string) string   { return "afm://runs/" + escapeSegment(runID) + "/cost" }

func planURI(runID, stageID, revision string) string {
	return "afm://runs/" + escapeSegment(runID) + "/stages/" + escapeSegment(stageID) + "/plans/" + escapeSegment(revision)
}

func artifactURI(runID, artifactID string) string {
	return "afm://runs/" + escapeSegment(runID) + "/artifacts/" + artifactID
}

func revisionString(rev int64) string { return strconv.FormatInt(rev, 10) }

// runStatusInput is empty: the endpoint is bound to one run, so there is nothing to select.
type runStatusInput struct{}

func (s *Service) registerStatusTool() {
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name: "afm_run_status",
		Description: "Read the run: run status, whether anything needs a person (attention_required), every pending action " +
			"with the tool actions allowed for it, stage list, token/cost totals and a cursor for afm_run_wait. " +
			"Read-only. There is no progress percentage on purpose.",
		Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ runStatusInput) (*sdkmcp.CallToolResult, any, error) {
		release, err := s.enter()
		if err != nil {
			return errorResult(err), nil, nil
		}
		defer release()
		return okResult(s.buildStatus(ctx)), nil, nil
	})
}
