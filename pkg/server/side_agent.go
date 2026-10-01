package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/akopichin/afm/pkg/sideagent"
)

// SideAgentService — узкий интерфейс «бокового» разговора, потребляемый сервером
// вместо конкретного *sideagent.Manager (ради тест-даблов). nil-значение в
// Server.sideAgent означает, что фича выключена (как workspace): маршруты
// /api/side-agent/* не регистрируются, capability в /api/status = false.
type SideAgentService interface {
	History() []sideagent.Event
	Send(clientMessageID, text string) (string, error)
	Stop(turnID string) error
	Subscribe() (<-chan sideagent.Event, func())
	ConversationID() string
	Command() string
	Unavailable() bool
}

// *sideagent.Manager удовлетворяет интерфейсу — проверка на этапе компиляции.
var _ SideAgentService = (*sideagent.Manager)(nil)

// maxSideAgentBodyBytes ограничивает тело POST-запроса. Берётся с запасом над
// sideagent.MaxMessageBytes, чтобы сообщение ровно на пределе дошло до Send и
// получило типизированный 413 message_too_large, а не обрезалось MaxBytesReader
// как invalid_body.
const maxSideAgentBodyBytes = sideagent.MaxMessageBytes + (16 << 10)

// sideAgentStateResponse — форма GET /api/side-agent (поллинг-состояние, когда
// SSE недоступен/просрочен). Курсор продвигается ТОЛЬКО по возвращённым
// событиям, а last_seq отражает весь журнал.
type sideAgentStateResponse struct {
	ConversationID string            `json:"conversation_id"`
	RunID          string            `json:"run_id"`
	Command        string            `json:"command"`
	Unavailable    bool              `json:"unavailable"`
	State          string            `json:"state"`
	Events         []sideagent.Event `json:"events"`
	LastSeq        uint64            `json:"last_seq"`
	NextCursor     uint64            `json:"next_cursor"`
	HasMore        bool              `json:"has_more"`
}

// sideAgentSendResponse — форма ответа на POST /api/side-agent/messages.
type sideAgentSendResponse struct {
	TurnID string `json:"turn_id"`
	State  string `json:"state"`
	Cursor uint64 `json:"cursor"`
}

const (
	sideStateIdle    = "idle"
	sideStateRunning = "running"
)

// routeSideAgent диспетчеризует все /api/side-agent[/...] запросы. Регистрируется
// в New ТОЛЬКО когда s.sideAgent != nil — иначе маршрутов нет и запрос доходит до
// статики (404), старый сервер не затронут.
func (s *Server) routeSideAgent(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/side-agent" && r.Method == http.MethodGet:
		s.handleSideAgentState(w, r)
	case r.URL.Path == "/api/side-agent/messages" && r.Method == http.MethodPost:
		s.handleSideAgentSend(w, r)
	case r.URL.Path == "/api/side-agent/interrupt" && r.Method == http.MethodPost:
		s.handleSideAgentInterrupt(w, r)
	case r.URL.Path == "/api/side-agent/stream" && r.Method == http.MethodGet:
		s.handleSideAgentStream(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleSideAgentState отдаёт текущее состояние разговора и страницу событий
// после курсора. Состояние (idle/running) считается по ВСЕЙ истории, а не только
// по странице.
func (s *Server) handleSideAgentState(w http.ResponseWriter, r *http.Request) {
	history := s.sideAgent.History()
	lastSeq := uint64(0)
	if n := len(history); n > 0 {
		lastSeq = history[n-1].Seq
	}
	after := parseUintQuery(r, "after_seq")
	limit := parseSideAgentLimit(r)

	events := []sideagent.Event{}
	nextCursor := after
	for _, e := range history {
		if e.Seq <= after {
			continue
		}
		if limit > 0 && len(events) >= limit {
			break
		}
		events = append(events, e)
		nextCursor = e.Seq
	}

	resp := sideAgentStateResponse{
		ConversationID: s.sideAgent.ConversationID(),
		RunID:          s.sideAgent.ConversationID(),
		Command:        s.sideAgent.Command(),
		Unavailable:    s.sideAgent.Unavailable(),
		State:          deriveSideAgentState(history),
		Events:         events,
		LastSeq:        lastSeq,
		NextCursor:     nextCursor,
		HasMore:        nextCursor < lastSeq,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleSideAgentSend принимает сообщение пользователя. Клиент управляет ТОЛЬКО
// client_message_id и text — команда/аргументы/CWD задаются сервером (D1), тело
// их не содержит и не читается.
func (s *Server) handleSideAgentSend(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeSideAgentErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var req struct {
		ClientMessageID string `json:"client_message_id"`
		Text            string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSideAgentBodyBytes)).Decode(&req); err != nil {
		// Тело превысило HTTP-кап (MaxBytesReader) — это сообщение, которое слишком
		// велико: типизированный 413 message_too_large, а не 400 invalid_body
		// (который остаётся для битого JSON).
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeSideAgentErrCode(w, http.StatusRequestEntityTooLarge, string(sideagent.ErrCodeMessageTooLarge))
			return
		}
		writeSideAgentErrCode(w, http.StatusBadRequest, "invalid_body")
		return
	}
	turnID, err := s.sideAgent.Send(req.ClientMessageID, req.Text)
	if err != nil {
		writeSideAgentError(w, err)
		return
	}
	// Состояние — РЕАЛЬНОЕ состояние возвращённого хода, не хардкод "running":
	// идемпотентный повтор уже завершённого хода возвращает его id, и клиент не
	// должен считать неактивный ход активным (Stop целил бы в протухший ход).
	h := s.sideAgent.History()
	cursor := uint64(0)
	if len(h) > 0 {
		cursor = h[len(h)-1].Seq
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(sideAgentSendResponse{TurnID: turnID, State: turnStateInHistory(h, turnID), Cursor: cursor})
}

// handleSideAgentInterrupt прерывает ровно указанный ход. Устаревший turn_id →
// 409 stale_turn (Manager.Stop).
func (s *Server) handleSideAgentInterrupt(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeSideAgentErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var req struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSideAgentBodyBytes)).Decode(&req); err != nil {
		writeSideAgentErrCode(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if req.TurnID == "" {
		writeSideAgentErrCode(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if err := s.sideAgent.Stop(req.TurnID); err != nil {
		writeSideAgentError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "interrupting"})
}

// turnStateInHistory возвращает "running", если последнее FSM-событие хода
// turnID не терминально, иначе "idle" (ход не найден → idle). В отличие от
// deriveSideAgentState (состояние ВСЕЙ беседы по последнему ходу), здесь —
// состояние КОНКРЕТНОГО возвращённого хода (идемпотентный повтор может вернуть
// завершённый ход, пока последний ход беседы — другой).
func turnStateInHistory(history []sideagent.Event, turnID string) string {
	var st sideagent.TurnState
	found := false
	for _, e := range history {
		if e.TurnID != turnID {
			continue
		}
		if s, ok := e.Type.TurnState(); ok {
			st = s
			found = true
		}
	}
	if found && !st.IsTerminal() {
		return sideStateRunning
	}
	return sideStateIdle
}

// deriveSideAgentState возвращает "running", если последний по порядку ход не
// терминален, иначе "idle" (пустой журнал → idle).
func deriveSideAgentState(history []sideagent.Event) string {
	states := make(map[string]sideagent.TurnState)
	var order []string
	for _, e := range history {
		st, ok := e.Type.TurnState()
		if !ok {
			continue
		}
		if _, seen := states[e.TurnID]; !seen {
			order = append(order, e.TurnID)
		}
		states[e.TurnID] = st
	}
	if len(order) == 0 {
		return sideStateIdle
	}
	if states[order[len(order)-1]].IsTerminal() {
		return sideStateIdle
	}
	return sideStateRunning
}

// sameOriginOK — defence-in-depth проверка Origin для мутирующих POST'ов. Это НЕ
// авторизация: реальная граница — loopback-bind (инвариант 9, sideAgentListenAddr).
// Отсутствующий или чужой Origin отклоняется; одинаковый host с r.Host проходит.
func sameOriginOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// sideAgentErrStatus маппит стабильный код ошибки в HTTP-статус.
func sideAgentErrStatus(code sideagent.ErrorCode) int {
	switch code {
	case sideagent.ErrCodeBusy, sideagent.ErrCodeStaleTurn:
		return http.StatusConflict
	case sideagent.ErrCodeInvalidMessage:
		return http.StatusBadRequest
	case sideagent.ErrCodeMessageTooLarge, sideagent.ErrCodeContextLimit:
		return http.StatusRequestEntityTooLarge
	case sideagent.ErrCodeShuttingDown, sideagent.ErrCodeStorageUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// writeSideAgentError превращает ошибку Send/Stop в JSON {"error":<code>} с
// соответствующим статусом. Не-side ошибка → 500 internal.
func writeSideAgentError(w http.ResponseWriter, err error) {
	var sae *sideagent.Error
	if errors.As(err, &sae) {
		writeSideAgentErrCode(w, sideAgentErrStatus(sae.Code), string(sae.Code))
		return
	}
	writeSideAgentErrCode(w, http.StatusInternalServerError, "internal")
}

func writeSideAgentErrCode(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func parseUintQuery(r *http.Request, key string) uint64 {
	n, _ := strconv.ParseUint(r.URL.Query().Get(key), 10, 64)
	return n
}

// parseSideAgentLimit читает ?limit= (0/отсутствие/мусор → 0 = без лимита).
func parseSideAgentLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
