package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/sideagent"
)

// blockingRunner — управляемый фейк агента для Phase D тестов: эмитит один видимый ответ, затем
// блокируется до close ЛИБО до сигнала InterruptCh, возвращая ErrUserInterrupted.
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
	if _, err := rt.manager.Send("m1", "hi", sideagent.TurnTarget{}); err != nil {
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

// Штатное завершение: приём закрывается ДО Drain (новый Send отвергается shutting_down), активный ход НЕ
// прерывается — доигрывает и завершается turn_completed, а не turn_interrupted.
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
		_, err := rt.manager.Send("m2", "second", sideagent.TurnTarget{})
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

// Ctrl+C отменяет штатный drain; shutdownAfterFlow прерывает подпроцесс.
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

// Инвариант «без записи в закрытый файл»: Drain не уложился в bound → drained=false → closeStores НЕ
// закрывает журнал, и доигравший позже ход успешно пишет терминал в ВСЁ ЕЩЁ открытый store.
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

func TestSideAgentEnabled_Precedence(t *testing.T) {
	tr, f := true, false
	cases := []struct {
		name string
		flow *bool  // flow.SideAgent
		cfg  *bool  // cfg.SideAgent.Enabled
		env  string // "" = unset
		want bool
	}{
		{name: "all unset → default ON", flow: nil, cfg: nil, env: "", want: true},
		{name: "config off, no env/flow", flow: nil, cfg: &f, env: "", want: false},
		{name: "env on overrides config off", flow: nil, cfg: &f, env: "1", want: true},
		{name: "flow off overrides env on", flow: &f, cfg: nil, env: "1", want: false},
		{name: "flow on overrides env off + config off", flow: &tr, cfg: &f, env: "0", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_SIDEAGENT", tc.env)
			cfg := config.Config{SideAgent: config.SideAgentConfig{Enabled: tc.cfg}}
			var fl *flow.Flow
			if tc.flow != nil {
				fl = &flow.Flow{SideAgent: tc.flow}
			}
			if got := sideAgentEnabled(cfg, fl); got != tc.want {
				t.Errorf("sideAgentEnabled: got %v, want %v", got, tc.want)
			}
		})
	}
}

// Agent Mesh Phase 6, T6.7: TestSideAgent_MentionsUseSharedResolver — "@qa" typed in the side-agent composer
// resolves through agentcomm.ResolveAlias against the run roster; an ambiguous alias is rejected; the
// resolved id (not the typed name) is what sideAgentMentionResolve returns.
func TestSideAgent_MentionsUseSharedResolver(t *testing.T) {
	roster := []agentcomm.Participant{
		{ID: "run:qaEngineer", Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun, Alias: "qa", Reachable: true},
		{ID: "stage:impl", Kind: agentcomm.KindStageAgent, Scope: agentcomm.ScopeStage, Alias: "impl", Reachable: true},
		{ID: "helper:t-1", Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage, Alias: "reviewer", Reachable: true},
		{ID: "helper:t-2", Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage, Alias: "reviewer", Reachable: true},
	}
	resolve := sideAgentMentionResolve(func() []agentcomm.Participant { return roster })

	id, ok := resolve("qa")
	if !ok {
		t.Fatal("resolve(\"qa\") = not ok, want a resolved id")
	}
	if id != "run:qaEngineer" {
		t.Fatalf("resolve(\"qa\") = %q, want the CANONICAL participant id %q, not the typed alias", id, "run:qaEngineer")
	}

	// "@" prefix is accepted too (agentcomm.ResolveAlias's own contract).
	if id2, ok := resolve("@qa"); !ok || id2 != id {
		t.Fatalf("resolve(\"@qa\") = (%q, %v), want (%q, true)", id2, ok, id)
	}

	if _, ok := resolve("reviewer"); ok {
		t.Fatal("resolve(\"reviewer\") with two matching participants: want ok=false (ambiguous), not a silent pick")
	}

	if _, ok := resolve("nobody"); ok {
		t.Fatal("resolve(\"nobody\"): want ok=false (unknown alias)")
	}
}

// TestSideAgent_PackageHasNoOwnAliasResolution asserts BY CONSTRUCTION (dev-plan.md T6.7's "Done when"
// clause) that pkg/sideagent's own (non-test) source never imports pkg/agentcomm or pkg/assistants — the
// decoupling boundary the package doc comments describe (Config.MentionResolve/KnownActionType are narrow
// injected functions precisely so this package never needs either import). A regression here would mean
// someone reintroduced a second, ad-hoc alias-matching implementation inside pkg/sideagent itself instead of
// going through the one shared resolver (agentcomm.ResolveAlias), called from cmd/afm.
func TestSideAgent_PackageHasNoOwnAliasResolution(t *testing.T) {
	const pkgDir = "../../pkg/sideagent"
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", pkgDir, err)
	}
	forbidden := []string{"github.com/akopichin/afm/pkg/agentcomm", "github.com/akopichin/afm/pkg/assistants"}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(pkgDir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		checked++
		for _, imp := range file.Imports {
			importPath := importPathValue(t, imp)
			for _, f := range forbidden {
				if importPath == f {
					t.Fatalf("%s imports %q — pkg/sideagent must stay decoupled (narrow injected functions only)", path, importPath)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-test .go files found under pkg/sideagent — test fixture is broken")
	}
}

func importPathValue(t *testing.T, imp *ast.ImportSpec) string {
	t.Helper()
	v, err := strconv.Unquote(imp.Path.Value)
	if err != nil {
		t.Fatalf("unquote import %s: %v", imp.Path.Value, err)
	}
	return v
}
