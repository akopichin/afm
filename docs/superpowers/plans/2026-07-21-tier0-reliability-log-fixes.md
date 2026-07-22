# Tier 0 — Reliability Log Fixes (B1, B2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Устранить два верифицированных бага целостности `events.jsonl`: (B2) потерянный финальный `\n` больше не портит лог, (B1) read-путь (`afm check`, поиск run) сигнализирует о порче лога так же, как write-путь (`afm run`).

**Architecture:** Оба бага коренятся в двух разошедшихся парсерах лога — `replayEvents` (store.go, путь `Open`) и `LoadRunState` (state.go, read-only). Task 1 чинит арифметику усечения хвоста прямо в `replayEvents`. Task 2 выносит единый парсер `parseEventLog`, через который начинают ходить оба пути, и `LoadRunState` возвращает `ErrCorruptLog` на порче в середине лога.

**Tech Stack:** Go, пакет `pkg/state`. Тесты — стандартный `testing`. Событийный лог append-only JSONL; durability-контракт: зафиксированной считается только запись, завершённая `\n` и прошедшая fsync.

## Global Constraints

- НЕ менять версию Go в `go.mod`.
- Линт не должен ругаться; в этом окружении `golangci-lint` отсутствует — использовать `go vet ./pkg/state/...` как доступную замену, плюс `go build ./...`.
- Все коммиты на русском языке.
- НИКОГДА не добавлять `Co-Authored-By` в коммиты.
- Работаем на текущей ветке `ux` (как весь сессионный поток).

## Ключевое проектное решение (durability)

Последняя строка без завершающего `\n` трактуется как **незакоммиченный** torn-write и **усекается — независимо от того, парсится ли её JSON**. Причина: (а) арифметика `offset+1` для такой строки давала `goodOffset = len(data)+1`, и `f.Truncate` **расширял** файл нулевым байтом (это и есть B2); (б) даже если не расширять (вариант `min(goodOffset, len)` из спека), следующий `Apply` дописал бы новую запись вплотную к незавершённой строке без разделителя `\n` → слияние двух записей в одну битую. Усечение newline-less хвоста закрывает оба сценария и согласуется с уже существующей обработкой невалидного torn-хвоста. Committed-запись всегда имеет свой `\n` (writer пишет `data+'\n'` и fsync'ит), поэтому усечение никогда не теряет зафиксированную запись.

---

### Task 1: B2 — newline-less хвост усекается, а не расширяет лог нулевым байтом

**Files:**
- Modify: `pkg/state/store.go:172-194` (тело цикла в `replayEvents`)
- Test: `pkg/state/store_replay_test.go`

**Interfaces:**
- Consumes: `Open(dir string, stageIDs []string) (*Store, error)`, `(*Store).Get(stageID string) StageStatus`, `(*Store).Close() error` — существующие.
- Produces: изменённое поведение `replayEvents` (внутренняя функция; сигнатура не меняется). Task 2 позже вынесет эту логику в `parseEventLog`.

- [ ] **Step 1: Написать падающий тест**

Добавить в конец `pkg/state/store_replay_test.go` и добавить `"bytes"` в его import-блок:

```go
// Валидный JSON в последней строке, но БЕЗ завершающего \n (потерян при crash):
// запись незакоммичена, должна усечься. Лог НЕ должен расшириться нулевым байтом.
func TestOpen_ValidLastLineWithoutNewline_TruncatedNotCorrupted(t *testing.T) {
	dir := t.TempDir()
	committed := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	uncommitted := `{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` // без \n
	orig := []byte(committed + uncommitted)
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, orig, 0644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: want success, got %v", err)
	}
	defer s.Close()

	after, _ := os.ReadFile(p)
	if len(after) != len(committed) {
		t.Fatalf("events.jsonl size after Open: want %d (усечён до закоммиченной), got %d", len(committed), len(after))
	}
	if bytes.IndexByte(after, 0) >= 0 {
		t.Fatal("events.jsonl содержит NUL-байт после Open (порча B2)")
	}
	if got := s.Get("a"); got != StatusPlanning {
		t.Fatalf("state: want planning (незакоммиченная отброшена), got %q", got)
	}
}
```

- [ ] **Step 2: Прогнать тест — убедиться, что падает**

Run: `go test ./pkg/state/ -run TestOpen_ValidLastLineWithoutNewline_TruncatedNotCorrupted -v`
Expected: FAIL — `size after Open: want 69, got 130` (или NUL-байт), т.к. `goodOffset=len(data)+1` и `Truncate` расширяет файл.

- [ ] **Step 3: Починить арифметику усечения в `replayEvents`**

В `pkg/state/store.go` заменить тело цикла (строки 172-193) на версию с ранним усечением newline-less хвоста:

```go
	for i, line := range lines {
		isLast := i == len(lines)-1
		// Последняя строка без завершающего \n — незакоммиченный torn-write
		// (crash в момент append). Усекаем её, НЕ доверяя даже валидному JSON:
		// durability-контракт считает зафиксированными только newline-terminated
		// записи. Иначе (а) offset+1 дал бы goodOffset>len(data) и Truncate
		// расширил бы лог нулевым байтом, либо (б) следующий Apply слил бы её
		// со своей записью без разделителя \n.
		if isLast && !endsWithNewline {
			break
		}
		offset += int64(len(line)) + 1 // +1 на \n (у не-последних строк он всегда есть)
		if len(bytes.TrimSpace(line)) == 0 {
			goodOffset = offset
			continue
		}
		var t Transition
		if uerr := json.Unmarshal(line, &t); uerr != nil {
			// Битая ПОЛНАЯ строка в середине лога — повреждение.
			return history, lastSeq, goodOffset, true, nil
		}
		rs.SetStageStatusAt(t.StageID, t.To, t.Time)
		history = append(history, t)
		lastSeq = t.Seq
		goodOffset = offset
	}
	return history, lastSeq, goodOffset, false, nil
```

- [ ] **Step 4: Прогнать новый тест и весь пакет**

Run: `go test ./pkg/state/ -run TestOpen_ValidLastLineWithoutNewline_TruncatedNotCorrupted -v`
Expected: PASS

Run: `go test ./pkg/state/`
Expected: ok — все существующие тесты зелёные (в частности `TestOpen_TornTailTruncates`, `TestOpen_MidCorruptionQuarantines`, `TestOpen_TruncatesPartialLine`).

- [ ] **Step 5: Коммит**

```bash
git add pkg/state/store.go pkg/state/store_replay_test.go
git commit -m "fix(state): newline-less хвост events.jsonl усекается, а не расширяет лог NUL-байтом

Валидная последняя строка без завершающего \n давала goodOffset=len+1,
и f.Truncate расширял файл нулевым байтом → следующий Apply писал после \0
→ на реоткрытии лог уходил в карантин. Теперь любой newline-less хвост
усекается как незакоммиченный (durability = только newline-terminated записи)."
```

---

### Task 2: B1 — единый парсер лога; LoadRunState сигнализирует о порче как Open

**Files:**
- Modify: `pkg/state/state.go` (добавить `replayResult` + `parseEventLog` после `splitLines`; переписать `LoadRunState`; добавить import `"bytes"`)
- Modify: `pkg/state/store.go` (переписать `replayEvents` на делегирование; удалить ставший ненужным import `"bytes"`)
- Test: `pkg/state/state_loadrunstate_test.go` (создать)

**Interfaces:**
- Consumes: `splitLines(data []byte) [][]byte`, `(*RunState).SetStageStatusAt(stageID string, status StageStatus, t time.Time)`, `ErrCorruptLog error` — существующие.
- Produces:
  - `type replayResult struct { history []Transition; lastSeq uint64; goodOffset int64; corrupted bool }`
  - `func parseEventLog(data []byte, rs *RunState) replayResult` — единый парсер, применяет переходы к `rs`, вычисляет `goodOffset` (с усечением newline-less хвоста из Task 1) и `corrupted` (битая полная строка в середине).
  - `func LoadRunState(runDir string) (RunState, error)` — теперь возвращает `ErrCorruptLog` при `corrupted`.
  - `func replayEvents(path string, rs *RunState) (history []Transition, lastSeq uint64, lastGoodOffset int64, corrupted bool, err error)` — сигнатура без изменений, тело делегирует в `parseEventLog`.

- [ ] **Step 1: Написать падающие тесты**

Создать `pkg/state/state_loadrunstate_test.go`:

```go
package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Битая полная строка в середине лога → LoadRunState обязан вернуть ErrCorruptLog
// (как и Open), а не молча отдать устаревший префикс.
func TestLoadRunState_MidCorruptionReturnsErr(t *testing.T) {
	dir := t.TempDir()
	line1 := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	bad := `NOT JSON AT ALL` + "\n"
	line3 := `{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(line1+bad+line3), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadRunState(dir)
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("LoadRunState на порче в середине: want ErrCorruptLog, got %v", err)
	}
}

// Оборванный хвост (без \n) — НЕ порча: LoadRunState отдаёт валидный префикс без ошибки.
func TestLoadRunState_TornTailReturnsPrefixNoErr(t *testing.T) {
	dir := t.TempDir()
	good := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	torn := `{"seq":2,"stage_id":"a","from":"planni` // оборвано, без \n
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(good+torn), 0644); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState на torn-tail: want nil err, got %v", err)
	}
	if got := rs.Stages["a"].Status; got != StatusPlanning {
		t.Fatalf("state: want planning, got %q", got)
	}
}
```

- [ ] **Step 2: Прогнать тесты — убедиться, что `MidCorruption` падает**

Run: `go test ./pkg/state/ -run 'TestLoadRunState_' -v`
Expected: `TestLoadRunState_MidCorruptionReturnsErr` FAIL (`want ErrCorruptLog, got <nil>` — текущий `LoadRunState` делает `break` и возвращает nil-ошибку). `TestLoadRunState_TornTailReturnsPrefixNoErr` может уже проходить.

- [ ] **Step 3: Добавить единый парсер `parseEventLog` в `state.go`**

В `pkg/state/state.go` добавить `"bytes"` в import-блок и вставить сразу ПОСЛЕ функции `splitLines`:

```go
// replayResult — итог разбора events.jsonl, общий для обоих путей чтения лога:
// replayEvents (Open, с усечением по goodOffset) и LoadRunState (read-only check).
type replayResult struct {
	history    []Transition
	lastSeq    uint64
	goodOffset int64 // байтовый конец последней ЗАФИКСИРОВАННОЙ (newline-terminated) записи
	corrupted  bool  // битая ПОЛНАЯ строка в середине лога (есть валидные записи после)
}

// parseEventLog — единственный парсер events.jsonl. Применяет переходы к rs.
// Оборванный/незакоммиченный хвост (последняя строка без \n) усекается и НЕ
// считается порчей. Битая полная строка в середине → corrupted=true.
func parseEventLog(data []byte, rs *RunState) replayResult {
	lines := splitLines(data)
	endsWithNewline := len(data) > 0 && data[len(data)-1] == '\n'
	var offset, goodOffset int64
	var res replayResult
	for i, line := range lines {
		isLast := i == len(lines)-1
		if isLast && !endsWithNewline {
			break // незакоммиченный хвост без \n — усечь (см. durability-решение плана)
		}
		offset += int64(len(line)) + 1 // +1 на \n (у не-последних строк он всегда есть)
		if len(bytes.TrimSpace(line)) == 0 {
			goodOffset = offset
			continue
		}
		var t Transition
		if json.Unmarshal(line, &t) != nil {
			res.goodOffset = goodOffset
			res.corrupted = true
			return res
		}
		rs.SetStageStatusAt(t.StageID, t.To, t.Time)
		res.history = append(res.history, t)
		res.lastSeq = t.Seq
		goodOffset = offset
	}
	res.goodOffset = goodOffset
	return res
}
```

- [ ] **Step 4: Переписать `LoadRunState` на делегирование + сигнал порчи**

В `pkg/state/state.go` заменить тело `LoadRunState` (строки 91-109):

```go
func LoadRunState(runDir string) (RunState, error) {
	rs := RunState{Stages: map[string]StageState{}}
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return rs, err
	}
	res := parseEventLog(data, &rs)
	if res.corrupted {
		return rs, ErrCorruptLog
	}
	rs.LastSeq = res.lastSeq
	return rs, nil
}
```

- [ ] **Step 5: Переписать `replayEvents` на делегирование**

В `pkg/state/store.go` заменить тело `replayEvents` (строки 160-195) на делегирующую версию и удалить `"bytes"` из import-блока (после переноса `bytes.TrimSpace` в `state.go` он в store.go больше не используется):

```go
func replayEvents(path string, rs *RunState) (history []Transition, lastSeq uint64, lastGoodOffset int64, corrupted bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, 0, false, nil
		}
		return nil, 0, 0, false, err
	}
	res := parseEventLog(data, rs)
	return res.history, res.lastSeq, res.goodOffset, res.corrupted, nil
}
```

- [ ] **Step 6: Собрать, проверить импорты, прогнать тесты**

Run: `go build ./...`
Expected: успешно. Если компилятор ругается `"bytes" imported and not used` в store.go — удалить строку `"bytes"` из его import-блока (Step 5). Если `undefined: bytes` в state.go — добавить `"bytes"` в его import-блок (Step 3).

Run: `go test ./pkg/state/ -run 'TestLoadRunState_' -v`
Expected: оба PASS.

Run: `go test ./pkg/state/`
Expected: ok — весь пакет зелёный (в т.ч. `TestOpen_*` из Task 1 и существующие replay-тесты через новый общий парсер).

- [ ] **Step 7: Регрессия по вызывающим + vet**

Run: `go vet ./pkg/state/...`
Expected: без замечаний.

Run: `go test ./pkg/state/... ./cmd/afm/... ./pkg/orchestrator/...`
Expected: ok — `LoadRunState` вызывают `cmd/afm/check.go` (уже пробрасывает ошибку через `fmt.Errorf("load state: %w", err)`) и `state.FindLatestRunForStage` (уже пропускает run при `lerr != nil`); новый `ErrCorruptLog` они обрабатывают корректно без изменений.

- [ ] **Step 8: Коммит**

```bash
git add pkg/state/state.go pkg/state/store.go pkg/state/state_loadrunstate_test.go
git commit -m "fix(state): единый парсер лога — LoadRunState сигналит о порче как Open

replayEvents (afm run) и LoadRunState (afm check, поиск run) читали лог
двумя разошедшимися парсерами: при битой строке в середине Open уходил в
карантин с ErrCorruptLog, а LoadRunState молча отдавал устаревший префикс,
игнорируя валидные записи после порчи — нарушение инварианта «лог —
единственный источник правды». Вынесен общий parseEventLog; LoadRunState
теперь возвращает ErrCorruptLog при порче в середине."
```

---

## Self-Review

**Spec coverage:**
- B1 (read/write расхождение на порче) → Task 2 (единый `parseEventLog`, `LoadRunState` → `ErrCorruptLog`). ✓
- B2 (потерянный `\n` → NUL-байт/карантин) → Task 1 (усечение newline-less хвоста). ✓
- B3 — явно вне охвата плана (отложен в спеке). ✓

**Placeholder scan:** нет TBD/TODO; каждый код-шаг содержит полный код; команды и ожидаемый вывод указаны. ✓

**Type consistency:** `parseEventLog(data []byte, rs *RunState) replayResult` определён в Task 2 Step 3 и потребляется в Step 4 (`LoadRunState`) и Step 5 (`replayEvents`) с теми же именами полей (`history`, `lastSeq`, `goodOffset`, `corrupted`). Сигнатура `replayEvents` сохранена дословно из исходника. `ErrCorruptLog`, `SetStageStatusAt`, `splitLines` — существующие, использованы как определены. ✓

**Порядок задач:** Task 1 чинит `replayEvents` инлайн; Task 2 переносит ту же (уже исправленную) логику в `parseEventLog` — переноса рабочего кода без потери фикса. Оба таска независимо тестируемы и коммитятся отдельно. ✓
