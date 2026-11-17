package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/pkg/mcpserver"
	"github.com/akopichin/afm/pkg/server"
)

const (
	hostSessionTimeout = 30 * time.Minute
	runsGroup          = "runs"
	readOnlyGroup      = "read"
	statusTimeout      = 3 * time.Second
)

// HostGroups are the permissions of the host's own MCP endpoint: the write groups of a single run's endpoint
// (applied to forwarded calls) plus runs, which allows starting, attaching, cancelling and releasing.
type HostGroups struct {
	Child mcpserver.Groups
	Runs  bool
}

// AllHostGroups enables every group, the default.
func AllHostGroups() HostGroups { return HostGroups{Child: mcpserver.AllGroups(), Runs: true} }

// ParseHostGroups reads "reply,stages,runs"; read alone means read-only.
func ParseHostGroups(csv string) (HostGroups, error) {
	var rest []string
	var out HostGroups
	for _, name := range strings.Split(csv, ",") {
		switch name = strings.TrimSpace(name); name {
		case "":
		case runsGroup:
			out.Runs = true
		default:
			rest = append(rest, name)
		}
	}
	if out.Runs && len(rest) == 1 && rest[0] == readOnlyGroup {
		return HostGroups{}, fmt.Errorf("%q cannot be combined with other MCP permission groups", readOnlyGroup)
	}
	child, err := mcpserver.ParseGroups(strings.Join(rest, ","))
	if err != nil {
		return HostGroups{}, fmt.Errorf("%w (the host also accepts %s)", err, runsGroup)
	}
	out.Child = child
	return out, nil
}

// GatewayConfig wires the host's MCP endpoint.
type GatewayConfig struct {
	Inventory  *Inventory
	Launcher   *Launcher
	Controller *Controller
	Sessions   *Sessions
	Groups     HostGroups
	Version    string
}

// Gateway is the host's MCP endpoint: its own run-management tools plus the tools and resources of every
// owned run, forwarded to the run's own endpoint under a run_id.
type Gateway struct {
	cfg    GatewayConfig
	server *sdkmcp.Server
	http   *sdkmcp.StreamableHTTPHandler

	mu      sync.Mutex
	watched map[*sdkmcp.ServerSession]bool // host sessions whose disconnect releases their subscriptions
	// unrecorded are the subscribes in flight by URI: the SDK records their subscriber only after it answers them.
	unrecorded map[string]map[*unrecordedSubscriber]struct{}
	// changing are the subscribes and unsubscribes in flight by client and URI; the channel closes when one ends.
	changing map[subscriptionKey]chan struct{}
	// subscribed runs after a subscription was made, before the SDK records its subscriber; tests hold it there.
	subscribed func()
}

// NewGateway registers the tools and resources.
func NewGateway(cfg GatewayConfig) *Gateway {
	g := &Gateway{cfg: cfg}
	g.server = sdkmcp.NewServer(&sdkmcp.Implementation{Name: "afm-serve", Version: cfg.Version}, &sdkmcp.ServerOptions{
		SubscribeHandler:   g.subscribe,
		UnsubscribeHandler: g.unsubscribe,
	})
	g.server.AddReceivingMiddleware(g.recordSubscribers)
	cfg.Sessions.Updated = g.resourceUpdated
	g.registerHostTools()
	g.registerForwardedTools()
	g.registerResources()
	g.http = sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return g.server },
		&sdkmcp.StreamableHTTPOptions{SessionTimeout: hostSessionTimeout})
	return g
}

// Handler serves /mcp behind the loopback guard.
func (g *Gateway) Handler() http.Handler { return server.LocalGuard(g.http) }

// Close ends the sessions to runs and then the host's client sessions; the runs themselves keep going. The run
// sessions go first: closing a host session waits for its running calls, and a forwarded wait only ends when
// its run session does.
func (g *Gateway) Close() {
	g.cfg.Sessions.Close()
	for ss := range g.server.Sessions() {
		_ = ss.Close()
	}
}

// toolFailure is the structured body of a failed call, shaped like the failures of a run's own endpoint.
type toolFailure struct {
	Code         string   `json:"code"`
	Kind         string   `json:"kind"`
	Message      string   `json:"message"`
	CurrentRunID string   `json:"current_run_id,omitempty"`
	Liveness     Liveness `json:"liveness,omitempty"`
	RunStatus    string   `json:"run_status,omitempty"`
}

func (f toolFailure) result() *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		IsError:           true,
		StructuredContent: f,
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: f.Code + ": " + f.Message}},
	}
}

func ok(v any) *sdkmcp.CallToolResult {
	raw, err := json.Marshal(v)
	if err != nil {
		return failure(err)
	}
	return &sdkmcp.CallToolResult{
		StructuredContent: json.RawMessage(raw),
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(raw)}},
	}
}

const (
	codePreconditionFailed = "precondition_failed"
	codeNotManaged         = "not_managed"
	codeRequestConflict    = "request_conflict"
	codeNotAllowed         = "not_allowed"
	codeRunChanged         = "run_changed"
	codeOutcomeUnknown     = "outcome_unknown"
	codeWaitCapacity       = "wait_capacity"
	codeNotAttached        = "not_attached"
)

const (
	kindInvalid  = "invalid"
	kindConflict = "conflict"
	kindNotFound = "not_found"
)

// failure names an error the way clients branch on it.
func failure(err error) *sdkmcp.CallToolResult {
	var (
		changed     *RunChangedError
		unavailable *RunUnavailableError
		unknown     *OutcomeUnknownError
		capacity    *WaitCapacityError
		admission   *AdmissionError
		pre         *PreconditionError
		locked      *LockedError
	)
	f := toolFailure{Code: "internal", Kind: "internal", Message: err.Error()}
	switch {
	case errors.As(err, &changed):
		f.Code, f.Kind, f.CurrentRunID = codeRunChanged, kindConflict, changed.CurrentRunID
	case errors.As(err, &unavailable):
		f.Code, f.Kind, f.Liveness = "run_unavailable", kindConflict, unavailable.Liveness
	case errors.As(err, &unknown):
		f.Code, f.Kind = codeOutcomeUnknown, kindConflict
	case errors.As(err, &capacity):
		f.Code, f.Kind = codeWaitCapacity, kindConflict
	case errors.As(err, &admission):
		f.Code, f.Kind = admission.Code, kindConflict
	case errors.As(err, &pre):
		f.Code, f.Kind, f.RunStatus = codePreconditionFailed, kindConflict, pre.RunStatus
	case errors.As(err, &locked):
		f.Code, f.Kind = "host_locked", kindConflict
	case errors.Is(err, ErrRunNotFound):
		f.Code, f.Kind = kindNotFound, kindNotFound
	case errors.Is(err, ErrNotOwned):
		f.Code, f.Kind = codeNotAttached, kindConflict
	case errors.Is(err, ErrNotManaged):
		f.Code, f.Kind = codeNotManaged, kindConflict
	case errors.Is(err, ErrRequestConflict):
		f.Code, f.Kind = codeRequestConflict, kindConflict
	case errors.Is(err, ErrInvalidRequest):
		f.Code, f.Kind = kindInvalid, kindInvalid
	default:
		// Anything else stays "internal" with the error's own message.
	}
	return f.result()
}

func notAllowed(what string) *sdkmcp.CallToolResult {
	return toolFailure{Code: codeNotAllowed, Kind: kindConflict, Message: what + " needs the " + runsGroup + " group, which this host was not started with"}.result()
}
