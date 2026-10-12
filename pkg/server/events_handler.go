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

// maxReplayEvents bounds the GLOBAL /api/events history. Raised to 10000 so the whole-flow feed is deep
// enough on long runs.
const maxReplayEvents = 10000

// maxStageReplayEvents bounds the PER-STAGE /api/events?stage=<id> history.
const maxStageReplayEvents = 2000

// Названия FSM-событий (Transition.Event) и производных feed-типов, которые из них выводятся, текстуально
// совпадают — общие константы вместо повторяющихся строковых литералов (goconst).
const (
	eventAskUser           = "ask_user"
	eventUserAnswered      = "user_answered"
	eventScheduleRetry     = "schedule_retry"
	eventFail              = "fail"
	reasonRetriesExhausted = "retries exhausted"
	typeRetryScheduled     = "retry_scheduled"
	typeRetryExhausted     = "retry_exhausted"
)

// feedEvent — одна запись реплея истории ленты событий. Для остальных типов (agent_action/notices) Seq
// остаётся нулевым.
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

// reconstructEventHistory reconstructs the feed.
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

// transitionToFeedEvents восстанавливает stage_status_changed и дополнительные ask/answer/retry-события,
// совпадающие с live-публикацией.
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
		// Остальные Event (approved/revised/manual_retry и т.п.) сегодня в проде отдельным live-типом не
		// публикуются — для них достаточно уже добавленного stage_status_changed.
	}
	return out
}

// reconstructAgentActions парсит <phase>.jsonl каждой фазы стадии тем же парсером, что и живой лог, и
// распределяет найденные действия равномерно во времени между началом и концом фазы.
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
		// Окно интерполяции: [mtime - len(lines)*shortStep, mtime]. Абсолютная точность не нужна — важно, чтобы
		// записи одной фазы шли по порядку и попадали в примерно правильный участок общей ленты.
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

// maxLinesPerLog bounds how many trailing lines readLines keeps per file. 2400 gives comfortable headroom
// above the 2000 per-stage cap while bounding worst case regardless of total log size.
const maxLinesPerLog = 2400

// readLines reads path and keeps only the last maxLinesPerLog lines — bounds memory even for very large
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

// dialogDedupTypes — allowlist of notice types deduped by content in reconstructNotices.
var dialogDedupTypes = map[string]bool{
	string(bus.EventDialogQuestion): true,
	string(bus.EventDialogAnswer):   true,
}

// isDialogDedupable reports whether a notice takes part in the dialog content dedup.
// dialog_question/dialog_answer always qualify.
func isDialogDedupable(typ string, data any) bool {
	if dialogDedupTypes[typ] {
		return true
	}
	if typ == string(bus.EventAutoAnswered) {
		m, _ := data.(map[string]any)
		dialog, _ := m["dialog"].(bool)
		return dialog
	}
	return false
}

// reconstructNotices reads the shared notices.jsonl. O(file) time, O(cap) memory; only invoked on an
// /api/events request, not a hot path.
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
		if isDialogDedupable(e.Type, e.Data) {
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

// dialogNoticeDedupKey builds the content dedup key for a dialog notice: type + stage_id + phase + id +
// title + question_title + answer.
func dialogNoticeDedupKey(typ, stageID string, data any) string {
	m, _ := data.(map[string]any)
	phase, _ := m["phase"].(string)
	id, _ := m["id"].(string)
	title, _ := m["title"].(string)
	questionTitle, _ := m["question_title"].(string)
	answer, _ := m["answer"].(string)
	return typ + "|" + stageID + "|" + phase + "|" + id + "|" + title + "|" + questionTitle + "|" + answer
}
