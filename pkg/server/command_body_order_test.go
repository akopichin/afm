package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/akopichin/afm/contracts/state"
)

// Нечитаемое тело не должно скрывать ошибку стадии: сначала её существование, статус и тип, потом тело.
func TestStageCommandBrokenBodyReportsStageErrorFirst(t *testing.T) {
	const (
		reviseStatuses = "stage is pending, not running, awaiting_approval or awaiting_assistants"
		buttonStatuses = "stage is pending, not awaiting_approval or running"
	)
	script := StageConfig{IsScript: true}
	cases := []struct {
		name   string
		id     string
		action string
		status state.StageStatus
		stage  StageConfig
		code   int
		body   string
	}{
		{"unknown_revise", "ghost", "revise", state.StatusRunning, StageConfig{}, http.StatusNotFound, "stage not found"},
		{"unknown_note", "ghost", "note", state.StatusPending, StageConfig{}, http.StatusNotFound, "stage not found"},
		{"unknown_button", "ghost", "button", state.StatusRunning, StageConfig{}, http.StatusNotFound, "stage not found"},
		{"pending_revise", testStageID, "revise", state.StatusPending, StageConfig{}, http.StatusBadRequest, reviseStatuses},
		{"pending_button", testStageID, "button", state.StatusPending, StageConfig{}, http.StatusBadRequest, buttonStatuses},
		{"running_note", testStageID, "note", state.StatusRunning, StageConfig{}, http.StatusBadRequest, "stage is running, not pending"},
		{"script_revise", testStageID, "revise", state.StatusRunning, script, http.StatusBadRequest, "script stage has no agent to note"},
		{"script_button", testStageID, "button", state.StatusRunning, script, http.StatusBadRequest, "script stage has no agent"},
		{"script_note", testStageID, "note", state.StatusPending, script, http.StatusBadRequest, "script stage has no agent to note"},
		{"valid_stage_revise", testStageID, "revise", state.StatusRunning, StageConfig{}, http.StatusBadRequest, "invalid request body"},
		{"valid_stage_note", testStageID, "note", state.StatusPending, StageConfig{}, http.StatusBadRequest, "invalid request body"},
		{"valid_stage_button", testStageID, "button", state.StatusRunning, StageConfig{}, http.StatusBadRequest, "invalid request body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: tc.status, stage: tc.stage})
			w := env.post(t, "/api/stages/"+tc.id+"/"+tc.action, "application/json", "{broken")
			wantResponse(t, w, tc.code, contentTypeText, textBody(tc.body))
		})
	}
}

// transitionOnRead переводит стадию при первом чтении тела: ответ должен отражать статус на момент
// проверки, а не тот, что стал после чтения.
type transitionOnRead struct {
	move func()
	once sync.Once
}

func (b *transitionOnRead) Read([]byte) (int, error) {
	b.once.Do(b.move)
	return 0, io.EOF
}

func TestStageCommandChecksStageBeforeReadingBody(t *testing.T) {
	cases := []struct {
		name         string
		action       string
		from, to     state.StageStatus
		wantResponse string
	}{
		{"revise", "revise", state.StatusPending, state.StatusRunning, "stage is pending, not running, awaiting_approval or awaiting_assistants"},
		{"button", "button", state.StatusPending, state.StatusRunning, "stage is pending, not awaiting_approval or running"},
		{"note", "note", state.StatusRunning, state.StatusDone, "stage is running, not pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: tc.from})
			body := &transitionOnRead{move: func() {
				err := env.store.Apply(&state.Transition{StageID: testStageID, From: tc.from, To: tc.to, Event: "test_move"})
				if err != nil {
					t.Error(err)
				}
			}}
			req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/"+tc.action, body)
			w := httptest.NewRecorder()
			env.srv.Handler().ServeHTTP(w, req)
			wantResponse(t, w, http.StatusBadRequest, contentTypeText, textBody(tc.wantResponse))
		})
	}
}
