package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// StageStatus represents the lifecycle state of a single stage.
type StageStatus string

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusFinished    RunStatus = "finished"
	RunStatusFailed      RunStatus = "failed"
	RunStatusInterrupted RunStatus = "interrupted"
)

const (
	runEventStarted     = "run_started"
	runEventFinished    = "run_finished"
	runEventFailed      = "run_failed"
	runEventInterrupted = "run_interrupted"
)

const (
	StatusPending          StageStatus = "pending"
	StatusPlanning         StageStatus = "planning"
	StatusAwaitingApproval StageStatus = "awaiting_approval"
	StatusRevising         StageStatus = "revising"
	StatusReady            StageStatus = "ready"
	StatusRunning          StageStatus = "running"
	StatusRetrying         StageStatus = "retrying"
	// StatusPaused — стадия приостановлена: либо auto_run:false не дал ей стартовать, либо пользователь
	// вручную поставил на паузу уже бегущую стадию.
	StatusPaused            StageStatus = "paused"
	StatusAwaitingUserInput StageStatus = "awaiting_user_input"
	StatusDone              StageStatus = "done"
	StatusFailed            StageStatus = "failed"
	// StatusHookFailed — script_before exhausted its retries; the stage is blocked until the user retries or
	// skips the hook via the dashboard. Non-terminal (see orchestrator.IsTerminal).
	StatusHookFailed StageStatus = "hook_failed"
	// StatusAwaitingAssistants — Этап 7 (T7.1): the stage's main executor has finished its own part (and, if
	// configured, already passed the independent verify gate), but at least one REQUIRED helper task under it
	// has not yet closed its gate (pkg/assistants.ModeRequired + Verdict.ClosesGate). Entered only from
	// main-executor completion; the process that was running the stage has already exited (see
	// AGENTS.md "Durable completion barrier"). Non-terminal; exits to done/failed/revising/paused.
	StatusAwaitingAssistants StageStatus = "awaiting_assistants"
	// StatusAwaitingPeer — Agent Mesh Phase 5 (T5.1): the stage's main executor registered a mesh Wait on a
	// colleague and exited cooperatively — the peer twin of StatusAwaitingUserInput, generalising the same
	// "park non-terminal, free the max_parallel lease, respawn the phase runner on an external event"
	// mechanism (onUserAnswered) to a reply from another participant instead of a human answer. Non-terminal;
	// exits back to the phase it was in (EvPeerReplied's phaseDispatch) or to paused/failed like any other
	// running work. Classified as ACTIVITY, not human-idle (isIdle below): a colleague working on a reply is
	// machine work, exactly like awaiting_assistants.
	StatusAwaitingPeer StageStatus = "awaiting_peer"
)

// AllStatuses returns every StageStatus in declaration order.
func AllStatuses() []StageStatus {
	return []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusPaused, StatusAwaitingUserInput,
		StatusDone, StatusFailed, StatusHookFailed, StatusAwaitingAssistants, StatusAwaitingPeer,
	}
}

// StageState holds persistent state for a single stage.
type StageState struct {
	Status    StageStatus `json:"status"`
	UpdatedAt time.Time   `json:"updated_at"`
	// PausedFrom хранит исходный статус паузы и навсегда остаётся маркером пройденного pause-цикла для
	// auto_run:false.
	PausedFrom StageStatus `json:"paused_from,omitempty"`
	// Epoch counts the takeovers of the stage from outside its running process — pause, continue, revise
	// (stageTakeoverEvents). A process remembers the epoch it started in; a transition it fences with that value
	// (Transition.ExpectEpoch) is refused once someone else took the stage over. Derived on replay.
	Epoch uint64 `json:"epoch,omitempty"`
	// PeerWaits — while parked in awaiting_peer: the waits its continuation depends on (Transition.PeerWaits).
	PeerWaits []agentcomm.WaitID `json:"peer_waits,omitempty"`
}

// stageTakeoverEvents are the FSM events (pkg/orchestrator/bus) by which someone other than the stage's running
// process takes the stage over or hands it to a new process; each one starts a new StageState.Epoch.
var stageTakeoverEvents = map[string]bool{"pause": true, "continue": true, "revise": true}

// RunState is the snapshot cache; events.jsonl is authoritative.
type RunState struct {
	FlowName  string    `json:"flow_name"`
	StartedAt time.Time `json:"started_at"`
	// RunStatus/EndedAt describe the latest process lifetime of this run.
	// A resumed run clears EndedAt with a durable run_started event.
	RunStatus RunStatus  `json:"run_status,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// RunStartedAt anchors open idle/backoff periods after a resume. Without it,
	// a failed or retrying stage would count the time while afm was stopped.
	RunStartedAt time.Time `json:"run_started_at,omitempty"`
	// ElapsedAccumulatedMs is the sum of completed process lifetimes. The open
	// lifetime starts at RunStartedAt and is added on a terminal run event.
	ElapsedAccumulatedMs int64    `json:"elapsed_accumulated_ms"`
	StageOrder           []string `json:"stage_order"`
	// StageNames maps stage id → human-readable name from the flow file. omitempty keeps old state.json files
	// (without stage_names) compatible and only emits the field when it has been populated.
	StageNames map[string]string     `json:"stage_names,omitempty"`
	Stages     map[string]StageState `json:"stages"`
	// LastSeq is the Seq of the last transition applied to this run, mirrored from the event log so consumers
	// of the snapshot alone (e.g. UI) can detect staleness without replaying events.jsonl.
	LastSeq uint64 `json:"last_seq"`
	// IdleAccumulatedMs/BackoffAccumulatedMs — накопленное время простоя/бэкоффа на момент последнего
	// примененного перехода.
	IdleAccumulatedMs    int64 `json:"idle_accumulated_ms"`
	BackoffAccumulatedMs int64 `json:"backoff_accumulated_ms"`
	// Assistants is the task-tree/policy/quota/idempotency projection restored from control records
	// (pkg/state/assistants_projection.go, T2.3+). Like the rest of RunState it is a derived cache —
	// events.jsonl stays authoritative; LoadRunState/Store.Open rebuild it by replay.
	Assistants AssistantsState `json:"assistants,omitempty"`
	// DynamicStages (Этап 9, T9.4/T9.5) records every stage_graph_added control record applied so far, in
	// commit order — the durable, replay-restored effect of every "add a regular stage to the live run" HTTP
	// commit. pkg/orchestrator/graph folds these on top of the stage set frozen at run start
	// (graph_registry.json) to reconstruct the CURRENT stage graph after a restart; flow.yaml on disk is never
	// touched by any of this. len(DynamicStages)+1 IS the runtime registry's current revision — the two can
	// never drift apart because stage_graph_added is the ONLY way either one changes.
	DynamicStages []DynamicStageAdded `json:"dynamic_stages,omitempty"`
}

// NewRunState creates an initial RunState with all stages pending.
func NewRunState(stageIDs []string) *RunState {
	rs := &RunState{
		StartedAt:  clk().Now(),
		StageOrder: stageIDs,
		Stages:     make(map[string]StageState, len(stageIDs)),
		Assistants: newAssistantsState(),
	}
	for _, id := range stageIDs {
		// UpdatedAt остаётся нулевым, а не time.Now: maxUpdatedAt/accountIdleAndBackoff считает максимум
		// UpdatedAt по ВСЕМ стадиям, включая нетронутые pending.
		rs.Stages[id] = StageState{Status: StatusPending}
	}
	return rs
}

// SetStageStatus updates a stage status, stamping the current time.
func (rs *RunState) SetStageStatus(stageID string, status StageStatus) {
	rs.SetStageStatusAt(stageID, status, clk().Now())
}

// SetStageStatusAt updates a stage status, stamping it with the given time t instead of time.Now.
func (rs *RunState) SetStageStatusAt(stageID string, status StageStatus, t time.Time) {
	st := rs.Stages[stageID]
	if status == StatusPaused {
		st.PausedFrom = st.Status // статус ДО этого перехода
	}
	st.Status, st.UpdatedAt, st.PeerWaits = status, t, nil
	rs.Stages[stageID] = st
}

// isIdle сообщает, ждёт ли флоу реакции пользователя прямо сейчас — единое состояние на весь флоу.
//
// awaiting_assistants (T7.7, §5.6) — особый случай: сам по себе статус НЕ пользовательский Idle (работающий
// помощник — машинная работа), но если durable-гейт сейчас блокирован required-задачей, которая требует
// решения человека (needs_changes/inconclusive-вердикт, failed/cancelled-задача или ручная пауза задачи) —
// это пользовательское ожидание, как и needs_changes/approval/pause у самой стадии. Классификация считается
// по rs.Assistants (durable projection), не по наличию WebSocket — одинаково и в live, и при replay.
func isIdle(rs *RunState) bool {
	hasFailed := false
	anyActive := false
	for stageID, st := range rs.Stages {
		switch st.Status {
		case StatusAwaitingUserInput, StatusAwaitingApproval, StatusPaused, StatusHookFailed:
			return true
		case StatusAwaitingAssistants:
			if rs.Assistants.NeedsHumanDecision(stageID) {
				return true
			}
			anyActive = true // машинная работа помощника — как running/planning/revising
		case StatusFailed:
			hasFailed = true
		case StatusRunning, StatusPlanning, StatusRevising, StatusAwaitingPeer:
			anyActive = true
		default:
			// pending/ready/retrying/done/hook_failed — не влияют на isIdle.
		}
	}
	return hasFailed && !anyActive
}

// maxUpdatedAt возвращает самый свежий UpdatedAt среди всех стадий — момент последнего примененного во
// флоу перехода.
func maxUpdatedAt(stages map[string]StageState) time.Time {
	var latest time.Time
	for _, st := range stages {
		if st.UpdatedAt.After(latest) {
			latest = st.UpdatedAt
		}
	}
	return latest
}

func openSince(rs *RunState, stageUpdatedAt time.Time) time.Time {
	if rs.RunStartedAt.After(stageUpdatedAt) {
		return rs.RunStartedAt
	}
	return stageUpdatedAt
}

func isRunEvent(event string) bool {
	switch event {
	case runEventStarted, runEventFinished, runEventFailed, runEventInterrupted:
		return true
	}
	return false
}

func applyRunEvent(rs *RunState, event string, t time.Time) {
	if event == runEventStarted {
		if rs.EndedAt == nil {
			// A previous process disappeared without a terminal event. Its exact exit time is unknowable; count
			// only up to its last durable stage transition, excluding the offline gap before this resume.
			previousStart := rs.RunStartedAt
			if previousStart.IsZero() {
				previousStart = rs.StartedAt // run created before run events existed
			}
			if last := maxUpdatedAt(rs.Stages); !previousStart.IsZero() && last.After(previousStart) {
				rs.ElapsedAccumulatedMs += last.Sub(previousStart).Milliseconds()
			}
		}
		rs.RunStatus = RunStatusRunning
		rs.EndedAt = nil
		rs.RunStartedAt = t
		return
	}
	if rs.EndedAt == nil {
		if !rs.RunStartedAt.IsZero() && t.After(rs.RunStartedAt) {
			rs.ElapsedAccumulatedMs += t.Sub(rs.RunStartedAt).Milliseconds()
		}
		if isIdle(rs) {
			if since := openSince(rs, maxUpdatedAt(rs.Stages)); !since.IsZero() && t.After(since) {
				rs.IdleAccumulatedMs += t.Sub(since).Milliseconds()
			}
		}
		for _, st := range rs.Stages {
			if st.Status == StatusRetrying {
				if since := openSince(rs, st.UpdatedAt); !since.IsZero() && t.After(since) {
					rs.BackoffAccumulatedMs += t.Sub(since).Milliseconds()
				}
			}
		}
	}
	rs.EndedAt = &t
	switch event {
	case runEventFinished:
		rs.RunStatus = RunStatusFinished
	case runEventFailed:
		rs.RunStatus = RunStatusFailed
	case runEventInterrupted:
		rs.RunStatus = RunStatusInterrupted
	default:
		return // parser and Store only pass known events
	}
}

// applyTransition projects one stage transition — the single step shared by live Store.Apply and replay:
// timers before the change, the new status, then the stage_status waits the new status resolves.
func (rs *RunState) applyTransition(t *Transition) {
	accountIdleAndBackoff(rs, t.StageID, t.To, t.Time)
	rs.SetStageStatusAt(t.StageID, t.To, t.Time)
	st := rs.Stages[t.StageID]
	if stageTakeoverEvents[t.Event] {
		st.Epoch++
	}
	st.PeerWaits = slices.Clone(t.PeerWaits)
	rs.Stages[t.StageID] = st
	rs.resolveStageStatusWaits()
}

// accountIdleAndBackoff обновляет таймеры ДО перехода; вызывается и из live Apply, и из replay
// parseEventLog.
func accountIdleAndBackoff(rs *RunState, stageID string, to StageStatus, t time.Time) {
	if rs.EndedAt != nil {
		return // offline CLI transitions must not reopen completed time intervals
	}
	if isIdle(rs) {
		if prev := openSince(rs, maxUpdatedAt(rs.Stages)); !prev.IsZero() && t.After(prev) {
			rs.IdleAccumulatedMs += t.Sub(prev).Milliseconds()
		}
	}

	before := rs.Stages[stageID]
	if since := openSince(rs, before.UpdatedAt); before.Status == StatusRetrying && to != StatusRetrying && t.After(since) {
		rs.BackoffAccumulatedMs += t.Sub(since).Milliseconds()
	}
}

// IdleSince возвращает момент начала текущего периода простоя, если флоу
// простаивает сейчас (см. isIdle) — иначе nil.
func (rs *RunState) IdleSince() *time.Time {
	if rs.EndedAt != nil {
		return nil
	}
	if !isIdle(rs) {
		return nil
	}
	t := openSince(rs, maxUpdatedAt(rs.Stages))
	if t.IsZero() {
		return nil
	}
	return &t
}

func (rs *RunState) ElapsedSince() *time.Time {
	if rs.EndedAt != nil || rs.RunStartedAt.IsZero() {
		return nil
	}
	t := rs.RunStartedAt
	return &t
}

// BackoffOpenSince возвращает момент входа в retrying для каждой стадии, которая сейчас в этом статусе —
// параллельные эпизоды суммируются на чтении, а не мёржатся здесь.
func (rs *RunState) BackoffOpenSince() []time.Time {
	if rs.EndedAt != nil {
		return nil
	}
	var out []time.Time
	for _, st := range rs.Stages {
		if st.Status == StatusRetrying {
			out = append(out, openSince(rs, st.UpdatedAt))
		}
	}
	return out
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

// LoadRunState восстанавливает состояние run-директории из events.jsonl — авторитетного источника.
func LoadRunState(runDir string) (RunState, error) {
	rs := RunState{Stages: map[string]StageState{}}
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return rs, err
	}
	res := parseEventLog(data, &rs)
	if res.unsupported != nil {
		return RunState{Stages: map[string]StageState{}}, res.unsupported
	}
	if res.corrupted {
		return rs, ErrCorruptLog
	}
	rs.LastSeq = res.lastSeq
	return rs, nil
}

// splitLines разбивает данные на строки по \n, не включая сам разделитель.
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

// replayResult — итог разбора events.jsonl, общий для обоих путей чтения лога:
// replayEvents (Open, с усечением по goodOffset) и LoadRunState (read-only check).
type replayResult struct {
	history     []Transition
	lastSeq     uint64
	goodOffset  int64 // байтовый конец последней ЗАФИКСИРОВАННОЙ (newline-terminated) записи
	corrupted   bool  // битая ПОЛНАЯ строка в середине лога (есть валидные записи после) — карантин
	unsupported error // control-запись с незнакомым kind/schema_version — fail-closed, БЕЗ карантина
	// seqOffsets maps a control record's seq -> the byte offset where its line STARTS (T2.5's direct
	// single-record journal read by seq, without scanning).
	seqOffsets map[uint64]int64
	// controls accumulates every control record in file order — the replay-time half of Store.controlHistory
	// (appendControlLocked appends the live half). Backs Store.TaskEvents' durable per-task event page; kept
	// here rather than re-derived from rs.Assistants (which only holds CURRENT projected state, not history).
	controls []Control
}

// parseEventLog — единственный парсер events.jsonl. Применяет переходы (и, начиная с T2.3, control-записи)
// к rs.
func parseEventLog(data []byte, rs *RunState) replayResult {
	lines := splitLines(data)
	endsWithNewline := len(data) > 0 && data[len(data)-1] == '\n'
	var offset, goodOffset int64
	var res replayResult
	for i, line := range lines {
		isLast := i == len(lines)-1
		if isLast && !endsWithNewline {
			break // незакоммиченный хвост без \n — усечь (см. durability-решение плана)
		}
		lineStart := offset
		offset += int64(len(line)) + 1 // +1 на \n (у не-последних строк он всегда есть)
		if len(bytes.TrimSpace(line)) == 0 {
			goodOffset = offset
			continue
		}
		dl, unsupported, err := decodeLine(line)
		if unsupported {
			// Fail-closed (T2.1, §6.2): синтаксически валидная, но непонятная запись останавливает разбор
			// немедленно — ран не продолжается с частичным графом задач. В отличие от corrupted ниже,
			// оригинал events.jsonl не трогается — вызывающий (Store.Open) не должен его карантинить.
			res.unsupported = err
			return res
		}
		if err != nil {
			res.goodOffset = goodOffset
			res.corrupted = true
			return res
		}
		if dl.control != nil {
			c := dl.control
			if perr := applyControlToRunState(rs, c); perr != nil {
				// The record itself decoded fine, but what it asserts is impossible (cycle/missing parent
				// — T2.3): treat as log corruption, not as ErrUnsupportedRecord.
				res.goodOffset = goodOffset
				res.corrupted = true
				return res
			}
			if res.seqOffsets == nil {
				res.seqOffsets = map[uint64]int64{}
			}
			res.seqOffsets[c.Seq] = lineStart
			res.controls = append(res.controls, *c)
			res.lastSeq = c.Seq
			goodOffset = offset
			continue
		}
		t := *dl.transition
		// Восстанавливаем реальное начало рана из первого события лога. На свежем ране лог при Open пуст, сюда
		// не заходим, и StartedAt остаётся временем Open ≈ реальному старту.
		if res.lastSeq == 0 && !t.Time.IsZero() {
			rs.StartedAt = t.Time
		}
		if t.StageID == "" && isRunEvent(t.Event) {
			applyRunEvent(rs, t.Event, t.Time)
			res.lastSeq = t.Seq
			goodOffset = offset
			continue
		}
		rs.applyTransition(&t)
		res.history = append(res.history, t)
		res.lastSeq = t.Seq
		goodOffset = offset
	}
	res.goodOffset = goodOffset
	return res
}

// runIDSuffixRe matches newRunID's shape (cmd/afm/run.go): an 8-digit date, a 6-digit time, and an
// optional hex suffix (optional because test fixtures across this package commonly omit it). Requiring the
// FULL shape — not just "the next byte is a digit" — is what lets RunIDBelongsTo tell
// "sprint-20260101-100000-aaaa" (a run of "sprint") apart from "sprint-2-20260101-100000-aaaa" (a run of
// the sibling name "sprint-2"): both have a digit right after "sprint-", but only the former's remainder is
// actually a timestamp.
var runIDSuffixRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}(-[0-9a-f]+)?$`)

// RunIDBelongsTo reports whether id is a run directory name for name: id must start with "name-" and the
// remainder must look like a full newRunID timestamp suffix, not merely start with a digit.
func RunIDBelongsTo(id, name string) bool {
	prefix := name + "-"
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
		return false
	}
	return runIDSuffixRe.MatchString(id[len(prefix):])
}

// findAllRunDirs возвращает ВСЕ run-директории для flowName под base, отсортированные по возрастанию
// (старые первыми). Several run dirs for one flow can coexist: resolveRun creates a fresh dir whenever the
// latest one is locked by a still-live `afm run`.
func findAllRunDirs(base, flowName string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("read runs dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && RunIDBelongsTo(n, flowName) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no run found for flow %q", flowName)
	}
	slices.Sort(names)
	dirs := make([]string, len(names))
	for i, n := range names {
		dirs[i] = filepath.Join(base, n)
	}
	return dirs, nil
}

// FindLatestRunDir возвращает самую свежую run-директорию для flowName под base.
func FindLatestRunDir(base, flowName string) (string, error) {
	dirs, err := findAllRunDirs(base, flowName)
	if err != nil {
		return "", err
	}
	return dirs[len(dirs)-1], nil
}

// ErrNoRun означает, что под base не нашлось ни одной подходящей run-директории для flowName (ни одной
// вообще, либо ни одной завершённой — см. FindLatestCompletedRunDir).
var ErrNoRun = errors.New("no matching run found")

// FindLatestCompletedRunDir читает лог без flock и требует точное множество done-стадий; corrupt log
// возвращает ошибку.
func FindLatestCompletedRunDir(base, flowName string, wantStages []string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoRun
		}
		return "", err
	}
	want := make(map[string]bool, len(wantStages))
	for _, s := range wantStages {
		want[s] = true
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && RunIDBelongsTo(n, flowName) {
			names = append(names, n)
		}
	}
	slices.SortFunc(names, func(a, b string) int { return strings.Compare(b, a) }) // новые первыми
	for _, n := range names {
		runDir := filepath.Join(base, n)
		rs, lerr := LoadRunState(runDir)
		if lerr != nil {
			if errors.Is(lerr, ErrCorruptLog) {
				return "", fmt.Errorf("run %s: %w", n, lerr)
			}
			continue
		}
		if stageSetAllDone(rs, want) {
			return runDir, nil
		}
	}
	return "", ErrNoRun
}

// stageSetAllDone сообщает, что множество стадий rs.Stages ТОЧНО совпадает с
// want (не больше, не меньше) и каждая из них в StatusDone.
func stageSetAllDone(rs RunState, want map[string]bool) bool {
	if len(rs.Stages) != len(want) {
		return false // точное множество: ни лишних, ни недостающих
	}
	for id, st := range rs.Stages {
		if !want[id] || st.Status != StatusDone {
			return false
		}
	}
	return true
}

// FindLatestRunForStage возвращает последнюю run-директорию, содержащую stageID, и все её stage id.
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
			if errors.Is(lerr, ErrCorruptLog) {
				// Битый лог нельзя тихо пропустить: иначе approve/retry/revise
				// уйдёт в более старый run с тем же stage id. Surface, как Open.
				return "", nil, fmt.Errorf("run %s: %w", name, lerr)
			}
			continue // прочие (напр. нет events.jsonl) — доброкачественно пропускаем
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

// SaveFeedbackOnce appends a feedback block tagged with opID, but only once: if feedback.md already
// contains the operation sentinel it is a no-op.
func SaveFeedbackOnce(stageDir, opID, feedback string) (bool, error) {
	fbFile := filepath.Join(stageDir, "feedback.md")
	sentinel := fmt.Sprintf("<!-- afm-review-op: %s -->", opID)
	existing, err := os.ReadFile(fbFile)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read feedback: %w", err)
	}
	if strings.Contains(string(existing), sentinel) {
		return false, nil
	}
	var b strings.Builder
	b.Write(existing)
	n := strings.Count(string(existing), "--- revision ") + 1
	fmt.Fprintf(&b, "\n--- revision %d | %s ---\n%s\n%s\n",
		n, time.Now().Format("2006-01-02 15:04"), sentinel, feedback)
	if err := atomicWriteFile(fbFile, []byte(b.String()), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// preNoteFile — заметка, которую пользователь прикрепил к стадии, пока она ещё была pending.
const preNoteFile = "prenote.md"

// SavePreNote атомарно записывает pre-note стадии, либо удаляет файл, когда note пустой/из одних пробелов.
// stageDir может ещё не существовать — pending-стадия часто не имеет каталога на диске.
func SavePreNote(stageDir, note string) error {
	path := filepath.Join(stageDir, preNoteFile)
	if strings.TrimSpace(note) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove pre-note: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return fmt.Errorf("mkdir stage dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(note), 0644); err != nil {
		return fmt.Errorf("write pre-note: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename pre-note: %w", err)
	}
	return nil
}

// LoadPreNote возвращает текст pre-note стадии или "" если заметки нет.
func LoadPreNote(stageDir string) string {
	data, err := os.ReadFile(filepath.Join(stageDir, preNoteFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// VersionPlan renames plan.md to plan.v{N}.md and returns N.
func VersionPlan(stageDir string) (int, error) {
	planFile := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planFile); err != nil {
		return 0, fmt.Errorf("plan.md not found: %w", err)
	}

	latest, _, err := LatestPlanVersion(stageDir)
	if err != nil {
		return 0, fmt.Errorf("scan plan versions: %w", err)
	}
	n := latest + 1

	dst := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
	if err := os.Rename(planFile, dst); err != nil {
		return 0, fmt.Errorf("rename plan: %w", err)
	}
	return n, nil
}

// LatestPlanVersion scans stageDir for plan.v{N}.md files and returns the highest N found along with that
// file's content.
func LatestPlanVersion(stageDir string) (version int, content string, err error) {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return 0, "", fmt.Errorf("read stage dir: %w", err)
	}

	best := 0
	var bestName string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "plan.v") || !strings.HasSuffix(name, ".md") {
			continue
		}
		numPart := strings.TrimSuffix(strings.TrimPrefix(name, "plan.v"), ".md")
		n, convErr := strconv.Atoi(numPart)
		if convErr != nil || n <= best {
			continue
		}
		best = n
		bestName = name
	}
	if bestName == "" {
		return 0, "", nil
	}
	data, err := os.ReadFile(filepath.Join(stageDir, bestName))
	if err != nil {
		return 0, "", fmt.Errorf("read %s: %w", bestName, err)
	}
	return best, string(data), nil
}

// FileContentSHA returns a stable "sha256:<hex>" digest of the given bytes.
// Used as the review-note content anchor (a content hash, not mtime+size).
func FileContentSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// atomicWriteFile fsyncs a same-directory temp file, renames it, then fsyncs the parent directory.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dirName := filepath.Dir(path)
	if err := os.MkdirAll(dirName, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dirName, err)
	}
	tmp, err := os.CreateTemp(dirName, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	dir, err := os.Open(dirName)
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}
