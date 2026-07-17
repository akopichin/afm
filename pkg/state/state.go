package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// LastSeq is the Seq of the last transition applied to this run,
	// mirrored from the event log so consumers of the snapshot alone
	// (e.g. UI) can detect staleness without replaying events.jsonl.
	LastSeq uint64 `json:"last_seq"`
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

// SetStageStatus updates a stage status, stamping the current time.
func (rs *RunState) SetStageStatus(stageID string, status StageStatus) {
	rs.SetStageStatusAt(stageID, status, time.Now())
}

// SetStageStatusAt updates a stage status, stamping it with the given time t
// instead of time.Now(). Used when replaying events.jsonl (LoadRunState,
// replayEvents) so a stage's UpdatedAt reflects the real transition time
// (Transition.Time) rather than the moment of replay.
func (rs *RunState) SetStageStatusAt(stageID string, status StageStatus, t time.Time) {
	rs.Stages[stageID] = StageState{Status: status, UpdatedAt: t}
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

// LoadRunState восстанавливает состояние run-директории из events.jsonl —
// авторитетного источника. Snapshot (state.json) НЕ используется: он лишь
// производный кэш и может отставать при сбое записи. Не берёт flock: путь
// только для чтения (check, поиск run) и не должен блокироваться живым run.
func LoadRunState(runDir string) (RunState, error) {
	rs := RunState{Stages: map[string]StageState{}}
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return rs, err
	}
	for _, line := range splitLines(data) {
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var t Transition
		if err := json.Unmarshal(line, &t); err != nil {
			break // оборванный/битый хвост — читаем валидный префикс
		}
		rs.SetStageStatusAt(t.StageID, t.To, t.Time)
		rs.LastSeq = t.Seq
	}
	return rs, nil
}

// splitLines разбивает данные на строки по \n, не включая сам разделитель.
// Единственная реализация этого алгоритма в пакете — используется и при
// восстановлении Store (replayEvents), и при read-only чтении LoadRunState.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// FindLatestRunDir возвращает самую свежую run-директорию для flowName под base.
// Имя run: "<flowName>-<timestamp>...". Чтобы "foo" не матчил "foo-bar", после
// префикса требуется цифра (начало timestamp'а). Сортировка имён совпадает с
// хронологией благодаря формату timestamp'а.
func FindLatestRunDir(base, flowName string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read runs dir: %w", err)
	}
	prefix := flowName + "-"
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && len(n) > len(prefix) && n[:len(prefix)] == prefix && n[len(prefix)] >= '0' && n[len(prefix)] <= '9' {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no run found for flow %q", flowName)
	}
	slices.Sort(names)
	return filepath.Join(base, names[len(names)-1]), nil
}

// FindLatestRunForStage возвращает последнюю run-директорию, содержащую stageID,
// и все её stage id. Состояние читается из events.jsonl (LoadRunState), не из
// state.json, чтобы не доверять возможно устаревшему снапшоту.
func FindLatestRunForStage(base, stageID string) (string, []string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", nil, fmt.Errorf("read runs dir: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	slices.SortFunc(dirs, func(a, b string) int { return strings.Compare(b, a) }) // новые первыми
	for _, name := range dirs {
		runDir := filepath.Join(base, name)
		rs, lerr := LoadRunState(runDir)
		if lerr != nil {
			continue
		}
		if _, ok := rs.Stages[stageID]; ok {
			ids := make([]string, 0, len(rs.Stages))
			for id := range rs.Stages {
				ids = append(ids, id)
			}
			return runDir, ids, nil
		}
	}
	return "", nil, fmt.Errorf("no active run found for stage %q", stageID)
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
