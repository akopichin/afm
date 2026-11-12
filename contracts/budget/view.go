package budget

import (
	"errors"
	"fmt"
)

// ViewKind is what a scope is for the user; it is not part of the ScopeID.
type ViewKind string

const (
	ViewRun      ViewKind = "run"
	ViewStage    ViewKind = "stage"
	ViewHelper   ViewKind = "helper"
	ViewRunAgent ViewKind = "run_agent"
)

// DeclarationKind says which YAML/flow declaration holds the base budget.
type DeclarationKind string

const (
	DeclFlow           DeclarationKind = "flow"
	DeclStage          DeclarationKind = "stage"
	DeclStageAssistant DeclarationKind = "stage_assistant"
	DeclTeamMember     DeclarationKind = "team_member"
)

// DeclarationRef points at the declaration of a scope's base budget (stage_assistant = stage + profile).
type DeclarationRef struct {
	Kind     DeclarationKind `json:"kind"`
	StageID  string          `json:"stage_id,omitempty"`
	Profile  string          `json:"profile,omitempty"`
	MemberID string          `json:"member_id,omitempty"`
}

// HoldReason explains why a scope is not running.
type HoldReason string

const (
	HoldLimitReached          HoldReason = "limit_reached"
	HoldParentLimitReached    HoldReason = "parent_limit_reached"
	HoldRatesMissing          HoldReason = "rates_missing"
	HoldAccountingUnavailable HoldReason = "accounting_unavailable"
)

// Hold is the current stop of a scope; Scope is the one whose limit caused it.
type Hold struct {
	Reason HoldReason `json:"reason"`
	Scope  ScopeID    `json:"scope,omitempty"`
	Metric Metric     `json:"metric,omitempty"`
}

// Action is a command the UI may offer for a scope right now.
type Action string

const (
	ActionUpdate     Action = "update"
	ActionResume     Action = "resume"
	ActionSaveToYAML Action = "save_to_yaml"
)

// ParentLimit is the state of one ancestor limit that also constrains a scope.
type ParentLimit struct {
	Scope      ScopeID    `json:"scope"`
	Limit      Spec       `json:"limit"`
	Spent      Spend      `json:"spent"`
	Evaluation Evaluation `json:"evaluation"`
}

// View is the engine's read-model of one scope; clients display it and never recompute admission.
type View struct {
	Scope        ScopeID         `json:"scope"`
	Kind         ViewKind        `json:"kind"`
	Revision     int64           `json:"revision"`
	Base         *Spec           `json:"base,omitempty"`
	BaseSource   Source          `json:"base_source"`
	Declaration  *DeclarationRef `json:"declaration,omitempty"`
	Override     Override        `json:"override"`
	Effective    *Spec           `json:"effective,omitempty"`
	Spent        Spend           `json:"spent"`
	Evaluation   Evaluation      `json:"evaluation"`
	ParentLimits []ParentLimit   `json:"parent_limits,omitempty"`
	Hold         *Hold           `json:"hold,omitempty"`
	// Draining is set while a medium limit is reached: running work may finish, nothing new starts.
	Draining bool     `json:"draining,omitempty"`
	Actions  []Action `json:"actions,omitempty"`
}

// UpdateRequest changes one scope's override; ExpectedRevision guards against concurrent edits.
type UpdateRequest struct {
	Scope            ScopeID  `json:"scope"`
	ExpectedRevision int64    `json:"expected_revision"`
	Override         Override `json:"override"`
	SaveToYAML       bool     `json:"save_to_yaml,omitempty"`
	Resume           bool     `json:"resume,omitempty"`
}

// ResumeRequest continues a held scope without changing its limit.
type ResumeRequest struct {
	Scope            ScopeID `json:"scope"`
	ExpectedRevision int64   `json:"expected_revision"`
}

// ExportStatus is the outcome of saving an override back to YAML.
type ExportStatus string

const (
	ExportSaved       ExportStatus = "saved"
	ExportFailed      ExportStatus = "failed"
	ExportUnavailable ExportStatus = "unavailable"
)

// ExportResult reports the optional "save to YAML" step.
type ExportResult struct {
	Status ExportStatus `json:"status"`
	Path   string       `json:"path,omitempty"`
	Error  string       `json:"error,omitempty"`
}

// UpdateResult returns the fresh view and the export outcome when one was requested.
type UpdateResult struct {
	View   View          `json:"view"`
	Export *ExportResult `json:"export,omitempty"`
}

// NoticeKind is the type of a budget feed notice.
type NoticeKind string

const (
	NoticeWarning      NoticeKind = "warning"
	NoticeLimitReached NoticeKind = "limit_reached"
	NoticeUpdated      NoticeKind = "updated"
	NoticeResumed      NoticeKind = "resumed"
)

// Notice is the payload of a budget feed message.
type Notice struct {
	Kind     NoticeKind `json:"kind"`
	Scope    ScopeID    `json:"scope"`
	Metric   Metric     `json:"metric,omitempty"`
	Limit    Spec       `json:"limit,omitzero"`
	Spent    Spend      `json:"spent"`
	Policy   Policy     `json:"policy,omitempty"`
	Revision int64      `json:"revision"`
}

// Validate checks the scope and the override.
func (r UpdateRequest) Validate() error {
	if _, err := ParseScopeID(string(r.Scope)); err != nil {
		return err
	}
	if r.ExpectedRevision < 0 {
		return errors.New("expected_revision must not be negative")
	}
	return r.Override.Validate()
}

// Validate checks the scope.
func (r ResumeRequest) Validate() error {
	if _, err := ParseScopeID(string(r.Scope)); err != nil {
		return err
	}
	if r.ExpectedRevision < 0 {
		return errors.New("expected_revision must not be negative")
	}
	return nil
}

// JSON keys the requests require.
const (
	keyScope            = "scope"
	keyExpectedRevision = "expected_revision"
	keyOverride         = "override"
)

// The exact keys each request accepts; another spelling is an error instead of overwriting a checked field.
var (
	updateRequestKeys = []string{keyScope, keyExpectedRevision, keyOverride, "save_to_yaml", "resume"}
	resumeRequestKeys = []string{keyScope, keyExpectedRevision}
)

// UnmarshalJSON decodes strictly and validates. Scope, expected_revision and override must be present
// and not null, so a dropped field cannot silently clear a limit or skip the revision guard.
func (r *UpdateRequest) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, updateRequestKeys, keyScope, keyExpectedRevision, keyOverride); err != nil {
		return fmt.Errorf("update request: %w", err)
	}
	type plain UpdateRequest
	var in plain
	if err := strictUnmarshal(data, &in); err != nil {
		return fmt.Errorf("update request: %w", err)
	}
	if in.Override.Kind == "" {
		return errors.New("update request: override.kind is required (use none to drop an override)")
	}
	if err := UpdateRequest(in).Validate(); err != nil {
		return err
	}
	*r = UpdateRequest(in)
	return nil
}

// UnmarshalJSON decodes strictly and validates; scope and expected_revision are required.
func (r *ResumeRequest) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, resumeRequestKeys, keyScope, keyExpectedRevision); err != nil {
		return fmt.Errorf("resume request: %w", err)
	}
	type plain ResumeRequest
	var in plain
	if err := strictUnmarshal(data, &in); err != nil {
		return fmt.Errorf("resume request: %w", err)
	}
	if err := ResumeRequest(in).Validate(); err != nil {
		return err
	}
	*r = ResumeRequest(in)
	return nil
}

// requireFields checks data against the exact allowed keys (see decodeObject) and that every required key is set.
func requireFields(data []byte, allowed []string, required ...string) error {
	obj, err := decodeObject(data, allowed...)
	if err != nil {
		return err
	}
	for _, k := range required {
		if _, ok := obj[k]; !ok {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}
