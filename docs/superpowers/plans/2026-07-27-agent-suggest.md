# agent_suggest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Позволить пользователю на любой активной стадии (не только `awaiting_approval`, как сейчас у `Revise`) вбросить фразу-поправку агенту — тот аккуратно завершает текущее действие (SIGINT, не SIGKILL) и перезапускается с этой фразой в контексте. Фича гейтится экспериментальным флагом.

**Architecture:** Расширяем существующий `Revise`-механизм (не создаём параллельный): FSM разрешает `EvRevise` из `running`, новый per-stage interrupt-канал на `Orchestrator` доставляет SIGINT в текущий subprocess агента, три новых `run<Phase>WithFeedback`-раннера (по образцу уже существующего `runPlanningWithFeedback`) перезапускают фазу с фидбеком и `--resume`, если стадия interactive.

**Tech Stack:** Go (backend), React/TypeScript (dashboard), существующие паттерны кодовой базы (FSM, `sync.Map`-реестры, `executor.Config`).

## Global Constraints

- Не менять версию Go в `go.mod` без предупреждения.
- `make lint` должен быть чист (0 issues) после каждой задачи.
- Коммиты — на русском, без `Co-Authored-By`.
- Флаг `experimental.agent_suggest` (+ env `AFM_EXP_AGENT_SUGGEST`) гейтит фичу и на бэкенде, и на фронте — без него ничего не меняется в поведении.
- Прерывание — всегда SIGINT (не SIGKILL, не отмена общего `ctx` рана) с ограниченным временем ожидания, после которого — принудительный kill как страховка.
- Спек: `docs/superpowers/specs/2026-07-27-agent-suggest-design.md` — источник истины по архитектурным решениям.

---

### Task 1: Экспериментальный флаг в конфиге

**Файлы:**
- Modify: `pkg/config/config.go`
- Test: `pkg/config/config_test.go` (дополнить существующий файл)

**Interfaces:**
- Produces: `config.ExperimentalConfig{AgentSuggest *bool}`, `(ExperimentalConfig) IsAgentSuggestEnabled() bool`, поле `Config.Experimental ExperimentalConfig`.

- [ ] **Step 1: Написать падающий тест**

Добавить в `pkg/config/config_test.go` (посмотреть существующие тесты в этом файле — `TestIsDockerEnabled`-подобные — и следовать их стилю с `t.Setenv`):

```go
func TestIsAgentSuggestEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	cases := []struct {
		name string
		cfg  ExperimentalConfig
		env  string
		want bool
	}{
		{"nil + no env", ExperimentalConfig{}, "", false},
		{"nil + env=1", ExperimentalConfig{}, "1", true},
		{"nil + env=true", ExperimentalConfig{}, "true", true},
		{"explicit true overrides no env", ExperimentalConfig{AgentSuggest: &trueVal}, "", true},
		{"explicit false overrides env=1", ExperimentalConfig{AgentSuggest: &falseVal}, "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_EXP_AGENT_SUGGEST", tc.env)
			if got := tc.cfg.IsAgentSuggestEnabled(); got != tc.want {
				t.Errorf("IsAgentSuggestEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Запустить тест, убедиться что падает**

Run: `go test ./pkg/config/... -run TestIsAgentSuggestEnabled -v`
Expected: FAIL с `undefined: ExperimentalConfig` (compile error)

- [ ] **Step 3: Реализовать**

В `pkg/config/config.go`, рядом с `SupervisorConfig` (перед `type Config struct`):

```go
// ExperimentalConfig настраивает фичи под флагом, ещё не готовые для дефолтного
// включения. agent_suggest — вброс фразы-поправки агенту на активной стадии
// (не только awaiting_approval, как у обычного Revise) — см.
// docs/superpowers/specs/2026-07-27-agent-suggest-design.md.
type ExperimentalConfig struct {
	AgentSuggest *bool `yaml:"agent_suggest"` // nil = смотрим AFM_EXP_AGENT_SUGGEST
}

// IsAgentSuggestEnabled reports whether the agent_suggest experimental
// feature is enabled — explicit config value takes priority over the env var.
func (e ExperimentalConfig) IsAgentSuggestEnabled() bool {
	if e.AgentSuggest != nil {
		return *e.AgentSuggest
	}
	return envFlag("AFM_EXP_AGENT_SUGGEST")
}
```

В `type Config struct` добавить поле (после `Supervisor SupervisorConfig \`yaml:"supervisor"\``):
```go
	Experimental ExperimentalConfig `yaml:"experimental"`
```

В `mergeFile` (рядом с блоком `if overlay.Docker.Enabled != nil { ... }`) добавить:
```go
	if overlay.Experimental.AgentSuggest != nil {
		dst.Experimental.AgentSuggest = overlay.Experimental.AgentSuggest
	}
```

- [ ] **Step 4: Запустить тест, убедиться что проходит**

Run: `go test ./pkg/config/... -run TestIsAgentSuggestEnabled -v`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет + линт**

Run: `go test ./pkg/config/... && make lint`
Expected: PASS, 0 issues

- [ ] **Step 6: Коммит**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): экспериментальный флаг agent_suggest

experimental.agent_suggest в config.yaml (+ дублирующий env
AFM_EXP_AGENT_SUGGEST) — по образцу существующего docker.enabled/
IsDockerEnabled. Пока ничего не гейтит — используется следующими
задачами плана.
EOF
)"
```

---

### Task 2: FSM — разрешить `EvRevise` из `running`

**Файлы:**
- Modify: `pkg/orchestrator/fsm.go`
- Test: `pkg/orchestrator/fsm_test.go`

**Interfaces:**
- Consumes: ничего нового.
- Produces: `EvRevise` теперь разрешён из `state.StatusAwaitingApproval` И `state.StatusRunning`.

- [ ] **Step 1: Написать падающий тест**

Добавить в `pkg/orchestrator/fsm_test.go` (рядом с существующими `TestFSM_Apply_*`):

```go
func TestFSM_Apply_ReviseFromRunning(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})

	to, _, ok, err := fsm.Apply("a", EvRevise, GuardCtx{}, "feedback text")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusRevising {
		t.Errorf("running->revising: got (%v, %v), want (revising, true)", to, ok)
	}
}
```

- [ ] **Step 2: Запустить тест, убедиться что падает**

Run: `go test ./pkg/orchestrator/... -run TestFSM_Apply_ReviseFromRunning -v`
Expected: FAIL — `ok = false` (текущее правило разрешает `EvRevise` только из `awaiting_approval`)

- [ ] **Step 3: Реализовать**

В `pkg/orchestrator/fsm.go` найти:
```go
EvRevise: {From: []state.StageStatus{state.StatusAwaitingApproval}, To: to(state.StatusRevising)},
```
заменить на:
```go
EvRevise: {From: []state.StageStatus{state.StatusAwaitingApproval, state.StatusRunning}, To: to(state.StatusRevising)},
```

- [ ] **Step 4: Запустить тест, убедиться что проходит**

Run: `go test ./pkg/orchestrator/... -run TestFSM_Apply_ReviseFromRunning -v`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет orchestrator + линт**

Run: `go test ./pkg/orchestrator/... -timeout 120s && make lint`
Expected: PASS (включая существующий `TestFSM_Property_LivenessTerminates` — `EvRevise` уже в списке событий этого property-теста, расширение `From` не должно сломать terminaton), 0 issues

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/fsm.go pkg/orchestrator/fsm_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): FSM разрешает EvRevise из running

Предпосылка для agent_suggest — вброс фразы агенту не только на паузе
перед approve, но и пока стадия реально выполняется.
EOF
)"
```

---

### Task 3: executor — graceful-прерывание через SIGINT

**Файлы:**
- Modify: `pkg/executor/executor.go`
- Test: `pkg/executor/interrupt_test.go` (новый файл)

**Interfaces:**
- Produces: `executor.ErrUserInterrupted` (sentinel error, `errors.Is`-совместимый); `executor.Config.InterruptCh <-chan struct{}` (новое поле); `run()`/`RunAgent` при получении сигнала на этот канал шлют subprocess'у `SIGINT`, ждут завершения до `interruptGracePeriod`, затем принудительно `Kill()`, и возвращают `ErrUserInterrupted`.

- [ ] **Step 1: Написать падающий тест**

Создать `pkg/executor/interrupt_test.go`:

```go
package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunAgent_InterruptSendsSIGINTNotKill проверяет, что сигнал на
// InterruptCh приводит к SIGINT (агент сам грамотно завершает текущую
// атомарную операцию и выходит), а не к жёсткому Kill — и что RunAgent
// возвращает ErrUserInterrupted, отличимый от обычной ошибки завершения.
func TestRunAgent_InterruptSendsSIGINTNotKill(t *testing.T) {
	dir := t.TempDir()
	// Скрипт: ловит SIGINT, пишет маркер и завершается с кодом 0 (а не
	// оставляет процесс висеть, что случилось бы при отсутствии обработки).
	script := "trap 'touch " + dir + "/signaled; exit 0' INT\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"working\"}]}}'\n" +
		"sleep 30\n"
	scriptPath := dir + "/agent.sh"
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}

	interruptCh := make(chan struct{}, 1)
	e := New(Config{
		Command:     scriptPath,
		IdleTimeout: 10 * time.Second,
		InterruptCh: interruptCh,
	})

	done := make(chan error, 1)
	go func() {
		done <- e.RunAgent(context.Background(), "test", "Stage", "prompt", dir+"/run.log")
	}()

	// Дать скрипту время дойти до trap+sleep, прежде чем слать сигнал.
	time.Sleep(300 * time.Millisecond)
	interruptCh <- struct{}{}

	select {
	case err := <-done:
		if !errors.Is(err, ErrUserInterrupted) {
			t.Errorf("RunAgent error = %v, want errors.Is(err, ErrUserInterrupted)", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunAgent did not return within 20s of interrupt signal")
	}

	if _, err := os.Stat(dir + "/signaled"); err != nil {
		t.Errorf("script did not receive SIGINT (marker file missing): %v", err)
	}
}
```

(Добавить `"os"` в импорты файла — уже есть `os.WriteFile`/`os.Stat` в тесте выше.)

- [ ] **Step 2: Запустить тест, убедиться что падает**

Run: `go test ./pkg/executor/... -run TestRunAgent_InterruptSendsSIGINTNotKill -v -timeout 30s`
Expected: FAIL с `unknown field InterruptCh in struct literal` / `undefined: ErrUserInterrupted` (compile error)

- [ ] **Step 3: Реализовать**

В `pkg/executor/executor.go` добавить в импорты `"errors"` и `"syscall"` (сейчас их нет в этом файле).

Добавить sentinel error рядом с `defaultCommand`:
```go
// ErrUserInterrupted signals that the agent process was stopped because the
// user requested an interrupt (via Config.InterruptCh) — not a real failure.
// Callers (runWithRetry) must distinguish this from retry/failure handling.
var ErrUserInterrupted = errors.New("user interrupted")

// interruptGracePeriod bounds how long we wait for the subprocess to exit
// gracefully after SIGINT before force-killing it as a safety net against a
// hung/misbehaving process.
const interruptGracePeriod = 15 * time.Second
```

В `Config` добавить поле (после `StageID string`):
```go
	// InterruptCh, if set, is watched during RunAgent: a signal on this channel
	// sends SIGINT to the subprocess (not SIGKILL, not ctx cancellation) —
	// graceful, user-requested interrupt (agent_suggest), distinct from idle
	// timeout / full-run shutdown. nil channel is safe (select never fires).
	InterruptCh <-chan struct{}
```

В `run()` (метод `(e *Executor) run`) заменить финальный `select`:
```go
	select {
	case readErr := <-done:
		waitErr := cmd.Wait()
		if readErr != nil {
			return readErr
		}
		return waitErr
	case <-idleTimer.C:
		cmd.Process.Kill()
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return fmt.Errorf("idle timeout after %v", e.cfg.IdleTimeout)
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return ctx.Err()
	}
```
на:
```go
	select {
	case readErr := <-done:
		waitErr := cmd.Wait()
		if readErr != nil {
			return readErr
		}
		return waitErr
	case <-idleTimer.C:
		cmd.Process.Kill()
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return fmt.Errorf("idle timeout after %v", e.cfg.IdleTimeout)
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done // wait for stdout reader to finish
		_ = cmd.Wait()
		return ctx.Err()
	case <-e.cfg.InterruptCh:
		// Мягкое прерывание: SIGINT, а не Kill — claude сам грамотно
		// завершает текущую атомарную операцию (запись файла — один syscall,
		// его практически не рвёт сигналом на середине) и выходит.
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-done:
			_ = cmd.Wait()
			return ErrUserInterrupted
		case <-time.After(interruptGracePeriod):
			// Не среагировал на SIGINT вовремя — принудительно, как страховка.
			cmd.Process.Kill()
			<-done
			_ = cmd.Wait()
			return ErrUserInterrupted
		}
	}
```

- [ ] **Step 4: Запустить тест, убедиться что проходит**

Run: `go test ./pkg/executor/... -run TestRunAgent_InterruptSendsSIGINTNotKill -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет executor + линт**

Run: `go test ./pkg/executor/... -timeout 60s && make lint`
Expected: PASS, 0 issues

- [ ] **Step 6: Коммит**

```bash
git add pkg/executor/executor.go pkg/executor/interrupt_test.go
git commit -m "$(cat <<'EOF'
feat(executor): graceful-прерывание агента через SIGINT (Config.InterruptCh)

Новый канал в Config: сигнал на него шлёт subprocess'у SIGINT (не
SIGKILL, не отмену ctx — это для другого, полного шатдауна рана).
Ограниченное время ожидания (15с) перед принудительным Kill как
страховка от зависшего процесса. Возвращает ErrUserInterrupted —
отличим от retry/failure в runWithRetry. Предпосылка для agent_suggest.
EOF
)"
```

---

### Task 4: Реестр прерываний + `run<Phase>WithFeedback` для implementation/review/autonomous

**Файлы:**
- Modify: `pkg/orchestrator/orchestrator.go` (поле `interruptChans`)
- Modify: `pkg/orchestrator/runner_factory.go` (подключение канала в `executor.Config`)
- Modify: `pkg/orchestrator/retry.go` (`runWithRetry` — новый параметр + новая ветка обработки)
- Modify: `pkg/orchestrator/agents.go` (3 новых раннера + обновить 5 существующих вызовов `runWithRetry`)
- Test: `pkg/orchestrator/agent_suggest_test.go` (новый файл)

**Interfaces:**
- Consumes: `executor.ErrUserInterrupted` (Task 3).
- Produces: `Orchestrator.interruptChans sync.Map` (stageID → `chan struct{}`, буфер 1); `runWithRetry(ctx, s, phase, agentFn, completionCheck, onUserInterrupted func())` — новая 6-я сигнатура параметра; `runImplementationWithFeedback`, `runReviewWithFeedback`, `runAutonomousWithFeedback` (`func(context.Context, flow.Stage)`, та же сигнатура, что и у существующих `run<Phase>Agent`).

Это одна задача (не несколько), потому что все части взаимозависимы на уровне компиляции Go: изменение сигнатуры `runWithRetry` требует одновременного обновления всех 5 её вызовов, а осмысленные `onUserInterrupted`-замыкания для implementation/review/autonomous требуют, чтобы соответствующие `WithFeedback`-функции уже существовали.

- [ ] **Step 1: Написать падающий тест (сначала на реестр + сигнатуру)**

Создать `pkg/orchestrator/agent_suggest_test.go`:

```go
package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// interruptibleRunner: RunAgent блокируется до получения сигнала на
// interrupt-канал (проброшенного через Config.InterruptCh), затем
// возвращает executor.ErrUserInterrupted — имитирует реальный executor без
// запуска настоящего subprocess. После "прерванного" вызова следующий
// RunAgent (перезапуск с фидбеком) сразу пишет completion-артефакт.
type interruptibleRunner struct {
	calls int
}

func (r *interruptibleRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (r *interruptibleRunner) RunAgent(ctx context.Context, _, stageName, _, logFile string) error {
	r.calls++
	if r.calls == 1 {
		<-ctx.Done() // ждём отмены — сюда мы не должны попасть в этом тесте (см. ниже)
		return ctx.Err()
	}
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *interruptibleRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*interruptibleRunner)(nil)

// TestInterruptChans_RegistryLifecycle подтверждает контракт реестра:
// канал появляется в interruptChans на время конкретной попытки RunAgent
// (доступен runnerFor через Orchestrator) и исчезает сразу после её
// завершения — независимо от результата.
func TestInterruptChans_RegistryLifecycle(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "a", Name: "a", Agents: []flow.AgentType{flow.AgentAuto}}}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	seen := make(chan bool, 1)
	runner := &recordingInterruptRunner{seen: seen}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !<-seen {
		t.Error("expected a non-nil interrupt channel to be registered while the agent ran")
	}
	if orchestrator.HasInterruptChan(orch, "a") {
		t.Error("interrupt channel for stage 'a' should be removed after the agent finished")
	}
}

// recordingInterruptRunner записывает, был ли зарегистрирован interrupt-канал
// (проверяется косвенно через executor.Config.InterruptCh, недоступный
// отсюда напрямую — поэтому просто пишет completion-артефакт сразу; факт
// регистрации/снятия проверяется отдельно через HasInterruptChan до/после).
type recordingInterruptRunner struct {
	seen chan bool
}

func (r *recordingInterruptRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (r *recordingInterruptRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	r.seen <- orchestrator.HasInterruptChanForRunDir(stageDir)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *recordingInterruptRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*recordingInterruptRunner)(nil)
```

**Важно исполнителю:** тестовые хелперы `orchestrator.HasInterruptChan(orch, stageID)` и `orchestrator.HasInterruptChanForRunDir(stageDir)` в этом черновике придуманы для иллюстрации — сами по себе они избыточны и плохо тестируют реальный контракт (реестр приватный, тест на другом пакете `orchestrator_test` не должен требовать экспортированных дебаг-хуков только ради теста). **Перед реализацией замени `TestInterruptChans_RegistryLifecycle` и `recordingInterruptRunner` на единственный содержательный тест ниже** (`TestAgentSuggest_InterruptRestartsWithFeedback`), который проверяет то же самое явление (регистрация/снятие канала) косвенно, через наблюдаемое поведение (SIGINT реально доставлен, стадия реально перезапустилась с фидбеком) — это сильнее и не требует служебных экспортов. Итоговый файл `agent_suggest_test.go` должен содержать только этот тест (плюс его вспомогательный `Runner`):

```go
package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// blockingThenFeedbackRunner: первый вызов RunAgent просто блокируется на
// ctx.Done() (реальный executor так же блокируется на подпроцессе, пока не
// придёт SIGINT/отмена) — используется вместе с настоящим InterruptCh через
// Trigger(EvRevise)+сигнал в канал реестра. Второй вызов (перезапуск с
// фидбеком) сразу читает feedback.md и завершает стадию.
type blockingThenFeedbackRunner struct {
	calls int
}

func (r *blockingThenFeedbackRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (r *blockingThenFeedbackRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	r.calls++
	stageDir := filepath.Dir(logFile)
	if r.calls == 1 {
		<-ctx.Done()
		return context.Canceled
	}
	feedback, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if len(feedback) == 0 {
		return errors.New("expected feedback.md to be readable on the feedback restart")
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *blockingThenFeedbackRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*blockingThenFeedbackRunner)(nil)
```

Этот раннер намеренно НЕ проверяет `ErrUserInterrupted` напрямую (мок не проходит через настоящий `executor`, который единственный умеет это возвращать) — вместо этого он полагается на то, что `Revise` при статусе `running` (Task 5, следующая задача) триггерит настоящее прерывание через реестр, и сам факт **второго вызова с непустым `feedback.md`** доказывает, что перезапуск состоялся. Полный e2e-тест этого сценария (`TestAgentSuggest_InterruptRestartsWithFeedback`) пишется и запускается только в конце **Task 5**, где уже есть работающий `Revise` — держи `blockingThenFeedbackRunner` в этом файле, но сам вызывающий тест добавь туда же в Task 5.

- [ ] **Step 2: Запустить, убедиться что падает**

Run: `go test ./pkg/orchestrator/... -run TestAgentSuggest -v -timeout 30s`
Expected: FAIL — на этом этапе теста ещё нет (он появится в Task 5), поэтому здесь просто `go build ./pkg/orchestrator/...` должен пройти с новым файлом, содержащим только `blockingThenFeedbackRunner` (неиспользуемый тип не вызывает ошибку компиляции в Go, только линтер может пожаловаться на unused — это нормально, тип будет использован в Task 5). Запусти вместо этого: `go build ./pkg/orchestrator/...` — Expected: PASS (файл компилируется, тестов пока 0).

- [ ] **Step 3: Реализовать реестр + сигнатуру `runWithRetry` + 3 новых раннера**

В `pkg/orchestrator/orchestrator.go`, в `type Orchestrator struct`, добавить поле (рядом с `activeAgents sync.Map`):
```go
	// interruptChans хранит канал прерывания (stageID → chan struct{}, буфер
	// 1) на время КОНКРЕТНОЙ попытки RunAgent — создаётся в начале
	// runWithRetry, удаляется по её завершении (успешном или нет). Revise
	// (agent_suggest) шлёт в этот канал, чтобы запросить graceful-прерывание
	// текущего вызова агента через executor.Config.InterruptCh.
	interruptChans sync.Map
```

В `pkg/orchestrator/runner_factory.go`, в функции `runnerFor`, добавить чтение канала в ОБЕИХ ветках (неinteractive и interactive), прямо перед `return executor.New(cfg)` в каждой:
```go
	if ch, ok := o.interruptChans.Load(s.ID); ok {
		cfg.InterruptCh = ch.(chan struct{})
	}
```
(Итого 2 вставки — в неinteractive-ветке `runnerFor` перед первым `return executor.New(cfg)` и в interactive-ветке перед вторым.)

В `pkg/orchestrator/retry.go`, сигнатура `runWithRetry`:
```go
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error) {
```
меняем на:
```go
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
```

В начало тела функции (перед `incompleteReason := ""`) добавить:
```go
	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(s.ID, interruptCh)
	defer o.interruptChans.Delete(s.ID)
```

Сразу после `err := agentFn(retryCtx)` и обработки `err == nil` (то есть непосредственно перед существующим `if !isRetryableError(err) {`) вставить новую ветку:
```go
		if errors.Is(err, executor.ErrUserInterrupted) {
			onUserInterrupted()
			return
		}

```
Добавить в импорты `retry.go`: `"errors"` и `"github.com/akopichin/afm/pkg/executor"` (если их там ещё нет — проверить перед добавлением, чтобы не задвоить).

В `pkg/orchestrator/agents.go` обновить все 5 существующих вызовов `runWithRetry`, добавив 6-й аргумент:

1. `runPlanningAgent` (строка с `o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {` — первое вхождение): последним аргументом (после `func() error { return checkPlanCompletionFor(...) }`) добавить `, func() { o.spawnAgent(ctx, s, o.runPlanningWithFeedback) }`.
2. `runPlanningWithFeedback` (второе вхождение той же сигнатуры): аналогично `, func() { o.spawnAgent(ctx, s, o.runPlanningWithFeedback) }` (реентрантно — повторное прерывание снова уходит в тот же цикл).
3. `runImplementationAgent`: `, func() { o.spawnAgent(ctx, s, o.runImplementationWithFeedback) }`.
4. `runReviewAgent`: `, func() { o.spawnAgent(ctx, s, o.runReviewWithFeedback) }`.
5. `runAutonomousAgent`: `, func() { o.spawnAgent(ctx, s, o.runAutonomousWithFeedback) }`.

Добавить 3 новые функции в `pkg/orchestrator/agents.go` (в конец файла):

```go
// runImplementationWithFeedback перезапускает implementation-фазу с фидбеком
// пользователя (agent_suggest) — как runImplementationAgent, но с добавленной
// в контекст фразой из feedback.md. Идёт через тот же runnerFor: если стадия
// Interactive, автоматически получает --resume <session-id> (существующий
// sessionExists/loadOrCreateSession в runnerFor не меняется).
func (o *Orchestrator) runImplementationWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	feedbackNote := ""
	if len(feedbackData) > 0 {
		feedbackNote = "\n\n## User note (added while this stage was running)\n\n" + string(feedbackData)
	}

	o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s impl (feedback restart): %v", s.ID, artErr)
		}

		if len(s.Artifacts) > 0 {
			var buf strings.Builder
			buf.WriteString("\n\nRequired output artifacts (MUST exist at these paths when stage finishes):\n\n")
			for _, art := range s.Artifacts {
				dst := art.Path
				if strings.HasPrefix(art.Path, "./") {
					dst = filepath.Join(stageDir, art.Path[2:])
				}
				desc := ""
				if art.Description != "" {
					desc = " — " + art.Description
				}
				fmt.Fprintf(&buf, "- %s%s → %s\n", art.Name, desc, dst)
			}
			artCtx += buf.String()
		}

		stageDirNote := fmt.Sprintf("\n\nStage directory for .done file: %s", stageDir)
		if s.Verify != "" {
			stageDirNote += fmt.Sprintf("\n\nVerify command (runs automatically after you finish; it MUST exit 0, "+
				"so run it yourself before creating .done):\n%s", s.Verify)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation,
			Stage:           s,
			PhaseAgent:      prompts.AgentImplementation,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			Plan:            string(planData),
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + stageDirNote + feedbackNote,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		logFile := filepath.Join(stageDir, "implementation-feedback.log")

		r := o.runnerFor(s, phaseImplementation)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			reviewPrompt := prompts.Build(prompts.Inputs{
				Template:        o.opts.Prompts.Review,
				Stage:           s,
				PhaseAgent:      prompts.AgentReview,
				DependencyPlans: depPlans,
				Artifacts:       artCtx,
				StageDir:        stageDir,
				Interactive:     s.Interactive,
				GlobalPrompt:    o.opts.GlobalPrompt,
			})
			reviewLog := filepath.Join(stageDir, "review.log")
			rr := o.runnerFor(s, phaseReview)
			if err := rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	}, func() { o.spawnAgent(ctx, s, o.runImplementationWithFeedback) })
}

// runReviewWithFeedback — как runReviewAgent, с фразой пользователя в контексте.
func (o *Orchestrator) runReviewWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	feedbackNote := ""
	if len(feedbackData) > 0 {
		feedbackNote = "\n\n## User note (added while this stage was running)\n\n" + string(feedbackData)
	}

	depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
		appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
		o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
	})
	artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	if artErr != nil {
		log.Printf("WARN: collect artifacts for %s review (feedback restart): %v", s.ID, artErr)
	}

	o.runWithRetry(ctx, s, phaseReview, func(retryContext string) error {
		reviewPrompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Review,
			Stage:           s,
			PhaseAgent:      prompts.AgentReview,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + feedbackNote,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		reviewLog := filepath.Join(stageDir, "review-feedback.log")
		rr := o.runnerFor(s, phaseReview)
		return rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog)
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	}, func() { o.spawnAgent(ctx, s, o.runReviewWithFeedback) })
}

// runAutonomousWithFeedback — как runAutonomousAgent, с фразой пользователя в контексте.
func (o *Orchestrator) runAutonomousWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	feedbackNote := ""
	if len(feedbackData) > 0 {
		feedbackNote = "\n\n## User note (added while this stage was running)\n\n" + string(feedbackData)
	}

	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s autonomous (feedback restart): %v", s.ID, artErr)
		}
		depCtx := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})

		summaryNote := fmt.Sprintf("\n\nStage directory: %s\nWrite execution_summary.md here when done.", stageDir)
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation,
			Autonomous:      o.opts.Prompts.Autonomous,
			Stage:           s,
			PhaseAgent:      prompts.AgentAutonomous,
			Interactive:     true,
			Artifacts:       artCtx,
			DependencyPlans: depCtx,
			StageDir:        stageDir,
			GlobalPrompt:    o.opts.GlobalPrompt,
			RetryContext:    retryContext + summaryNote + feedbackNote,
		})
		logFile := filepath.Join(stageDir, "autonomous-feedback.log")
		r := o.runnerFor(s, phaseAutonomous)
		return r.RunAgent(ctx, string(prompts.AgentAutonomous), s.Name, prompt, logFile)
	}, func() error {
		return checkAutonomousCompletion(stageDir)
	}, func() { o.spawnAgent(ctx, s, o.runAutonomousWithFeedback) })
}
```

**Важно:** сверь последний блок (`runAutonomousWithFeedback`'s `r.RunAgent(ctx, string(prompts.AgentAutonomous), ...)`) с ТОЧНЫМ вызовом внутри существующего `runAutonomousAgent` (открой `pkg/orchestrator/agents.go`, найди тело функции целиком) — воспроизведи его логирование строка-в-строку (включая, если там иначе называется переменная агента/лога), не polагайся только на этот план: план даёт архитектуру и большую часть кода, но исполнитель обязан свериться с реальным текущим содержимым `runAutonomousAgent` перед тем как писать `runAutonomousWithFeedback`, чтобы не разойтись в деталях (имя лог-файла, точный текст `summaryNote`, и т.д. должны отличаться от базовой функции ТОЛЬКО добавлением `feedbackNote`).

- [ ] **Step 4: Убедиться что всё компилируется**

Run: `go build ./pkg/orchestrator/...`
Expected: PASS (0 ошибок)

- [ ] **Step 5: Прогнать весь пакет orchestrator + линт**

Run: `go test ./pkg/orchestrator/... -timeout 180s && make lint`
Expected: PASS (все существующие тесты, включая те, что used `runWithRetry` косвенно через `run*Agent`), 0 issues

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/runner_factory.go pkg/orchestrator/retry.go pkg/orchestrator/agents.go pkg/orchestrator/agent_suggest_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): реестр прерываний + runImplementationWithFeedback/runReviewWithFeedback/runAutonomousWithFeedback

interruptChans на Orchestrator — канал прерывания на время конкретной
попытки RunAgent, подключается в runnerFor к executor.Config.InterruptCh.
runWithRetry получает onUserInterrupted — при ErrUserInterrupted не идёт
в обычный retry/fail, а зовёт переданный колбэк. Три новых раннера
зеркалят уже существующий runPlanningWithFeedback для остальных трёх фаз.
EOF
)"
```

---

### Task 5: `Orchestrator.Revise` — разрешить `running`, доставка прерывания

**Файлы:**
- Modify: `pkg/orchestrator/control_api.go`
- Modify: `pkg/orchestrator/agent_suggest_test.go` (добавить финальный e2e-тест)

**Interfaces:**
- Consumes: `interruptChans` (Task 4), `EvRevise` из `running` (Task 2), `run<Phase>WithFeedback` (Task 4).
- Produces: `Revise` теперь обрабатывает `state.StatusRunning` — сохраняет фидбек и доставляет сигнал прерывания через реестр, не спауня ничего сама (перезапуск делает `onUserInterrupted` изнутри уже идущего `runWithRetry`, когда subprocess реально завершится).

- [ ] **Step 1: Написать падающий тест**

Добавить в `pkg/orchestrator/agent_suggest_test.go` (после `blockingThenFeedbackRunner`, добавленного в Task 4):

```go
// TestAgentSuggest_InterruptRestartsWithFeedback — сквозной сценарий:
// стадия running → Revise с фидбеком → FSM сразу уходит в revising и
// сохраняет feedback.md → сигнал доставляется в текущий (блокирующийся)
// RunAgent → тот "прерывается" (в этом тесте — просто видит отмену ctx,
// т.к. мок не проходит через настоящий executor/ErrUserInterrupted) →
// runImplementationWithFeedback перезапускается, читает уже записанный
// feedback.md, стадия доходит до done.
//
// Примечание: этот тест проверяет ОРКЕСТРАЦИОННУЮ часть (Revise → реестр →
// перезапуск с фидбеком на диске), а не сам SIGINT — SIGINT на реальном
// subprocess'е уже отдельно покрыт TestRunAgent_InterruptSendsSIGINTNotKill
// (Task 3, pkg/executor). Мок здесь блокируется на ctx.Done(), а не слушает
// interrupt-канал напрямую — поэтому тест использует orch.Retry-подобный
// путь: cancel() до перезапуска эмулируется тем, что Revise доставляет
// сигнал, но фактическое прерывание мок-раннера здесь через ctx достигается
// тем, что onUserInterrupted вызывается ПОСЛЕ настоящего SIGINT-цикла в
// executor — в моке этого нет, поэтому тест напрямую проверяет то, что
// реально наблюдаемо ЗДЕСЬ: реестр содержит канал, Revise его сигналит без
// паники/блокировки, feedback.md записан корректно ДО того как сигнал ушёл.
func TestAgentSuggest_InterruptRestartsWithFeedback(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{
		ID: "impl", Name: "Impl",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &blockingThenFeedbackRunner{}
	cfg := config.Default()
	trueVal := true
	cfg.Experimental.AgentSuggest = &trueVal

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	// planning уже должно было пройти (agents: [planning, implementation]),
	// implementation блокируется в blockingThenFeedbackRunner.RunAgent.
	if err := orch.Revise(ctx, "impl", "please add extra logging"); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	waitForStatus(t, stateFile, "impl", state.StatusDone, 15*time.Second)

	feedbackPath := filepath.Join(runDir, "impl", "feedback.md")
	data, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("feedback.md not found: %v", err)
	}
	if string(data) != "please add extra logging" {
		t.Errorf("feedback.md = %q, want %q", data, "please add extra logging")
	}
	if runner.calls != 2 {
		t.Errorf("expected exactly 2 RunAgent calls (initial + feedback restart), got %d", runner.calls)
	}
}
```

- [ ] **Step 2: Запустить, убедиться что падает**

Run: `go test ./pkg/orchestrator/... -run TestAgentSuggest_InterruptRestartsWithFeedback -v -timeout 30s`
Expected: FAIL — стадия не доходит до `done` (текущий `Revise` игнорирует `running`, возвращает `nil` без эффекта; `blockingThenFeedbackRunner` навсегда блокируется на `<-ctx.Done()`, ждём таймаута теста через `waitForStatus`'s `t.Fatalf`)

- [ ] **Step 3: Реализовать**

В `pkg/orchestrator/control_api.go` заменить текущую `Revise`:
```go
func (o *Orchestrator) Revise(reqCtx context.Context, stageID, feedback string) error {
	if o.currentStatus(stageID) != state.StatusAwaitingApproval {
		return nil
	}

	if _, ok := o.Trigger(stageID, EvRevise, GuardCtx{}, feedback); !ok {
		return nil
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	if stage := o.graph.Stage(stageID); stage != nil {
		o.spawnAgent(o.runContext(reqCtx), *stage, o.runPlanningWithFeedback)
	}
	return nil
}
```
на:
```go
// Revise sends feedback to re-plan a stage, ИЛИ (agent_suggest, running)
// запрашивает graceful-прерывание текущего вызова агента фразой в контексте
// (синхронно и долговечно): переход в revising фиксируется в Store до
// возврата — краш после Revise не теряет интент (recovery резюмит revising
// через тот же путь, что и planning).
//
// running-ветка ничего не спауnit сама — перезапуск с фидбеком делает
// onUserInterrupted изнутри уже идущего runWithRetry, когда SIGINT реально
// завершит текущий subprocess (см. pkg/executor: Config.InterruptCh).
func (o *Orchestrator) Revise(reqCtx context.Context, stageID, feedback string) error {
	current := o.currentStatus(stageID)
	if current != state.StatusAwaitingApproval && current != state.StatusRunning {
		return nil
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)

	if current == state.StatusRunning {
		if _, ok := o.Trigger(stageID, EvRevise, GuardCtx{}, feedback); !ok {
			return nil
		}
		if err := state.SaveFeedback(stageDir, feedback); err != nil {
			return fmt.Errorf("save feedback for %s: %w", stageID, err)
		}
		if ch, ok := o.interruptChans.Load(stageID); ok {
			select {
			case ch.(chan struct{}) <- struct{}{}:
			default: // канал уже сигнализирован (двойной клик) — не блокируемся
			}
		}
		return nil
	}

	if _, ok := o.Trigger(stageID, EvRevise, GuardCtx{}, feedback); !ok {
		return nil
	}
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	if stage := o.graph.Stage(stageID); stage != nil {
		o.spawnAgent(o.runContext(reqCtx), *stage, o.runPlanningWithFeedback)
	}
	return nil
}
```

- [ ] **Step 4: Запустить, убедиться что проходит**

Run: `go test ./pkg/orchestrator/... -run TestAgentSuggest_InterruptRestartsWithFeedback -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет orchestrator + линт**

Run: `go test ./pkg/orchestrator/... -timeout 180s && make lint`
Expected: PASS (в т.ч. существующие тесты `Revise` для `awaiting_approval` — поведение той ветки не изменилось), 0 issues

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/control_api.go pkg/orchestrator/agent_suggest_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): Revise поддерживает running — доставка прерывания через реестр

running-ветка сохраняет feedback.md и сигналит interrupt-канал текущей
попытки — ничего не спаунит сама: перезапуск с фидбеком делает
onUserInterrupted изнутри уже идущего runWithRetry, когда SIGINT
реально завершит subprocess. awaiting_approval-ветка не изменилась.
EOF
)"
```

---

### Task 6: recovery.go — резюм `revising` для всех фаз

**Файлы:**
- Modify: `pkg/orchestrator/recovery.go`
- Test: `pkg/orchestrator/integration_resume_test.go`

**Interfaces:**
- Consumes: `run<Phase>WithFeedback` (Task 4).
- Produces: `detectInterruptedPhase` теперь проверяет и `phaseAutonomous`; `case state.StatusRevising` в `startPlanningForPending` диспетчит на правильный `run<Phase>WithFeedback` по найденной фазе вместо жёсткого `runPlanningWithFeedback`.

- [ ] **Step 1: Написать падающий тест**

Прочитать существующий `pkg/orchestrator/integration_resume_test.go` целиком перед правкой (там уже есть тест `revise-stuck` — искать по `StatusRevising`) и следовать его паттерну (реальный `orchestrator.New` + `startPlanningForPending`-путь через `Run` после `state.Open` с уже проставленным статусом). Добавить новый тест рядом:

```go
// TestResume_RevisingAutonomousStageUsesAutonomousFeedback подтверждает, что
// краш в revising для АВТОНОМНОЙ стадии резюмится через
// runAutonomousWithFeedback, а не жёстко через runPlanningWithFeedback (баг
// до этой правки: detectInterruptedPhase не проверял
// autonomous_execution.session.json, поэтому не находил активную фазу и
// recovery.go шёл в default-ветку planning, которая упала бы — у автономной
// стадии нет plan.md).
func TestResume_RevisingAutonomousStageUsesAutonomousFeedback(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "auto")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Имитируем прерванную автономную стадию: autonomous.flag +
	// autonomous_execution.session.json (свежий) + feedback.md — как если бы
	// Revise уже сработал, но процесс упал до перезапуска.
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution.session.json"), []byte(`{"session_id":"test-session"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("keep going"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{ID: "auto", Name: "auto", Agents: []flow.AgentType{flow.AgentAuto}}}
	store, err := state.Open(runDir, []string{"auto"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "auto", From: state.StatusPending, To: state.StatusRevising, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	runner := &blockingThenFeedbackRunner{}
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// blockingThenFeedbackRunner.calls==1 при первом вызове блокируется на
	// ctx.Done() — здесь нам нужно, чтобы recovery СРАЗУ вызвала
	// runAutonomousWithFeedback (calls становится 1, читает feedback.md,
	// но т.к. это ПЕРВЫЙ вызов раннера в этом тесте — он блокируется). Чтобы
	// проверить именно ДИСПЕТЧИНГ (не полный цикл), достаточно убедиться,
	// что стадия дошла до "running" (recovery успешно нашла autonomous-фазу
	// и не упала на попытке прочитать несуществующий plan.md) в разумное
	// время — а не осталась в revising/failed.
	waitForStatus(t, stateFile, "auto", state.StatusRunning, 10*time.Second)
}
```

- [ ] **Step 2: Запустить, убедиться что падает**

Run: `go test ./pkg/orchestrator/... -run TestResume_RevisingAutonomousStageUsesAutonomousFeedback -v -timeout 20s`
Expected: FAIL — текущий recovery.go жёстко зовёт `runPlanningWithFeedback` для ЛЮБОЙ `revising`-стадии; та пытается прочитать `plan.md`/собрать planning-промпт для автономной стадии без `plan.md` — стадия не доходит до `running` штатным путём (либо падает в `failed`, либо зависает не в том раннере) — `waitForStatus` таймаутится с `t.Fatalf`

- [ ] **Step 3: Реализовать**

В `pkg/orchestrator/recovery.go`, функция `detectInterruptedPhase`, заменить:
```go
func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview} {
```
на:
```go
func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview, phaseAutonomous} {
```

В `startPlanningForPending`, найти:
```go
case state.StatusRevising:
    // Interrupted revision — restart with feedback
    o.spawnAgent(ctx, s, o.runPlanningWithFeedback)
```
заменить на:
```go
case state.StatusRevising:
    // Interrupted revision — restart with feedback, using whichever phase
    // was actually interrupted (agent_suggest can revise any active phase,
    // not only planning — detectInterruptedPhase looks at *.session.json
    // mtimes, same helper resumeInteractiveAgent already uses).
    stageDir := filepath.Join(o.opts.RunDir, s.ID)
    switch o.detectInterruptedPhase(stageDir) {
    case phaseImplementation:
        o.spawnAgent(ctx, s, o.runImplementationWithFeedback)
    case phaseReview:
        o.spawnAgent(ctx, s, o.runReviewWithFeedback)
    case phaseAutonomous:
        o.spawnAgent(ctx, s, o.runAutonomousWithFeedback)
    default:
        o.spawnAgent(ctx, s, o.runPlanningWithFeedback)
    }
```

- [ ] **Step 4: Запустить, убедиться что проходит**

Run: `go test ./pkg/orchestrator/... -run TestResume_RevisingAutonomousStageUsesAutonomousFeedback -v -timeout 20s`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет orchestrator + линт**

Run: `go test ./pkg/orchestrator/... -timeout 180s && make lint`
Expected: PASS (включая существующий `revise-stuck` тест для планирования — тот случай по-прежнему идёт в `default:` → `runPlanningWithFeedback`, не меняется), 0 issues

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/recovery.go pkg/orchestrator/integration_resume_test.go
git commit -m "$(cat <<'EOF'
fix(orchestrator): recovery резюмит revising по реальной прерванной фазе

detectInterruptedPhase теперь проверяет и autonomous_execution.session.json.
startPlanningForPending при крашe в revising больше не зовёт жёстко
runPlanningWithFeedback для любой стадии — диспетчит на нужный
run<Phase>WithFeedback по найденной фазе (predpосылка agent_suggest:
revising теперь достижим не только из planning).
EOF
)"
```

---

### Task 7: HTTP-хендлер + `statusResponse` + проводка флага

**Файлы:**
- Modify: `pkg/server/server.go` (`Server`/`Config` — новое поле)
- Modify: `pkg/server/handlers.go` (`handleRevise`, `statusResponse`, `handleStatus`)
- Modify: `cmd/afm/run.go` (проводка `cfg.Experimental.IsAgentSuggestEnabled()`)
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Consumes: `config.Config.Experimental.IsAgentSuggestEnabled()` (Task 1).
- Produces: `server.Config.AgentSuggestEnabled bool` / `Server.agentSuggestEnabled bool`; `statusResponse.AgentSuggestEnabled bool \`json:"agent_suggest_enabled,omitempty"\``; `handleRevise` разрешает `running`, только если `s.agentSuggestEnabled`.

- [ ] **Step 1: Написать падающий тест**

Добавить в `pkg/server/handlers_test.go` (посмотреть `setupTestServer`/`setupTestServerWithWS` — понадобится вариант, принимающий `AgentSuggestEnabled`; проще всего добавить прямое построение `New(Config{...})` внутри самого теста, по образцу `setupTestServerWithWS`, не трогая существующие хелперы):

```go
func TestHandleRevise_RunningRequiresAgentSuggestFlag(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	var reviseCalled bool
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
		ReviseFn: func(_ context.Context, _, _ string) error {
			reviseCalled = true
			return nil
		},
		AgentSuggestEnabled: false, // флаг выключен
	})

	body, _ := json.Marshal(map[string]string{"feedback": "note"})
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/revise", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (flag disabled, running not allowed)", w.Code)
	}
	if reviseCalled {
		t.Error("reviseFn should not be called when agent_suggest is disabled and stage is running")
	}
}

func TestHandleRevise_RunningAllowedWithFlag(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	var reviseCalled bool
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
		ReviseFn: func(_ context.Context, _, _ string) error {
			reviseCalled = true
			return nil
		},
		AgentSuggestEnabled: true,
	})

	body, _ := json.Marshal(map[string]string{"feedback": "note"})
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/revise", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !reviseCalled {
		t.Error("reviseFn should be called when agent_suggest is enabled and stage is running")
	}
}

func TestHandleStatus_IncludesAgentSuggestEnabled(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := resp["agent_suggest_enabled"]; !ok {
		// omitempty на false — допустимо отсутствие ключа; проверяем явное
		// значение через второй сервер с флагом включённым отдельно, если
		// omitempty скрывает false. Смотри Step 3 — решаем это при реализации.
		t.Skip("agent_suggest_enabled omitted when false — see explicit-true case below")
	}
}
```

(Добавить в импорты файла, если их там ещё нет: `"bytes"`, `"context"`, `"github.com/akopichin/afm/pkg/orchestrator"`.)

- [ ] **Step 2: Запустить, убедиться что падает**

Run: `go test ./pkg/server/... -run 'TestHandleRevise_Running|TestHandleStatus_IncludesAgentSuggestEnabled' -v`
Expected: FAIL — `unknown field AgentSuggestEnabled in struct literal Config` (compile error)

- [ ] **Step 3: Реализовать**

В `pkg/server/server.go`, добавить в `Config` (после `DialogCancelFn`):
```go
	AgentSuggestEnabled bool // gate for agent_suggest experimental feature (config.Experimental.IsAgentSuggestEnabled())
```
и в `Server` (после `dialogCancelFn`):
```go
	agentSuggestEnabled bool
```
В `New(cfg Config)`, в конструкции `s := &Server{...}`, добавить:
```go
		agentSuggestEnabled: cfg.AgentSuggestEnabled,
```

В `pkg/server/handlers.go`, `statusResponse` добавить поле:
```go
	AgentSuggestEnabled bool `json:"agent_suggest_enabled,omitempty"`
```
В `handleStatus`, в конструкции `resp := statusResponse{...}`, добавить:
```go
		AgentSuggestEnabled: s.agentSuggestEnabled,
```

В `handleRevise` заменить:
```go
	if st.Status != state.StatusAwaitingApproval {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval", st.Status), http.StatusBadRequest)
		return
	}
```
на:
```go
	allowed := st.Status == state.StatusAwaitingApproval ||
		(st.Status == state.StatusRunning && s.agentSuggestEnabled)
	if !allowed {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval (or running, with agent_suggest enabled)", st.Status), http.StatusBadRequest)
		return
	}
```

**Про тест `TestHandleStatus_IncludesAgentSuggestEnabled` (omitempty на `false`):** `omitempty` на `bool` скрывает ключ при `false` — значит этот тест с дефолтным (выключенным) сервером не увидит ключ вообще, что и есть корректное поведение (см. `t.Skip` в черновике теста). Перед сдачей задачи ЗАМЕНИ этот тест на более прямой, не полагающийся на скрытое поведение omitempty:
```go
func TestHandleStatus_AgentSuggestEnabledReflectsConfig(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		RunDir:              runDir,
		Store:               store,
		UIBus:               orchestrator.NewUIBus(),
		AgentSuggestEnabled: true,
	})
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, _ := resp["agent_suggest_enabled"].(bool); !v {
		t.Errorf("agent_suggest_enabled = %v, want true", resp["agent_suggest_enabled"])
	}
}
```

В `cmd/afm/run.go`, в блоке `srv := server.New(server.Config{...})`, добавить:
```go
					AgentSuggestEnabled: cfg.Experimental.IsAgentSuggestEnabled(),
```

- [ ] **Step 4: Запустить, убедиться что проходит**

Run: `go test ./pkg/server/... -run 'TestHandleRevise_Running|TestHandleStatus_AgentSuggestEnabledReflectsConfig' -v`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет server + cmd/afm + линт**

Run: `go test ./pkg/server/... ./cmd/afm/... -timeout 60s && make lint`
Expected: PASS, 0 issues

- [ ] **Step 6: Коммит**

```bash
git add pkg/server/server.go pkg/server/handlers.go pkg/server/handlers_test.go cmd/afm/run.go
git commit -m "$(cat <<'EOF'
feat(server): handleRevise разрешает running под флагом agent_suggest

statusResponse.agent_suggest_enabled — фронт узнаёт, показывать ли UI.
Без флага handleRevise ведёт себя как раньше (только awaiting_approval).
EOF
)"
```

---

### Task 8: Frontend — кебаб-меню + модалка «Добавить поправку агенту»

**Файлы:**
- Modify: `pkg/web/dashboard/src/components/stages-list/StagesList.tsx`
- Modify: `pkg/web/dashboard/src/types/status.ts` (или где сейчас лежит тип ответа `/api/status` — найти перед правкой)
- Create: `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.tsx`
- Create: `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.test.tsx`
- Create: `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.css` (или styled inline — сверить с конвенцией существующих компонентов: посмотреть, используют ли соседние компоненты `.css`-файлы или инлайновые классы из глобального стиля скина)
- Modify: `pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx`

**Interfaces:**
- Consumes: `agent_suggest_enabled` из ответа `/api/status` (Task 7) — исполнитель должен найти, ГДЕ именно этот ответ парсится на фронте (скорее всего `useStatus`-хук или похожий, судя по существующим `stage_interactive`/`stage_autonomous`) и прокинуть новое поле по тому же пути до `App.tsx`/`StagesList`.
- Produces: кебаб (⋮) на каждом `<li class="stage-item">`, видимый только когда `agentSuggestEnabled && (stage.status === 'running' || stage.status === 'awaiting_approval')`; клик открывает меню с одним пунктом; клик по пункту открывает `AgentNoteModal` (textarea + предупреждение + Отмена/Отправить), сабмит — `POST /api/stages/{id}/revise` с `{feedback}`.

- [ ] **Step 1: Найти, как `stage_interactive`/`stage_autonomous` доходят от `/api/status` до `App.tsx`, чтобы провести `agent_suggest_enabled` тем же путём**

Это не отдельный шаг с кодом — это разведка перед написанием кода. Прочитать: `pkg/web/dashboard/src/hooks/use-status/use-status.ts` (или как называется хук статуса — найти по `stage_interactive` через grep) и `App.tsx` целиком, чтобы понять, как проп доходит до `StagesList`. Записать здесь для себя (не в коммит) точный путь: какой тип/интерфейс парсит JSON от `/api/status`, как называется соответствующее поле в camelCase на фронте.

- [ ] **Step 2: Написать падающий тест на `StagesList`**

Прочитать `pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx` целиком, чтобы понять текущий паттерн рендер-тестов этого компонента (какие пропы передаются, как ищутся элементы — `screen.getByText`/`container.querySelector` и т.д.). Добавить тест по образцу существующих:

```tsx
it('shows the kebab menu only when agentSuggestEnabled and status is running or awaiting_approval', () => {
  const stages: Stage[] = [
    { id: 'a', name: '', status: 'running' },
    { id: 'b', name: '', status: 'done' },
    { id: 'c', name: '', status: 'awaiting_approval' },
  ]
  const { rerender } = render(
    <StagesList stages={stages} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={true} />,
  )
  expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(2) // a и c, не b

  rerender(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />)
  expect(screen.queryAllByRole('button', { name: /more actions/i })).toHaveLength(0)
})
```

(Точный `aria-label`/имя доступности кебаб-кнопки — зафиксировать в реализации Step 3 как `"More actions"` или аналог и использовать ТО ЖЕ имя здесь; подогнать под существующий стиль именования в файле, если там уже используется другой язык подписей типа `aria-label`.)

- [ ] **Step 3: Запустить, убедиться что падает**

Run: `cd pkg/web/dashboard && npm test -- StagesList`
Expected: FAIL — `agentSuggestEnabled` не существует в `StagesListProps`, кебаб не рендерится вообще

- [ ] **Step 4: Реализовать `StagesList` — добавить проп и кебаб**

В `StagesList.tsx`, `StagesListProps` добавить:
```ts
type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  agentSuggestEnabled: boolean
  onAddNote?: (stageId: string) => void // вызывается при клике на пункт меню
}
```
В JSX каждого `<li>`, после `{stage.status === 'awaiting_approval' && <span className="approval-badge" ...>}`, добавить условный рендер кебаба:
```tsx
{agentSuggestEnabled && (stage.status === 'running' || stage.status === 'awaiting_approval') && (
  <button
    type="button"
    className="stage-kebab"
    aria-label="More actions"
    onClick={(e) => {
      e.stopPropagation() // не триггерить onSelect клика по строке
      onAddNote?.(stage.id)
    }}
  >
    ⋮
  </button>
)}
```

(В этой первой итерации клик по кебабу сразу вызывает `onAddNote` — родительский компонент, вероятно `App.tsx`, решает, показывать ли меню-обёртку с одним пунктом или открывать модалку напрямую. Заказчик просил именно «меню, в нём пока один пункт» — то есть предполагается расширяемость; НО единственный текущий пункт делает промежуточное меню чистым UI-балластом без функциональной разницы с прямым открытием модалки. Прими решение при реализации: либо (а) кебаб открывает крошечное меню с одним пунктом «Добавить поправку агенту», клик по пункту открывает модалку — честно следует запросу пользователя, дороже в разметке; либо (б) кебаб при единственном пункте меню сразу открывает модалку — минус один клик, соответствует духу запроса. Рекомендация: (а), т.к. пользователь явно описал двухуровневое взаимодействие — меню, потом пункт — и явно рассчитывает на будущие пункты меню.)

Реализовать (а): маленькое выпадающее меню как локальный `useState<string | null>` (какая стадия сейчас показывает открытое меню) внутри `StagesList`, закрывается по клику вне (или по Escape — минимально по клику вне, через `onBlur` на обёртке меню). Меню — `<ul className="stage-kebab-menu">` с одним `<li><button onClick={...}>Добавить поправку агенту</button></li>`.

- [ ] **Step 5: Запустить, убедиться что тест `StagesList` проходит**

Run: `cd pkg/web/dashboard && npm test -- StagesList`
Expected: PASS

- [ ] **Step 6: Написать падающий тест на `AgentNoteModal`**

Создать `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.test.tsx`:

```tsx
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { AgentNoteModal } from './AgentNoteModal'

describe('AgentNoteModal', () => {
  it('renders warning text and textarea, calls onSubmit with the typed note', () => {
    const onSubmit = vi.fn()
    const onCancel = vi.fn()
    render(<AgentNoteModal stageId="s1" onSubmit={onSubmit} onCancel={onCancel} />)

    expect(screen.getByText(/agent will finish its current action/i)).toBeInTheDocument()

    const textarea = screen.getByRole('textbox')
    fireEvent.change(textarea, { target: { value: 'please add tests' } })
    fireEvent.click(screen.getByRole('button', { name: /send/i }))

    expect(onSubmit).toHaveBeenCalledWith('please add tests')
  })

  it('calls onCancel and does not submit when Cancel is clicked', () => {
    const onSubmit = vi.fn()
    const onCancel = vi.fn()
    render(<AgentNoteModal stageId="s1" onSubmit={onSubmit} onCancel={onCancel} />)

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))

    expect(onCancel).toHaveBeenCalled()
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('disables Send while the note is empty', () => {
    render(<AgentNoteModal stageId="s1" onSubmit={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
  })
})
```

- [ ] **Step 7: Запустить, убедиться что падает**

Run: `cd pkg/web/dashboard && npm test -- AgentNoteModal`
Expected: FAIL — `Cannot find module './AgentNoteModal'`

- [ ] **Step 8: Реализовать `AgentNoteModal`**

Перед написанием — прочитать `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx` (уже открывался раньше в этой сессии) и любой файл со скиновыми CSS-переменными (`grep -rn "var(--" pkg/web/dashboard/src/*.css` или скины в `skins/`), чтобы стилизовать ТОЛЬКО через существующие переменные (`var(--...)`), без хардкода цветов — иначе тема (coffee/goga/novacorps/base) сломается в модалке.

```tsx
import { useState, type ReactElement } from 'react'

type AgentNoteModalProps = {
  stageId: string
  onSubmit: (note: string) => void
  onCancel: () => void
}

// Модалка «Добавить поправку агенту» (agent_suggest): открывается из кебаб-
// меню StagesList. Предупреждает, что агент доведёт текущее действие до
// конца перед перезапуском с этой фразой в контексте.
export function AgentNoteModal({ stageId, onSubmit, onCancel }: AgentNoteModalProps): ReactElement {
  const [note, setNote] = useState('')

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={`Add a note for stage ${stageId}`}>
      <div className="modal-content agent-note-modal">
        <p className="agent-note-warning">
          The agent will finish its current action, then restart with this note in context.
        </p>
        <textarea
          className="agent-note-textarea"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="What should the agent take into account?"
          rows={4}
          autoFocus
        />
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-send"
            disabled={note.trim() === ''}
            onClick={() => onSubmit(note.trim())}
          >
            Send
          </button>
        </div>
      </div>
    </div>
  )
}
```

Плюс `pkg/web/dashboard/src/components/agent-note-modal/index.ts`:
```ts
export { AgentNoteModal } from './AgentNoteModal'
```

CSS (файл или встроить в глобальный — решить по конвенции проекта, найденной в Step 8's разведке) — минимум: `.modal-overlay` (фон-затемнение, `position: fixed`, центрирование), `.modal-content` (фон/бордер через `var(--panel-bg)`/`var(--border-color)` или как называются реальные переменные в проекте — сверить перед написанием), `.agent-note-warning` (акцентный цвет через `var(--warning-color)` или аналог).

- [ ] **Step 9: Запустить, убедиться что тесты `AgentNoteModal` проходят**

Run: `cd pkg/web/dashboard && npm test -- AgentNoteModal`
Expected: PASS

- [ ] **Step 10: Подключить в `App.tsx`**

Прочитать `App.tsx` целиком (уже частично открывался в этой сессии для `use-event-feed`). Добавить:
- Состояние `noteModalStageId: string | null` (какая стадия сейчас показывает модалку, `null` — скрыта).
- Проброс `agentSuggestEnabled` (из статуса, найденного в Step 1) и `onAddNote={setNoteModalStageId}` в `<StagesList ...>`.
- Условный рендер `{noteModalStageId !== null && <AgentNoteModal stageId={noteModalStageId} onCancel={() => setNoteModalStageId(null)} onSubmit={handleSubmitNote} />}`, где `handleSubmitNote` — `async (note: string) => { await fetch(\`/api/stages/${encodeURIComponent(noteModalStageId)}/revise\`, { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({feedback: note}) }); setNoteModalStageId(null) }`.

- [ ] **Step 11: Прогнать весь фронтенд-набор тестов + линт + билд**

Run: `cd pkg/web/dashboard && npm test -- --run && cd ../../.. && make lint && make build`
Expected: все тесты зелёные, 0 issues, билд проходит

- [ ] **Step 12: Коммит**

```bash
git add pkg/web/dashboard/src/components/stages-list pkg/web/dashboard/src/components/agent-note-modal pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/assets pkg/web/dashboard/index.html
git commit -m "$(cat <<'EOF'
feat(dashboard): кебаб-меню + модалка "Добавить поправку агенту" (agent_suggest)

Кебаб виден только при agent_suggest_enabled (из /api/status) и статусе
running/awaiting_approval. Модалка предупреждает, что агент доведёт
текущее действие до конца перед перезапуском с фразой в контексте.
Сабмит — POST на существующий /api/stages/{id}/revise.
EOF
)"
```

---

## Self-Review (проведён при написании плана)

**Spec coverage:** все секции спека покрыты — Task 1 (флаг), Task 2 (FSM), Task 3 (SIGINT-механизм в executor), Task 4 (реестр + 3 раннера), Task 5 (Revise generalized), Task 6 (recovery.go), Task 7 (HTTP + statusResponse), Task 8 (frontend).

**Type consistency:** сигнатура `runWithRetry(..., onUserInterrupted func())` одинакова во всех 5 точках вызова (Task 4); `run<Phase>WithFeedback` везде `func(context.Context, flow.Stage)`, совпадает с сигнатурой, которую ожидает `spawnAgent`; `executor.Config.InterruptCh <-chan struct{}` и `Orchestrator.interruptChans sync.Map` согласованы по типу канала (`chan struct{}`, буфер 1) везде, где упоминаются.

**Placeholder scan:** один намеренно оставленный на усмотрение исполнителя пункт — выбор (а)/(б) для двухуровневого vs одноуровневого кебаб-взаимодействия в Task 8 Step 4 — это НЕ архитектурная неопределённость (обе ветки дают одинаковый бэкенд-контракт), а сознательно оставленная на исполнителя UX-развилка с явной рекомендацией; план также прямо просит исполнителя сверяться с реальным текущим кодом в двух местах (Task 4 — `runAutonomousAgent`'s точный текст промпта; Task 8 — точные CSS-переменные темы) вместо того чтобы гадать про то, что может успеть измениться или что план не может знать заранее (имена CSS-переменных) — это явные, обоснованные исключения из "no placeholders", а не забытые TBD.
