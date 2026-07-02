package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StageStatus represents the lifecycle state of a single stage.
type StageStatus string

const (
	StatusPending           StageStatus = "pending"
	StatusPlanning          StageStatus = "planning"
	StatusAwaitingApproval  StageStatus = "awaiting_approval"
	StatusRevising          StageStatus = "revising"
	StatusReady             StageStatus = "ready"
	StatusRunning           StageStatus = "running"
	StatusRetrying          StageStatus = "retrying"
	StatusAwaitingUserInput StageStatus = "awaiting_user_input"
	StatusDone              StageStatus = "done"
	StatusFailed            StageStatus = "failed"
)

// StageState holds persistent state for a single stage.
type StageState struct {
	Status    StageStatus `json:"status"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// RunState is the top-level state persisted in state.json.
type RunState struct {
	FlowName   string    `json:"flow_name"`
	StartedAt  time.Time `json:"started_at"`
	StageOrder []string  `json:"stage_order"`
	// StageNames maps stage id → human-readable name from the flow file.
	// omitempty keeps old state.json files (without stage_names) compatible
	// and only emits the field when it has been populated.
	StageNames map[string]string     `json:"stage_names,omitempty"`
	Stages     map[string]StageState `json:"stages"`
}

// NewRunState creates an initial RunState with all stages pending.
func NewRunState(stageIDs []string) *RunState {
	rs := &RunState{
		StartedAt:  time.Now(),
		StageOrder: stageIDs,
		Stages:     make(map[string]StageState, len(stageIDs)),
	}
	for _, id := range stageIDs {
		rs.Stages[id] = StageState{Status: StatusPending, UpdatedAt: time.Now()}
	}
	return rs
}

// SetStageStatus updates a stage status and its timestamp.
func (rs *RunState) SetStageStatus(stageID string, status StageStatus) {
	rs.Stages[stageID] = StageState{Status: status, UpdatedAt: time.Now()}
}

// AllDone returns true when every stage has StatusDone.
func (rs *RunState) AllDone() bool {
	for _, s := range rs.Stages {
		if s.Status != StatusDone {
			return false
		}
	}
	return true
}

// FindLatestRunDir finds the most recent run directory for a given flow name
// under base/.
func FindLatestRunDir(base, flowName string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read runs dir: %w", err)
	}
	var latest string
	prefix := flowName + "-"
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
			latest = filepath.Join(base, e.Name())
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no run found for flow %q", flowName)
	}
	return latest, nil
}

// SaveFeedback appends feedback to a stage's feedback.md with revision separators.
func SaveFeedback(stageDir, feedback string) error {
	fbFile := filepath.Join(stageDir, "feedback.md")

	n := 1
	existing, err := os.ReadFile(fbFile)
	if err == nil {
		n = strings.Count(string(existing), "--- revision ") + 1
	}

	separator := fmt.Sprintf("\n--- revision %d | %s ---\n",
		n, time.Now().Format("2006-01-02 15:04"))

	f, err := os.OpenFile(fbFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open feedback file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(separator + feedback + "\n"); err != nil {
		return fmt.Errorf("write feedback: %w", err)
	}
	return nil
}

// VersionPlan renames plan.md to plan.v{N}.md and returns N.
func VersionPlan(stageDir string) (int, error) {
	planFile := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planFile); err != nil {
		return 0, fmt.Errorf("plan.md not found: %w", err)
	}

	n := 1
	for {
		versionedPath := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
		if _, err := os.Stat(versionedPath); os.IsNotExist(err) {
			break
		}
		n++
	}

	dst := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
	if err := os.Rename(planFile, dst); err != nil {
		return 0, fmt.Errorf("rename plan: %w", err)
	}
	return n, nil
}
