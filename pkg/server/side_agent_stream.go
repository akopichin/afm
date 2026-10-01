package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/akopichin/afm/pkg/sideagent"
)

// sideAgentHeartbeatPeriod — интервал SSE-heartbeat (comment-строка), чтобы
// соединение и промежуточные прокси не считали поток мёртвым. Var (не const),
// чтобы тест мог укоротить интервал.
var sideAgentHeartbeatPeriod = 15 * time.Second

// handleSideAgentStream стримит журнал разговора через Server-Sent Events:
// сначала replay истории после курсора, затем live-события. id каждого
// SSE-события = chat seq.
//
// Инвариант «хвост не теряется»: подписка регистрируется ДО снимка истории, так
// что событие, закоммиченное в окне между replay и live, попадает в канал и
// отфильтровывается по монотонному seq (дубли отбрасываются). Reconnect через
// Last-Event-ID/after_seq добирает весь пропущенный хвост без дублей.
//
// Hаndler ограничен по времени жизни: select на ctx запроса И на server-level
// done (закрывается в Shutdown) — разрыв подписки отменяет ТОЛЬКО handler, агент
// продолжает работать под ctx менеджера. Переполнивший буфер медленный клиент
// теряет соединение (Subscribe закрывает канал) и перечитывает историю при
// reconnect — агент при этом не блокируется.
func (s *Server) handleSideAgentStream(w http.ResponseWriter, r *http.Request) {
	cursor := streamCursor(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // не буферизовать на прокси (nginx)
	rc := http.NewResponseController(w)

	// Подписка ДО чтения истории — см. инвариант «хвост не теряется».
	ch, cancel := s.sideAgent.Subscribe()
	defer cancel()

	lastSent := cursor
	for _, e := range s.sideAgent.History() {
		if e.Seq <= lastSent {
			continue
		}
		if err := writeSSEEvent(w, rc, e, s.wsWriteWait); err != nil {
			return
		}
		lastSent = e.Seq
	}

	ticker := time.NewTicker(sideAgentHeartbeatPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case e, ok := <-ch:
			if !ok {
				return // overflow/drop: клиент переподключится и перечитает историю
			}
			if e.Seq <= lastSent {
				continue // монотонный фильтр: дубль replay/live
			}
			if err := writeSSEEvent(w, rc, e, s.wsWriteWait); err != nil {
				return
			}
			lastSent = e.Seq
		case <-ticker.C:
			if err := writeSSEComment(w, rc, s.wsWriteWait); err != nil {
				return
			}
		}
	}
}

// streamCursor берёт курсор из Last-Event-ID (EventSource reconnect) или, если
// заголовка нет, из ?after_seq=.
func streamCursor(r *http.Request) uint64 {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return parseUintQuery(r, "after_seq")
}

// writeSSEEvent пишет одно событие как SSE-кадр с ограниченным дедлайном записи.
// Неэнкодируемое событие пропускается, поток не рвётся.
func writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, e sideagent.Event, writeWait time.Duration) error {
	data, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	_ = rc.SetWriteDeadline(time.Now().Add(writeWait)) // best-effort (recorder не поддерживает)
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, data); err != nil {
		return err
	}
	return rc.Flush()
}

// writeSSEComment пишет heartbeat-строку (SSE-комментарий) с ограниченным
// дедлайном записи.
func writeSSEComment(w http.ResponseWriter, rc *http.ResponseController, writeWait time.Duration) error {
	_ = rc.SetWriteDeadline(time.Now().Add(writeWait))
	if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
		return err
	}
	return rc.Flush()
}
