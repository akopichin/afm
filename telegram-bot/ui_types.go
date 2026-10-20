package main

import "time"

// RunKey identifies one local registration, even when an AFM dashboard port is reused.
type RunKey string

// DataState distinguishes a live snapshot from a loading, absent, or stale one.
type DataState string

const (
	DataReady   DataState = "ready"
	DataLoading DataState = "loading"
	DataOffline DataState = "offline"
	DataEmpty   DataState = "empty"
	DataError   DataState = "error"
)

// Telegram callback_data carries only a short, stored callback ID. It never
// carries a serialized Intent, which can be much larger than this byte limit.
const CallbackDataLimit = 64

type IntentKind string

const (
	IntentHome            IntentKind = "home"
	IntentRuns            IntentKind = "runs"
	IntentSelectRun       IntentKind = "select_run"
	IntentInbox           IntentKind = "inbox"
	IntentRoster          IntentKind = "roster"
	IntentToggleRecipient IntentKind = "toggle_recipient"
	IntentCompose         IntentKind = "compose"
	IntentThread          IntentKind = "thread"
	IntentReply           IntentKind = "reply"
	IntentAnswerQuestion  IntentKind = "answer_question"
	IntentCustomAnswer    IntentKind = "custom_answer"
	IntentPlan            IntentKind = "plan"
	IntentApprovePlan     IntentKind = "approve_plan"
	IntentRevisePlan      IntentKind = "revise_plan"
	IntentConfirmRevision IntentKind = "confirm_revision"
	IntentPause           IntentKind = "pause"
	IntentContinue        IntentKind = "continue"
	IntentAddAgent        IntentKind = "add_agent"
	IntentSelectProfile   IntentKind = "select_profile"
	IntentSelectScope     IntentKind = "select_scope"
	IntentSelectMode      IntentKind = "select_mode"
	IntentConfirmAddAgent IntentKind = "confirm_add_agent"
	IntentFilter          IntentKind = "filter"
	IntentSettings        IntentKind = "settings"
	IntentRefresh         IntentKind = "refresh"
	IntentLoadMore        IntentKind = "load_more"
	IntentInterrupt       IntentKind = "interrupt"
	IntentCancel          IntentKind = "cancel"
)

// Intent is the typed action behind a button. Each target and precondition has
// its own field; the backend binds it to a random callback ID and Telegram user.
type Intent struct {
	Kind           IntentKind `json:"kind"`
	RunKey         RunKey     `json:"run_key,omitempty"`
	StageID        string     `json:"stage_id,omitempty"`
	ThreadID       string     `json:"thread_id,omitempty"`
	InReplyTo      string     `json:"in_reply_to,omitempty"`
	QuestionID     string     `json:"question_id,omitempty"`
	QuestionPhase  string     `json:"question_phase,omitempty"`
	QuestionDigest string     `json:"question_digest,omitempty"`
	ComposerDigest string     `json:"composer_digest,omitempty"`
	ProfileID      string     `json:"profile_id,omitempty"`
	Scope          string     `json:"scope,omitempty"`
	Mode           string     `json:"mode,omitempty"`
	Filter         string     `json:"filter,omitempty"`
	PlanDigest     string     `json:"plan_digest,omitempty"`
	ExpectedStatus string     `json:"expected_status,omitempty"`
	StageUpdatedAt time.Time  `json:"stage_updated_at,omitempty"`
	RecipientIDs   []string   `json:"recipient_ids,omitempty"`
	OptionValue    string     `json:"option_value,omitempty"`
}

type View struct {
	Parts []ViewPart
}

type ViewPart struct {
	Text    string
	Buttons [][]Button
}

type Button struct {
	Label  string
	Intent Intent
}

type RunSummary struct {
	Key           RunKey
	Label         string
	DashboardPort int
	State         DataState
	StageName     string
	Status        string
	LastSeenAt    time.Time
	PendingCount  int
	Completed     bool
}

type HomeCard struct {
	State        DataState
	RunKey       RunKey
	RunLabel     string
	StageID      string
	StageName    string
	Status       string
	PendingCount int
	AgentCount   int
	HasQuestion  bool
	HasPlan      bool
	CanMessage   bool
	Completed    bool
	LastSeenAt   time.Time
	Error        string
}

// RunCard contains the complete run selector, with the selected run as context.
type RunCard struct {
	State    DataState
	RunKey   RunKey
	RunLabel string
	Runs     []RunSummary
	Error    string
}

type InboxItem struct {
	RunKey         RunKey
	RunLabel       string
	Kind           string
	StageID        string
	StageName      string
	ThreadID       string
	MessageID      string
	QuestionPhase  string
	QuestionID     string
	QuestionDigest string
	PlanDigest     string
	Summary        string
	UpdatedAt      time.Time
	Actionable     bool
}

type InboxCard struct {
	State  DataState
	Filter string // "important" or "all"
	Items  []InboxItem
	Error  string
}

type AgentCard struct {
	ID           string
	Alias        string
	Name         string
	Scope        string
	StageID      string
	Status       string
	Reachable    bool
	Running      bool
	CanInterrupt bool
}

type RosterCard struct {
	Completed            bool
	State                DataState
	RunKey               RunKey
	RunLabel             string
	StageID              string
	Agents               []AgentCard
	SelectedRecipientIDs []string
	Error                string
}

type ThreadMessage struct {
	ID          string
	SenderID    string
	SenderName  string
	Text        string
	InReplyTo   string
	MessageKind string
	SentAt      time.Time
	IsResult    bool
}

type ThreadCard struct {
	Completed  bool
	State      DataState
	RunKey     RunKey
	RunLabel   string
	ThreadID   string
	Title      string
	Messages   []ThreadMessage
	HasMore    bool
	NextCursor uint64
	Filter     string
	Error      string
}

type QuestionOption struct {
	Label string
	Value string // exact displayed AFM option value, never a positional index
}

type QuestionCard struct {
	State          DataState
	RunKey         RunKey
	RunLabel       string
	StageID        string
	StageName      string
	Prompt         string
	QuestionPhase  string
	QuestionID     string
	QuestionDigest string
	StageUpdatedAt time.Time
	Options        []QuestionOption
	AllowCustom    bool
	Answered       bool
	CanAnswer      bool
	Error          string
}

// Plan holds the complete source text. Rendering may split it into View.Parts;
// PlanDigest always identifies the complete source bytes, not an individual part.
type PlanCard struct {
	State                     DataState
	RunKey                    RunKey
	RunLabel                  string
	StageID                   string
	StageName                 string
	Plan                      string
	PlanDigest                string
	ExpectedStatus            string
	StageUpdatedAt            time.Time
	CanApprove                bool
	CanRevise                 bool
	CanPause                  bool
	CanContinue               bool
	RevisionDraft             string
	RevisionNeedsConfirmation bool
	Error                     string
}

type AgentProfile struct {
	ID        string
	Name      string
	Available bool
	Reason    string
}

type AddAgentCard struct {
	State        DataState
	RunKey       RunKey
	RunLabel     string
	StageID      string
	StageName    string
	Step         string
	Profiles     []AgentProfile
	ProfileID    string
	Scope        string // "run" or "stage"
	Mode         string // "advisory" or "required" for stage helpers
	Instruction  string
	FirstMessage string
	CanAdd       bool
	Error        string
}

type ReceiptStatus string

const (
	ReceiptQueued   ReceiptStatus = "queued"
	ReceiptAccepted ReceiptStatus = "accepted"
	ReceiptReplied  ReceiptStatus = "replied"
	ReceiptFailed   ReceiptStatus = "failed"
	ReceiptUnknown  ReceiptStatus = "unknown"
)

type RecipientReceipt struct {
	ID                string
	Name              string
	Status            ReceiptStatus
	OutboundMessageID string
	FailureReason     string
}

type ReceiptCard struct {
	State         DataState
	RunKey        RunKey
	RunLabel      string
	Text          string
	Status        ReceiptStatus
	Recipients    []RecipientReceipt
	ThreadID      string
	InReplyTo     string
	AFMMessageID  string
	FailureReason string
	Error         string
}

type ComposerCard struct {
	State          DataState
	RunKey         RunKey
	RunLabel       string
	Mode           string
	StageID        string
	StageName      string
	ThreadID       string
	InReplyTo      string
	ReplyAuthor    string
	ReplyText      string
	MessageKind    string
	Interrupt      bool
	CanInterrupt   bool
	RecipientIDs   []string
	RecipientNames []string
	Draft          string
	QuestionPhase  string
	QuestionID     string
	QuestionDigest string
	QuestionPrompt string
	StageUpdatedAt time.Time
	CanSend        bool
	Error          string
}

// These function types freeze the renderer signatures while the frontend writer
// owns the implementations in ui.go and ui_decisions.go.
type RenderHomeFunc func(HomeCard) View
type RenderRunsFunc func(RunCard) View
type RenderInboxFunc func(InboxCard) View
type RenderRosterFunc func(RosterCard) View
type RenderThreadFunc func(ThreadCard) View
type RenderQuestionFunc func(QuestionCard) View
type RenderPlanFunc func(PlanCard) View
type RenderAddAgentFunc func(AddAgentCard) View
type RenderReceiptFunc func(ReceiptCard) View
type RenderComposerFunc func(ComposerCard) View
