package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// StageStatus represents the lifecycle state of a single stage.
type StageStatus string

const (
	StatusPending          StageStatus = "pending"
	StatusPlanning         StageStatus = "planning"
	StatusAwaitingApproval StageStatus = "awaiting_approval"
	StatusRevising         StageStatus = "revising"
	StatusReady            StageStatus = "ready"
	StatusRunning          StageStatus = "running"
	StatusRetrying         StageStatus = "retrying"
	// StatusPaused — стадия приостановлена: либо auto_run:false не дал ей
	// стартовать (PausedFrom=pending), либо пользователь вручную поставил на
	// паузу уже бегущую стадию (PausedFrom=running/planning/revising/retrying).
	// См. StageState.PausedFrom и bus.EvPause/EvContinue.
	StatusPaused            StageStatus = "paused"
	StatusAwaitingUserInput StageStatus = "awaiting_user_input"
	StatusDone              StageStatus = "done"
	StatusFailed            StageStatus = "failed"
	// StatusHookFailed — script_before exhausted its retries; the stage is
	// blocked until the user retries or skips the hook via the dashboard.
	// Non-terminal (see orchestrator.IsTerminal).
	StatusHookFailed StageStatus = "hook_failed"
)

// AllStatuses returns every StageStatus in declaration order. This is the
// single source of truth tools/genstagestatus reads to generate the
// frontend's StageStatus TypeScript union — add a new status here (and to
// the const block above) and both stay in sync automatically instead of
// requiring a matching hand-edit in pkg/web/dashboard/src/types/stage.ts.
func AllStatuses() []StageStatus {
	return []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusPaused, StatusAwaitingUserInput,
		StatusDone, StatusFailed, StatusHookFailed,
	}
}

// StageState holds persistent state for a single stage.
type StageState struct {
	Status    StageStatus `json:"status"`
	UpdatedAt time.Time   `json:"updated_at"`
	// PausedFrom — статус, из которого стадия ушла в paused. Заполняется один
	// раз, при первом входе в paused, и НЕ очищается на выходе из paused
	// (Continue) — это совмещает две роли: (1) пока стадия в paused — куда
	// резюмиться; (2) после Continue — постоянная метка "эта стадия уже
	// проходила цикл паузы хотя бы раз", которую auto_run-гейт (Task 5)
	// использует, чтобы срабатывать только при самой первой активации.
	PausedFrom StageStatus `json:"paused_from,omitempty"`
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
	// IdleAccumulatedMs/BackoffAccumulatedMs — накопленное время простоя/бэкоффа
	// на момент последнего примененного перехода. Текущий ОТКРЫТЫЙ эпизод (если
	// флоу простаивает/стадия ретраится прямо сейчас) НЕ хранится отдельным
	// полем — он добавляется при чтении через IdleSince()/BackoffOpenSince(),
	// потому что момент его начала всегда совпадает с UpdatedAt той стадии,
	// которая последней сменила статус (см. accountIdleAndBackoff).
	IdleAccumulatedMs    int64 `json:"idle_accumulated_ms"`
	BackoffAccumulatedMs int64 `json:"backoff_accumulated_ms"`
}

// NewRunState creates an initial RunState with all stages pending.
func NewRunState(stageIDs []string) *RunState {
	rs := &RunState{
		StartedAt:  time.Now(),
		StageOrder: stageIDs,
		Stages:     make(map[string]StageState, len(stageIDs)),
	}
	for _, id := range stageIDs {
		// UpdatedAt остаётся нулевым (стадия ещё ни разу не переходила), а не
		// time.Now(): maxUpdatedAt/accountIdleAndBackoff считает максимум
		// UpdatedAt по ВСЕМ стадиям, включая нетронутые pending. Если бы тут
		// стоял time.Now(), это было бы время конструирования RunState —
		// разное на живом Open() (старт рана) и на replay-Open() при resume
		// (момент повторного открытия, т.е. позже последней транзакции в
		// логе), и максимум "ехал" бы вперёд только из-за replay, раздувая
		// накопленный Idle. Нулевое время игнорируется maxUpdatedAt/isZero-
		// проверкой в accountIdleAndBackoff — как и для стадий, ещё не
		// получивших ни одной транзакции.
		rs.Stages[id] = StageState{Status: StatusPending}
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
	pausedFrom := rs.Stages[stageID].PausedFrom
	if status == StatusPaused {
		pausedFrom = rs.Stages[stageID].Status // статус ДО этого перехода
	}
	rs.Stages[stageID] = StageState{Status: status, UpdatedAt: t, PausedFrom: pausedFrom}
}

// isIdle сообщает, ждёт ли флоу реакции пользователя прямо сейчас — единое
// состояние на весь флоу (в отличие от backoff, который считается по каждой
// стадии отдельно, см. accountIdleAndBackoff). Порт isIdle() из
// use-idle-time.ts (pkg/web/dashboard/src/hooks/use-idle-time/use-idle-time.ts):
//
//	idle = есть вопрос к пользователю на любой стадии (awaiting_user_input,
//	       awaiting_approval), ИЛИ (есть failed-стадия И ни один агент не
//	       активен: running/planning/revising). retrying намеренно не
//	       считается «активной работой» — это пассивный бэкофф-таймер,
//	       отдельная метрика (см. BackoffOpenSince).
func isIdle(stages map[string]StageState) bool {
	hasFailed := false
	anyActive := false
	for _, st := range stages {
		switch st.Status {
		case StatusAwaitingUserInput, StatusAwaitingApproval, StatusPaused:
			return true
		case StatusFailed:
			hasFailed = true
		case StatusRunning, StatusPlanning, StatusRevising:
			anyActive = true
		default:
			// pending/ready/retrying/done/hook_failed — не влияют на isIdle.
		}
	}
	return hasFailed && !anyActive
}

// maxUpdatedAt возвращает самый свежий UpdatedAt среди всех стадий — момент
// последнего примененного во флоу перехода. Используется и как «время
// предыдущего события» при накоплении Idle, и как idle_since для API (см.
// RunState.IdleSince).
func maxUpdatedAt(stages map[string]StageState) time.Time {
	var latest time.Time
	for _, st := range stages {
		if st.UpdatedAt.After(latest) {
			latest = st.UpdatedAt
		}
	}
	return latest
}

// accountIdleAndBackoff обновляет RunState.IdleAccumulatedMs/BackoffAccumulatedMs
// ДО применения перехода {stageID, to, t} к rs — читает rs.Stages как оно было
// ПЕРЕД этим переходом. Вызывается из ОБОИХ мест, применяющих переходы к
// RunState (parseEventLog при replay и Store.Apply при живой работе), чтобы
// восстановление после перезапуска (Store.Open → replayEvents → parseEventLog)
// давало те же накопленные значения, что и живой прогон.
func accountIdleAndBackoff(rs *RunState, stageID string, to StageStatus, t time.Time) {
	if isIdle(rs.Stages) {
		if prev := maxUpdatedAt(rs.Stages); !prev.IsZero() && t.After(prev) {
			rs.IdleAccumulatedMs += t.Sub(prev).Milliseconds()
		}
	}

	before := rs.Stages[stageID]
	if before.Status == StatusRetrying && to != StatusRetrying && t.After(before.UpdatedAt) {
		rs.BackoffAccumulatedMs += t.Sub(before.UpdatedAt).Milliseconds()
	}
}

// IdleSince возвращает момент начала текущего периода простоя, если флоу
// простаивает сейчас (см. isIdle) — иначе nil.
func (rs *RunState) IdleSince() *time.Time {
	if !isIdle(rs.Stages) {
		return nil
	}
	t := maxUpdatedAt(rs.Stages)
	if t.IsZero() {
		return nil
	}
	return &t
}

// BackoffOpenSince возвращает момент входа в retrying для каждой стадии,
// которая сейчас в этом статусе — параллельные эпизоды суммируются на чтении
// (фронтенд), а не мёржатся здесь (осознанное упрощение, см.
// use-status-duration.ts).
func (rs *RunState) BackoffOpenSince() []time.Time {
	var out []time.Time
	for _, st := range rs.Stages {
		if st.Status == StatusRetrying {
			out = append(out, st.UpdatedAt)
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
	res := parseEventLog(data, &rs)
	if res.corrupted {
		return rs, ErrCorruptLog
	}
	rs.LastSeq = res.lastSeq
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

// replayResult — итог разбора events.jsonl, общий для обоих путей чтения лога:
// replayEvents (Open, с усечением по goodOffset) и LoadRunState (read-only check).
type replayResult struct {
	history    []Transition
	lastSeq    uint64
	goodOffset int64 // байтовый конец последней ЗАФИКСИРОВАННОЙ (newline-terminated) записи
	corrupted  bool  // битая ПОЛНАЯ строка в середине лога (есть валидные записи после)
}

// parseEventLog — единственный парсер events.jsonl. Применяет переходы к rs.
// Оборванный/незакоммиченный хвост (последняя строка без \n) усекается и НЕ
// считается порчей. Битая полная строка в середине → corrupted=true.
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
		offset += int64(len(line)) + 1 // +1 на \n (у не-последних строк он всегда есть)
		if len(bytes.TrimSpace(line)) == 0 {
			goodOffset = offset
			continue
		}
		var t Transition
		if json.Unmarshal(line, &t) != nil {
			res.goodOffset = goodOffset
			res.corrupted = true
			return res
		}
		// Восстанавливаем реальное начало рана из первого события лога.
		// NewRunState штампует StartedAt = time.Now(), что на resume/restart-Open
		// равно моменту повторного открытия, а не старту рана — без этой
		// перезаписи STARTED/ELAPSED в дашборде скачком обнулялись после каждого
		// рестарта afm (лог хранит t.Time каждого перехода, первое событие —
		// EvStartRun — и есть настоящее начало). На свежем ране лог при Open пуст,
		// сюда не заходим, и StartedAt остаётся временем Open ≈ реальному старту.
		if len(res.history) == 0 && !t.Time.IsZero() {
			rs.StartedAt = t.Time
		}
		accountIdleAndBackoff(rs, t.StageID, t.To, t.Time)
		rs.SetStageStatusAt(t.StageID, t.To, t.Time)
		res.history = append(res.history, t)
		res.lastSeq = t.Seq
		goodOffset = offset
	}
	res.goodOffset = goodOffset
	return res
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

// preNoteFile — заметка, которую пользователь прикрепил к стадии, пока она ещё
// была pending (до старта). В отличие от feedback.md (поправка по ходу работы,
// дописывается с revision-разделителями), pre-note — одно редактируемое поле:
// сохранение заменяет текст, сохранение пустого — удаляет файл. Оркестратор
// вклеивает её в начальный контекст агента на первом старте стадии, см.
// (*Orchestrator).preNoteBlock.
const preNoteFile = "prenote.md"

// SavePreNote атомарно (temp+rename) записывает pre-note стадии, либо удаляет
// файл, когда note пустой/из одних пробелов (пользователь очистил поле =
// «убрал заметку»). stageDir может ещё не существовать — pending-стадия часто
// не имеет каталога на диске.
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

// LatestPlanVersion scans stageDir for plan.v{N}.md files and returns the
// highest N found (0 if none) along with that file's content ("" if none).
// Garbage names (non-numeric middle, wrong extension) are ignored, not errors.
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
