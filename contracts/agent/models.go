// Package executor defines the values exchanged with the execution engine.
package agent

import (
	"context"
	"time"
)

type ActivityEvent struct {
	Kind         ActivityKind
	InvocationID string
	ItemID       string
	Revision     int
	Partial      bool
	LiveOnly     bool
	Final        bool
	Tool         string
	Detail       string
	Outcome      ActivityOutcome
	ExitCode     *int
	StartedAt    time.Time
	FinishedAt   time.Time
}

type ActivityKind string

type ActivityOutcome string

type BrowserMCP struct {
	ServerName   string
	LauncherPath string
	WSEndpoint   string
	Token        string
	AppURL       string
	AppPort      int
	Roots        []string
	Codex        bool
}

type CLIReportedError struct {
	Subtype string
	Message string
	Cause   error `json:"-"`
}

type HelperOutcome struct {
	ProcessOK             bool
	Interrupted           bool
	TimedOut              bool
	ProcessErr            error
	ProtocolErr           error
	RawText               string
	StructuredPayload     bool
	NativeFallback        NativeSchemaFallback
	NativeStarted         bool
	NativeEnforced        bool
	TextBeforeEnforcement string
}

type LimitWarning struct {
	Provider    string
	Window      string
	ResetAt     time.Time
	Utilization float64
}

type MeshDescriptor struct {
	URL           string `json:"url"`
	Token         string `json:"token"`
	ParticipantID string `json:"participant_id"`
}

type NativeSchema struct {
	Runtime SchemaRuntime
	Schema  string
}

type NativeSchemaFallback string

type Observation struct {
	RunID       string
	StageID     string
	ExecutionID string
	Seq         int
	ID          string
	Type        ObservationType
	Complete    bool
	Tool        string
	Content     string
	Actor       string
	CausesID    string
}

type ObservationType string

type RunMode int

type Runner interface {
	RunAgent(ctx context.Context, agentType string, stageName string, prompt string, logFile string) error
	RunJSONQuery(ctx context.Context, prompt string) ([]byte, error)
	RunPlanning(ctx context.Context, stageName string, prompt string, outFile string, logFile string) error
}

type SchemaRuntime string

type StopDecision struct {
	Block  bool
	Reason string
}

type StopHookDescriptor struct {
	URL        string `json:"url"`
	Token      string `json:"token"`
	Invocation string `json:"invocation"`
}

type StopHookRuntime string

type StopHookUnavailable string

type StopRequest struct {
	RequestID  string
	HookActive bool
}

type TerminalResultError struct {
	Subtype        string
	Message        string
	APIErrorStatus int
}

type TranscriptItem struct {
	Text      string
	AskUserID string
}

type UsageLimitError struct {
	Provider string
	ResetAt  time.Time
	Window   string
	Message  string
	NoReset  bool
	Source   string
	Progress bool
	Cause    error `json:"-"`
}

const KindInvocationFinished ActivityKind = "invocation_finished"
const KindMessage ActivityKind = "message"
const KindToolFinished ActivityKind = "tool_finished"
const KindToolStarted ActivityKind = "tool_started"
const ModeAuthor RunMode = 0
const ModeHelper RunMode = 2
const ModeVerify RunMode = 1
const NativeSchemaUnknownRuntime NativeSchemaFallback = "unknown_runtime"
const NativeSchemaUnsupportedShape NativeSchemaFallback = "unsupported_shape"
const ObservationAssistantMessage ObservationType = "assistant_message"
const ObservationCheckpoint ObservationType = "checkpoint"
const ObservationToolAction ObservationType = "tool_action"
const OutcomeCompleted ActivityOutcome = "completed"
const OutcomeFailed ActivityOutcome = "failed"
const OutcomeInterrupted ActivityOutcome = "interrupted"
const OutcomeTimedOut ActivityOutcome = "timed_out"
const OutcomeUsageLimit ActivityOutcome = "usage_limit"
const OutputSchemaFileEnv = "AFM_OUTPUT_SCHEMA_FILE"
const SchemaRuntimeClaude SchemaRuntime = "claude"
const SchemaRuntimeCodex SchemaRuntime = "codex"
const SchemaRuntimeOpenAIAgent SchemaRuntime = "openai-agent"
const StopHookBridgeFailed StopHookUnavailable = "bridge_failed"
const StopHookClaude StopHookRuntime = "claude"
const StopHookCodex StopHookRuntime = "codex"
const StopHookCodexHooksEnv = "AFM_STOP_HOOK_CODEX_HOOKS"
const StopHookCodexStateEnv = "AFM_STOP_HOOK_CODEX_STATE"
const StopHookDescriptorEnv = "AFM_STOP_HOOK_DESCRIPTOR"
const StopHookNotAsked StopHookUnavailable = "not_asked"
const StopHookSettingsConflict StopHookUnavailable = "settings_conflict"
const StreamExtensionEnv = "AFM_STREAM_EXTENSION"
const UsageLimitSourceStructured = "structured"
const UsageLimitSourceText = "text"

func (e *CLIReportedError) Error() string {
	msg := "CLI reported an error"
	if e.Subtype != "" {
		msg += " (" + e.Subtype + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func (e *CLIReportedError) Unwrap() error { return e.Cause }

func (e *UsageLimitError) Error() string { return "usage limit reached: " + e.Message }

func (e *TerminalResultError) Error() string {
	msg := "agent finished with an error result"
	if e.Subtype != "" {
		msg += " (" + e.Subtype + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Unwrap отдаёт исходную ошибку запуска (например, exit status 1); она может быть nil.
func (e *UsageLimitError) Unwrap() error { return e.Cause }
