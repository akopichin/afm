package server

import (
	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/state"

	bus "github.com/akopichin/afm/contracts/events"

	implbus "github.com/akopichin/afm-engine/testkit/orchestrator/bus"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/akopichin/afm-engine/testkit/engine"
	implorchestrator "github.com/akopichin/afm-engine/testkit/orchestrator"
	"github.com/akopichin/afm/contracts/mcp"
)

// Контракт HTTP-команд стадий: статус, заголовок, тело и побочные эффекты каждой ветки. Тесты идут через
// настоящий маршрутизатор с реальными Store, runDir и UIBus; фейкам оставлены только нижние действия.

const (
	contentTypeText = "text/plain; charset=utf-8"
	contentTypeJSON = "application/json"
	contentTypeFlow = "application/json; charset=utf-8"

	flowPausedBody = `{"error":"flow_paused"}` + "\n"
)

// contractOpts задаёт стадию и фейки; нулевое значение — стадия в pending с успешными фейковыми действиями.
type contractOpts struct {
	status    state.StageStatus
	stage     StageConfig
	actions   fakeStageActions
	secondary engine.DialogOps // nil — диалог и хуки не подключены
	review    string           // состояние review-паузы; пусто — "none"
}

type contractEnv struct {
	srv    *Server
	store  *implstate.Store
	runDir string
	events <-chan bus.Event
}

func newContractEnv(t *testing.T, o contractOpts) *contractEnv {
	t.Helper()
	runDir := t.TempDir()
	store, err := implstate.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if o.status != "" && o.status != state.StatusPending {
		if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: o.status, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	review := o.review
	if review == "" {
		review = reviewStateNone
	}
	uiBus := implbus.NewUIBus()
	stages := StaticStageConfigs{testStageID: o.stage}
	reviewState := func() (string, []string) { return review, nil }
	// Настоящая логика команд поверх фейковых нижних действий; без Secondary диалог не подключён.
	control := engine.NewStageControl(engine.StageDeps{
		Ops: o.actions, Dialog: o.secondary, State: store, RunDir: runDir, Events: uiBus,
		Settings: func(id string) engine.StageSettings {
			c := stages[id]
			return engine.StageSettings{Interactive: c.Interactive, IsScript: c.IsScript, Buttons: c.Buttons}
		},
		ReviewState: reviewState,
	})
	cfg := Config{
		RunDir: runDir, Store: store, UIBus: uiBus,
		Actions:     control,
		Stages:      stages,
		ReviewState: reviewState,
	}
	if o.secondary != nil {
		cfg.Secondary = control
	}
	srv := New(cfg)
	subID, events := uiBus.Subscribe(64)
	t.Cleanup(func() { uiBus.Unsubscribe(subID) })
	return &contractEnv{srv: srv, store: store, runDir: runDir, events: events}
}

func (e *contractEnv) stageDir() string { return filepath.Join(e.runDir, testStageID) }

// post отправляет POST через настоящий маршрутизатор и возвращает ответ.
func (e *contractEnv) post(t *testing.T, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, req)
	return w
}

func (e *contractEnv) postStage(t *testing.T, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.post(t, "/api/stages/"+testStageID+"/"+action, "application/json", body)
}

func wantResponse(t *testing.T, w *httptest.ResponseRecorder, code int, contentType, body string) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status = %d, want %d; body=%q", w.Code, code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != contentType {
		t.Errorf("Content-Type = %q, want %q", got, contentType)
	}
	if got := w.Body.String(); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

// okBody — успешный JSON-ответ команды: json.Encoder сортирует ключи и добавляет перевод строки.
func okBody(status string) string {
	return `{"stage_id":"` + testStageID + `","status":"` + status + `"}` + "\n"
}

func textBody(msg string) string { return msg + "\n" }

// drainEvents собирает события UIBus заданного типа, уже лежащие в канале.
func drainEvents(events <-chan bus.Event, typ bus.EventType) []bus.Event {
	var got []bus.Event
	for {
		select {
		case ev := <-events:
			if ev.Type == typ {
				got = append(got, ev)
			}
		default:
			return got
		}
	}
}

// readNotices возвращает строки notices.jsonl; отсутствие файла — пустой список.
func readNotices(t *testing.T, runDir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("bad notices line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

func wantNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s must not exist, stat err=%v", path, err)
	}
}

var errBoom = errors.New("boom")

// actionErrorCases — общая для команд StageActions матрица ошибок нижнего действия.
func actionErrorCases(failPrefix string) []struct {
	name     string
	err      error
	wantCode int
	wantCT   string
	wantBody string
} {
	return []struct {
		name     string
		err      error
		wantCode int
		wantCT   string
		wantBody string
	}{
		{"flow_paused", implorchestrator.ErrFlowPaused, http.StatusConflict, contentTypeFlow, flowPausedBody},
		{"wrapped_flow_paused", errors.Join(errBoom, implorchestrator.ErrFlowPaused), http.StatusConflict, contentTypeFlow, flowPausedBody},
		{"not_applied", implorchestrator.ErrNotApplied, http.StatusConflict, contentTypeText, textBody(implorchestrator.ErrNotApplied.Error())},
		{"internal", errBoom, http.StatusInternalServerError, contentTypeText, textBody(failPrefix + ": boom")},
	}
}

// stageCommand описывает команду над стадией без тела: путь, нужный статус и вызов фейка.
type stageCommand struct {
	action    string
	okStatus  state.StageStatus
	badStatus state.StageStatus
	badMsg    string
	okReply   string
	failMsg   string
	install   func(*contractOpts, func(call string, err error) error)
	wantCall  string
}

func stageCommands() []stageCommand {
	return []stageCommand{
		{
			action: "approve", okStatus: state.StatusAwaitingApproval, badStatus: state.StatusRunning,
			badMsg: "stage is running, not awaiting_approval", okReply: "approved", failMsg: "approve failed",
			wantCall: "approve s1",
			install: func(o *contractOpts, rec func(string, error) error) {
				o.actions.approve = func(_ context.Context, id string) error { return rec("approve "+id, nil) }
			},
		},
		{
			action: "retry", okStatus: state.StatusFailed, badStatus: state.StatusRunning,
			badMsg: "stage is running, not failed", okReply: "retried", failMsg: "retry failed",
			wantCall: "retry s1",
			install: func(o *contractOpts, rec func(string, error) error) {
				o.actions.retry = func(_ context.Context, id string) error { return rec("retry "+id, nil) }
			},
		},
		{
			action: "pause", okStatus: state.StatusRunning, badStatus: state.StatusFailed,
			badMsg: "stage is failed, cannot be paused", okReply: "paused", failMsg: "pause failed",
			wantCall: "pause s1",
			install: func(o *contractOpts, rec func(string, error) error) {
				o.actions.pause = func(_ context.Context, id string) error { return rec("pause "+id, nil) }
			},
		},
		{
			action: "continue", okStatus: state.StatusPaused, badStatus: state.StatusRunning,
			badMsg: "stage is running, not paused", okReply: "continued", failMsg: "continue failed",
			wantCall: "continue s1",
			install: func(o *contractOpts, rec func(string, error) error) {
				o.actions.continue_ = func(_ context.Context, id string) error { return rec("continue "+id, nil) }
			},
		},
	}
}

// Approve, Retry, Pause и Continue делят одну форму: проверка статуса, вызов действия, JSON-ответ.
func TestCommandContract_StageCommands(t *testing.T) {
	for _, cmd := range stageCommands() {
		t.Run(cmd.action+"/ok", func(t *testing.T) {
			var calls []string
			o := contractOpts{status: cmd.okStatus}
			cmd.install(&o, func(call string, err error) error { calls = append(calls, call); return err })
			env := newContractEnv(t, o)

			wantResponse(t, env.postStage(t, cmd.action, ""), http.StatusOK, contentTypeJSON, okBody(cmd.okReply))
			if len(calls) != 1 || calls[0] != cmd.wantCall {
				t.Fatalf("calls = %v, want [%s]", calls, cmd.wantCall)
			}
		})

		t.Run(cmd.action+"/wrong_status", func(t *testing.T) {
			var calls []string
			o := contractOpts{status: cmd.badStatus}
			cmd.install(&o, func(call string, err error) error { calls = append(calls, call); return err })
			env := newContractEnv(t, o)

			wantResponse(t, env.postStage(t, cmd.action, ""), http.StatusBadRequest, contentTypeText, textBody(cmd.badMsg))
			if len(calls) != 0 {
				t.Fatalf("action must not run on a wrong status, calls = %v", calls)
			}
		})

		t.Run(cmd.action+"/unknown_stage", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: cmd.okStatus})
			w := env.post(t, "/api/stages/ghost/"+cmd.action, "", "")
			wantResponse(t, w, http.StatusNotFound, contentTypeText, textBody("stage not found"))
		})

		t.Run(cmd.action+"/invalid_stage_id", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: cmd.okStatus})
			w := env.post(t, "/api/stages/a$b/"+cmd.action, "", "")
			wantResponse(t, w, http.StatusBadRequest, contentTypeText, textBody("invalid stage id"))
		})

		t.Run(cmd.action+"/wrong_method_is_404", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: cmd.okStatus})
			req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/"+cmd.action, nil)
			w := httptest.NewRecorder()
			env.srv.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", w.Code)
			}
		})

		for _, ec := range actionErrorCases(cmd.failMsg) {
			t.Run(cmd.action+"/error_"+ec.name, func(t *testing.T) {
				o := contractOpts{status: cmd.okStatus}
				cmd.install(&o, func(string, error) error { return ec.err })
				env := newContractEnv(t, o)
				wantResponse(t, env.postStage(t, cmd.action, ""), ec.wantCode, ec.wantCT, ec.wantBody)
			})
		}
	}
}

func TestCommandContract_Pause(t *testing.T) {
	for _, status := range []state.StageStatus{
		state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying,
		state.StatusAwaitingAssistants, state.StatusAwaitingPeer, state.StatusAwaitingQuota,
	} {
		t.Run("allowed_"+string(status), func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: status})
			wantResponse(t, env.postStage(t, "pause", ""), http.StatusOK, contentTypeJSON, okBody("paused"))
		})
	}

	t.Run("script_running_conflicts", func(t *testing.T) {
		paused := false
		env := newContractEnv(t, contractOpts{
			status: state.StatusRunning, stage: StageConfig{IsScript: true},
			actions: fakeStageActions{pause: func(context.Context, string) error { paused = true; return nil }},
		})
		wantResponse(t, env.postStage(t, "pause", ""), http.StatusConflict, contentTypeText, textBody("pause is not supported mid-script execution"))
		if paused {
			t.Fatal("Pause must not run for a script stage in running")
		}
	})

	t.Run("script_planning_is_allowed", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusPlanning, stage: StageConfig{IsScript: true}})
		wantResponse(t, env.postStage(t, "pause", ""), http.StatusOK, contentTypeJSON, okBody("paused"))
	})
}

func TestCommandContract_Revise(t *testing.T) {
	reviseOK := func(seq uint64, calls *[]string) fakeStageActions {
		return fakeStageActions{revise: func(_ context.Context, id, feedback string) (bool, uint64, error) {
			*calls = append(*calls, "revise "+id+" "+feedback)
			return true, seq, nil
		}}
	}

	for _, status := range []state.StageStatus{state.StatusAwaitingApproval, state.StatusRunning, state.StatusAwaitingAssistants} {
		t.Run("ok_"+string(status), func(t *testing.T) {
			var calls []string
			env := newContractEnv(t, contractOpts{status: status, actions: reviseOK(7, &calls)})

			wantResponse(t, env.postStage(t, "revise", `{"feedback":"fix the API"}`), http.StatusOK, contentTypeJSON, okBody("revised"))
			if len(calls) != 1 || calls[0] != "revise s1 fix the API" {
				t.Fatalf("calls = %v", calls)
			}

			// Одна и та же заметка уходит live и в notices.jsonl; id — seq перехода.
			notes := drainEvents(env.events, bus.EventAgentNote)
			if len(notes) != 1 || notes[0].StageID != testStageID {
				t.Fatalf("agent_note events = %+v, want one for %s", notes, testStageID)
			}
			wantPayload := map[string]any{"id": uint64(7), "text": "fix the API"}
			if got, _ := json.Marshal(notes[0].Data); string(got) != contractJSON(t, wantPayload) {
				t.Errorf("live payload = %s, want %s", got, contractJSON(t, wantPayload))
			}
			notices := readNotices(t, env.runDir)
			if len(notices) != 1 || notices[0]["type"] != "agent_note" || notices[0]["stage_id"] != testStageID {
				t.Fatalf("notices = %v", notices)
			}
			data, _ := notices[0]["data"].(map[string]any)
			if data["id"] != float64(7) || data["text"] != "fix the API" {
				t.Errorf("notice data = %v", data)
			}
		})
	}

	t.Run("text_is_first_line_snippet", func(t *testing.T) {
		var calls []string
		env := newContractEnv(t, contractOpts{status: state.StatusRunning, actions: reviseOK(1, &calls)})
		feedback := "\n  first line  \nsecond line"
		env.postStage(t, "revise", contractJSON(t, map[string]string{"feedback": feedback}))
		notices := readNotices(t, env.runDir)
		if len(notices) != 1 {
			t.Fatalf("notices = %v", notices)
		}
		data, _ := notices[0]["data"].(map[string]any)
		if data["text"] != mcp.DialogSnippet(feedback) {
			t.Errorf("text = %v, want %q", data["text"], mcp.DialogSnippet(feedback))
		}
	})

	t.Run("wrong_status", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusPending})
		wantResponse(t, env.postStage(t, "revise", `{"feedback":"x"}`), http.StatusBadRequest, contentTypeText,
			textBody("stage is pending, not running, awaiting_approval or awaiting_assistants"))
	})

	t.Run("unknown_stage", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusRunning})
		w := env.post(t, "/api/stages/ghost/revise", "application/json", `{"feedback":"x"}`)
		wantResponse(t, w, http.StatusNotFound, contentTypeText, textBody("stage not found"))
	})

	t.Run("invalid_stage_id", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusRunning})
		w := env.post(t, "/api/stages/a$b/revise", "application/json", `{"feedback":"x"}`)
		wantResponse(t, w, http.StatusBadRequest, contentTypeText, textBody("invalid stage id"))
	})

	t.Run("script_stage", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusRunning, stage: StageConfig{IsScript: true}})
		wantResponse(t, env.postStage(t, "revise", `{"feedback":"x"}`), http.StatusBadRequest, contentTypeText,
			textBody("script stage has no agent to note"))
	})

	t.Run("invalid_body", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusRunning})
		wantResponse(t, env.postStage(t, "revise", `{`), http.StatusBadRequest, contentTypeText, textBody("invalid request body"))
	})

	t.Run("empty_feedback", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusRunning})
		wantResponse(t, env.postStage(t, "revise", `{"feedback":""}`), http.StatusBadRequest, contentTypeText, textBody("feedback is required"))
	})

	t.Run("not_applied_is_conflict_without_notice", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusRunning, actions: fakeStageActions{
			revise: func(context.Context, string, string) (bool, uint64, error) { return false, 0, nil },
		}})
		wantResponse(t, env.postStage(t, "revise", `{"feedback":"x"}`), http.StatusConflict, contentTypeText,
			textBody("stage no longer accepts a note"))
		if got := drainEvents(env.events, bus.EventAgentNote); len(got) != 0 {
			t.Fatalf("no agent_note event expected, got %+v", got)
		}
		if got := readNotices(t, env.runDir); len(got) != 0 {
			t.Fatalf("no notice expected, got %v", got)
		}
	})

	for _, ec := range actionErrorCases("revise failed") {
		t.Run("error_"+ec.name+"_has_no_notice", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: state.StatusRunning, actions: fakeStageActions{
				revise: func(context.Context, string, string) (bool, uint64, error) { return false, 0, ec.err },
			}})
			wantResponse(t, env.postStage(t, "revise", `{"feedback":"x"}`), ec.wantCode, ec.wantCT, ec.wantBody)
			if got := drainEvents(env.events, bus.EventAgentNote); len(got) != 0 {
				t.Fatalf("no agent_note event expected, got %+v", got)
			}
			if got := readNotices(t, env.runDir); len(got) != 0 {
				t.Fatalf("no notice expected, got %v", got)
			}
		})
	}
}

func contractJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCommandContract_PreNote(t *testing.T) {
	notePath := func(env *contractEnv) string { return filepath.Join(env.stageDir(), "prenote.md") }

	t.Run("save_and_clear", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		wantResponse(t, env.postStage(t, "note", `{"note":"read the spec first"}`), http.StatusOK, contentTypeJSON, okBody("noted"))
		got, err := os.ReadFile(notePath(env))
		if err != nil || string(got) != "read the spec first" {
			t.Fatalf("prenote.md = %q, err=%v", got, err)
		}

		// Пустой текст удаляет заметку; повторное удаление остаётся успехом.
		for range 2 {
			wantResponse(t, env.postStage(t, "note", `{"note":"  "}`), http.StatusOK, contentTypeJSON, okBody("noted"))
			wantNoFile(t, notePath(env))
		}
	})

	t.Run("overwrite_replaces_text", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		env.postStage(t, "note", `{"note":"one"}`)
		env.postStage(t, "note", `{"note":"two"}`)
		if got, _ := os.ReadFile(notePath(env)); string(got) != "two" {
			t.Fatalf("prenote.md = %q, want two", got)
		}
	})

	t.Run("not_pending", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusRunning})
		wantResponse(t, env.postStage(t, "note", `{"note":"x"}`), http.StatusBadRequest, contentTypeText, textBody("stage is running, not pending"))
		wantNoFile(t, notePath(env))
	})

	t.Run("unknown_stage", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		wantResponse(t, env.post(t, "/api/stages/ghost/note", "application/json", `{"note":"x"}`), http.StatusNotFound, contentTypeText, textBody("stage not found"))
	})

	t.Run("invalid_stage_id", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		wantResponse(t, env.post(t, "/api/stages/a$b/note", "application/json", `{"note":"x"}`), http.StatusBadRequest, contentTypeText, textBody("invalid stage id"))
	})

	t.Run("script_stage", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{stage: StageConfig{IsScript: true}})
		wantResponse(t, env.postStage(t, "note", `{"note":"x"}`), http.StatusBadRequest, contentTypeText, textBody("script stage has no agent to note"))
		wantNoFile(t, notePath(env))
	})

	t.Run("invalid_body", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		wantResponse(t, env.postStage(t, "note", `not json`), http.StatusBadRequest, contentTypeText, textBody("invalid request body"))
	})

	t.Run("save_failure_is_500", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		// Файл на месте каталога стадии: MkdirAll не сможет его создать.
		if err := os.WriteFile(env.stageDir(), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		w := env.postStage(t, "note", `{"note":"x"}`)
		if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Body.String(), "save note failed: mkdir stage dir: ") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})
}

func TestCommandContract_Button(t *testing.T) {
	stage := StageConfig{Buttons: []string{"Fix", "Polish"}}
	buttonOK := func(calls *[]string) fakeStageActions {
		return fakeStageActions{button: func(_ context.Context, id, name string) error {
			*calls = append(*calls, "button "+id+" "+name)
			return nil
		}}
	}

	for _, status := range []state.StageStatus{state.StatusAwaitingApproval, state.StatusRunning} {
		t.Run("ok_"+string(status), func(t *testing.T) {
			var calls []string
			env := newContractEnv(t, contractOpts{status: status, stage: stage, actions: buttonOK(&calls)})
			wantResponse(t, env.postStage(t, "button", `{"name":"Polish"}`), http.StatusOK, contentTypeJSON, okBody("button"))
			if len(calls) != 1 || calls[0] != "button s1 Polish" {
				t.Fatalf("calls = %v", calls)
			}
		})
	}

	rejected := []struct {
		name, path, body string
		status           state.StageStatus
		stage            StageConfig
		wantCode         int
		wantMsg          string
	}{
		{"wrong_status", "s1", `{"name":"Fix"}`, state.StatusPending, stage, 400, "stage is pending, not awaiting_approval or running"},
		{"unknown_stage", "ghost", `{"name":"Fix"}`, state.StatusRunning, stage, 404, "stage not found"},
		{"invalid_stage_id", "a$b", `{"name":"Fix"}`, state.StatusRunning, stage, 400, "invalid stage id"},
		{"script_stage", "s1", `{"name":"Fix"}`, state.StatusRunning, StageConfig{IsScript: true, Buttons: []string{"Fix"}}, 400, "script stage has no agent"},
		{"invalid_body", "s1", `{`, state.StatusRunning, stage, 400, "invalid request body"},
		{"missing_name", "s1", `{"name":""}`, state.StatusRunning, stage, 400, "button name is required"},
		{"unknown_button", "s1", `{"name":"Nuke"}`, state.StatusRunning, stage, 400, "unknown button"},
		{"label_is_case_sensitive", "s1", `{"name":"fix"}`, state.StatusRunning, stage, 400, "unknown button"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			env := newContractEnv(t, contractOpts{status: tc.status, stage: tc.stage, actions: buttonOK(&calls)})
			w := env.post(t, "/api/stages/"+tc.path+"/button", "application/json", tc.body)
			wantResponse(t, w, tc.wantCode, contentTypeText, textBody(tc.wantMsg))
			if len(calls) != 0 {
				t.Fatalf("Button must not run, calls = %v", calls)
			}
		})
	}

	for _, ec := range actionErrorCases("button failed") {
		t.Run("error_"+ec.name, func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: state.StatusRunning, stage: stage, actions: fakeStageActions{
				button: func(context.Context, string, string) error { return ec.err },
			}})
			wantResponse(t, env.postStage(t, "button", `{"name":"Fix"}`), ec.wantCode, ec.wantCT, ec.wantBody)
		})
	}
}

func TestCommandContract_HookCommands(t *testing.T) {
	hooks := []struct {
		action, okReply, label string
		install                func(func(string) error) fakeSecondaryActions
	}{
		{"retry-hook", "retried", "retry-hook", func(fn func(string) error) fakeSecondaryActions {
			return fakeSecondaryActions{retryHook: fn}
		}},
		{"skip-hook", "skipped", "skip-hook", func(fn func(string) error) fakeSecondaryActions {
			return fakeSecondaryActions{skipHook: fn}
		}},
	}
	for _, h := range hooks {
		t.Run(h.action+"/ok_without_status_check", func(t *testing.T) {
			var got []string
			// Стадия в pending: хук-команды статус не проверяют, решает нижнее действие.
			env := newContractEnv(t, contractOpts{secondary: h.install(func(id string) error { got = append(got, id); return nil })})
			wantResponse(t, env.postStage(t, h.action, ""), http.StatusOK, contentTypeJSON, okBody(h.okReply))
			if len(got) != 1 || got[0] != testStageID {
				t.Fatalf("calls = %v", got)
			}
		})

		t.Run(h.action+"/not_configured", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{})
			wantResponse(t, env.postStage(t, h.action, ""), http.StatusNotImplemented, contentTypeText, textBody(h.label+" not supported"))
		})

		t.Run(h.action+"/invalid_stage_id", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{secondary: fakeSecondaryActions{}})
			wantResponse(t, env.post(t, "/api/stages/a$b/"+h.action, "", ""), http.StatusBadRequest, contentTypeText, textBody("invalid stage id"))
		})

		t.Run(h.action+"/error_is_bad_request", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{secondary: h.install(func(string) error { return errBoom })})
			wantResponse(t, env.postStage(t, h.action, ""), http.StatusBadRequest, contentTypeText, textBody(h.label+" failed: boom"))
		})

		t.Run(h.action+"/flow_paused", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{secondary: h.install(func(string) error { return implorchestrator.ErrFlowPaused })})
			wantResponse(t, env.postStage(t, h.action, ""), http.StatusConflict, contentTypeFlow, flowPausedBody)
		})

		t.Run(h.action+"/not_applied", func(t *testing.T) {
			env := newContractEnv(t, contractOpts{secondary: h.install(func(string) error { return implorchestrator.ErrNotApplied })})
			wantResponse(t, env.postStage(t, h.action, ""), http.StatusConflict, contentTypeText, textBody(implorchestrator.ErrNotApplied.Error()))
		})
	}
}

// writeQuestion кладёт question.json, как это делает агент.
func writeQuestion(t *testing.T, stageDir, phase, id, body string) {
	t.Helper()
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, phase+"."+id+".question.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func answerPath(env *contractEnv, phase, id string) string {
	return filepath.Join(env.stageDir(), phase+"."+id+".answer.json")
}

func dialogPath(env *contractEnv, phase string) string {
	return mcp.DialogPath(env.stageDir(), phase)
}

type notifyCall struct {
	stage, phase, id, answer string
	fromOptions              bool
}

func recordingSecondary(calls *[]notifyCall, err error) fakeSecondaryActions {
	return fakeSecondaryActions{notifyAnswer: func(stage, phase, id, answer string, fromOptions bool) error {
		*calls = append(*calls, notifyCall{stage, phase, id, answer, fromOptions})
		return err
	}}
}

const openQuestion = `{"id":"q1","question":"Which one?","options":["A","B"],"allow_custom":true}`

func TestCommandContract_DialogAnswer_Success(t *testing.T) {
	var calls []notifyCall
	env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, nil)})
	writeQuestion(t, env.stageDir(), "planning", "q1", openQuestion)

	w := env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"B","from_options":true}`)
	wantResponse(t, w, http.StatusOK, contentTypeJSON, `{"status":"ok"}`+"\n")

	// Файл ответа, который подбирает агент.
	raw, err := os.ReadFile(answerPath(env, "planning", "q1"))
	if err != nil {
		t.Fatalf("answer.json: %v", err)
	}
	if string(raw) != `{"answer":"B","from_options":true,"id":"q1"}` {
		t.Errorf("answer.json = %s", raw)
	}
	if want := []notifyCall{{testStageID, "planning", "q1", "B", true}}; len(calls) != 1 || calls[0] != want[0] {
		t.Errorf("NotifyAnswer calls = %+v, want %+v", calls, want)
	}

	// История диалога для UI.
	entries, err := mcp.ReadDialog(dialogPath(env, "planning"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("dialog entries = %+v, err=%v", entries, err)
	}
	if e := entries[0]; e.ID != "q1" || e.Answer == nil || *e.Answer != "B" || !e.FromOptions {
		t.Errorf("dialog entry = %+v", e)
	}

	// Одна и та же notice-нагрузка идёт live и в notices.jsonl.
	want := `{"id":"q1","phase":"planning","title":"B"}`
	evs := drainEvents(env.events, bus.EventDialogAnswer)
	if len(evs) != 1 || evs[0].StageID != testStageID || contractJSON(t, evs[0].Data) != want {
		t.Fatalf("dialog_answer events = %+v", evs)
	}
	notices := readNotices(t, env.runDir)
	if len(notices) != 1 || notices[0]["type"] != "dialog_answer" || notices[0]["stage_id"] != testStageID ||
		contractJSON(t, notices[0]["data"]) != want {
		t.Fatalf("notices = %v", notices)
	}
}

func TestCommandContract_DialogAnswer_WithoutSecondaryStillRecords(t *testing.T) {
	env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput})
	writeQuestion(t, env.stageDir(), "planning", "q1", openQuestion)
	wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"free text"}`),
		http.StatusOK, contentTypeJSON, `{"status":"ok"}`+"\n")
	if _, err := os.Stat(answerPath(env, "planning", "q1")); err != nil {
		t.Fatalf("answer.json: %v", err)
	}
}

func TestCommandContract_DialogAnswer_Rejections(t *testing.T) {
	type tc struct {
		name     string
		question string // содержимое question.json; пусто — вопроса нет
		stage    StageConfig
		review   string
		body     string
		path     string // подставляется вместо stage id
		wantCode int
		wantCT   string
		wantBody string
	}
	cases := []tc{
		{name: "invalid_stage_id", path: "a$b", body: `{"id":"q1","phase":"planning","answer":"x"}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("invalid stage id")},
		{name: "invalid_json", question: openQuestion, body: `{`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("invalid body")},
		{name: "missing_id", question: openQuestion, body: `{"phase":"planning","answer":"x"}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("id, phase, answer required")},
		{name: "missing_phase", question: openQuestion, body: `{"id":"q1","answer":"x"}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("id, phase, answer required")},
		{name: "missing_answer", question: openQuestion, body: `{"id":"q1","phase":"planning"}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("id, phase, answer required")},
		{name: "invalid_phase", question: openQuestion, body: `{"id":"q1","phase":"bogus","answer":"x"}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("invalid phase")},
		{name: "traversal_id", question: openQuestion, body: `{"id":"../q1","phase":"planning","answer":"x"}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("invalid question id")},
		{name: "review_paused", question: openQuestion, review: reviewStatePaused, body: `{"id":"q1","phase":"planning","answer":"x"}`, wantCode: 409, wantCT: contentTypeFlow, wantBody: flowPausedBody},
		{name: "review_resuming", question: openQuestion, review: "resuming", body: `{"id":"q1","phase":"planning","answer":"x"}`, wantCode: 409, wantCT: contentTypeFlow, wantBody: flowPausedBody},
		{name: "question_not_found", body: `{"id":"q9","phase":"planning","answer":"x"}`, wantCode: 404, wantCT: contentTypeText, wantBody: textBody("question not found")},
		{name: "unknown_option_allow_custom_true", question: `{"id":"q1","question":"?","options":["A"],"allow_custom":true}`, body: `{"id":"q1","phase":"planning","answer":"C","from_options":true}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("answer not in allowed options")},
		{name: "option_claim_without_options_allow_custom_true", question: `{"id":"q1","question":"?","allow_custom":true}`, body: `{"id":"q1","phase":"planning","answer":"x","from_options":true}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("answer not in allowed options")},
		{name: "unknown_option_allow_custom_false", question: `{"id":"q1","question":"?","options":["A"],"allow_custom":false}`, body: `{"id":"q1","phase":"planning","answer":"C","from_options":true}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("answer not in allowed options")},
		{name: "option_claim_without_options_allow_custom_false", question: `{"id":"q1","question":"?","allow_custom":false}`, body: `{"id":"q1","phase":"planning","answer":"x","from_options":true}`, wantCode: 400, wantCT: contentTypeText, wantBody: textBody("answer not in allowed options")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []notifyCall
			env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, stage: c.stage, review: c.review, secondary: recordingSecondary(&calls, nil)})
			if c.question != "" {
				writeQuestion(t, env.stageDir(), "planning", "q1", c.question)
			}
			stage := testStageID
			if c.path != "" {
				stage = c.path
			}
			wantResponse(t, env.post(t, "/api/stages/"+stage+"/dialog/answer", "application/json", c.body), c.wantCode, c.wantCT, c.wantBody)

			// Отказ не оставляет ни файла ответа, ни истории, ни notice, ни вызова NotifyAnswer.
			wantNoFile(t, answerPath(env, "planning", "q1"))
			wantNoFile(t, dialogPath(env, "planning"))
			if len(calls) != 0 {
				t.Errorf("NotifyAnswer must not run, calls = %+v", calls)
			}
			if got := readNotices(t, env.runDir); len(got) != 0 {
				t.Errorf("no notice expected, got %v", got)
			}
			if got := drainEvents(env.events, bus.EventDialogAnswer); len(got) != 0 {
				t.Errorf("no dialog_answer event expected, got %+v", got)
			}
		})
	}
}

// The answer text is free on every question; only a claimed option choice must name a real option.
func TestCommandContract_DialogAnswer_FreeText(t *testing.T) {
	cases := []struct{ name, question, fromOptionsField string }{
		{"legacy_false_with_options", `{"id":"q1","question":"?","options":["A","B"],"allow_custom":false}`, `,"from_options":false`},
		{"legacy_false_without_options", `{"id":"q1","question":"?","allow_custom":false}`, `,"from_options":false`},
		{"legacy_false_field_absent", `{"id":"q1","question":"?","options":["A","B"],"allow_custom":false}`, ``},
		{"no_allow_custom_field", `{"id":"q1","question":"?"}`, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []notifyCall
			env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, nil)})
			writeQuestion(t, env.stageDir(), "planning", "q1", c.question)

			body := `{"id":"q1","phase":"planning","answer":"my own"` + c.fromOptionsField + `}`
			wantResponse(t, env.postStage(t, "dialog/answer", body), http.StatusOK, contentTypeJSON, `{"status":"ok"}`+"\n")

			raw, err := os.ReadFile(answerPath(env, "planning", "q1"))
			if err != nil || string(raw) != `{"answer":"my own","from_options":false,"id":"q1"}` {
				t.Errorf("answer.json = %s, err=%v", raw, err)
			}
			if want := (notifyCall{testStageID, "planning", "q1", "my own", false}); len(calls) != 1 || calls[0] != want {
				t.Errorf("NotifyAnswer calls = %+v, want %+v", calls, want)
			}
			entries, err := mcp.ReadDialog(dialogPath(env, "planning"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("dialog entries = %+v, err=%v", entries, err)
			}
			if e := entries[0]; e.Answer == nil || *e.Answer != "my own" || e.FromOptions {
				t.Errorf("dialog entry = %+v", e)
			}
			want := `{"id":"q1","phase":"planning","title":"my own"}`
			evs := drainEvents(env.events, bus.EventDialogAnswer)
			if len(evs) != 1 || contractJSON(t, evs[0].Data) != want {
				t.Fatalf("dialog_answer events = %+v", evs)
			}
			notices := readNotices(t, env.runDir)
			if len(notices) != 1 || notices[0]["type"] != "dialog_answer" || contractJSON(t, notices[0]["data"]) != want {
				t.Fatalf("notices = %v", notices)
			}
		})
	}
}

func TestCommandContract_DialogAnswer_AllowedOption(t *testing.T) {
	env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput})
	writeQuestion(t, env.stageDir(), "planning", "q1", `{"id":"q1","question":"?","options":["A","B"],"allow_custom":false}`)
	wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"A","from_options":true}`),
		http.StatusOK, contentTypeJSON, `{"status":"ok"}`+"\n")
}

func TestCommandContract_DialogAnswer_MalformedQuestionFile(t *testing.T) {
	env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput})
	writeQuestion(t, env.stageDir(), "planning", "q1", `{not json`)
	w := env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"x"}`)
	if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Body.String(), "parse question: ") {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	wantNoFile(t, answerPath(env, "planning", "q1"))
}

func TestCommandContract_DialogAnswer_Duplicate(t *testing.T) {
	var calls []notifyCall
	env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, nil)})
	writeQuestion(t, env.stageDir(), "planning", "q1", openQuestion)
	body := `{"id":"q1","phase":"planning","answer":"first"}`

	wantResponse(t, env.postStage(t, "dialog/answer", body), http.StatusOK, contentTypeJSON, `{"status":"ok"}`+"\n")
	wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"second"}`),
		http.StatusConflict, contentTypeText, textBody("question already answered"))

	// Победил первый ответ; второй не уведомил оркестратор и не дописал историю.
	if raw, _ := os.ReadFile(answerPath(env, "planning", "q1")); !strings.Contains(string(raw), `"answer":"first"`) {
		t.Errorf("answer.json = %s, want the first answer", raw)
	}
	if len(calls) != 1 {
		t.Errorf("NotifyAnswer calls = %d, want 1", len(calls))
	}
	if got := len(drainEvents(env.events, bus.EventDialogAnswer)); got != 1 {
		t.Errorf("dialog_answer events = %d, want 1", got)
	}
	if got := readNotices(t, env.runDir); len(got) != 1 {
		t.Errorf("notices = %v, want 1", got)
	}
}

func TestCommandContract_DialogAnswer_NotifyFailures(t *testing.T) {
	t.Run("flow_paused_rolls_back_answer_history_and_notice", func(t *testing.T) {
		var calls []notifyCall
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, implorchestrator.ErrFlowPaused)})
		writeQuestion(t, env.stageDir(), "planning", "q1", openQuestion)
		wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"x"}`),
			http.StatusConflict, contentTypeFlow, flowPausedBody)
		if len(calls) != 1 {
			t.Fatalf("NotifyAnswer calls = %d, want 1", len(calls))
		}
		wantNoFile(t, answerPath(env, "planning", "q1"))
		wantNoFile(t, dialogPath(env, "planning"))
		if got := readNotices(t, env.runDir); len(got) != 0 {
			t.Errorf("no notice expected, got %v", got)
		}
		if got := drainEvents(env.events, bus.EventDialogAnswer); len(got) != 0 {
			t.Errorf("no event expected, got %+v", got)
		}
	})

	t.Run("generic_error_keeps_answer_file", func(t *testing.T) {
		// Текущее поведение: при прочей ошибке NotifyAnswer ответ уже durable и не откатывается.
		var calls []notifyCall
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, errBoom)})
		writeQuestion(t, env.stageDir(), "planning", "q1", openQuestion)
		wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"x"}`),
			http.StatusInternalServerError, contentTypeText, textBody("notify: boom"))
		if _, err := os.Stat(answerPath(env, "planning", "q1")); err != nil {
			t.Fatalf("answer.json must stay after a generic notify error: %v", err)
		}
		wantNoFile(t, dialogPath(env, "planning"))
		if got := readNotices(t, env.runDir); len(got) != 0 {
			t.Errorf("no notice expected, got %v", got)
		}
	})
}

func TestCommandContract_DialogAnswer_InteractiveOrdering(t *testing.T) {
	interactive := StageConfig{Interactive: true}
	older, newer := `{"id":"q1","question":"first","allow_custom":true}`, `{"id":"q2","question":"second","allow_custom":true}`

	t.Run("out_of_order_is_conflict", func(t *testing.T) {
		var calls []notifyCall
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, stage: interactive, secondary: recordingSecondary(&calls, nil)})
		writeQuestion(t, env.stageDir(), "planning", "q1", older)
		writeQuestion(t, env.stageDir(), "planning", "q2", newer)
		wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q2","phase":"planning","answer":"x"}`),
			http.StatusConflict, contentTypeFlow, `{"error":"answer_out_of_order"}`+"\n")
		wantNoFile(t, answerPath(env, "planning", "q2"))
		if len(calls) != 0 {
			t.Errorf("NotifyAnswer must not run, calls = %+v", calls)
		}
	})

	t.Run("current_question_is_accepted", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, stage: interactive})
		writeQuestion(t, env.stageDir(), "planning", "q1", older)
		writeQuestion(t, env.stageDir(), "planning", "q2", newer)
		wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"x"}`),
			http.StatusOK, contentTypeJSON, `{"status":"ok"}`+"\n")
	})

	t.Run("non_interactive_may_answer_any_order", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput})
		writeQuestion(t, env.stageDir(), "planning", "q1", older)
		writeQuestion(t, env.stageDir(), "planning", "q2", newer)
		wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q2","phase":"planning","answer":"x"}`),
			http.StatusOK, contentTypeJSON, `{"status":"ok"}`+"\n")
	})

	t.Run("lookup_failure_is_500", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, stage: interactive})
		// Файл на месте каталога стадии: ReadDir возвращает ошибку, не «нет каталога».
		if err := os.WriteFile(env.stageDir(), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		wantResponse(t, env.postStage(t, "dialog/answer", `{"id":"q1","phase":"planning","answer":"x"}`),
			http.StatusInternalServerError, contentTypeFlow, `{"error":"current_question_lookup_failed"}`+"\n")
	})
}

func TestCommandContract_DialogCancel(t *testing.T) {
	cancelOK := func(calls *[]string) fakeSecondaryActions {
		return fakeSecondaryActions{cancelDialog: func(id string) error { *calls = append(*calls, id); return nil }}
	}

	t.Run("ok", func(t *testing.T) {
		var calls []string
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: cancelOK(&calls)})
		wantResponse(t, env.postStage(t, "dialog/cancel", ""), http.StatusOK, contentTypeJSON, `{"status":"cancelled"}`+"\n")
		if len(calls) != 1 || calls[0] != testStageID {
			t.Fatalf("calls = %v", calls)
		}
	})

	t.Run("ok_without_secondary", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput})
		wantResponse(t, env.postStage(t, "dialog/cancel", ""), http.StatusOK, contentTypeJSON, `{"status":"cancelled"}`+"\n")
	})

	t.Run("not_awaiting_user_input", func(t *testing.T) {
		var calls []string
		env := newContractEnv(t, contractOpts{status: state.StatusRunning, secondary: cancelOK(&calls)})
		wantResponse(t, env.postStage(t, "dialog/cancel", ""), http.StatusBadRequest, contentTypeText, textBody("stage is not awaiting user input"))
		if len(calls) != 0 {
			t.Fatalf("CancelDialog must not run, calls = %v", calls)
		}
	})

	t.Run("unknown_stage_has_the_same_message", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput})
		wantResponse(t, env.post(t, "/api/stages/ghost/dialog/cancel", "", ""), http.StatusBadRequest, contentTypeText, textBody("stage is not awaiting user input"))
	})

	t.Run("invalid_stage_id", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput})
		wantResponse(t, env.post(t, "/api/stages/a$b/dialog/cancel", "", ""), http.StatusBadRequest, contentTypeText, textBody("invalid stage id"))
	})

	for _, ec := range actionErrorCases("cancel") {
		t.Run("error_"+ec.name, func(t *testing.T) {
			env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: fakeSecondaryActions{
				cancelDialog: func(string) error { return ec.err },
			}})
			wantResponse(t, env.postStage(t, "dialog/cancel", ""), ec.wantCode, ec.wantCT, ec.wantBody)
		})
	}
}

var attachmentNameRe = regexp.MustCompile(`^paste-[0-9a-f]{16}\.(png|jpg|webp|gif)$`)

func TestCommandContract_Attachment(t *testing.T) {
	for contentType, ext := range map[string]string{
		"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif",
	} {
		t.Run("saves_"+ext, func(t *testing.T) {
			env := newContractEnv(t, contractOpts{})
			payload := "binary\x00image"
			w := env.post(t, "/api/stages/"+testStageID+"/attachments", contentType, payload)
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != contentTypeJSON {
				t.Fatalf("status=%d ct=%q body=%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || len(resp) != 1 {
				t.Fatalf("body = %q, err=%v", w.Body.String(), err)
			}
			path := resp["path"]
			wantDir := filepath.Join(env.stageDir(), "attachments")
			if !filepath.IsAbs(path) || filepath.Dir(path) != wantDir {
				t.Fatalf("path = %q, want absolute file in %s", path, wantDir)
			}
			name := filepath.Base(path)
			if !attachmentNameRe.MatchString(name) || !strings.HasSuffix(name, ext) {
				t.Errorf("name = %q, want paste-<16 hex>%s", name, ext)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != payload {
				t.Errorf("file = %q, err=%v", got, err)
			}
		})
	}

	t.Run("each_upload_gets_its_own_file", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		first := env.post(t, "/api/stages/"+testStageID+"/attachments", "image/png", "a")
		second := env.post(t, "/api/stages/"+testStageID+"/attachments", "image/png", "b")
		if first.Body.String() == second.Body.String() {
			t.Fatalf("both uploads returned %s", first.Body.String())
		}
	})

	t.Run("stage_existence_is_not_checked", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		w := env.post(t, "/api/stages/ghost/attachments", "image/png", "a")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%q", w.Code, w.Body.String())
		}
		if entries, err := os.ReadDir(filepath.Join(env.runDir, "ghost", "attachments")); err != nil || len(entries) != 1 {
			t.Fatalf("ghost attachments = %v, err=%v", entries, err)
		}
	})

	t.Run("invalid_stage_id", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		wantResponse(t, env.post(t, "/api/stages/a$b/attachments", "image/png", "a"), http.StatusBadRequest, contentTypeText, textBody("invalid stage id"))
	})

	t.Run("unsupported_type", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		for _, ct := range []string{"", "text/plain", "image/svg+xml", "image/png; charset=binary"} {
			wantResponse(t, env.post(t, "/api/stages/"+testStageID+"/attachments", ct, "a"), http.StatusUnsupportedMediaType, contentTypeText, textBody("unsupported image type"))
		}
		wantNoFile(t, filepath.Join(env.stageDir(), "attachments"))
	})

	t.Run("empty", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		wantResponse(t, env.post(t, "/api/stages/"+testStageID+"/attachments", "image/png", ""), http.StatusBadRequest, contentTypeText, textBody("empty attachment"))
		wantNoFile(t, filepath.Join(env.stageDir(), "attachments"))
	})

	t.Run("too_large", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		body := bytes.Repeat([]byte{1}, engine.MaxAttachmentBytes+1)
		wantResponse(t, env.post(t, "/api/stages/"+testStageID+"/attachments", "image/png", string(body)), http.StatusRequestEntityTooLarge, contentTypeText, textBody("attachment too large"))
		wantNoFile(t, filepath.Join(env.stageDir(), "attachments"))
	})

	t.Run("limit_is_inclusive", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		body := bytes.Repeat([]byte{1}, engine.MaxAttachmentBytes)
		if w := env.post(t, "/api/stages/"+testStageID+"/attachments", "image/png", string(body)); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	})

	t.Run("mkdir_failure_is_500", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		if err := os.WriteFile(env.stageDir(), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		w := env.post(t, "/api/stages/"+testStageID+"/attachments", "image/png", "a")
		if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Body.String(), "mkdir attachments: ") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})
}

// Ошибки файловой системы: 500 без побочных эффектов; исчезновение вопроса между чтением и записью ответа
// откатывает ответ (400), а не оставляет его на диске.
func TestCommandContract_FailureBranches(t *testing.T) {
	const body = `{"id":"q1","phase":"planning","answer":"x"}`
	t.Run("read_question_failure", func(t *testing.T) {
		var calls []notifyCall
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, nil)})
		if err := os.MkdirAll(filepath.Join(env.stageDir(), "planning.q1.question.json"), 0755); err != nil {
			t.Fatal(err)
		}
		w := env.postStage(t, "dialog/answer", body)
		if w.Code != 500 || !strings.HasPrefix(w.Body.String(), "read question: ") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
		if len(calls) != 0 {
			t.Fatalf("NotifyAnswer calls=%v", calls)
		}
		wantNoFile(t, answerPath(env, "planning", "q1"))
	})
	t.Run("write_answer_failure", func(t *testing.T) {
		var calls []notifyCall
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, nil)})
		writeQuestion(t, env.stageDir(), "planning", "q1", openQuestion)
		if err := os.Chmod(env.stageDir(), 0555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(env.stageDir(), 0755) })
		w := env.postStage(t, "dialog/answer", body)
		if w.Code != 500 || !strings.HasPrefix(w.Body.String(), "write answer: ") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
		if len(calls) != 0 {
			t.Fatalf("NotifyAnswer calls=%v", calls)
		}
		wantNoFile(t, answerPath(env, "planning", "q1"))
	})
	t.Run("question_disappears_before_answer_is_written", func(t *testing.T) {
		var calls []notifyCall
		env := newContractEnv(t, contractOpts{status: state.StatusAwaitingUserInput, secondary: recordingSecondary(&calls, nil)})
		if err := os.MkdirAll(env.stageDir(), 0755); err != nil {
			t.Fatal(err)
		}
		q := filepath.Join(env.stageDir(), "planning.q1.question.json")
		if err := syscall.Mkfifo(q, 0600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			f, err := os.OpenFile(q, os.O_WRONLY, 0)
			if err != nil {
				done <- err
				return
			}
			if _, err = f.WriteString(openQuestion); err == nil {
				err = os.Remove(q)
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			done <- err
		}()
		w := env.postStage(t, "dialog/answer", body)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		wantResponse(t, w, 400, contentTypeText, "question disappeared during write\n")
		wantNoFile(t, answerPath(env, "planning", "q1"))
		wantNoFile(t, dialogPath(env, "planning"))
		if len(calls) != 0 {
			t.Fatalf("NotifyAnswer calls=%v", calls)
		}
		if got := readNotices(t, env.runDir); len(got) != 0 {
			t.Fatalf("notices=%v", got)
		}
		if got := drainEvents(env.events, bus.EventDialogAnswer); len(got) != 0 {
			t.Fatalf("events=%v", got)
		}
	})
	t.Run("write_attachment_failure", func(t *testing.T) {
		env := newContractEnv(t, contractOpts{})
		d := filepath.Join(env.stageDir(), "attachments")
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(d, 0755) })
		w := env.post(t, "/api/stages/"+testStageID+"/attachments", "image/png", "a")
		if w.Code != 500 || !strings.HasPrefix(w.Body.String(), "write attachment: ") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})
}
