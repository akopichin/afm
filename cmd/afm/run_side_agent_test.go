package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/sideagent"
)

// blockingRunner — управляемый фейк агента для Phase D (shutdown/drain) тестов:
// эмитит один видимый ответ, затем блокируется до close(release) ЛИБО (если
// honorInterrupt) до сигнала InterruptCh, возвращая ErrUserInterrupted.
type blockingRunner struct {
	cfg            executor.Config
	started        chan struct{}
	release        chan struct{}
	honorInterrupt bool
}

func (r *blockingRunner) RunAgent(_ context.Context, _, _, _, _ string) error {
	if r.cfg.OnAction != nil {
		r.cfg.OnAction("text", "an answer") // видимый ответ → чистое завершение возможно
	}
	close(r.started)
	for {
		select {
		case <-r.release:
			return nil
		case <-r.cfg.InterruptCh:
			if r.honorInterrupt {
				return executor.ErrUserInterrupted
			}
			// Игнорируем прерывание (моделируем «не дренируемый» ход).
		}
	}
}

// newRuntimeWithRunner строит sideAgentRuntime с реальным Store и управляемым
// раннером (без подпроцесса).
func newRuntimeWithRunner(t *testing.T, r *blockingRunner) *sideAgentRuntime {
	t.Helper()
	store, err := sideagent.Open(t.TempDir(), "run-d-test")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	mgr := sideagent.New(sideagent.Config{
		Store:   store,
		Command: "claude",
		NewExecutor: func(cfg executor.Config) sideagent.AgentRunner {
			r.cfg = cfg
			return r
		},
	})
	return &sideAgentRuntime{manager: mgr, store: store}
}

func sendAndWaitStarted(t *testing.T, rt *sideAgentRuntime, r *blockingRunner) {
	t.Helper()
	if _, err := rt.manager.Send("m1", "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case <-r.started:
	case <-time.After(2 * time.Second):
		t.Fatal("раннер не стартовал")
	}
}

func historyHas(rt *sideAgentRuntime, typ sideagent.EventType) bool {
	for _, e := range rt.manager.History() {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// Штатное завершение: приём закрывается ДО Drain (новый Send отвергается
// shutting_down), активный ход НЕ прерывается — доигрывает и завершается
// turn_completed, а не turn_interrupted.
func TestShutdownNormal_ClosesAdmissionAndDoesNotInterrupt(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{}), release: make(chan struct{}), honorInterrupt: true}
	rt := newRuntimeWithRunner(t, r)
	sendAndWaitStarted(t, rt, r)

	done := make(chan bool, 1)
	go func() { done <- rt.shutdownNormal(context.Background()) }()

	// Приём закрыт (CloseAdmission внутри shutdownNormal бежит до Drain): новый
	// Send быстро начинает отвергаться shutting_down.
	deadline := time.After(2 * time.Second)
	for {
		_, err := rt.manager.Send("m2", "second")
		var se *sideagent.Error
		if ok := asSideErr(err, &se); ok && se.Code == sideagent.ErrCodeShuttingDown {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("приём не закрылся: Send вернул %v", err)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	close(r.release) // ход доигрывает сам
	select {
	case drained := <-done:
		if !drained {
			t.Error("shutdownNormal: ожидался drained=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownNormal не вернулся")
	}

	if historyHas(rt, sideagent.EventTurnInterrupted) {
		t.Error("штатное завершение не должно прерывать активный ход")
	}
	if !historyHas(rt, sideagent.EventTurnCompleted) {
		t.Error("активный ход должен завершиться turn_completed")
	}
	rt.closeStores(true)
}

// Аварийное завершение прерывает активный ход (Shutdown → InterruptCh).
func TestShutdownEmergency_InterruptsActiveTurn(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{}), release: make(chan struct{}), honorInterrupt: true}
	rt := newRuntimeWithRunner(t, r)
	sendAndWaitStarted(t, rt, r)

	if drained := rt.shutdownEmergency(2 * time.Second); !drained {
		t.Error("ожидался drained=true (раннер уважает InterruptCh)")
	}
	if !historyHas(rt, sideagent.EventTurnInterrupted) {
		t.Error("аварийное завершение должно прервать ход (turn_interrupted)")
	}
	rt.closeStores(true)
}

// Ctrl+C во время штатного дренирования: ctx уже отменён → shutdownNormal
// возвращает drained=false без прерывания, поэтому shutdownAfterFlow эскалирует к
// аварийному завершению (interrupt), и подпроцесс останавливается.
func TestShutdownAfterFlow_EscalatesOnCanceledCtx(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{}), release: make(chan struct{}), honorInterrupt: true}
	rt := newRuntimeWithRunner(t, r)
	sendAndWaitStarted(t, rt, r)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl+C уже случился

	if drained := rt.shutdownAfterFlow(ctx, 2*time.Second); !drained {
		t.Error("shutdownAfterFlow должен эскалировать к аварийному и дренировать")
	}
	if !historyHas(rt, sideagent.EventTurnInterrupted) {
		t.Error("эскалация должна прервать активный ход (turn_interrupted)")
	}
	rt.closeStores(true)
}

// Инвариант «без записи в закрытый файл»: Drain не уложился в bound (ход
// игнорирует interrupt) → drained=false → closeStores НЕ закрывает журнал, и
// доигравший позже ход успешно пишет терминал в ВСЁ ЕЩЁ открытый store.
func TestCloseStores_SkippedWhenNotDrained(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{}), release: make(chan struct{}), honorInterrupt: false}
	rt := newRuntimeWithRunner(t, r)
	sendAndWaitStarted(t, rt, r)

	if drained := rt.shutdownEmergency(50 * time.Millisecond); drained {
		t.Fatal("ожидался drained=false (ход игнорирует interrupt)")
	}
	rt.closeStores(false) // НЕ должен закрыть журнал

	close(r.release) // теперь ход доигрывает
	if drained := rt.manager.Drain(contextWithTimeout(t, 2*time.Second)); !drained {
		t.Fatal("ход должен был завершиться после release")
	}
	// Терминал записан в НЕ закрытый store — доказывает, что closeStores(false)
	// не тронул журнал (иначе был бы write-to-closed-file).
	if !historyHas(rt, sideagent.EventTurnCompleted) {
		t.Error("turn_completed должен быть записан в открытый журнал")
	}

	// А closeStores(true) реально закрывает: последующий Append падает.
	rt.closeStores(true)
	if _, err := rt.store.Append(sideagent.EventTurnAccepted, "t-after-close", sideagent.TurnAcceptedData{Message: "x", ClientMessageID: "c"}); err == nil {
		t.Error("после closeStores(true) запись в журнал должна падать (store закрыт)")
	}
}

func contextWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func asSideErr(err error, target **sideagent.Error) bool {
	if e, ok := err.(*sideagent.Error); ok {
		*target = e
		return true
	}
	return false
}

// sideAgentProtectedValues редактирует СОБСТВЕННЫЙ auth-токен агента (claude), но
// не ANTHROPIC_BASE_URL (это URL, не секрет).
func TestSideAgentProtectedValues_IncludesClaudeAuthToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret-xyz")
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.example.com")

	values := sideAgentProtectedValues(config.Config{})
	if !contains(values, "sk-ant-secret-xyz") {
		t.Error("auth-токен агента должен попадать в набор редактируемых значений")
	}
	if contains(values, "https://api.example.com") {
		t.Error("ANTHROPIC_BASE_URL (URL) не должен редактироваться как секрет")
	}
}

// Провайдеро-нативные credential нативного (не-claude) агента тоже редактируются.
func TestSideAgentProtectedValues_IncludesProviderNativeCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai-123")
	t.Setenv("CURSOR_API_KEY", "crsr_456")

	values := sideAgentProtectedValues(config.Config{})
	if !contains(values, "sk-openai-123") {
		t.Error("OPENAI_API_KEY должен редактироваться для openai-агента на хосте")
	}
	if !contains(values, "crsr_456") {
		t.Error("CURSOR_API_KEY должен редактироваться для cursor-агента на хосте")
	}
}

// Для выбранной команды с docker-рецептом редактируется и токен из auth.from
// (здесь — file:), и значение auth.to env.
func TestSideAgentProtectedValues_IncludesRecipeAuth(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("recipe-secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MY_AGENT_TOKEN", "recipe-env-token")

	cfg := config.Config{}
	cfg.Client.Command = "glm51"
	cfg.Docker.Agents = map[string]config.AgentRecipe{
		"glm51": {Auth: config.RecipeAuth{From: "file:" + tokenFile, To: "env:MY_AGENT_TOKEN"}},
	}

	values := sideAgentProtectedValues(cfg)
	if !contains(values, "recipe-secret-from-file") {
		t.Error("токен из recipe auth.from (file:) должен редактироваться")
	}
	if !contains(values, "recipe-env-token") {
		t.Error("значение recipe auth.to env должно редактироваться")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
