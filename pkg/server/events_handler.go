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
	"github.com/akopichin/afm/pkg/state"
)

// maxReplayEvents ограничивает историю, отдаваемую /api/events, последними
// 200 записями — совпадает с MAX_EVENTS во фронте (use-event-feed.ts):
// отдавать больше бессмысленно, клиент всё равно обрежет.
const maxReplayEvents = 200

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

// autonomousLabel — имя фазы (reconstructAgentActions) и значение
// supervisor-решения (Task 3, logSupervisorDecision track="autonomous")
// текстуально совпадают, поэтому используем одну общую константу вместо
// двух одинаковых строковых литералов (goconst).
const autonomousLabel = "autonomous"

// feedEvent — одна запись реплея истории ленты событий. Seq заполняется
// только для событий, производных от реальной FSM-transition (events.jsonl) —
// это стабильный ключ дедупликации на фронте при слиянии с live-потоком
// WebSocket. Для остальных типов (agent_action/supervisor_decision/notices)
// Seq остаётся нулевым.
type feedEvent struct {
	Type      string    `json:"type"`
	StageID   string    `json:"stage_id"`
	Data      any       `json:"data,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Seq       uint64    `json:"seq,omitempty"`
}

func (s *Server) handleEvents(w http.ResponseWriter, _ *http.Request) {
	events := s.reconstructEventHistory()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

func (s *Server) reconstructEventHistory() []feedEvent {
	var out []feedEvent

	history, _ := s.store.History()
	for _, t := range history {
		out = append(out, transitionToFeedEvents(t)...)
	}

	snap := s.store.Snapshot()
	for stageID := range snap.Stages {
		out = append(out, reconstructAgentActions(s.runDir, stageID)...)
	}

	out = append(out, reconstructSupervisorDecisions(s.runDir)...)
	out = append(out, reconstructNotices(s.runDir)...)

	slices.SortFunc(out, func(a, b feedEvent) int { return a.Timestamp.Compare(b.Timestamp) })
	if len(out) > maxReplayEvents {
		out = out[len(out)-maxReplayEvents:]
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
	for _, phase := range []string{"planning", "implementation", "review", autonomousLabel} {
		path := filepath.Join(stageDir, phase+".jsonl")
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
// only the last maxReplayEvents (200) events survive the final cap across
// ALL sources anyway, so reading an unbounded number of lines per phase log
// (agent stream-json logs routinely run multi-MB) wastes memory/CPU on every
// request without ever being used. 500 gives generous headroom above 200
// while bounding worst case regardless of total log size.
const maxLinesPerLog = 500

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

// reconstructSupervisorDecisions читает run-level supervisor.jsonl
// (пишется logSupervisorDecision, pkg/orchestrator/supervisor_track.go).
func reconstructSupervisorDecisions(runDir string) []feedEvent {
	path := filepath.Join(runDir, "supervisor.jsonl")
	var out []feedEvent
	for _, line := range readLines(path) {
		var e struct {
			Ts       string `json:"ts"`
			StageID  string `json:"stage_id"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.Ts)
		if err != nil {
			continue
		}
		out = append(out, feedEvent{
			Type: "supervisor_decision", StageID: e.StageID,
			Data:      map[string]any{"can_execute_autonomously": e.Decision == autonomousLabel, "reason": e.Reason},
			Timestamp: ts,
		})
	}
	return out
}

// reconstructNotices читает run-level notices.jsonl (Task 3, appendNotice).
func reconstructNotices(runDir string) []feedEvent {
	path := filepath.Join(runDir, "notices.jsonl")
	var out []feedEvent
	for _, line := range readLines(path) {
		var e struct {
			Time    time.Time `json:"time"`
			Type    string    `json:"type"`
			StageID string    `json:"stage_id"`
			Data    any       `json:"data,omitempty"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, feedEvent{Type: e.Type, StageID: e.StageID, Data: e.Data, Timestamp: e.Time})
	}
	return out
}
