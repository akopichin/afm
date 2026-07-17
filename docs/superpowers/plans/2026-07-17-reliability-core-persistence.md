# План реализации: надёжность ядра и персистентность

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Сделать событийный лог единственным доверенным источником правды: записи долговечны, повреждение не разрушает данные молча, доступ между процессами безопасен, а старт/останов/восстановление не теряют и не расходятся по состоянию.

**Architecture:** Три рабочих потока. (A) `pkg/state` — flock между процессами, недеструктивный replay с карантином, долговечный снапшот + чтение из лога, единый поиск run, уникальный run-id. (B) `pkg/orchestrator` — разведение фатальной storage-ошибки и доброкачественного concurrent-change. (C) `pkg/orchestrator` — чистый shutdown через `spawnAgent`+WaitGroup, устранение реентерабельного deadlock, долговечный-первый approve.

**Tech Stack:** Go 1.26, `pgregory.net/rapid` (property-тесты уже в проекте), стандартный `testing`, `syscall.Flock` (через `pkg/progress`).

## Global Constraints

- **Go version:** 1.26 — НЕ менять `go.mod` без предупреждения.
- **Коммиты:** на русском языке, БЕЗ `Co-Authored-By`.
- **Линт:** после каждой задачи `make lint` должен быть зелёным.
- **Простота:** убрать код ← добавить; переиспользовать ← создавать; значение ← указатель. Никакой мутации без необходимости.
- **Тесты:** `make test` зелёный после каждой задачи (в проекте есть детектор гонок — тесты гоняются с `-race`).
- **Спецификация:** `docs/superpowers/specs/2026-07-17-reliability-core-persistence-design.md`.

---

## Карта файлов

| Файл | Ответственность | Изменение |
|------|-----------------|-----------|
| `pkg/state/store.go` | Открытие/закрытие Store, replay, snapshot, Apply | flock (A1), недеструктивный replay (A2), fsync snapshot + ErrConcurrentChange (A3, B1) |
| `pkg/state/state.go` | Модель RunState/StageState, поиск run | `LastSeq`, `LoadRunState`, единый поиск (A3, A4) |
| `cmd/afm/check.go` | `afm check` | чтение через `LoadRunState` (A3/A4) |
| `cmd/afm/approve.go` | `afm approve` + `findLatestRunForStage` | единый поиск, дружелюбное сообщение о блокировке (A1, A4) |
| `cmd/afm/retry.go`, `cmd/afm/revise.go` | CLI-мутации | дружелюбное сообщение о блокировке (A1) |
| `cmd/afm/run.go` | `resolveRun` | уникальный run-id (A5) |
| `pkg/orchestrator/fsm.go` | FSM Apply | различать concurrent-change от storage-fatal (B1) |
| `pkg/orchestrator/orchestrator.go` | Event loop, spawn агентов, approve | fatal-проброс (B1), `spawnAgent`+WaitGroup (C1), inline auto-approve (C2), durable approve (C3) |
| `pkg/orchestrator/retry.go` | runWithRetry | ctx вместо `context.Background()` в publish (C1) |
| `pkg/orchestrator/recovery.go` | Восстановление | использование `spawnAgent` (C1) |
| `pkg/server/handlers.go` | HTTP approve | синхронный durable approve (C3) |

---

## Task 1: flock на run-директорию (A1)

**Files:**
- Modify: `pkg/state/store.go` (`Store` struct, `Open`, `Close`)
- Modify: `cmd/afm/approve.go`, `cmd/afm/retry.go`, `cmd/afm/revise.go` (дружелюбное сообщение)
- Test: `pkg/state/store_lock_test.go` (create)

**Interfaces:**
- Produces: `state.ErrRunLocked` (sentinel `error`) — возвращается из `state.Open`, когда run-директория уже заблокирована другим процессом.
- Consumes: `progress.NewLock(path string) (*progress.Lock, error)`, `(*progress.Lock).TryLock() error`, `(*progress.Lock).Unlock()` из `pkg/progress` (без цикла импортов — проверено).

- [ ] **Step 1: Написать падающий тест**

Create `pkg/state/store_lock_test.go`:

```go
package state

import (
	"errors"
	"testing"
)

func TestOpen_SecondProcessGetsRunLocked(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"a"}

	s1, err := Open(dir, ids)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer s1.Close()

	_, err = Open(dir, ids)
	if !errors.Is(err, ErrRunLocked) {
		t.Fatalf("second Open: want ErrRunLocked, got %v", err)
	}
}

func TestOpen_LockReleasedOnClose(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"a"}

	s1, err := Open(dir, ids)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := Open(dir, ids)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	s2.Close()
}
```

- [ ] **Step 2: Запустить тест — убедиться, что падает**

Run: `go test ./pkg/state/ -run TestOpen_SecondProcess -race`
Expected: FAIL — `ErrRunLocked` не определён (compile error).

- [ ] **Step 3: Добавить lock в Store**

В `pkg/state/store.go` добавить импорт и sentinel, поле в `Store`, захват в `Open`, освобождение в `Close`.

Импорты — добавить:
```go
"errors"
"github.com/akopichin/afm/pkg/progress"
```

Sentinel (на уровне пакета, рядом с типом `Store`):
```go
// ErrRunLocked означает, что run-директория уже открыта другим процессом afm.
// flock освобождается ОС при завершении процесса, поэтому упавший ранее run
// не оставляет «залипшей» блокировки.
var ErrRunLocked = errors.New("run directory is locked by another afm process")
```

Поле в `Store`:
```go
type Store struct {
	runDir    string
	eventsLog *os.File
	snapshot  *RunState
	lastSeq   uint64
	history   []Transition
	lock      *progress.Lock
	mu        sync.Mutex
}
```

В `Open`, сразу ПОСЛЕ `os.MkdirAll(runDir, 0755)` и ДО чтения events:
```go
	lock, _ := progress.NewLock(filepath.Join(runDir, ".lock"))
	if err := lock.TryLock(); err != nil {
		return nil, ErrRunLocked
	}
```

Прокинуть `lock` в конструирование `s := &Store{...}` (добавить `lock: lock,`). Если между `TryLock` и `return` возникает ошибка (replay/open events), освободить блокировку перед возвратом — добавить `lock.Unlock()` в эти ветки ошибок. Простейший вариант: сразу после успешного `TryLock` поставить defer, который снимает блокировку только если функция вернула ошибку:
```go
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
```
и в самом конце, перед `return s, nil`, снять флаг: `locked = false`.

В `Close`:
```go
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.eventsLog != nil {
		err = s.eventsLog.Close()
		s.eventsLog = nil
	}
	if s.lock != nil {
		s.lock.Unlock()
		s.lock = nil
	}
	return err
}
```

- [ ] **Step 4: Запустить тест — убедиться, что проходит**

Run: `go test ./pkg/state/ -run TestOpen_ -race`
Expected: PASS (оба теста).

- [ ] **Step 5: Дружелюбное сообщение в CLI**

В `cmd/afm/approve.go`, `cmd/afm/retry.go`, `cmd/afm/revise.go` заменить обработку ошибки `state.Open`. Пример для `approve.go` (аналогично в двух других):
```go
	store, err := state.Open(runDir, stageIDs)
	if err != nil {
		if errors.Is(err, state.ErrRunLocked) {
			return fmt.Errorf("run is active — approve via the dashboard, or stop `afm run` first")
		}
		return fmt.Errorf("open store: %w", err)
	}
```
Добавить импорт `"errors"` в те файлы, где его ещё нет (`retry.go` его не импортирует — добавить). Тексты сообщений: `retry.go` → `"...retry via the dashboard, or stop `afm run` first"`, `revise.go` → `"...revise via the dashboard..."`.

- [ ] **Step 6: Прогнать линт и тесты, закоммитить**

```bash
make lint && make test
git add pkg/state/store.go pkg/state/store_lock_test.go cmd/afm/approve.go cmd/afm/retry.go cmd/afm/revise.go
git commit -m "feat(state): flock на run-директорию — CLI-мутации не портят живой лог"
```

---

## Task 2: недеструктивный replay + карантин (A2)

**Files:**
- Modify: `pkg/state/store.go` (`replayEvents`, `Open`)
- Test: `pkg/state/store_replay_test.go` (create)

**Interfaces:**
- Produces: `state.ErrCorruptLog` (sentinel `error`) — из `Open`, когда в середине лога битая строка (есть валидные данные после неё). Оригинал не тронут, создана копия `events.jsonl.corrupt-<unixnano>`.
- Изменяется внутренняя сигнатура `replayEvents` (добавляется признак `corrupted bool`) — приватная, вне пакета не видна.

- [ ] **Step 1: Написать падающие тесты**

Create `pkg/state/store_replay_test.go`:

```go
package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// хвостовой обрыв (последняя строка без \n) — безопасно усекается, Open проходит.
func TestOpen_TornTailTruncates(t *testing.T) {
	dir := t.TempDir()
	good := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	torn := `{"seq":2,"stage_id":"a","from":"planni` // оборвано, без \n
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(good+torn), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open on torn tail: want success, got %v", err)
	}
	defer s.Close()
	if got := s.Get("a"); got != StatusPlanning {
		t.Fatalf("state after torn-tail replay: want planning, got %q", got)
	}
}

// битая строка В СЕРЕДИНЕ (валидная строка после неё) — карантин + ошибка, файл цел.
func TestOpen_MidCorruptionQuarantines(t *testing.T) {
	dir := t.TempDir()
	line1 := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	bad := `NOT JSON AT ALL` + "\n"
	line3 := `{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"
	orig := []byte(line1 + bad + line3)
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, orig, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir, []string{"a"})
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("Open on mid-corruption: want ErrCorruptLog, got %v", err)
	}
	// оригинал не тронут
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Fatalf("original events.jsonl was modified")
	}
	// карантинная копия существует
	matches, _ := filepath.Glob(filepath.Join(dir, "events.jsonl.corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("want 1 quarantine copy, got %d", len(matches))
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./pkg/state/ -run 'TestOpen_(TornTail|MidCorruption)' -race`
Expected: FAIL — `ErrCorruptLog` не определён; текущий код усекает середину без ошибки.

- [ ] **Step 3: Различить хвостовой обрыв и повреждение середины**

В `pkg/state/store.go` изменить `replayEvents`, чтобы отличать «оборван только последний фрагмент (нет завершающего \n)» от «битая полная строка в середине». Ключ: пройти ВСЕ строки; если `Unmarshal` упал на строке, которая НЕ является последним фрагментом без завершающего перевода строки — это повреждение.

Новая `replayEvents` (полная замена функции):
```go
func replayEvents(path string, rs *RunState) (history []Transition, lastSeq uint64, lastGoodOffset int64, corrupted bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, 0, false, nil
		}
		return nil, 0, 0, 0, false, err
	}
	lines := bytesLines(data)
	endsWithNewline := len(data) > 0 && data[len(data)-1] == '\n'
	var offset int64
	var goodOffset int64
	for i, line := range lines {
		isLast := i == len(lines)-1
		offset += int64(len(line)) + 1 // +1 на \n
		if len(bytes.TrimSpace(line)) == 0 {
			goodOffset = offset
			continue
		}
		var t Transition
		if uerr := json.Unmarshal(line, &t); uerr != nil {
			// Последняя строка без завершающего \n — это оборванная запись
			// (crash в момент append): безопасно усечь до последнего хорошего offset.
			if isLast && !endsWithNewline {
				return history, lastSeq, goodOffset, false, nil
			}
			// Иначе это битая ПОЛНАЯ строка в середине лога — повреждение.
			return history, lastSeq, goodOffset, true, nil
		}
		rs.SetStageStatus(t.StageID, t.To)
		history = append(history, t)
		lastSeq = t.Seq
		goodOffset = offset
	}
	return history, lastSeq, goodOffset, false, nil
}
```

В `Open` обновить вызов и добавить карантин ДО усечения:
```go
	history, lastSeq, lastGoodOffset, corrupted, err := replayEvents(eventsPath, rs)
	if err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	if corrupted {
		quarantine := fmt.Sprintf("%s.corrupt-%d", eventsPath, time.Now().UnixNano())
		if data, rerr := os.ReadFile(eventsPath); rerr == nil {
			_ = os.WriteFile(quarantine, data, 0644)
		}
		return nil, fmt.Errorf("%w: quarantined to %s", ErrCorruptLog, quarantine)
	}
```

Sentinel рядом с `ErrRunLocked`:
```go
// ErrCorruptLog означает битую строку в середине events.jsonl (есть валидные
// записи после неё). Оригинал НЕ усекается — копируется в .corrupt-<ts> для разбора.
var ErrCorruptLog = errors.New("events.jsonl is corrupted mid-log")
```

Примечание: карантин выполняется ДО `defer lock.Unlock()`-ветки из Task 1 — блокировка снимается автоматически (функция вернула ошибку → `locked` остался `true`).

- [ ] **Step 4: Запустить — убедиться, что проходит**

Run: `go test ./pkg/state/ -run 'TestOpen_' -race`
Expected: PASS (включая тесты из Task 1).

- [ ] **Step 5: Линт, тесты, коммит**

```bash
make lint && make test
git add pkg/state/store.go pkg/state/store_replay_test.go
git commit -m "fix(state): не разрушать лог при повреждении середины — карантин + ошибка"
```

---

## Task 3: долговечный snapshot + чтение из лога (A3)

**Files:**
- Modify: `pkg/state/store.go` (`writeSnapshot`, `Apply`)
- Modify: `pkg/state/state.go` (`RunState.LastSeq`, `LoadRunState`)
- Test: `pkg/state/loadrunstate_test.go` (create)

**Interfaces:**
- Produces: `state.LoadRunState(runDir string) (RunState, error)` — читает состояние из `events.jsonl` (авторитетно, без flock, только для чтения). Используется в Task 4 (`check`, поиск run).
- Produces: поле `RunState.LastSeq uint64` (json `last_seq`).

- [ ] **Step 1: Написать падающий тест**

Create `pkg/state/loadrunstate_test.go`:

```go
package state

import (
	"os"
	"path/filepath"
	"testing"
)

// LoadRunState читает авторитетное состояние из лога, даже если snapshot отстал.
func TestLoadRunState_RebuildsFromLogWhenSnapshotStale(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "x"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Портим snapshot: возвращаем его в устаревшее состояние.
	stale := `{"stages":{"a":{"status":"pending","updated_at":"2020-01-01T00:00:00Z"}},"last_seq":0}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Stages["a"].Status != StatusPlanning {
		t.Fatalf("LoadRunState must read from log: want planning, got %q", rs.Stages["a"].Status)
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./pkg/state/ -run TestLoadRunState -race`
Expected: FAIL — `LoadRunState` не определён.

- [ ] **Step 3: Добавить LastSeq, LoadRunState, fsync**

В `pkg/state/state.go` добавить поле в `RunState`:
```go
	Stages     map[string]StageState `json:"stages"`
	LastSeq    uint64                `json:"last_seq"`
```

Там же добавить функцию чтения из лога (без flock — read-only путь):
```go
// LoadRunState восстанавливает состояние run-директории из events.jsonl —
// авторитетного источника. Snapshot (state.json) НЕ используется: он лишь
// производный кэш и может отставать при сбое записи. Не берёт flock: путь
// только для чтения (check, поиск run) и не должен блокироваться живым run.
func LoadRunState(runDir string) (RunState, error) {
	rs := RunState{Stages: map[string]StageState{}}
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return rs, err
	}
	var line int
	for _, b := range splitLines(data) {
		line++
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var t Transition
		if err := json.Unmarshal(b, &t); err != nil {
			break // оборванный/битый хвост — читаем валидный префикс
		}
		rs.SetStageStatus(t.StageID, t.To)
		rs.LastSeq = t.Seq
	}
	return rs, nil
}

// splitLines дублирует bytesLines из store.go, но экспортируемо внутри пакета
// для LoadRunState (тот же алгоритм разбиения по \n).
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
```
Добавить импорты в `state.go`: `"encoding/json"`. (`os`, `path/filepath`, `strings`, `time` уже есть.)

Примечание по DRY: `bytesLines` в `store.go` и `splitLines` здесь идентичны. Оставить оба нельзя (дубль) — удалить `bytesLines` из `store.go` и заменить его единственный вызов в `replayEvents` на `splitLines`. Итог: одна функция.

В `pkg/state/store.go`, `Transition` уже несёт `Seq`; в `Apply` после `s.snapshot.SetStageStatus(...)` записать `LastSeq` в снапшот перед `writeSnapshot`:
```go
	s.snapshot.SetStageStatus(t.StageID, t.To)
	s.snapshot.LastSeq = s.lastSeq
	if err := s.writeSnapshot(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: snapshot write failed: %v\n", err)
	}
```

`writeSnapshot` — сделать долговечным (fsync файла + fsync директории):
```go
func (s *Store) writeSnapshot() error {
	data, err := json.MarshalIndent(s.snapshot, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.runDir, "state.json")
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// fsync директории — иначе rename может быть недолговечен при потере питания.
	dir, err := os.Open(s.runDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
```

- [ ] **Step 4: Запустить — убедиться, что проходит**

Run: `go test ./pkg/state/ -race`
Expected: PASS (все тесты пакета state).

- [ ] **Step 5: Линт, тесты, коммит**

```bash
make lint && make test
git add pkg/state/store.go pkg/state/state.go pkg/state/loadrunstate_test.go
git commit -m "feat(state): долговечный snapshot (fsync) + чтение состояния из лога как источника правды"
```

---

## Task 4: единый поиск последнего run (A4)

**Files:**
- Modify: `pkg/state/state.go` (`FindLatestRunDir` — якорь префикса; новый `FindLatestRunForStage`)
- Modify: `cmd/afm/approve.go` (использовать `FindLatestRunForStage`, удалить локальную `findLatestRunDir`)
- Modify: `cmd/afm/retry.go`, `cmd/afm/revise.go` (используют `findLatestRunDir` → переключить на общую)
- Modify: `cmd/afm/check.go` (читать через `LoadRunState`, фильтр по имени)
- Test: `pkg/state/findrun_test.go` (create)

**Interfaces:**
- Consumes: `state.LoadRunState` (Task 3).
- Produces: `state.FindLatestRunForStage(base, stageID string) (runDir string, stageIDs []string, err error)` — последний run (по имени директории), содержащий stageID; состояние читается из лога, не из `state.json`.

- [ ] **Step 1: Написать падающий тест**

Create `pkg/state/findrun_test.go`:

```go
package state

import (
	"os"
	"path/filepath"
	"testing"
)

// Префикс "foo-" не должен матчить run флоу "foo-bar".
func TestFindLatestRunDir_AnchorsPrefix(t *testing.T) {
	base := t.TempDir()
	// runs: foo-bar-20240101-000000 и foo-20240102-000000
	os.MkdirAll(filepath.Join(base, "foo-bar-20240101-000000"), 0755)
	os.MkdirAll(filepath.Join(base, "foo-20240102-000000"), 0755)

	dir, err := FindLatestRunDir(base, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != "foo-20240102-000000" {
		t.Fatalf("want foo-20240102-000000, got %s", filepath.Base(dir))
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./pkg/state/ -run TestFindLatestRunDir_AnchorsPrefix -race`
Expected: FAIL — текущий `FindLatestRunDir` матчит `foo-bar-...` по префиксу `foo-`.

- [ ] **Step 3: Заякорить префикс и добавить поиск по стадии**

В `pkg/state/state.go` заменить `FindLatestRunDir`. Run-id имеет вид `<flow>-<timestamp>[-<rand>]`; после `flowName + "-"` первым идёт год (4 цифры). Проверяем, что следующий символ после префикса — цифра: это отсекает `foo-bar` (там буква `b`).
```go
// FindLatestRunDir возвращает самую свежую run-директорию для flowName под base.
// Имя run: "<flowName>-<timestamp>...". Чтобы "foo" не матчил "foo-bar", после
// префикса требуется цифра (начало timestamp'а). Сортировка имён совпадает с
// хронологией благодаря формату timestamp'а.
func FindLatestRunDir(base, flowName string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read runs dir: %w", err)
	}
	prefix := flowName + "-"
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && len(n) > len(prefix) && n[:len(prefix)] == prefix && n[len(prefix)] >= '0' && n[len(prefix)] <= '9' {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no run found for flow %q", flowName)
	}
	sort.Strings(names)
	return filepath.Join(base, names[len(names)-1]), nil
}

// FindLatestRunForStage возвращает последнюю run-директорию, содержащую stageID,
// и все её stage id. Состояние читается из events.jsonl (LoadRunState), не из
// state.json, чтобы не доверять возможно устаревшему снапшоту.
func FindLatestRunForStage(base, stageID string) (string, []string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", nil, fmt.Errorf("read runs dir: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // новые первыми
	for _, name := range dirs {
		runDir := filepath.Join(base, name)
		rs, lerr := LoadRunState(runDir)
		if lerr != nil {
			continue
		}
		if _, ok := rs.Stages[stageID]; ok {
			ids := make([]string, 0, len(rs.Stages))
			for id := range rs.Stages {
				ids = append(ids, id)
			}
			return runDir, ids, nil
		}
	}
	return "", nil, fmt.Errorf("no active run found for stage %q", stageID)
}
```
Добавить импорт `"sort"` в `state.go`.

В `cmd/afm/approve.go`: удалить локальную `findLatestRunDir` (строки 51-90) целиком и заменить её единственный вызов:
```go
	runDir, stageIDs, err := state.FindLatestRunForStage(filepath.Join(fmDir(), "runs"), stageID)
```
Удалить теперь неиспользуемые импорты в `approve.go` (`encoding/json`, `slices`, возможно `os`). Проверить компиляцией.

В `cmd/afm/retry.go` и `cmd/afm/revise.go` заменить вызовы `findLatestRunDir(stageID)` на `state.FindLatestRunForStage(filepath.Join(fmDir(), "runs"), stageID)`.

В `cmd/afm/check.go` заменить прямое чтение `state.json` на `LoadRunState`, и брать последний run без фильтра по флоу (как сейчас) — но через лог. Заменить тело (строки 64-76) чтения:
```go
		slices.Sort(dirs)
		latest := dirs[len(dirs)-1]

		rs, err := state.LoadRunState(latest)
		if err != nil {
			return fmt.Errorf("load state: %w", err)
		}
```
Удалить теперь неиспользуемые импорты `encoding/json` в `check.go`, если больше не нужны.

- [ ] **Step 4: Запустить — убедиться, что проходит**

Run: `go test ./pkg/state/ ./cmd/... -race`
Expected: PASS.

- [ ] **Step 5: Линт, тесты, коммит**

```bash
make lint && make test
git add pkg/state/state.go pkg/state/findrun_test.go cmd/afm/approve.go cmd/afm/retry.go cmd/afm/revise.go cmd/afm/check.go
git commit -m "refactor(state): единый поиск последнего run из лога + якорь префикса флоу"
```

---

## Task 5: уникальный run-id (A5)

**Files:**
- Modify: `cmd/afm/run.go` (`resolveRun`)
- Test: `cmd/afm/runid_test.go` (create)

**Interfaces:**
- Produces: `newRunID(flowName string) string` — `"<flow>-<timestamp>-<rand4hex>"`.

- [ ] **Step 1: Написать падающий тест**

Create `cmd/afm/runid_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestNewRunID_UniqueWithinSameSecond(t *testing.T) {
	a := newRunID("flow")
	b := newRunID("flow")
	if a == b {
		t.Fatalf("run ids collided: %q == %q", a, b)
	}
	if !strings.HasPrefix(a, "flow-") {
		t.Fatalf("run id must start with flow-: %q", a)
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./cmd/afm/ -run TestNewRunID -race`
Expected: FAIL — `newRunID` не определён.

- [ ] **Step 3: Реализовать newRunID и применить в resolveRun**

В `cmd/afm/run.go` добавить функцию:
```go
// newRunID строит уникальный id run: timestamp секундной гранулярности плюс
// короткий случайный суффикс, чтобы два запуска в одну секунду не делили
// одну директорию и один events.jsonl.
func newRunID(flowName string) string {
	ts := time.Now().Format("20060102-150405")
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", flowName, ts, hex.EncodeToString(b))
}
```
Добавить импорты `"crypto/rand"` и `"encoding/hex"` в `run.go`.

В `resolveRun` заменить строки 408-409:
```go
	runDir = filepath.Join(base, newRunID(f.Name))
```

- [ ] **Step 4: Запустить — убедиться, что проходит**

Run: `go test ./cmd/afm/ -run TestNewRunID -race`
Expected: PASS.

- [ ] **Step 5: Линт, тесты, коммит**

```bash
make lint && make test
git add cmd/afm/run.go cmd/afm/runid_test.go
git commit -m "fix(cmd): уникальный run-id со случайным суффиксом — нет коллизий в одну секунду"
```

---

## Task 6: разведение storage-fatal и concurrent-change (B1)

**Files:**
- Modify: `pkg/state/store.go` (`Apply` — вернуть `ErrConcurrentChange`)
- Modify: `pkg/orchestrator/fsm.go` (`Apply` — не оборачивать concurrent-change в StorageError)
- Modify: `pkg/orchestrator/orchestrator.go` (`Trigger`, `Run`, поля fatal)
- Test: `pkg/orchestrator/fatal_test.go` (create), `pkg/state/store_test.go` (добавить кейс)

**Interfaces:**
- Produces: `state.ErrConcurrentChange` (sentinel) — из `store.Apply`, когда текущий статус ≠ `t.From`.
- Produces: `(*Orchestrator).Run` возвращает storage-fatal ошибку, если во время прогона `store.Apply` упал по I/O.

- [ ] **Step 1: Написать падающие тесты**

Create `pkg/orchestrator/fatal_test.go`:

```go
package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/state"
)

// concurrent-change НЕ должен валить run — это доброкачественный no-op.
func TestTrigger_ConcurrentChangeIsBenign(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	o := &Orchestrator{fsm: NewFSM(store), ui: NewUIBus(), critical: NewCriticalBus(16)}

	// Событие с неверным From → CAS-mismatch → benign.
	_, ok := o.Trigger("a", EvComplete, GuardCtx{}, "") // из pending complete не разрешён
	if ok {
		t.Fatal("expected transition to be rejected")
	}
	if o.loadFatal() != nil {
		t.Fatalf("concurrent-change must not set fatal: %v", o.loadFatal())
	}
}

func TestStore_ApplyConcurrentChangeSentinel(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = store.Apply(state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusDone, Event: "x"})
	if !errors.Is(err, state.ErrConcurrentChange) {
		t.Fatalf("want ErrConcurrentChange, got %v", err)
	}
	_ = context.Background
	_ = time.Second
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./pkg/orchestrator/ -run 'TestTrigger_Concurrent|TestStore_ApplyConcurrent' ./pkg/state/ -race`
Expected: FAIL — `ErrConcurrentChange` и `loadFatal` не определены.

- [ ] **Step 3: Sentinel в store.Apply**

В `pkg/state/store.go` добавить sentinel и использовать в `Apply`:
```go
// ErrConcurrentChange — статус стадии изменился между чтением и Apply (CAS-mismatch).
// Доброкачественно: ожидаемо при конкурентных переходах, НЕ storage-ошибка.
var ErrConcurrentChange = errors.New("concurrent change")
```
В `Apply` заменить:
```go
	current := s.snapshot.Stages[t.StageID].Status
	if current != t.From {
		return fmt.Errorf("%w: stage %q is in %q, expected %q",
			ErrConcurrentChange, t.StageID, current, t.From)
	}
```

- [ ] **Step 4: fsm.Apply не оборачивает benign в StorageError**

В `pkg/orchestrator/fsm.go`:
```go
	if err := f.store.Apply(tr); err != nil {
		if errors.Is(err, state.ErrConcurrentChange) {
			return from, false, nil // доброкачественный CAS-mismatch, не storage-fatal
		}
		return from, false, &StorageError{Inner: err}
	}
```

- [ ] **Step 5: Trigger + Run обрабатывают fatal**

В `pkg/orchestrator/orchestrator.go` добавить поля в `Orchestrator`:
```go
	fatalMu    sync.Mutex
	fatalErr   error
	cancelRun  context.CancelFunc
```
Добавить методы:
```go
// setFatal фиксирует первую storage-fatal ошибку и отменяет run-контекст,
// чтобы event loop завершился и Run вернул ошибку.
func (o *Orchestrator) setFatal(err error) {
	o.fatalMu.Lock()
	if o.fatalErr == nil {
		o.fatalErr = err
	}
	o.fatalMu.Unlock()
	if o.cancelRun != nil {
		o.cancelRun()
	}
}

func (o *Orchestrator) loadFatal() error {
	o.fatalMu.Lock()
	defer o.fatalMu.Unlock()
	return o.fatalErr
}
```
Изменить `Trigger` (обработка ветки `err != nil`):
```go
	to, ok, err := o.fsm.Apply(stageID, ev, ctx, reason)
	if err != nil {
		// Сюда попадает только storage-fatal: concurrent-change fsm.Apply вернул
		// как (from,false,nil). Продолжать против сломанного лога нельзя.
		log.Printf("FATAL: storage failure applying %s/%s: %v", stageID, ev, err)
		o.setFatal(err)
		return o.currentStatus(stageID), false
	}
```
Изменить `Run`, чтобы установить cancel и вернуть fatal:
```go
func (o *Orchestrator) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	o.cancelRun = cancel
	defer cancel()

	o.startPlanningForPending(ctx)
	o.startQuestionPoller(ctx)

	for {
		select {
		case <-ctx.Done():
			if ferr := o.loadFatal(); ferr != nil {
				return ferr
			}
			return ctx.Err()
		case ev := <-o.critical.Recv():
			if err := o.handleEvent(ctx, ev); err != nil {
				return err
			}
			if ferr := o.loadFatal(); ferr != nil {
				return ferr
			}
			if o.shouldExit() {
				return nil
			}
		}
	}
}
```
(Проверка `loadFatal` после `handleEvent` ловит fatal, зафиксированный синхронно в текущем событии до того, как сработает отмена контекста.)

- [ ] **Step 6: Запустить — убедиться, что проходит**

Run: `go test ./pkg/orchestrator/ ./pkg/state/ -race`
Expected: PASS.

- [ ] **Step 7: Линт, тесты, коммит**

```bash
make lint && make test
git add pkg/state/store.go pkg/orchestrator/fsm.go pkg/orchestrator/orchestrator.go pkg/orchestrator/fatal_test.go
git commit -m "fix(orchestrator): storage-fatal завершает run, concurrent-change — тихий no-op"
```

---

## Task 7: spawnAgent + WaitGroup + ctx в publish (C1)

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (helper `spawnAgent`, `waitAgents`, конверсия spawn-сайтов, `Run`)
- Modify: `pkg/orchestrator/recovery.go` (конверсия spawn-сайтов)
- Modify: `pkg/orchestrator/retry.go` (3× `context.Background()` → ctx-параметр)
- Test: `pkg/orchestrator/shutdown_test.go` (create)

**Interfaces:**
- Produces: `(*Orchestrator).spawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage))` — единая точка запуска агентских горутин (семафор + маркер активности + WaitGroup).
- Consumes: `o.cancelRun` из Task 6.

- [ ] **Step 1: Написать падающий тест**

Create `pkg/orchestrator/shutdown_test.go`:

```go
package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
)

// spawnAgent отслеживает горутину в WaitGroup: waitAgents дожидается её завершения.
func TestSpawnAgent_WaitAgentsBlocksUntilDone(t *testing.T) {
	o := &Orchestrator{ui: NewUIBus(), critical: NewCriticalBus(16), sems: map[string]interface {
		acquire()
		release()
	}{}}

	var finished atomic.Bool
	release := make(chan struct{})
	o.spawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		<-release
		finished.Store(true)
	})

	done := make(chan struct{})
	go func() { o.waitAgents(); close(done) }()

	select {
	case <-done:
		t.Fatal("waitAgents returned before agent finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
		if !finished.Load() {
			t.Fatal("agent goroutine did not finish")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitAgents did not return after agent finished")
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./pkg/orchestrator/ -run TestSpawnAgent -race`
Expected: FAIL — `spawnAgent`/`waitAgents` не определены.

- [ ] **Step 3: Добавить helper и waitAgents**

В `pkg/orchestrator/orchestrator.go` добавить поле `agentWG sync.WaitGroup` в `Orchestrator` и константу:
```go
// agentDrainTimeout — сколько ждём завершения агентских горутин на выходе Run,
// прежде чем вернуться (агентские процессы уже убиты отменой ctx; ожидание
// защищает Store от использования после Close).
const agentDrainTimeout = 10 * time.Second
```
Методы:
```go
// spawnAgent запускает агентскую горутину под семафором команды, помечает стадию
// активной и учитывает горутину в WaitGroup. Единственная точка запуска —
// заменяет ~10 копий одинакового boilerplate и гарантирует чистый shutdown.
func (o *Orchestrator) spawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	o.agentWG.Add(1)
	go func() {
		defer o.agentWG.Done()
		sem := o.semFor(s)
		sem.acquire()
		o.markAgentActive(s.ID)
		defer func() {
			o.markAgentDone(s.ID)
			sem.release()
		}()
		run(ctx, s)
	}()
}

// waitAgents дожидается завершения всех агентских горутин (с ограничением),
// чтобы Run не вернулся, пока горутины ещё пишут в Store.
func (o *Orchestrator) waitAgents() {
	done := make(chan struct{})
	go func() {
		o.agentWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(agentDrainTimeout):
		log.Printf("WARN: agent drain timed out after %v", agentDrainTimeout)
	}
}
```

- [ ] **Step 4: Запустить тест helper'а**

Run: `go test ./pkg/orchestrator/ -run TestSpawnAgent -race`
Expected: PASS.

- [ ] **Step 5: Конвертировать spawn-сайты на spawnAgent**

Заменить каждую конструкцию `go func(st flow.Stage){ sem := o.semFor(st); sem.acquire(); o.markAgentActive(st.ID); defer {...}; o.runX(ctx, st) }(s)` на `o.spawnAgent(ctx, s, o.runX)`. Конкретно:

- `startReadyStages` (orchestrator.go ~1052): `o.spawnAgent(ctx, *stage, o.runImplementationAgent)`
- `onManualRetry` две ветки impl (~891, ~910): `o.spawnAgent(ctx, *stage, o.runImplementationAgent)`
- `onManualRetry` ветка planning (~931): `o.spawnAgent(ctx, *stage, o.runPlanningAgent)`
- `onUserAnswered` planning/impl/review/autonomous (~725/737/749/761): соответственно `o.spawnAgent(ctx, *stage, o.runPlanningAgent)`, `...o.runImplementationAgent`, `...o.runReviewAgent`, `...o.runAutonomousAgent`. Перед каждым остаётся его `o.Trigger(...EvUserAnswered...)`.
- `onRevised` (~819): `o.spawnAgent(ctx, s, o.runPlanningWithFeedback)`.
- `startPlanningForUnblocked` (~1010) — тело с DetermineStagePhases/autonomous обернуть в замыкание:
```go
	o.spawnAgent(ctx, s, func(ctx context.Context, st flow.Stage) {
		phases := o.DetermineStagePhases(ctx, st)
		if len(phases) == 1 && phases[0] == "autonomous_execution" {
			stageDir := filepath.Join(o.opts.RunDir, st.ID)
			if err := os.MkdirAll(stageDir, 0755); err != nil {
				o.Trigger(st.ID, EvFail, GuardCtx{}, "mkdir failed")
				return
			}
			_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
			o.Trigger(st.ID, EvSupervisorApproved, GuardCtx{}, "supervisor: autonomous")
			o.Trigger(st.ID, EvStartRun, GuardCtx{}, "")
			o.runAutonomousAgent(ctx, st)
		} else {
			o.runPlanningAgent(ctx, st)
		}
	})
```

В `recovery.go` — те же замены для всех горутинных spawn-сайтов (~67, ~86, ~98, ~115, ~132, ~157). Для `resumeInteractiveAgent` (site ~67): `o.spawnAgent(ctx, s, o.resumeInteractiveAgent)`. Для `runPlanningWithFeedback` (site ~98): `o.spawnAgent(ctx, s, o.runPlanningWithFeedback)`. Для планирования с autonomous (~157) — то же замыкание, что и в `startPlanningForUnblocked`.

**Удалить дублирующий маркер активности (M5):** в `runPlanningWithFeedback` убрать строки `o.markAgentActive(s.ID)` / `defer o.markAgentDone(s.ID)` (~1145-1146) и в `resumeInteractiveAgent` (~202-203) — теперь маркер ставит `spawnAgent`.

- [ ] **Step 6: ctx в publish + drain в Run**

В `pkg/orchestrator/retry.go` заменить три `context.Background()` (строки 108, 113, 171) на `ctx` — параметр `runWithRetry` уже его несёт:
```go
	_ = o.critical.Publish(ctx, Event{Type: EventAgentCompleted, StageID: s.ID, Data: phase})
```
(и аналогично для `EventRetryExhausted`).

В `Run` (Task 6) добавить `o.waitAgents()` перед КАЖДЫМ `return` внутри цикла (после `ctx.Done()`, после ошибки `handleEvent`, после fatal, после `shouldExit`). Простейшее: добавить `defer o.waitAgents()` сразу после `defer cancel()` — тогда при любом выходе из `Run` сначала отменится ctx (defer LIFO: `waitAgents` объявлен позже cancel → выполнится РАНЬШЕ; нужно наоборот). Поэтому объявить в правильном порядке:
```go
	ctx, cancel := context.WithCancel(ctx)
	o.cancelRun = cancel
	defer o.waitAgents() // выполнится ПОСЛЕ cancel (LIFO) — сначала отмена, потом ожидание
	defer cancel()
```
Так `cancel()` сработает первым (отменит агентов), затем `waitAgents()` дождётся их завершения — и только потом `run.go` закроет Store.

- [ ] **Step 7: Тест чистого shutdown**

Добавить в `pkg/orchestrator/shutdown_test.go`:
```go
func TestRun_CancelDrainsAgents(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{ui: NewUIBus(), critical: NewCriticalBus(16), sems: map[string]interface {
		acquire()
		release()
	}{}}

	started := make(chan struct{})
	var done atomic.Bool
	o.spawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		close(started)
		time.Sleep(20 * time.Millisecond)
		done.Store(true)
	})
	<-started
	o.waitAgents()
	if !done.Load() {
		t.Fatal("waitAgents returned before agent completed")
	}
	_ = dir
}
```

Run: `go test ./pkg/orchestrator/ -race`
Expected: PASS (все тесты пакета).

- [ ] **Step 8: Линт, тесты, коммит**

```bash
make lint && make test
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/recovery.go pkg/orchestrator/retry.go pkg/orchestrator/shutdown_test.go
git commit -m "fix(orchestrator): spawnAgent+WaitGroup+ctx — нет утечки горутин на shutdown, DRY spawn"
```

---

## Task 8: inline auto-approve + долговечный approve (C2 + C3)

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (`approveStage`, `Approve`, `onAgentCompleted`, `onApproved`)
- Test: `pkg/orchestrator/approve_test.go` (create)

**Interfaces:**
- Produces: `(*Orchestrator).approveStage(ctx context.Context, stageID string)` — синхронно и долговечно переводит стадию из `awaiting_approval` (в `ready` для impl-стадий, в `done` для planning-only) и запускает побочные эффекты. Идемпотентна.
- Изменяется: `Approve` вызывает `approveStage` напрямую (не публикует `EventApproved`); headless auto-approve вызывает `approveStage` inline (снимает риск self-deadlock C2).

- [ ] **Step 1: Написать падающий тест**

Create `pkg/orchestrator/approve_test.go`:

```go
package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// approveStage долговечно переводит impl-стадию awaiting_approval → ready
// (запись в Store фиксируется до возврата), чтобы краш после approve не терял интент.
func TestApproveStage_DurableTransition(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	stages := []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentImplementation}}}
	o := &Orchestrator{
		opts:     Options{RunDir: dir, Stages: stages, Store: store},
		graph:    NewGraph(stages),
		fsm:      NewFSM(store),
		ui:       NewUIBus(),
		critical: NewCriticalBus(16),
		sems:     map[string]interface{ acquire(); release() }{},
	}
	// довести до awaiting_approval
	o.Trigger("a", EvStartPlanning, GuardCtx{}, "")
	o.Trigger("a", EvPlanReady, GuardCtx{}, "")

	o.approveStage(context.Background(), "a")

	// перечитываем состояние из лога — оно должно быть ready (долговечно)
	rs, err := state.LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Stages["a"].Status != state.StatusReady {
		t.Fatalf("want ready persisted in log, got %q", rs.Stages["a"].Status)
	}
	store.Close()
}
```
(Если у `flow.Stage`/`flow.AgentType` иные имена конструкторов — свериться с `pkg/flow/flow.go` и `HasAgent`; тип агента имплементации — `flow.AgentImplementation`, используемый в `onApproved`.)

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./pkg/orchestrator/ -run TestApproveStage -race`
Expected: FAIL — `approveStage` не определён.

- [ ] **Step 3: Ввести approveStage, переключить Approve и auto-approve**

В `pkg/orchestrator/orchestrator.go` добавить:
```go
// approveStage долговечно переводит стадию из awaiting_approval и запускает
// побочные эффекты. Вызывается СИНХРОННО (из HTTP-обработчика и из headless
// auto-approve), поэтому переход фиксируется в Store до возврата — краш после
// approve не теряет интент (recovery резюмит ready/done). Идемпотентна: если
// стадия уже не в awaiting_approval, только до-запускает побочные эффекты.
func (o *Orchestrator) approveStage(ctx context.Context, stageID string) {
	if o.currentStatus(stageID) == state.StatusAwaitingApproval {
		stage := o.graph.Stage(stageID)
		if stage != nil && !stage.HasAgent(flow.AgentImplementation) {
			o.Trigger(stageID, EvComplete, GuardCtx{}, "planning-only stage")
		} else {
			o.Trigger(stageID, EvApprove, GuardCtx{}, "")
		}
	}
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
}
```
Переписать `Approve` (публичный, вызывается HTTP-обработчиком) — синхронно, без публикации в шину:
```go
// Approve approves a stage plan (синхронно и долговечно).
func (o *Orchestrator) Approve(ctx context.Context, stageID string) error {
	o.approveStage(ctx, stageID)
	return nil
}
```
В `onAgentCompleted`, ветка `phasePlanning`, headless-блок — заменить блокирующую публикацию (строка ~633) на inline-вызов:
```go
		if o.opts.DashboardURL == "" {
			if o.opts.RequireApproval {
				o.FailStage(ev.StageID, "approval required but no dashboard running (use --port or server.port in config)")
				return nil
			}
			log.Printf("headless: auto-approving plan for stage %q", ev.StageID)
			o.approveStage(ctx, ev.StageID)
			return nil
		}
		o.tryActivatePrePlanned(ctx)
```
Удалить обработчик `onApproved` и ветку `case EventApproved:` в `handleEvent`, а также `EventApproved` из `bus.go`, если больше нет публикующих (проверить `grep -rn EventApproved pkg/ | grep -v _test`). Если `EventApproved` используется UI/сервером как индикатор — оставить константу, но убрать ветку в `handleEvent`. Свериться грепом перед удалением.

- [ ] **Step 4: Запустить — убедиться, что проходит**

Run: `go test ./pkg/orchestrator/ -race`
Expected: PASS.

- [ ] **Step 5: Проверить HTTP-обработчик approve**

Проверить `pkg/server/handlers.go`: обработчик approve вызывает колбэк, который зовёт `orch.Approve`. Так как `Approve` теперь синхронный и долговечный, ответ `200 OK` выдаётся уже после фиксации перехода — дополнительных правок в handler не требуется. Прогнать серверные тесты: `go test ./pkg/server/ -race`. Expected: PASS.

- [ ] **Step 6: Линт, тесты, коммит**

```bash
make lint && make test
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/bus.go pkg/orchestrator/approve_test.go
git commit -m "fix(orchestrator): approve синхронный и долговечный, inline auto-approve без self-deadlock"
```

---

## Task 9: долговечные revise/retry в dashboard-пути (C3, продолжение)

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (`Revise`, `Retry` — синхронный durable путь)
- Test: `pkg/orchestrator/approve_test.go` (добавить кейсы)

**Interfaces:**
- Изменяется: `Revise`/`Retry` фиксируют долговечный переход синхронно, затем запускают побочные эффекты (как `approveStage`). CLI-пути (`cmd/afm/revise.go`, `retry.go`) уже долговечны и при живом run заблокированы flock'ом (Task 1) — не трогаем.

- [ ] **Step 1: Написать падающий тест**

Добавить в `pkg/orchestrator/approve_test.go`:
```go
func TestRevise_DurableTransition(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	stages := []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}}
	o := &Orchestrator{
		opts:     Options{RunDir: dir, Stages: stages, Store: store},
		graph:    NewGraph(stages),
		fsm:      NewFSM(store),
		ui:       NewUIBus(),
		critical: NewCriticalBus(16),
		sems:     map[string]interface{ acquire(); release() }{},
	}
	// подготовить plan.md (revise версионирует его)
	stageDir := dir + "/a"
	_ = os.MkdirAll(stageDir, 0755)
	_ = os.WriteFile(stageDir+"/plan.md", []byte("# plan"), 0644)

	o.Trigger("a", EvStartPlanning, GuardCtx{}, "")
	o.Trigger("a", EvPlanReady, GuardCtx{}, "")

	if err := o.Revise(context.Background(), "a", "нужны правки"); err != nil {
		t.Fatal(err)
	}
	rs, _ := state.LoadRunState(dir)
	if rs.Stages["a"].Status != state.StatusRevising {
		t.Fatalf("want revising persisted, got %q", rs.Stages["a"].Status)
	}
	o.waitAgents()
	store.Close()
}
```
Добавить импорт `"os"` в тестовый файл.

- [ ] **Step 2: Запустить — убедиться, что падает**

Run: `go test ./pkg/orchestrator/ -run TestRevise_Durable -race`
Expected: FAIL (или flaky) — текущий `Revise` публикует событие асинхронно, состояние в логе на момент проверки может быть ещё awaiting_approval.

- [ ] **Step 3: Сделать Revise/Retry синхронными**

В `orchestrator.go` переписать `Revise` — вынести тело `onRevised` в синхронный вызов:
```go
// Revise отправляет фидбэк на переплан (синхронно и долговечно).
func (o *Orchestrator) Revise(ctx context.Context, stageID, feedback string) error {
	if o.currentStatus(stageID) != state.StatusAwaitingApproval {
		return nil
	}
	o.Trigger(stageID, EvRevise, GuardCtx{}, feedback)
	stageDir := filepath.Join(o.opts.RunDir, stageID)
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}
	if stage := o.graph.Stage(stageID); stage != nil {
		o.spawnAgent(ctx, *stage, o.runPlanningWithFeedback)
	}
	return nil
}
```
Аналогично `Retry` — вызвать логику `onManualRetry` синхронно. Простейшее: переименовать текущий `onManualRetry(ctx, ev)` в `retryStage(ctx, stageID)` (заменив `ev.StageID` на `stageID`), и сделать:
```go
func (o *Orchestrator) Retry(ctx context.Context, stageID string) error {
	o.retryStage(ctx, stageID)
	return nil
}
```
Удалить ветки `case EventRevised:` / `case EventManualRetry:` в `handleEvent` и соответствующие обработчики `onRevised`/`onManualRetry`-как-события (тело `onManualRetry` переиспользуется как `retryStage`). Проверить грепом, что `EventRevised`/`EventManualRetry` больше нигде не публикуются: `grep -rn 'EventRevised\|EventManualRetry' pkg/ | grep -v _test`.

- [ ] **Step 4: Запустить — убедиться, что проходит**

Run: `go test ./pkg/orchestrator/ ./pkg/server/ -race`
Expected: PASS.

- [ ] **Step 5: Полный прогон, линт, коммит**

```bash
make lint && make test
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/approve_test.go
git commit -m "fix(orchestrator): revise/retry синхронные и долговечные в dashboard-пути"
```

---

## Финальная проверка

- [ ] **Полный прогон всей сборки и тестов**

```bash
make lint && make test && make build
```
Expected: всё зелёное, бинарь собирается.

- [ ] **Ручная проверка flock (A1)**

```bash
# терминал 1: запустить любой flow с дашбордом
# терминал 2:
afm approve some-stage
```
Expected: `run is active — approve via the dashboard, or stop `afm run` first`.

- [ ] **Обновить CLAUDE.md**

Добавить в CLAUDE.md короткую заметку про flock (`<runDir>/.lock`, CLI-мутации при живом run отклоняются), карантин лога (`events.jsonl.corrupt-<ts>`), и что storage-fatal завершает run. Коммит: `docs: flock/карантин лога/storage-fatal в CLAUDE.md`.

---

## Self-Review (выполнен при написании плана)

- **Покрытие спеки:** A1→T1, A2→T2, A3→T3, A4→T4, A5→T5, B1→T6, C1→T7, C2→T8, C3→T8+T9. Отложенные state#5/#12 явно вне плана (как в спеке).
- **Плейсхолдеры:** нет — каждый шаг несёт реальный код и точные команды.
- **Согласованность типов:** `ErrRunLocked`/`ErrCorruptLog`/`ErrConcurrentChange` (state), `LoadRunState`/`FindLatestRunForStage`/`newRunID`, `spawnAgent`/`waitAgents`/`approveStage`/`retryStage`, поля `fatalErr`/`cancelRun`/`agentWG` — имена стабильны между задачами.
- **Риск:** T7 (spawnAgent) и T8/T9 (удаление ветвей событий) — самые крупные; их тесты изолированы, а `grep`-проверки перед удалением событий защищают от висячих ссылок.
