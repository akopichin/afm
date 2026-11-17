package mcpserver

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	toolRunStatus        = "afm_run_status"
	toolEventsRead       = "afm_events_read"
	toolArtifactRead     = "afm_artifact_read"
	toolRunWait          = "afm_run_wait"
	toolInteractionReply = "afm_interaction_reply"
	toolStageDecide      = "afm_stage_decide"
	toolBudgetAdjust     = "afm_budget_adjust"
	toolBudgetResume     = "afm_budget_resume"
)

// ToolSpec declares one MCP tool. Service registers its tools from this table and a host that forwards the
// tools of several runs reads the same table, so names, schemas and groups exist in one place.
type ToolSpec struct {
	Name        string
	Description string
	// Group must be enabled for the tool to be offered; empty means a read tool that is always on.
	Group Group
	// AltGroup also enables the tool: a decision is offered to approve and to stages alike.
	AltGroup    Group
	Annotations *sdkmcp.ToolAnnotations
	InputSchema *jsonschema.Schema
	// RunBound tools take run_id; read tools of a single-run endpoint do not.
	RunBound bool
}

// Offered reports whether the tool is available with the given groups.
func (t ToolSpec) Offered(g Groups) bool {
	return t.Group == "" || g.Has(t.Group) || (t.AltGroup != "" && g.Has(t.AltGroup))
}

// Tool is the SDK form of the spec.
func (t ToolSpec) Tool() *sdkmcp.Tool {
	return &sdkmcp.Tool{Name: t.Name, Description: t.Description, Annotations: t.Annotations, InputSchema: t.InputSchema}
}

func newSpec[In any](name, description string, group Group, annotations *sdkmcp.ToolAnnotations) ToolSpec {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic("mcpserver: input schema of " + name + ": " + err.Error())
	}
	_, runBound := schema.Properties["run_id"]
	return ToolSpec{Name: name, Description: description, Group: group, Annotations: annotations, InputSchema: schema, RunBound: runBound}
}

// Catalog lists every tool the service can offer. Each call builds fresh schemas, so callers may keep them.
func Catalog() []ToolSpec {
	readOnly := &sdkmcp.ToolAnnotations{ReadOnlyHint: true}
	control := &sdkmcp.ToolAnnotations{DestructiveHint: ptr(false)}
	idempotentControl := &sdkmcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: ptr(false)}
	decide := newSpec[decideInput](toolStageDecide,
		"Decide a stage: approve (needs group approve and expected_revision, the plan revision you read; it is "+
			"applied only if that exact revision is still current, otherwise approval_stale), revise (needs group "+
			"stages; sends feedback to the planner and is not idempotent: do not repeat it blindly) or retry (needs "+
			"group stages). Returns the stage state after the call.",
		GroupApprove, control)
	decide.AltGroup = GroupStages
	return []ToolSpec{
		newSpec[runStatusInput](toolRunStatus,
			"Read the run: run status, whether anything needs a person (attention_required), every pending action "+
				"with the tool actions allowed for it, stage list, token/cost totals and a cursor for afm_run_wait. "+
				"Read-only. There is no progress percentage on purpose.",
			"", readOnly),
		newSpec[eventsReadInput](toolEventsRead,
			"Read the stage transitions of the run journal in pages ordered by seq. Pass the previous next_seq as "+
				"after_seq to continue. This is history, not a wait: use afm_run_wait to block until something needs attention. Read-only.",
			"", readOnly),
		newSpec[artifactReadInput](toolArtifactRead,
			"Read one artifact by the opaque artifact_id that afm_run_status lists: a stage plan, an execution "+
				"summary, an image or a file the stage declares as an artifact. Text comes in pages (next_offset is null on the last one); "+
				"secrets are masked. Logs and dialog files are not readable. Read-only.",
			"", readOnly),
		newSpec[runWaitInput](toolRunWait,
			"Block until the run's attention model changes from the given cursor (a new question, approval, failure, "+
				"hold or a resolved one), or until timeout_seconds (default 30, at most 60). Returns the fresh status and "+
				"whether it changed. Pass back the returned cursor. reset=true means the cursor belonged to another run. "+
				"Cancelling the request does not affect the run. Read-only.",
			"", readOnly),
		newSpec[replyInput](toolInteractionReply,
			"Answer the question a stage is waiting on (phase and question_id come from its pending action). "+
				"Not idempotent: an answer that was applied is not applied twice, but a second call after a lost response "+
				"fails with question_not_pending, so read afm_run_status before retrying.",
			GroupReply, control),
		decide,
		newSpec[stageInput]("afm_stage_"+toolActionPause,
			"Stage control: "+toolActionPause+" the stage. Returns the stage state after the call. Needs group stages.",
			GroupStages, idempotentControl),
		newSpec[stageInput]("afm_stage_"+toolActionContinue,
			"Stage control: "+toolActionContinue+" the stage. Returns the stage state after the call. Needs group stages.",
			GroupStages, idempotentControl),
		newSpec[budgetAdjustInput](toolBudgetAdjust,
			"Set the absolute limit of a budget scope (tokens and/or money_usd) or remove it (unlimited). It does not "+
				"release held work; call afm_budget_resume for that. Fails with a conflict if expected_revision is stale. Needs group budget.",
			GroupBudget, control),
		newSpec[budgetResumeInput](toolBudgetResume,
			"Continue a scope held by a budget limit without changing the limit. Needs group budget.",
			GroupBudget, control),
	}
}

func catalogByName() map[string]ToolSpec {
	specs := map[string]ToolSpec{}
	for _, spec := range Catalog() {
		specs[spec.Name] = spec
	}
	return specs
}

// ResourceTemplates lists the resources of a run as URI templates; run_id selects the run.
func ResourceTemplates() []*sdkmcp.ResourceTemplate {
	return []*sdkmcp.ResourceTemplate{
		{
			URITemplate: "afm://runs/{run_id}/status", Name: "run status", MIMEType: jsonMIME,
			Description: "Run status, pending actions and cursor; subscribe to be told when it changes.",
		},
		{
			URITemplate: "afm://runs/{run_id}/cost", Name: "run cost", MIMEType: jsonMIME,
			Description: "Token and cost totals of the run; subscribe to be told when they change.",
		},
		{
			URITemplate: "afm://runs/{run_id}/stages/{stage_id}/plans/{revision}", Name: "stage plan", MIMEType: "text/markdown",
			Description: "The plan of a stage at the revision a client reviewed; a revision that is no longer current is not found.",
		},
		{
			URITemplate: "afm://runs/{run_id}/artifacts/{artifact_id}", Name: "artifact",
			Description: "An artifact listed by afm_run_status: text (first page) or an image.",
		},
	}
}

// addTool registers the cataloged tool when the service's groups offer it. The handler is the only part that
// stays with the tool's code.
func addTool[In any](s *Service, name string, handler func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, any, error)) {
	spec := s.specs[name]
	if !spec.Offered(s.cfg.Groups) {
		return
	}
	sdkmcp.AddTool(s.server, spec.Tool(), handler)
}
