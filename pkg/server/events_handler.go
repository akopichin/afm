package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// maxReplayEvents bounds the GLOBAL /api/events history (Full feed + the WS
// status-refresh trigger). Raised from 200 to 1000 so the whole-flow feed is
// deep enough on long runs. (The AI-verify badge no longer reads this — it's a
// durable per-stage field on /api/status, see Task 7.)
const maxReplayEvents = 1000

// maxStageReplayEvents bounds the PER-STAGE /api/events?stage=<id> history
// (the Feed tab). Independent of maxReplayEvents so a completed stage always
// shows its own last 200 events regardless of global recency — the whole point
// of the fix.
const maxStageReplayEvents = 200

// Названия FSM-событий (Transition.Event) и производных feed-типов, которые
// из них выводятся, текстуально совпадают — общие константы вместо
// повторяющихся строковых литералов (goconst).
const (
	eventAskUser           = "ask_user"
	eventUserAnswered      = "user_answered"
	eventScheduleRetry     = "schedule_retry"
	eventFail              = "fail"
	reasonRetriesExhausted = "retries exhausted"
	typeRetryScheduled     = "retry_scheduled"
	typeRetryExhausted     = "retry_exhausted"
)

// feedEvent — одна запись реплея истории ленты событий. Seq заполняется
// только для событий, производных от реальной FSM-transition (events.jsonl) —
// это стабильный ключ дедупликации на фронте при слиянии с live-потоком
// WebSocket. Для остальных типов (agent_action/notices) Seq остаётся нулевым.
type feedEvent struct {
	Type      string    `json:"type"`
	StageID   string    `json:"stage_id"`
	Data      any       `json:"data,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Seq       uint64    `json:"seq,omitempty"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	stage := r.URL.Query().Get("stage")
	events := s.reconstructEventHistory(stage)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

// reconstructEventHistory reconstructs the feed. When stageID is non-empty it
// returns only that stage's events, capped at maxStageReplayEvents; otherwise
// the whole flow, capped at maxReplayEvents.
func (s *Server) reconstructEventHistory(stageID string) []feedEvent {
	var out []feedEvent

	history, _ := s.store.History()
	for _, t := range history {
		out = append(out, transitionToFeedEvents(t)...)
	}

	snap := s.store.Snapshot()
	for id := range snap.Stages {
		if stageID != "" && id != stageID {
			continue // per-stage: skip other stages' agent-action reconstruction
		}
		out = append(out, reconstructAgentActions(s.runDir, id)...)
	}

	out = append(out, reconstructNotices(s.runDir, stageID)...) // stage-aware (Step 3b)

	limit := maxReplayEvents
	if stageID != "" {
		out = slices.DeleteFunc(out, func(e feedEvent) bool { return e.StageID != stageID })
		limit = maxStageReplayEvents
	}

	slices.SortFunc(out, func(a, b feedEvent) int { return a.Timestamp.Compare(b.Timestamp) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// transitionToFeedEvents мапит одну FSM-transition в one-or-two feedEvent —
// повторяя ровно то, что публикует live-система: Trigger (orchestrator.go)
// ВСЕГДА публикует stage_status_changed при любом переходе; ask_user/
// user_answered/retry_scheduled/retry_exhausted публикуются ДОПОЛНИТЕЛЬНО из
// своих конкретных call site (dialog_poller.go/retry.go) — узнаваемы по
// Event/Reason самой transition, других живых типов (approved/revised/
// manual_retry) в проде сегодня никто не публикует, поэтому и здесь не
// реконструируем.
func transitionToFeedEvents(t state.Transition) []feedEvent {
	out := []feedEvent{{
		Type: "stage_status_changed", StageID: t.StageID, Data: string(t.To),
		Timestamp: t.Time, Seq: t.Seq,
	}}
	switch {
	case t.Event == eventAskUser:
		out = append(out, feedEvent{Type: eventAskUser, StageID: t.StageID, Timestamp: t.Time, Seq: t.Seq})
	case t.Event == eventUserAnswered:
		out = append(out, feedEvent{Type: eventUserAnswered, StageID: t.StageID, Timestamp: t.Time, Seq: t.Seq})
	case t.Event == eventScheduleRetry:
		out = append(out, feedEvent{Type: typeRetryScheduled, StageID: t.StageID, Data: t.Reason, Timestamp: t.Time, Seq: t.Seq})
	case t.Event == eventFail && t.Reason == reasonRetriesExhausted:
		out = append(out, feedEvent{Type: typeRetryExhausted, StageID: t.StageID, Timestamp: t.Time, Seq: t.Seq})
	default:
		// Остальные Event (approved/revised/manual_retry и т.п.) сегодня в
		// проде отдельным live-типом не публикуются — для них достаточно
		// уже добавленного stage_status_changed.
	}
	return out
}

// reconstructAgentActions парсит <phase>.jsonl каждой фазы стадии тем же
// парсером, что и живой лог (executor.ParseToolAction), и распределяет
// найденные действия равномерно во времени между началом и концом фазы
// (границы — из events.jsonl). Stream-json не содержит per-строчного
// timestamp — точный порядок внутри фазы сохраняется (индекс в файле), но
// точное время интерполируется, а не измеряется.
func reconstructAgentActions(runDir, stageID string) []feedEvent {
	stageDir := filepath.Join(runDir, stageID)
	var out []feedEvent
	for _, p := range flow.Phases() {
		path := filepath.Join(stageDir, flow.PhaseJSONL(p))
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		lines := readLines(path)
		if len(lines) == 0 {
			continue
		}
		// Окно интерполяции: [mtime - len(lines)*shortStep, mtime]. Абсолютная
		// точность не нужна — важно, чтобы записи одной фазы шли по порядку и
		// попадали в примерно правильный участок общей ленты.
		end := info.ModTime()
		step := time.Second
		start := end.Add(-time.Duration(len(lines)) * step)
		for i, line := range lines {
			toolName, detail, ok := executor.ParseToolAction(line, 200)
			if !ok {
				continue
			}
			out = append(out, feedEvent{
				Type:      "agent_action",
				StageID:   stageID,
				Data:      map[string]string{"tool": toolName, "detail": detail},
				Timestamp: start.Add(time.Duration(i) * step),
			})
		}
	}
	return out
}

// maxLinesPerLog bounds how many trailing lines readLines keeps per file —
// only the last maxReplayEvents (1000) events survive the final cap across
// ALL sources anyway, so reading an unbounded number of lines per phase log
// (agent stream-json logs routinely run multi-MB) wastes memory/CPU on every
// request without ever being used. 1200 gives comfortable headroom above the
// 1000 global cap while bounding worst case regardless of total log size.
//
// Approximation caveat: this window counts RAW lines, not parseable actions —
// a phase log with many non-assistant/unparseable lines yields fewer than
// 1200 reconstructed events, so the global feed (and, symmetrically, the
// per-stage 200) can be under-filled for a log that is mostly noise. Accepted
// approximation (agent stream-json logs are overwhelmingly assistant events
// in practice, and the durable per-stage feed is always >= what the old
// flow-wide-200 gave); do not add a scan-until-N-valid loop (YAGNI unless a
// real under-fill is observed).
const maxLinesPerLog = 1200

// readLines reads path and keeps only the last maxLinesPerLog lines (a
// sliding window, not the whole file) — bounds memory even for very large
// logs. Returns nil if the file doesn't exist or can't be opened.
func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	lines := make([]string, 0, maxLinesPerLog)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > maxLinesPerLog {
			lines = lines[1:]
		}
	}
	return lines
}

// dialogDedupTypes — allowlist of notice types deduped by content in
// reconstructNotices (task-fixB, F2/F3). ONLY dialog_question/dialog_answer:
// `processed` in dialog_poller.go's pollQuestions is in-memory, so an afm
// restart with a still-unanswered interactive question re-scans and re-emits
// the SAME dialog_question notice (and the F1 give-up fallback can itself
// double-emit for the same reason — see publishDialogQuestion/
// giveUpOnMalformedQuestion). Every OTHER notice type is intentionally left
// alone: script_output/auto_answered/context_warning legitimately repeat
// (e.g. two script stages can genuinely print the same line), so a blanket
// dedup would silently drop real, distinct feed rows.
var dialogDedupTypes = map[string]bool{
	string(bus.EventDialogQuestion): true,
	string(bus.EventDialogAnswer):   true,
}

// reconstructNotices reads the shared notices.jsonl. When stageID != "" it
// keeps only that stage's notices (last maxStageReplayEvents of them);
// otherwise it keeps the last maxReplayEvents across all stages. Retention is
// applied PER SCOPE while scanning (a ring buffer bounded by the cap), NOT a
// flow-wide line truncation first — otherwise an early stage's notices vanish
// under newer other-stage lines (the exact bug this feature fixes). O(file)
// time, O(cap) memory; only invoked on an /api/events request, not a hot path.
//
// dialog_question/dialog_answer records are deduplicated by content
// (type+stage_id+phase+id+title) — first occurrence wins, order/timestamp of
// the rest is untouched (see dialogDedupTypes). The dedup `seen` set spans the
// whole file (not bounded by the ring), so total memory is O(cap + distinct
// dialog-notice keys) — a correctness improvement (dialog notices now dedup
// across the entire run) at bounded, small extra memory.
func reconstructNotices(runDir, stageID string) []feedEvent {
	path := filepath.Join(runDir, "notices.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	ringCap := maxReplayEvents
	if stageID != "" {
		ringCap = maxStageReplayEvents
	}
	ring := make([]feedEvent, 0, ringCap)
	seen := map[string]bool{} // dialog_question/dialog_answer content dedup (unchanged intent)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e struct {
			Time    time.Time `json:"time"`
			Type    string    `json:"type"`
			StageID string    `json:"stage_id"`
			Data    any       `json:"data,omitempty"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if stageID != "" && e.StageID != stageID {
			continue // per-stage: only this stage's notices enter the ring
		}
		if dialogDedupTypes[e.Type] {
			key := dialogNoticeDedupKey(e.Type, e.StageID, e.Data)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		ring = append(ring, feedEvent{Type: e.Type, StageID: e.StageID, Data: e.Data, Timestamp: e.Time})
		if len(ring) > ringCap {
			ring = ring[1:]
		}
	}
	return ring
}

// dialogNoticeDedupKey builds the content dedup key for a dialog_question/
// dialog_answer notice: type + stage_id + phase + id + title. Data arrives as
// `any` holding a map[string]any (the generic JSON round-trip of
// mcp.DialogFeedNotice's payload) — fields are read defensively so a
// malformed/missing field degrades to an empty string component instead of
// panicking.
func dialogNoticeDedupKey(typ, stageID string, data any) string {
	m, _ := data.(map[string]any)
	phase, _ := m["phase"].(string)
	id, _ := m["id"].(string)
	title, _ := m["title"].(string)
	return typ + "|" + stageID + "|" + phase + "|" + id + "|" + title
}
