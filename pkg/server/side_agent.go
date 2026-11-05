package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/sideagent"
)

// SideAgentService — узкий интерфейс «бокового» разговора, потребляемый сервером вместо конкретного
// *sideagent.Manager.
type SideAgentService interface {
	History() []sideagent.Event
	Enqueue(clientMessageID, text string, target sideagent.TurnTarget) (sideagent.MessageReceipt, error)
	CancelQueued(messageID string) (sideagent.MessageReceipt, error)
	Stop(turnID string) error
	Subscribe() (<-chan sideagent.Event, func())
	ConversationID() string
	Command() string
	Unavailable() bool
}

// *sideagent.Manager удовлетворяет интерфейсу — проверка на этапе компиляции.
var _ SideAgentService = (*sideagent.Manager)(nil)

// maxSideAgentBodyBytes ограничивает тело POST-запроса.
const maxSideAgentBodyBytes = sideagent.MaxMessageBytes + (16 << 10)

// sideAgentStateResponse — форма GET /api/side-agent (поллинг-состояние, когда SSE недоступен/просрочен).
// Курсор продвигается ТОЛЬКО по возвращённым событиям, а last_seq отражает весь журнал.
type sideAgentStateResponse struct {
	ConversationID string            `json:"conversation_id"`
	RunID          string            `json:"run_id"`
	Command        string            `json:"command"`
	CommandName    string            `json:"command_name,omitempty"`
	Unavailable    bool              `json:"unavailable"`
	State          string            `json:"state"`
	ActiveTurnID   string            `json:"active_turn_id"`
	ActiveSource   *sideAgentSource  `json:"active_source"`
	Queued         []sideAgentQueued `json:"queued"`
	Events         []sideagent.Event `json:"events"`
	LastSeq        uint64            `json:"last_seq"`
	NextCursor     uint64            `json:"next_cursor"`
	HasMore        bool              `json:"has_more"`
}

// sideAgentSource — чей запрос выполняет активный ход: пользователь из модалки или Mesh-участник.
type sideAgentSource struct {
	Kind       string `json:"kind"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
}

// sideAgentQueued — ожидающее сообщение в снимке разговора.
type sideAgentQueued struct {
	MessageID       string `json:"message_id"`
	ClientMessageID string `json:"client_message_id"`
	Text            string `json:"text"`
	StageID         string `json:"stage_id"`
	AssistantTaskID string `json:"assistant_task_id"`
	Seq             uint64 `json:"seq"`
}

// sideAgentSendResponse — квитанция POST /api/side-agent/messages. MessageState — состояние записи очереди,
// State — состояние разговора (для старой открытой вкладки); TurnID есть, только пока сообщение забрано ходом.
type sideAgentSendResponse struct {
	MessageID    string `json:"message_id"`
	MessageState string `json:"message_state"`
	ErrorCode    string `json:"error_code,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	State        string `json:"state"`
	Cursor       uint64 `json:"cursor"`
}

// sideAgentCancelResponse — ответ DELETE /api/side-agent/messages/{id}.
type sideAgentCancelResponse struct {
	MessageID    string `json:"message_id"`
	MessageState string `json:"message_state"`
	Cursor       uint64 `json:"cursor"`
}

const sideAgentMessagesPrefix = "/api/side-agent/messages/"

// safeMessageIDPattern — единственный допустимый сегмент пути с id сообщения.
var safeMessageIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// routeSideAgent диспетчеризует все /api/side-agent[/...] запросы.
func (s *Server) routeSideAgent(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/side-agent" && r.Method == http.MethodGet:
		s.handleSideAgentState(w, r)
	case r.URL.Path == "/api/side-agent/messages" && r.Method == http.MethodPost:
		s.handleSideAgentSend(w, r)
	case strings.HasPrefix(r.URL.Path, sideAgentMessagesPrefix) && r.Method == http.MethodDelete:
		s.handleSideAgentCancel(w, r)
	case r.URL.Path == "/api/side-agent/interrupt" && r.Method == http.MethodPost:
		s.handleSideAgentInterrupt(w, r)
	case r.URL.Path == "/api/side-agent/stream" && r.Method == http.MethodGet:
		s.handleSideAgentStream(w, r)
	case r.URL.Path == "/api/side-agent/drafts/confirm" && r.Method == http.MethodPost:
		s.handleConfirmActionDraft(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleSideAgentState отдаёт текущее состояние разговора и страницу событий после курсора. Состояние
// (idle/running) считается по ВСЕЙ истории, а не только по странице.
func (s *Server) handleSideAgentState(w http.ResponseWriter, r *http.Request) {
	history := s.sideAgent.History()
	lastSeq := lastSideSeq(history)
	conv := sideagent.ProjectConversation(history)
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
		CommandName:    s.commandName(s.sideAgent.Command()),
		Unavailable:    s.sideAgent.Unavailable(),
		State:          conv.State(),
		ActiveTurnID:   conv.ActiveTurnID(),
		ActiveSource:   toSideAgentSource(conv.ActiveSource()),
		Queued:         toSideAgentQueued(conv.Queued()),
		Events:         events,
		LastSeq:        lastSeq,
		NextCursor:     nextCursor,
		HasMore:        nextCursor < lastSeq,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleSideAgentSend ставит сообщение пользователя в очередь. Клиент управляет client_message_id, text и
// (опционально) stage_id — какой стадии касается сообщение, для отображения «Context: …» (T4.3). Команда/
// аргументы/CWD по-прежнему задаются сервером (D1). Любое другое поле (в частности depth/root_stage_id/
// mentions) тут читать НЕКУДА — у структуры запроса нет для них поля, они всегда вычисляются сервером
// (TestTurn_DepthAndRootDerivedServerSide, mentions — sideagent.Manager.Enqueue через Config.MentionResolve).
func (s *Server) handleSideAgentSend(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeSideAgentErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var req struct {
		ClientMessageID string `json:"client_message_id"`
		Text            string `json:"text"`
		StageID         string `json:"stage_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSideAgentBodyBytes)).Decode(&req); err != nil {
		// Тело превысило HTTP-кап (MaxBytesReader) — это сообщение, которое слишком велико: типизированный 413
		// message_too_large, а не 400 invalid_body (который остаётся для битого JSON).
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeSideAgentErrCode(w, http.StatusRequestEntityTooLarge, string(sideagent.ErrCodeMessageTooLarge))
			return
		}
		writeSideAgentErrCode(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if strings.HasPrefix(req.ClientMessageID, "mesh:") {
		writeSideAgentErrCode(w, http.StatusBadRequest, string(sideagent.ErrCodeInvalidMessage))
		return
	}
	receipt, err := s.sideAgent.Enqueue(req.ClientMessageID, req.Text, sideagent.TurnTarget{StageID: req.StageID})
	if err != nil {
		writeSideAgentError(w, err)
		return
	}
	// state и cursor — из одной копии журнала: состояние разговора, а не сообщения.
	h := s.sideAgent.History()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(sideAgentSendResponse{
		MessageID: receipt.MessageID, MessageState: string(receipt.State), ErrorCode: string(receipt.ErrorCode),
		TurnID: receipt.TurnID, State: sideagent.ProjectConversation(h).State(), Cursor: lastSideSeq(h),
	})
}

// handleSideAgentCancel удаляет ожидающее сообщение из очереди. Повтор уже отменённого — тоже 200;
// забранное ходом — 409 message_not_queued.
func (s *Server) handleSideAgentCancel(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeSideAgentErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, sideAgentMessagesPrefix)
	if !safeMessageIDPattern.MatchString(id) {
		writeSideAgentErrCode(w, http.StatusNotFound, string(sideagent.ErrCodeMessageNotFound))
		return
	}
	receipt, err := s.sideAgent.CancelQueued(id)
	if err != nil {
		writeSideAgentError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sideAgentCancelResponse{
		MessageID: receipt.MessageID, MessageState: string(receipt.State), Cursor: lastSideSeq(s.sideAgent.History()),
	})
}

func lastSideSeq(history []sideagent.Event) uint64 {
	if len(history) == 0 {
		return 0
	}
	return history[len(history)-1].Seq
}

func toSideAgentSource(src *sideagent.ActiveSource) *sideAgentSource {
	if src == nil {
		return nil
	}
	return &sideAgentSource{Kind: src.Kind, SenderID: src.SenderID, SenderName: src.SenderName}
}

func toSideAgentQueued(queue []sideagent.MessageInfo) []sideAgentQueued {
	out := make([]sideAgentQueued, len(queue))
	for i, m := range queue {
		out[i] = sideAgentQueued{
			MessageID: m.MessageID, ClientMessageID: m.ClientMessageID, Text: m.Text,
			StageID: m.StageID, AssistantTaskID: m.AssistantTaskID, Seq: m.Seq,
		}
	}
	return out
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

// sameOriginOK — defence-in-depth проверка Origin для мутирующих POST'ов. Это НЕ авторизация: реальная
// граница — loopback-bind (инвариант 9, dashboardListenAddr).
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
	case sideagent.ErrCodeBusy, sideagent.ErrCodeStaleTurn, sideagent.ErrCodeClientMessageIDConflict, sideagent.ErrCodeMessageNotQueued:
		return http.StatusConflict
	case sideagent.ErrCodeMessageNotFound:
		return http.StatusNotFound
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
	writeSideAgentErrCode(w, http.StatusInternalServerError, errCodeInternal)
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
