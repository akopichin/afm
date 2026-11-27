package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/pkg/mcpserver"
	"github.com/akopichin/afm/pkg/runreport"
	"github.com/akopichin/afm/pkg/usageview"
)

const (
	toolRunsList = "afm_runs_list"
	toolRunStart = "afm_run_start"
	toolAttach   = "afm_run_attach"
	toolCancel   = "afm_run_cancel"
	toolRelease  = "afm_run_release"
	toolReport   = "afm_run_report"

	actionAttach = "attach"
	actionReport = "report"
)

type emptyInput struct{}

type startInput struct {
	MatchingImage bool     `json:"matching_image,omitempty" jsonschema:"use the Desktop host matching image for this launch"`
	RequestID     string   `json:"request_id" jsonschema:"your idempotency key: repeating it with the same parameters returns the same launch, with other parameters it is refused"`
	Project       string   `json:"project" jsonschema:"project id from serve.yaml"`
	Pipeline      string   `json:"pipeline,omitempty" jsonschema:"pipeline id from serve.yaml; give this or flow"`
	Flow          string   `json:"flow,omitempty" jsonschema:"flow file name inside the project's .afm/flows; give this or pipeline"`
	Mode          string   `json:"mode" jsonschema:"core (HTTP API only) or dashboard (API and Web UI)"`
	Worktree      string   `json:"worktree,omitempty" jsonschema:"worktree name; a second run of the same flow in the same project needs its own"`
	NoWorktree    bool     `json:"no_worktree,omitempty" jsonschema:"run in the project checkout even if the flow or config enables worktrees; not with worktree"`
	MCPAllow      []string `json:"mcp_allow,omitempty" jsonschema:"permissions of the run's own MCP: reply, stages, approve, budget, or read; default is what the host allows"`
	Task          string   `json:"task,omitempty" jsonschema:"starting prompt text, appended to the flow's prompt"`
}

type attachInput struct {
	Endpoint string `json:"endpoint" jsonschema:"loopback URL of the run's HTTP API, e.g. http://127.0.0.1:8080"`
}

type runIDInput struct {
	RunID string `json:"run_id" jsonschema:"the run_id from afm_runs_list"`
}

type reportInput struct {
	RunID      string `json:"run_id" jsonschema:"the run_id from afm_runs_list; a finished run is read from its files"`
	InstanceID string `json:"instance_id,omitempty" jsonschema:"select the instance_id from afm_runs_list when run IDs may overlap"`
	StageID    string `json:"stage_id,omitempty" jsonschema:"limit the report to one stage"`
}

// launchEntry is a launch as clients see it; the receipt's internals stay on disk.
type launchEntry struct {
	LaunchID   string `json:"launch_id"`
	State      string `json:"state"`
	RunID      string `json:"run_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

func launchOf(r Receipt) launchEntry {
	return launchEntry{LaunchID: r.LaunchID, State: r.State, RunID: r.RunID, InstanceID: r.InstanceID, ExitCode: r.ExitCode, Error: r.Error}
}

// runEntry is one row of afm_runs_list.
type runEntry struct {
	RunID             string       `json:"run_id,omitempty"`
	InstanceID        string       `json:"instance_id,omitempty"`
	Ownership         Ownership    `json:"ownership"`
	Liveness          Liveness     `json:"liveness,omitempty"`
	Mode              string       `json:"mode,omitempty"`
	ProjectRoot       string       `json:"project_root,omitempty"`
	RunName           string       `json:"run_name,omitempty"`
	APIURL            string       `json:"api_url,omitempty"`
	Docker            bool         `json:"docker,omitempty"`
	PID               int          `json:"pid,omitempty"`
	Launch            *launchEntry `json:"launch,omitempty"`
	RunStatus         string       `json:"run_status,omitempty"`
	AttentionRequired *bool        `json:"attention_required,omitempty"`
	StatusNote        string       `json:"status_note,omitempty"`
	Actions           []string     `json:"actions"`
}

var (
	readAnnotations   = &sdkmcp.ToolAnnotations{ReadOnlyHint: true}
	idempotentRuns    = &sdkmcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: ptr(false)}
	destructiveAction = &sdkmcp.ToolAnnotations{DestructiveHint: ptr(true)}
)

func ptr[T any](v T) *T { return &v }

func (g *Gateway) registerHostTools() {
	s := g.server
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: toolRunsList, Annotations: readAnnotations, Description: "List the runs this host knows: " +
		"started by the host (managed), attached, or only discovered. Shows identity, liveness (active, unreachable, stale, exited, " +
		"identity_mismatch), project, endpoint, launch state, run status and the actions open now. Read-only."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ emptyInput) (*sdkmcp.CallToolResult, any, error) {
			views, err := g.cfg.Inventory.List(ctx)
			if err != nil {
				return failure(err), nil, nil
			}
			rows := make([]runEntry, 0, len(views))
			for _, v := range views {
				rows = append(rows, g.entry(ctx, v))
			}
			return ok(map[string]any{"runs": rows}), nil, nil
		})
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: toolReport, Annotations: readAnnotations, Description: "Usage, cost and stage-time report of a run " +
		"the host started or attached, read from its files, so it works after the run's process exited. Read-only."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in reportInput) (*sdkmcp.CallToolResult, any, error) {
			report, err := g.report(ctx, in)
			if err != nil {
				return failure(err), nil, nil
			}
			return ok(report), nil, nil
		})
	if !g.cfg.Groups.Runs {
		return
	}
	g.registerRuntime()
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: toolRunStart, Annotations: idempotentRuns, Description: "Start a run from a project and a " +
		"pipeline or flow that serve.yaml allows. Returns at once with the launch (state starting); follow it with afm_runs_list. The same " +
		"request_id returns the same launch and never starts a second run. Needs group runs."},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, in startInput) (*sdkmcp.CallToolResult, any, error) {
			if !g.cfg.Groups.Runs {
				return notAllowed(toolRunStart), nil, nil
			}
			// The launcher takes no request context: a cancelled call must not kill accepted work.
			receipt, err := g.cfg.Launcher.Start(StartRequest(in))
			if err != nil {
				return failure(err), nil, nil
			}
			return ok(launchOf(receipt)), nil, nil
		})
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: toolAttach, Annotations: idempotentRuns, Description: "Attach a run that was started " +
		"elsewhere: loopback endpoints only, identity checked. The host then reads and controls it through MCP but never starts, " +
		"signals or stops its process. Needs group runs."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in attachInput) (*sdkmcp.CallToolResult, any, error) {
			if !g.cfg.Groups.Runs {
				return notAllowed(toolAttach), nil, nil
			}
			attached, err := g.cfg.Inventory.Attach(ctx, in.Endpoint)
			if err != nil {
				return toolFailure{Code: kindInvalid, Kind: kindInvalid, Message: err.Error()}.result(), nil, nil
			}
			return ok(map[string]any{"run_id": attached.RunID, "instance_id": attached.InstanceID, "api_url": attached.Endpoint}), nil, nil
		})
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: toolCancel, Annotations: destructiveAction, Description: "Stop a run this host started " +
		"while its workflow is active: interrupt, then kill after a grace period (Docker runs are only interrupted and may report " +
		"stop_pending). Attached runs are refused. Needs group runs."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runIDInput) (*sdkmcp.CallToolResult, any, error) {
			return g.control(ctx, toolCancel, in.RunID, g.cfg.Controller.Cancel)
		})
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: toolRelease, Annotations: destructiveAction, Description: "End a run this host started " +
		"whose workflow finished but whose process still holds the API. The run's own files stay. Needs group runs."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runIDInput) (*sdkmcp.CallToolResult, any, error) {
			return g.control(ctx, toolRelease, in.RunID, g.cfg.Controller.Release)
		})
}

func (g *Gateway) control(ctx context.Context, tool, runID string, act func(context.Context, string) (ControlResult, error)) (*sdkmcp.CallToolResult, any, error) {
	if !g.cfg.Groups.Runs {
		return notAllowed(tool), nil, nil
	}
	res, err := act(ctx, runID)
	if err != nil {
		return failure(err), nil, nil
	}
	return ok(res), nil, nil
}

// entry describes one run, asking its own endpoint for the status when it is owned and alive.
func (g *Gateway) entry(ctx context.Context, v RunView) runEntry {
	e := runEntry{
		RunID: v.RunID, InstanceID: v.InstanceID, Ownership: v.Ownership, Liveness: v.Liveness, Mode: v.Mode,
		ProjectRoot: v.Project.Root, RunName: v.Project.RunName, APIURL: v.Endpoint, Docker: v.Docker, PID: v.PID, Actions: []string{},
	}
	if v.Launch != nil {
		launch := launchOf(*v.Launch)
		e.Launch = &launch
	}
	live := v.Liveness == LiveActive || v.Liveness == LiveActiveOrphaned
	switch {
	case v.Ownership == OwnershipDiscovered:
		if live && g.cfg.Groups.Runs {
			e.Actions = append(e.Actions, actionAttach)
		}
		return e
	case live:
		statusCtx, cancel := context.WithTimeout(ctx, statusTimeout)
		st, _, err := g.cfg.Sessions.Status(statusCtx, v)
		cancel()
		if err != nil {
			e.StatusNote = "status unavailable: " + err.Error()
			break
		}
		e.RunStatus = st.RunStatus
		e.AttentionRequired = &st.AttentionRequired
		if v.Ownership == OwnershipManaged && g.cfg.Groups.Runs {
			if runFinished(st.RunStatus) {
				e.Actions = append(e.Actions, ActionRelease)
			} else {
				e.Actions = append(e.Actions, ActionCancel)
			}
		}
	default:
		e.RunStatus = finalStatus(v)
	}
	if runDirOf(v, v.RunID) != "" {
		e.Actions = append(e.Actions, actionReport)
	}
	return e
}

// finalStatus reads the status a run ended with from its files; empty when they cannot be read.
func finalStatus(v RunView) string {
	dir := runDirOf(v, v.RunID)
	if dir == "" {
		return ""
	}
	src, err := runreport.Load(dir, runreport.LoadOptions{})
	if err != nil {
		return ""
	}
	return string(src.State.RunStatus)
}

// runDirOf is where runID's files are, from the host's own records only; empty when unknown or unsafe.
func runDirOf(v RunView, runID string) string {
	if v.RunsDir == "" || !filepath.IsAbs(v.RunsDir) || !namePattern.MatchString(runID) {
		return ""
	}
	dir := filepath.Join(v.RunsDir, runID)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

func (g *Gateway) report(ctx context.Context, in reportInput) (*runreport.Report, error) {
	v, err := g.cfg.Inventory.findReportRun(ctx, in.RunID, in.InstanceID)
	if err != nil {
		return nil, err
	}
	dir := runDirOf(v, in.RunID)
	if dir == "" {
		return nil, fmt.Errorf("the files of run %s are not known to this host", in.RunID)
	}
	src, err := runreport.Load(dir, runreport.LoadOptions{Now: time.Now})
	if err != nil {
		return nil, err
	}
	return runreport.Build(src, runreport.Options{Mode: usageview.ModeNoMoney, StageID: in.StageID})
}

// registerForwardedTools offers the tools of a single run's endpoint for every run, selected by run_id.
func (g *Gateway) registerForwardedTools() {
	for _, spec := range mcpserver.Catalog() {
		if !spec.Offered(g.cfg.Groups.Child) {
			continue
		}
		tool := spec.Tool()
		tool.InputSchema = withRunID(spec.InputSchema)
		g.server.AddTool(tool, g.forwarder(spec))
	}
}

// withRunID returns the schema of a tool with run_id as a required argument; tools that already take it are
// left alone.
func withRunID(schema *jsonschema.Schema) *jsonschema.Schema {
	if _, has := schema.Properties["run_id"]; has {
		return schema
	}
	out := *schema
	out.Properties = map[string]*jsonschema.Schema{"run_id": {Type: "string", Description: "the run_id from afm_runs_list"}}
	for name, prop := range schema.Properties {
		out.Properties[name] = prop
	}
	out.Required = append([]string{"run_id"}, schema.Required...)
	return &out
}

// forwarder sends one call to the run named by its run_id. The group is checked on every call, and a call that
// fails in transit is never repeated unless it only reads.
func (g *Gateway) forwarder(spec mcpserver.ToolSpec) sdkmcp.ToolHandler {
	readOnly := spec.Annotations != nil && spec.Annotations.ReadOnlyHint
	return func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		if !spec.Offered(g.cfg.Groups.Child) {
			return toolFailure{Code: codeNotAllowed, Kind: kindConflict, Message: spec.Name + " is not allowed by this host's permissions"}.result(), nil
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return toolFailure{Code: kindInvalid, Kind: kindInvalid, Message: "arguments must be a JSON object"}.result(), nil
		}
		var runID string
		if err := json.Unmarshal(args["run_id"], &runID); err != nil || runID == "" {
			return toolFailure{Code: kindInvalid, Kind: kindInvalid, Message: "run_id is required"}.result(), nil
		}
		v, err := g.cfg.Inventory.Resolve(ctx, runID)
		if err != nil {
			return failure(err), nil
		}
		if !spec.RunBound {
			delete(args, "run_id")
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return failure(err), nil
		}
		res, err := g.cfg.Sessions.CallTool(ctx, v, spec.Name, raw, readOnly)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return failure(err), nil
		}
		return res, nil
	}
}
