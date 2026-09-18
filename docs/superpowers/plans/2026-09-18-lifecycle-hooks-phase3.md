# Lifecycle-хуки Phase 3 — Implementation Plan (секреты и env)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Дать lifecycle-хукам безопасную доставку секретов/переменных: поле `env` со ссылками `env:`/`file:` (та же семантика, что `auth.from`), вынос резолвера в нейтральный `pkg/secrets`, минимальное окружение hook-процесса по умолчанию + `inherit_env`, редакцию `[REDACTED]` в логах/dashboard, приоритеты `secrets.env` (project > global > env процесса), и Docker-транспорт секретов без монтирования файлов в контейнер.

**Architecture:** Резолвер секретов (`ResolveAuthValue`/`LoadSecrets`/`LoadSecretLayers`/`expandHome`) переезжает из `pkg/docker` в нейтральный `pkg/secrets`; `pkg/docker` и lifecycle-хуки используют его. Хук получает `env map[string]SecretRef` и `inherit_env bool`. Секреты резолвятся ОДИН раз при сборке dispatcher (host, `cmd/afm/run.go`), до `flow_started`, fail-fast с указанием hook id + имени переменной; резолвнутые значения живут только в памяти (`RegisteredHook.ResolvedEnv`), не пишутся в payload/логи/journal. Runner строит окружение hook-процесса: по умолчанию (spec) минимальный базовый набор (PATH/HOME/locale/tmp/proxy-certs) + `AFM_*` + резолвнутый `env`; полный `os.Environ()` только при `inherit_env: true` (и всегда с вырезанием transport-префиксов); лог оборачивается в редактирующий writer (`[REDACTED]`). В Docker-режиме host-launcher резолвит ссылки до `docker run`, передаёт значения транзиентными bare `-e AFM_HOOK_SECRET_<hookIdx>_<varIdx>` (инъективно по индексам, codex #1); in-container резолвер читает их вместо файлов и делает `Unsetenv`, transport-переменные вырезаются из окружения всех дочерних процессов (агенты/скрипты/verify/другие хуки).

**Tech Stack:** Go (stdlib), существующие `pkg/lifecyclehooks`, `pkg/docker` (`secrets.go`/`launcher.go`/`wrapper.go`), `pkg/config`, `pkg/flow`, `cmd/afm/run.go`, `schema/*.json`.

**Spec:** `docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md` — «Phase 3 — секреты и Docker», «Пользовательские переменные окружения и секреты (Phase 3)», «Поведение в Docker (Phase 3)».

## Global Constraints

- Все коммиты — на русском, БЕЗ Co-Authored-By.
- Не менять версию Go в go.mod.
- После каждой задачи: `go build ./... && go vet ./...` чисто; `golangci-lint run` без НОВЫХ issues vs baseline; `GOOS=windows go build ./pkg/lifecyclehooks/` зелёный (fsync/env — переносимо; process-group уже в runner_unix/windows).
- Новое YAML-поле в hook (`env`, `inherit_env`) → обновить `schema/config.schema.json` И `schema/flow.schema.json` (оба содержат `lifecycleHook`); guard `pkg/schemacheck` зелёный.
- **Секреты не протекают:** резолвнутые значения не в payload, не в `hook_events`/`deliveries` (их нет — Phase 2 отложена), не в `*.log` (кроме как через редакцию), не в аргументах `sh -c`/`docker run`, не в сообщениях об ошибках AFM. Только в окружении конкретного hook-процесса.
- **Инвариант Phase 1 сохраняется:** ошибка хука не трогает FSM. Но НЕразрешённый секрет — это **ошибка конфигурации** (fail-fast ДО `flow_started`), а не best-effort: ран не должен стартовать с хуком, чей секрет не резолвится.
- **Дефолт окружения = minimal (по спеке, codex #4).** Фича lifecycle-хуков ещё НЕ релизнута (ветка `notify`, не запушена) — реальных пользователей, полагающихся на Phase 1-наследование, нет, поэтому «обратная совместимость с наследованием» неактуальна. Действуем буквально по спеке (`spec:457`): **по умолчанию hook-процесс НЕ наследует окружение AFM целиком** — минимальный набор (PATH/HOME/locale/tmp/proxy-certs) + `AFM_*` + резолвнутый `env`. Полное наследование — только явным `inherit_env: true`. Это единообразно для ВСЕХ хуков (с `env` и без), не зависит от наличия секретов.
- Префикс `AFM_` зарезервирован (проверка **регистронезависимо** — Windows env case-insensitive, codex #7); имена целевых переменных уникальны после ASCII case-fold; имя — `[A-Za-z_][A-Za-z0-9_]*`.

## Контракт окружения hook-процесса (по спеке)

- **По умолчанию (нет `inherit_env` или `inherit_env: false`)** → минимальное окружение (`minimalBaseEnv`) + `AFM_*` + резолвнутый `env`. Единообразно для всех хуков.
- **`inherit_env: true`** → `os.Environ()` + `AFM_*` + резолвнутый `env` (для legacy-скриптов, которым нужно полное окружение).
- Резолвнутый `env` и `AFM_*` всегда добавляются последними (перекрывают базовые одноимённые только для hook-процесса).
- Внутренние transport-переменные (`AFM_HOOK_SECRET_*`, autoShim `AFM_SECRET_*`/`AFM_SYSPROMPT_*`) ВСЕГДА вырезаются из окружения хука, даже при `inherit_env: true` (codex #2).

## File Structure

Новые файлы:
- `pkg/secrets/secrets.go` — `ResolveRef` (бывш. ResolveAuthValue), `LoadSecrets`, `LoadSecretLayers`, `ExpandHome`; нейтрально (без зависимости на pkg/docker).
- `pkg/secrets/secrets_test.go` — перенос docker secrets-тестов.
- `pkg/lifecyclehooks/env.go` — `SecretRef`, `ResolveHookEnv`, построение окружения (`buildHookEnv`), редактирующий writer (`redactingWriter`).
- `pkg/lifecyclehooks/env_test.go`.

Модифицируемые:
- `pkg/docker/secrets.go` — стать тонкой обёрткой над `pkg/secrets` (или удалить, обновив call sites launcher.go/wrapper.go/run.go); `ResolveSystemPrompt` остаётся в docker (docker-специфичен) либо тоже переезжает — по месту.
- `pkg/docker/launcher.go` — переиспользовать `pkg/secrets`; добавить транспорт секретов хуков (`AFM_HOOK_SECRET_<hookIdx>_<varIdx>`, инъективно по индексам), `docker.UsesLifecycleSecrets`.
- `pkg/orchestrator/stagefiles/completion.go` — `RunVerify` (прямой `exec.Command`) тоже вырезает transport-префиксы из окружения (codex #2).
- `pkg/lifecyclehooks/types.go` — `Hook.Env map[string]SecretRef`, `Hook.InheritEnv bool`, `Hook.ResolvedEnv map[string]string` (yaml:"-", runtime).
- `pkg/lifecyclehooks/validate.go` — валидация `env` (имя переменной, AFM_-префикс, префикс источника).
- `pkg/lifecyclehooks/runner.go` — `buildEnv` учитывает ResolvedEnv/InheritEnv/минимальное окружение; лог через `redactingWriter`.
- `cmd/afm/run.go` — загрузка `secrets.env`-слоёв, `ResolveHookEnv` per hook (fail-fast), проставление `ResolvedEnv`; в Docker — резолв на хосте + транзиентный env.
- `schema/config.schema.json`, `schema/flow.schema.json` — `env`/`inherit_env` в `lifecycleHook`.
- `AGENTS.md` — Phase 3 в разделе Lifecycle hooks.

Ссылки на код (проверено 2026-09-18):
- `pkg/docker/secrets.go` — `ResolveAuthValue`:55, `LoadSecrets`:24, `LoadSecretLayers`:103 (зависит от `config.AfmDir`), `homeDir`:15; `expandHome` — `pkg/docker/launcher.go:446`.
- autoShim транспорт секретов (образец для хуков): `pkg/docker/launcher.go:391-424` (`os.Setenv("AFM_SECRET_"+name, val)` + `args=append(args,"-e","AFM_SECRET_"+name)`), потребление/unset — `pkg/docker/wrapper.go` (`export <to>="$AFM_SECRET_<NAME>"; unset ...`). `envName` — `wrapper.go:18`.
- runner Phase 1: `buildEnv` — `pkg/lifecyclehooks/runner.go:27-46`; `execOne` (лог через `openAttemptLog`, `cmd.Stdout=logFile`) — runner.go:~80-116.
- `Hook`/`RegisteredHook` — `pkg/lifecyclehooks/types.go`; `ValidateLayer` — `pkg/lifecyclehooks/validate.go`.
- dispatcher сборка + `DispatcherOptions` + OnError/Outbox(nil) — `cmd/afm/run.go` (Phase 1 wiring).

---

### Task 1: Вынести резолвер секретов в нейтральный `pkg/secrets`

**Files:**
- Create: `pkg/secrets/secrets.go`, `pkg/secrets/secrets_test.go`
- Modify: `pkg/docker/secrets.go` (обёртки/делегация), call sites в `pkg/docker/launcher.go` (и `cmd/afm` если есть)

**Interfaces:**
- Produces:
  - `func secrets.ExpandHome(p, home string) string`.
  - `func secrets.LoadSecrets(paths []string) (map[string]string, error)`.
  - `func secrets.ResolveRef(ref string, loaded map[string]string) (string, error)` — `env:NAME` (loaded → os.Getenv) | `file:PATH` (read+TrimSpace); ошибка иначе. (Переименование `ResolveAuthValue`→`ResolveRef`, смысл тот же.)
  - `func secrets.LoadLayers(paths ...string) (map[string]string, error)` — тонкая обёртка над LoadSecrets (порядок: позже=приоритетнее). Config-зависимую `LoadSecretLayers(override, projectDir)` ОСТАВИТЬ в pkg/docker (она знает `config.AfmDir`) и переписать через `secrets.LoadSecrets` + `secrets.ExpandHome`.
- Consumes: только stdlib.

- [ ] **Step 1: Write the failing test** — перенести `TestResolveAuthValue`/`TestLoadSecrets*` из `pkg/docker/secrets_test.go` в `pkg/secrets/secrets_test.go`, адаптировав имена (`ResolveRef`, `ExpandHome`). Плюс явный тест `ExpandHome` (`~/x` → `<home>/x`, абсолютный не трогается, `~user` не раскрывается).

```go
package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRef_EnvAndFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "tok")
	os.WriteFile(fp, []byte("  file-secret\n"), 0o600)

	if v, err := ResolveRef("file:"+fp, nil); err != nil || v != "file-secret" {
		t.Fatalf("file: got %q err %v", v, err)
	}
	loaded := map[string]string{"K": "map-secret"}
	if v, _ := ResolveRef("env:K", loaded); v != "map-secret" {
		t.Fatalf("env from map: %q", v)
	}
	t.Setenv("K2", "proc-secret")
	if v, _ := ResolveRef("env:K2", nil); v != "proc-secret" {
		t.Fatalf("env from process: %q", v)
	}
	if _, err := ResolveRef("env:MISSING", nil); err == nil {
		t.Fatal("missing env must error")
	}
	if _, err := ResolveRef("plain", nil); err == nil {
		t.Fatal("unprefixed must error")
	}
	if _, err := ResolveRef("file:"+filepath.Join(dir, "nope"), nil); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestExpandHome(t *testing.T) {
	if got := ExpandHome("~/x", "/home/u"); got != "/home/u/x" {
		t.Fatalf("got %q", got)
	}
	if got := ExpandHome("/abs", "/home/u"); got != "/abs" {
		t.Fatalf("abs changed: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/secrets/ -v`
Expected: FAIL (пакета нет).

- [ ] **Step 3: Write implementation** — скопировать тело функций из `pkg/docker/secrets.go` в `pkg/secrets/secrets.go` (убрать зависимость на `pkg/config`: `LoadLayers` берёт готовые пути), перенести `expandHome`→`ExpandHome` (из launcher.go), `ResolveAuthValue`→`ResolveRef`. Затем в `pkg/docker`:
  - `secrets.go`: удалить перенесённые функции; оставить `LoadSecretLayers(override, projectDir)` переписанной через `secrets.LoadSecrets`+`config.AfmDir`; `homeDir` можно оставить или заменить на `os.UserHomeDir` inline. `ResolveSystemPrompt` — оставить (docker-специфичен) или тоже перенести (по вкусу; проще оставить).
  - Обновить call sites: `ResolveAuthValue(...)` → `secrets.ResolveRef(...)`; `expandHome(...)` в launcher.go → `secrets.ExpandHome(...)` (или оставить локальный алиас `func expandHome(p,h string) string { return secrets.ExpandHome(p,h) }`, чтобы не трогать много мест).
  - Перенести соответствующие docker secrets-тесты в pkg/secrets; в `pkg/docker/secrets_test.go` оставить только тесты `LoadSecretLayers`/`ResolveSystemPrompt`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/secrets/ ./pkg/docker/ -count=1 && go build ./... && go vet ./...`
Expected: PASS; docker-поведение не изменилось (тот же резолв, те же тесты LoadSecretLayers зелёные).

- [ ] **Step 5: Commit**

```bash
git add pkg/secrets/ pkg/docker/
git commit -m "refactor(secrets): вынести резолвер секретов в нейтральный pkg/secrets"
```

---

### Task 2: Поле `env`/`inherit_env` у хука + валидация + схема

**Files:**
- Modify: `pkg/lifecyclehooks/types.go`, `pkg/lifecyclehooks/validate.go`, `schema/config.schema.json`, `schema/flow.schema.json`
- Test: `pkg/lifecyclehooks/validate_test.go` (доп.), `pkg/flow/flow_test.go`/`pkg/config/config_test.go` (доп., парс)

**Interfaces:**
- Produces:
  - `type SecretRef string`.
  - `Hook.Env map[string]SecretRef` (yaml `env,omitempty`), `Hook.InheritEnv bool` (yaml `inherit_env,omitempty` — дефолт `false` = minimal-окружение, что и есть spec-дефолт; указатель не нужен), `Hook.ResolvedEnv map[string]string` (yaml:"-", проставляется в рантайме — Task 3).
  - `ValidateLayer` дополнительно проверяет `env`: имя целевой переменной матчит `^[A-Za-z_][A-Za-z0-9_]*$`; **регистронезависимо** не начинается с `AFM_` (codex #7 — Windows env case-insensitive); имена уникальны после ASCII upper-case (две ссылки `TOKEN`/`token` — ошибка); источник начинается с `env:`/`file:` с непустым хвостом. (Существование источника проверяется при резолве — Task 3.)

- [ ] **Step 1: Write the failing test**

```go
func TestValidateLayer_HookEnv(t *testing.T) {
	base := func(env map[string]SecretRef) []Hook {
		return []Hook{{ID: "h", Events: EventSelector{All: true}, Command: "true", Env: env}}
	}
	cases := []struct {
		name    string
		env     map[string]SecretRef
		wantErr bool
	}{
		{"ok env", map[string]SecretRef{"TOK": "env:TELEGRAM"}, false},
		{"ok file", map[string]SecretRef{"TOK": "file:~/.afm/secrets/x"}, false},
		{"unprefixed source", map[string]SecretRef{"TOK": "plain-secret"}, true},
		{"empty source tail", map[string]SecretRef{"TOK": "env:"}, true},
		{"AFM_ reserved", map[string]SecretRef{"AFM_X": "env:Y"}, true},
		{"AFM_ reserved lowercase", map[string]SecretRef{"afm_x": "env:Y"}, true}, // codex #7: case-insensitive
		{"bad var name", map[string]SecretRef{"1BAD": "env:Y"}, true},
		{"bad var name dash", map[string]SecretRef{"A-B": "env:Y"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLayer(base(tc.env), false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("env=%v err=%v wantErr=%v", tc.env, err, tc.wantErr)
			}
		})
	}
}

func TestValidateLayer_HookEnvCaseCollision(t *testing.T) {
	// codex #7: TOKEN и token коллизируют на Windows (case-insensitive env)
	h := []Hook{{ID: "h", Events: EventSelector{All: true}, Command: "true",
		Env: map[string]SecretRef{"TOKEN": "env:A", "token": "env:B"}}}
	if ValidateLayer(h, false) == nil {
		t.Fatal("case-colliding env var names must be rejected")
	}
}
```

Плюс parse-тест в pkg/flow: хук с `env:` и `inherit_env: true` парсится, поля заполнены.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run TestValidateLayer_HookEnv -v`
Expected: FAIL (полей/проверки нет).

- [ ] **Step 3: Write implementation**

`types.go` — расширить `Hook`:

```go
type SecretRef string

type Hook struct {
	ID         string               `yaml:"id"`
	Events     EventSelector        `yaml:"events"`
	SkipEvents []EventType          `yaml:"skip_events,omitempty"`
	Command    string               `yaml:"command"`
	Timeout    time.Duration        `yaml:"timeout,omitempty"`
	Retries    int                  `yaml:"retries,omitempty"`
	Env        map[string]SecretRef `yaml:"env,omitempty"`
	InheritEnv bool                 `yaml:"inherit_env,omitempty"` // дефолт false = minimal env (spec-дефолт)
	// ResolvedEnv — резолвнутые значения env (targetVar→value), проставляются
	// при сборке dispatcher (cmd/afm/run.go), в памяти. Не сериализуется, не
	// пишется в payload/логи. yaml:"-".
	ResolvedEnv map[string]string `yaml:"-"`
}
```

`validate.go` — в цикле по хукам добавить (после проверки command/events). Резерв `AFM_` и уникальность имён — регистронезависимо (codex #7):

```go
	seenUpper := make(map[string]bool, len(h.Env))
	for varName, ref := range h.Env {
		if !envVarNameRe.MatchString(varName) {
			return fmt.Errorf("hooks[%d] (%s): env var name %q must match [A-Za-z_][A-Za-z0-9_]*", i, h.ID, varName)
		}
		up := strings.ToUpper(varName)
		if strings.HasPrefix(up, "AFM_") {
			return fmt.Errorf("hooks[%d] (%s): env var name %q: prefix AFM_ is reserved (case-insensitive)", i, h.ID, varName)
		}
		if seenUpper[up] {
			return fmt.Errorf("hooks[%d] (%s): env var name %q collides case-insensitively with another", i, h.ID, varName)
		}
		seenUpper[up] = true
		s := string(ref)
		okEnv := strings.HasPrefix(s, "env:") && len(s) > len("env:")
		okFile := strings.HasPrefix(s, "file:") && len(s) > len("file:")
		if !okEnv && !okFile {
			return fmt.Errorf("hooks[%d] (%s): env %q source must be env:NAME or file:PATH, got %q", i, h.ID, varName, s)
		}
	}
```

с package-level `var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)` и импортом `regexp`/`strings`. Примечание: yaml.v3 отклоняет дублирующиеся ключи мэппинга, так что точный дубль `TOKEN`/`TOKEN` не дойдёт; case-collision `TOKEN`/`token` — дойдёт и ловится здесь.

`schema/config.schema.json` и `schema/flow.schema.json` — в `lifecycleHook.properties` добавить:

```json
"env": {
  "type": "object",
  "additionalProperties": { "type": "string" },
  "description": "Hook process env: target var -> env:NAME | file:PATH source reference"
},
"inherit_env": { "type": "boolean" }
```

(свериться со `pkg/schemacheck`: `Hook.Env`/`InheritEnv`/`ResolvedEnv` — reflection guard может потребовать спец-обработку `ResolvedEnv` (yaml:"-" → не в схеме) и `map[string]SecretRef` (object). Посмотреть, как guard трактует yaml:"-" поля и map-типы; при необходимости — special-case по прецеденту `eventSelectorType`/`buttonsType`. `InheritEnv *bool` → boolean.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/lifecyclehooks/ ./pkg/flow/ ./pkg/config/ ./pkg/schemacheck/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/ pkg/flow/ pkg/config/ schema/
git commit -m "feat(lifecyclehooks): поле env/inherit_env у хука с валидацией и схемой"
```

---

### Task 3: Резолв секретов при сборке dispatcher (host, fail-fast) + secrets.env приоритеты

**Files:**
- Create: `pkg/lifecyclehooks/env.go` (часть: `ResolveHookEnv`)
- Modify: `cmd/afm/run.go`
- Test: `pkg/lifecyclehooks/env_test.go`, (доп.) `cmd/afm` мини-тест

**Interfaces:**
- Consumes: Task 1 (`secrets.ResolveRef`/`LoadSecrets`), Task 2 (`Hook.Env`).
- Produces:
  - `func ResolveHookEnv(h Hook, loaded map[string]string) (map[string]string, error)` — резолвит каждую пару `env`; ошибка с hook id + именем переменной, если источник не найден/пуст. Пустой `env` → `nil, nil`.
  - Wiring в `cmd/afm/run.go`: загрузить слои `secrets.env` (project `<afm-root>/.afm/secrets.env` приоритетнее global `~/.afm/secrets.env`, затем env процесса — порядок как в `secrets.ResolveRef`), для каждого собранного (`Combine`) хука вызвать `ResolveHookEnv`, проставить `rh.Hook.ResolvedEnv`; ЛЮБАЯ ошибка → вернуть из `runHandler` (ран не стартует, до `flow_started`).

- [ ] **Step 1: Write the failing test**

```go
func TestResolveHookEnv(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "chat")
	os.WriteFile(fp, []byte("-100500\n"), 0o600)
	loaded := map[string]string{"TELEGRAM": "bot-token"}
	h := Hook{ID: "tg", Env: map[string]SecretRef{
		"TELEGRAM_BOT_TOKEN": "env:TELEGRAM",
		"TELEGRAM_CHAT_ID":   SecretRef("file:" + fp),
	}}
	got, err := ResolveHookEnv(h, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if got["TELEGRAM_BOT_TOKEN"] != "bot-token" || got["TELEGRAM_CHAT_ID"] != "-100500" {
		t.Fatalf("resolved: %v", got)
	}
}

func TestResolveHookEnv_MissingIsError(t *testing.T) {
	h := Hook{ID: "tg", Env: map[string]SecretRef{"X": "env:NOPE_MISSING"}}
	_, err := ResolveHookEnv(h, nil)
	if err == nil {
		t.Fatal("missing secret must fail-fast")
	}
	if !strings.Contains(err.Error(), "tg") || !strings.Contains(err.Error(), "X") {
		t.Fatalf("error must name hook id and var: %v", err)
	}
	// значение секрета НЕ должно попадать в текст ошибки — источник может быть в проце
}

func TestResolveHookEnv_EmptyNil(t *testing.T) {
	got, err := ResolveHookEnv(Hook{ID: "h"}, nil)
	if err != nil || got != nil {
		t.Fatalf("empty env → nil,nil; got %v %v", got, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run TestResolveHookEnv -v`
Expected: FAIL.

- [ ] **Step 3: Write implementation (`env.go`)**

```go
package lifecyclehooks

import (
	"fmt"

	"github.com/akopichin/afm/pkg/secrets"
)

// ResolveHookEnv резолвит env-ссылки хука в конкретные значения (targetVar→value)
// через pkg/secrets. Пустой env → nil. Ошибка (источник не найден/пуст) называет
// hook id и имя целевой переменной, но НЕ значение секрета.
func ResolveHookEnv(h Hook, loaded map[string]string) (map[string]string, error) {
	if len(h.Env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(h.Env))
	for varName, ref := range h.Env {
		val, err := secrets.ResolveRef(string(ref), loaded)
		if err != nil {
			return nil, fmt.Errorf("hook %q env %q: %w", h.ID, varName, err)
		}
		out[varName] = val
	}
	return out, nil
}
```

(Убедиться, что `secrets.ResolveRef` не включает значение в текст ошибки — Task 1 это гарантирует: ошибки типа «secret not found: env:NAME».)

`cmd/afm/run.go` — в блоке сборки хуков, ПОСЛЕ `Combine`, ДО `New`. `secrets.env` грузим ТОЛЬКО если хотя бы у одного собранного хука непустой `Env` (codex #8 — иначе нечитаемый secrets.env заблокировал бы чистый Phase 1-хук без секретов):

```go
			anyEnv := false
			for i := range combined {
				if len(combined[i].Hook.Env) > 0 {
					anyEnv = true
					break
				}
			}
			var hookSecrets map[string]string
			if anyEnv {
				var serr error
				// Слои: project .afm/secrets.env > global ~/.afm/secrets.env > env
				// процесса (порядок обеспечивает secrets.ResolveRef: сначала loaded
				// map, затем os.Getenv; в loaded проектный слой перекрывает глобальный).
				if hookSecrets, serr = loadHookSecretLayers(rootDir); serr != nil {
					return fmt.Errorf("lifecycle hooks: load secrets.env: %w", serr)
				}
			}
			for i := range combined {
				if len(combined[i].Hook.Env) == 0 {
					continue // чистый Phase 1-хук: секретов нет, слои не нужны
				}
				resolved, rerr := lifecyclehooks.ResolveHookEnv(combined[i].Hook, hookSecrets)
				if rerr != nil {
					return fmt.Errorf("lifecycle hooks: %w", rerr) // fail-fast до flow_started
				}
				combined[i].Hook.ResolvedEnv = resolved
			}
```

`loadHookSecretLayers(afmRoot)` — хелпер рядом (в run.go или agent_environment.go): `secrets.LoadSecrets([]string{ filepath.Join(homeAfmDir,"secrets.env"), filepath.Join(afmRoot, ".afm/secrets.env") })` (project последним → приоритет). Свериться с существующим `docker.LoadSecretLayers` для путей.

(Docker-режим: резолв секретов хуков делает ХОСТ до `docker run`, значения доезжают транзиентными переменными; in-container этот блок читает их из транспорта, а не из файлов — см. Task 5. Combine выносится ДО docker-ветки — Task 5, codex #3.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/lifecyclehooks/ -run TestResolveHookEnv -v && go build ./cmd/... && go test ./cmd/afm/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/env.go pkg/lifecyclehooks/env_test.go cmd/afm/
git commit -m "feat(lifecyclehooks): резолв env-секретов хука при старте (fail-fast, secrets.env слои)"
```

---

### Task 4: Runner — окружение hook-процесса (minimal/inherit + резолвнутый env) + `[REDACTED]`

**Files:**
- Modify: `pkg/lifecyclehooks/runner.go`; вынести редактирующий writer в `env.go` (или `redact.go`)
- Test: `pkg/lifecyclehooks/runner_test.go` (доп.), `pkg/lifecyclehooks/env_test.go` (redact)

**Interfaces:**
- Consumes: Task 3 (`Hook.ResolvedEnv`, `Hook.InheritEnv`).
- Produces:
  - `buildEnv` (spec-дефолт minimal): `InheritEnv==false` (дефолт) → `minimalBaseEnv()`; `InheritEnv==true` → `stripTransportVars(os.Environ())`. Затем всегда + `AFM_*` + `ResolvedEnv` последними (перекрывают базовые). Transport-префиксы не попадают ни в один режим.
  - `func minimalBaseEnv() []string` — PATH, HOME, locale (LANG/LC_*), TMPDIR, proxy/certs (HTTP(S)_PROXY/NO_PROXY/SSL_CERT_*), извлечённые из `os.Environ()`.
  - `type redactingWriter struct{...}` — io.Writer, заменяющий вхождения известных секретных значений на `[REDACTED]` перед записью в лог. Буферизует хвост длиной max(len(secret)) для стыков между Write-ами.

- [ ] **Step 1: Write the failing test**

```go
func TestBuildEnv_Modes(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("UNRELATED_KEY", "leak-me")
	t.Setenv("AFM_HOOK_SECRET_0_0", "transport-secret") // должен вырезаться при inherit
	cfg := DispatcherConfig{RunID: "r"}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted}, "id")

	// spec-дефолт: без inherit_env → минимальное окружение (UNRELATED НЕ виден)
	e1 := buildEnv(Hook{ID: "h"}, cfg, p)
	if hasEnvKey(e1, "UNRELATED_KEY") {
		t.Fatal("default must be minimal env (no ambient leakage)")
	}
	if !hasEnvKey(e1, "PATH") || !hasEnv(e1, "AFM_HOOK_EVENT", "flow_started") {
		t.Fatalf("minimal must include PATH + AFM_*: %v", e1)
	}
	// с Env → минимальное + резолвнутый секрет
	e2 := buildEnv(Hook{ID: "h", Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "secret"}}, cfg, p)
	if hasEnvKey(e2, "UNRELATED_KEY") || !hasEnv(e2, "T", "secret") {
		t.Fatalf("env-hook: minimal + resolved: %v", e2)
	}
	// inherit_env: true → наследование + resolved, но БЕЗ transport-переменных
	e3 := buildEnv(Hook{ID: "h", InheritEnv: true, Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "secret"}}, cfg, p)
	if !hasEnv(e3, "UNRELATED_KEY", "leak-me") || !hasEnv(e3, "T", "secret") {
		t.Fatal("inherit_env:true must inherit AND include resolved")
	}
	if hasEnvKey(e3, "AFM_HOOK_SECRET_0_0") {
		t.Fatal("inherit_env:true must still strip transport vars (codex #2)")
	}
}

func TestRedactingWriter(t *testing.T) {
	var buf bytes.Buffer
	w := newRedactingWriter(&buf, []string{"topsecret"})
	// секрет, разорванный между двумя Write, тоже редактируется
	w.Write([]byte("before top"))
	w.Write([]byte("secret after"))
	w.Close()
	out := buf.String()
	if strings.Contains(out, "topsecret") {
		t.Fatalf("secret leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("no redaction marker: %q", out)
	}
	if !strings.Contains(out, "before ") || !strings.Contains(out, " after") {
		t.Fatalf("non-secret text mangled: %q", out)
	}
}

func TestRunCommand_RedactsSecretInLog(t *testing.T) {
	dir := t.TempDir()
	h := Hook{ID: "h", Command: "printf 'my token is s3cr3t\\n'", Timeout: 5 * time.Second,
		Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "s3cr3t"}}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "id")
	logPath := filepath.Join(dir, "h.log")
	if err := runCommand(context.Background(), h, cfg, p, logPath); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), "s3cr3t") {
		t.Fatalf("secret leaked into log: %s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("expected redaction: %s", raw)
	}
}
```

(Хелперы `hasEnv`/`hasEnvKey` — проверка `KEY=VAL`/`KEY=` в срезе.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run 'TestBuildEnv_Modes|TestRedactingWriter|TestRunCommand_RedactsSecret' -v`
Expected: FAIL.

- [ ] **Step 3: Write implementation**

`runner.go` — переписать `buildEnv(h Hook, cfg, p)` (сигнатура меняется: теперь берёт `Hook`, не только cfg/p):

```go
func buildEnv(h Hook, cfg DispatcherConfig, p Payload) []string {
	var env []string
	if h.InheritEnv {
		env = stripTransportVars(os.Environ()) // полное наследование, но без transport-переменных (codex #2)
	} else {
		env = minimalBaseEnv() // spec-дефолт: минимальное окружение (whitelist — transport не тащится)
	}
	env = append(env, afmVars(cfg, p)...) // AFM_* (вынести из старого buildEnv)
	for k, v := range h.ResolvedEnv {
		env = append(env, k+"="+v) // резолвнутые секреты — последними (перекрывают)
	}
	return env
}

// stripTransportVars убирает внутренние transport-переменные из унаследованного
// окружения (codex #2): секреты хуков/agent-shim не должны течь в hook-процесс.
func stripTransportVars(env []string) []string {
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "AFM_HOOK_SECRET_") || strings.HasPrefix(kv, "AFM_SECRET_") || strings.HasPrefix(kv, "AFM_SYSPROMPT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
```

> Примечание: `stripTransportVars` (или эквивалент) нужен и в общей точке формирования окружения агентов/скриптов/verify (executor/runner_factory) — Task 5, codex #2. Здесь — только для hook при `inherit_env:true`; `minimalBaseEnv` (whitelist) transport не включает.

func minimalBaseEnv() []string {
	keys := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR"}
	var out []string
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
```

`afmVars(cfg, p) []string` — вынести существующий блок `AFM_*` из Phase 1 `buildEnv` в отдельную функцию (без изменения значений). Обновить вызов в `execOne`: `cmd.Env = buildEnv(h, cfg, p)`.

`execOne` — обернуть лог в редактирующий writer, если у хука есть секреты. **finish-строка и текст ошибки ТОЖЕ идут через редактор** (codex #5 — иначе секрет из stderr агента, попавший в `err`, утечёт в лог и через `OnError` в `notices.jsonl`):

```go
	logFile, logErr := openAttemptLog(logPath, p, attempt)
	var sink io.Writer = logFile
	var redactor *redactingWriter
	if logErr == nil {
		if vals := secretValues(h); len(vals) > 0 {
			redactor = newRedactingWriter(logFile, vals)
			sink = redactor
		}
		cmd.Stdout = sink
		cmd.Stderr = sink
	}
	err = cmd.Run()
	if logErr == nil {
		fmt.Fprintf(sink, "=== attempt %d finished: %v ===\n", attempt, err) // finish через редактор
		if redactor != nil {
			redactor.Close() // флаш хвоста в logFile
		}
		logFile.Close()
	}
	// Санитизировать ошибку ДО возврата (она уходит в OnError → notices.jsonl).
	if err != nil {
		err = errors.New(redactString(err.Error(), secretValues(h)))
	}
	return err
```

Санитизация ошибки: `redactString(s, secrets)` = применить `redactAll` к строке (та же замена, что в writer). Если у хука нет секретов — `secretValues` пуст, `redactString` — no-op.

(Аккуратно с закрытием: `redactor.Close()` сбрасывает буферизованный хвост в `logFile`, ПОТОМ `logFile.Close()`. finish-строка пишется в `sink` (редактор при наличии секретов), т.е. до `Close()`.)

`env.go`/`redact.go` — `redactingWriter`:

```go
type redactingWriter struct {
	w       io.Writer
	secrets []string // дедуп, отсортированы по убыванию длины
	marker  string
	maxLen  int
	buf     []byte // накопитель: заменяем полные вхождения, придерживаем хвост-префикс
}

func newRedactingWriter(w io.Writer, secrets []string) *redactingWriter {
	// дедуп + сорт по убыванию длины (длинные секреты заменяем первыми: если
	// короткий секрет — подстрока длинного, длинный уже редактирован).
	seen := map[string]bool{}
	nonEmpty := make([]string, 0, len(secrets))
	max := 0
	for _, s := range secrets {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		nonEmpty = append(nonEmpty, s)
		if len(s) > max {
			max = len(s)
		}
	}
	sort.Slice(nonEmpty, func(i, j int) bool { return len(nonEmpty[i]) > len(nonEmpty[j]) })
	return &redactingWriter{w: w, secrets: nonEmpty, marker: redactMarker(nonEmpty), maxLen: max}
}

// Write накапливает, заменяет все ПОЛНЫЕ вхождения секретов, затем придерживает
// самый длинный суффикс (≤ maxLen-1), который является ПРЕФИКСОМ какого-либо
// секрета (возможное начало секрета, разорванного на границе Write). Остальное
// сбрасывает в w. Корректно ловит секрет, разорванный между Write-ами.
func (r *redactingWriter) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	r.buf = redactAll(r.buf, r.secrets, r.marker) // заменить все полные вхождения
	hold := r.longestSecretPrefixSuffix()          // сколько байт хвоста придержать
	flush := len(r.buf) - hold
	if flush > 0 {
		if _, err := r.w.Write(r.buf[:flush]); err != nil {
			return 0, err
		}
		r.buf = append(r.buf[:0], r.buf[flush:]...)
	}
	return len(p), nil
}

// longestSecretPrefixSuffix возвращает длину самого длинного суффикса r.buf,
// который равен собственному префиксу какого-либо секрета (кандидат на
// разорванное вхождение). ≤ maxLen-1.
func (r *redactingWriter) longestSecretPrefixSuffix() int {
	max := 0
	for _, s := range r.secrets {
		lim := len(s) - 1
		if lim > len(r.buf) {
			lim = len(r.buf)
		}
		for k := lim; k > max; k-- {
			if bytes.HasPrefix([]byte(s), r.buf[len(r.buf)-k:]) {
				if k > max {
					max = k
				}
				break
			}
		}
	}
	return max
}

func (r *redactingWriter) Close() error {
	if len(r.buf) > 0 {
		// хвост — только частичный префикс секрета (полные вхождения уже
		// заменены), поэтому пишем как есть после финальной замены на всякий случай.
		if _, err := r.w.Write(redactAll(r.buf, r.secrets, r.marker)); err != nil {
			return err
		}
		r.buf = nil
	}
	return nil
}
```

> Корректность: полные вхождения заменяются на КАЖДОМ Write по всему накопителю; наружу отдаются только байты, за которыми не может начинаться разорванный секрет (придержан суффикс-префикс). Секрет `topsecret`, пришедший одним `Write("topsecret")`: `redactAll` заменит его целиком сразу (полное вхождение) → в `w` уйдёт marker. Секрет, разорванный `Write("top")`+`Write("secret")`: после первого Write `buf="top"`, это префикс секрета → придержан весь; после второго `buf="topsecret"` → заменён. Импорты: `bytes`, `sort`.

// redactMarker выбирается так, чтобы САМ не содержать ни одного секрета
// (codex #6: секрет "REDACTED"/"["/"]" сделал бы обычный "[REDACTED]" носителем
// секрета). Fallback — пустая строка (удаление), если даже расширенные маркеры
// содержат секрет.
func redactMarker(secrets []string) string {
	for _, cand := range []string{"[REDACTED]", "[REDACTED-SECRET]", "***REDACTED***"} {
		if !containsAnySecret(cand, secrets) {
			return cand
		}
	}
	return "" // ни один маркер не безопасен → просто вырезаем секрет
}

func containsAnySecret(s string, secrets []string) bool {
	for _, sec := range secrets {
		if sec != "" && strings.Contains(s, sec) {
			return true
		}
	}
	return false
}

func redactAll(b []byte, secrets []string, marker string) []byte {
	for _, s := range secrets {
		if s == "" {
			continue
		}
		b = bytes.ReplaceAll(b, []byte(s), []byte(marker))
	}
	return b
}

func redactString(s string, secrets []string) string {
	if len(secrets) == 0 {
		return s
	}
	return string(redactAll([]byte(s), secrets, redactMarker(secrets)))
}

func secretValues(h Hook) []string {
	out := make([]string, 0, len(h.ResolvedEnv))
	for _, v := range h.ResolvedEnv {
		out = append(out, v)
	}
	return out
}
```

`redactingWriter` хранит выбранный `marker` (из `redactMarker(secrets)` в `newRedactingWriter`) и передаёт его в `redactAll`. Важно: длина маркера может отличаться от длины секрета — это не влияет на корректность (замена по значению), только на выравнивание в логе.

(Хвост-буфер: keep = maxLen-1, последние maxLen-1 байт не пишутся до следующего Write/Close — гарантирует, что любое вхождение секрета целиком проходит через `redactAll` на границе Write. Пустые секреты игнорируются (иначе `ReplaceAll` по "" разорвал бы текст). Ограничение: редакция — защита от `set -x`/`echo`, не абсолютная (скрипт может закодировать секрет).)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/lifecyclehooks/ -race -count=1 && GOOS=windows go build ./pkg/lifecyclehooks/`
Expected: PASS; windows-сборка чиста.

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/
git commit -m "feat(lifecyclehooks): окружение hook-процесса (minimal/inherit + секреты) и [REDACTED] в логах"
```

---

### Task 5: Docker-транспорт секретов хуков

**Files:**
- Modify: `pkg/docker/launcher.go` (резолв на хосте + транзиентный env + `UsesLifecycleSecrets`), возможно `pkg/docker/wrapper.go`; `cmd/afm/run.go` (in-container чтение транспорта)
- Test: `pkg/docker/launcher_test.go` (доп.), `pkg/lifecyclehooks`/`cmd/afm` (in-container резолв)

**Interfaces:**
- Consumes: Tasks 1-3; образец autoShim-транспорта (`launcher.go:391-424`, `wrapper.go`).
- Produces: в Docker-режиме секреты хуков резолвятся ХОСТОМ до `docker run` и передаются транзиентными bare `-e AFM_HOOK_SECRET_<hookIdx>_<varIdx>` (инъективно по индексам итогового `combined`, codex #1); in-container путь берёт значения из этих переменных (не из файлов/secrets.env), делает `Unsetenv`; транспортные переменные вырезаются из окружения агентов/скриптов/verify/других хуков (codex #2).

> ВАЖНО: перед реализацией прочитать `pkg/docker/launcher.go` (ReExec, блок autoShim-секретов :391-424, dockerForwardEnvVars, сборку `-e`), `pkg/docker/wrapper.go` (`envName`, unset-паттерн) и docker-режим в `cmd/afm/run.go` (как флоу/конфиг доезжает в контейнер, `AFM_IN_DOCKER`). Реализация должна ЗЕРКАЛИТЬ существующий AFM_SECRET_<CMD>-паттерн, а не изобретать новый.

- [ ] **Step 1: Design + failing test**

**Инъективное транспортное имя (codex #1):** НЕ санитизировать hookID (разные id `foo-bar`/`foo_bar`/`FOO_BAR` схлопнулись бы в один suffix → один хук получил бы секрет другого). Использовать ИНДЕКСЫ в итоговом `combined`: `AFM_HOOK_SECRET_<hookIdx>_<varIdx>`, где `hookIdx` — позиция хука в `combined`, `varIdx` — позиция переменной в отсортированном по имени списке ключей `Env` (детерминированно). Индекс инъективен по построению. Соответствие (hookIdx,varIdx)→имя целевой переменной вычисляется одинаково на хосте и в контейнере из ТОГО ЖЕ `combined` (см. #3 — Combine общий).

**Порядок (codex #3):** сборку слоёв хуков и `Combine(...)` вынести ДО docker-ветки в `cmd/afm/run.go`, чтобы и host-резолв, и передача в контейнер работали по ИТОГОВЫМ `RegisteredHook` (с учётом override по id), а не по сырым cfg/flow/stage (иначе секреты переопределённых хуков утекут, least-privilege нарушится). `ReExec`/`ReExecConfig` получают итоговый `[]RegisteredHook` (или уже резолвнутые пары для транспорта).

Хост (docker-ветка):
1. Если ни у одного хука в `combined` нет `Env` — ничего не делаем.
2. Загрузить `secrets.env`-слои + `ResolveHookEnv` каждый хук (fail-fast ДО `docker run`); для каждой резолвнутой пары `os.Setenv(transportName, val)` + `args=append(args,"-e",transportName)` (bare, без значения в argv — как autoShim `launcher.go:412`).
3. Секрет-файлы НЕ монтировать в контейнер.

In-container (`AFM_IN_DOCKER=1`): резолвить `Hook.Env` НЕ через файлы, а из транспортных переменных — `ResolvedEnv[targetVar] = os.Getenv(transportName(hookIdx,varIdx))`; отсутствие/пусто → ошибка (fail-fast). **Затем ОБЯЗАТЕЛЬНО `os.Unsetenv(transportName)` для ВСЕХ транспортных переменных** (codex #2 — не «unset ИЛИ фильтр», а unset всегда), чтобы транспорт не наследовался никакими дочерними процессами (агенты, script-стадии, verify/JSONQuery, другие хуки).

**Дополнительно (codex #2):** в построении окружения ЛЮБОГО дочернего процесса, наследующего окружение (агенты/скрипты — executor/runner_factory; verify — stagefiles.RunVerify/completion.go, прямой exec.Command; и hook при `inherit_env:true`), вырезать все внутренние transport-префиксы: `AFM_HOOK_SECRET_*`, а также уже существующие autoShim `AFM_SECRET_*`/`AFM_SYSPROMPT_*` (сейчас их снимают только generated-wrappers; обычные агенты/скрипты их наследуют — закрыть). `minimalBaseEnv` их не тащит по построению (whitelist), но `inherit_env:true`-хук и агенты с полным окружением — тащат.

Тест (host, unit — как существующие `TestReExec_*`): флоу с двумя хуками, у обоих `env: {TOK: ...}`, id различаются только пунктуацией/регистром → транспортные имена РАЗНЫЕ (инъективность); `-e <transport>` присутствует bare; значение в `os.Environ` хоста, НЕ в argv; секрет-файл НЕ среди `-v`. In-container unit: `resolveHookEnvFromTransport(combined)` даёт правильный `ResolvedEnv` и делает Unsetenv транспортных.

```go
func TestReExec_HookSecretTransport(t *testing.T) {
	// собрать LaunchConfig с флоу, где есть хук с env; проверить args:
	//  - содержит "-e" + транспортное имя (bare, без "=value")
	//  - значение доступно через os.Getenv(транспортное имя)
	//  - argv НЕ содержит самого значения секрета
	//  - секрет-файл не появляется среди -v mounts
	// (следовать структуре существующих TestReExec_* тестов)
}
```

In-container резолв — unit-тест: при `AFM_IN_DOCKER=1` и выставленных транспортных переменных `resolveHookEnvInContainer(hook)` даёт правильный `ResolvedEnv` и ошибку при отсутствии.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run TestReExec_HookSecret -v`
Expected: FAIL.

- [ ] **Step 3: Implement** — по образцу autoShim-секретов (`launcher.go:391-424`). Общий хелпер `transportName(hookIdx, varIdx int) string` (в pkg/lifecyclehooks или pkg/docker, доступный обоим). Развилка host vs in-container в `cmd/afm/run.go`: `os.Getenv("AFM_IN_DOCKER")=="1"` → `ResolvedEnv` из транспортных переменных + `os.Unsetenv` каждой; иначе → `secrets.env`+файлы (Task 3). Combine вынесен ДО docker-ветки (#3). Фильтрацию transport-префиксов (`AFM_HOOK_SECRET_*`, `AFM_SECRET_*`, `AFM_SYSPROMPT_*`) из наследуемого окружения сделать во ВСЕХ spawn-точках (executor/runner_factory И stagefiles.RunVerify/completion.go — прямой exec.Command, codex #2) И в `buildEnv` хука при `inherit_env:true`. Тесты покрыть: agent, script, verify, no-env-hook, inherit-hook — ни один не видит транспортных переменных.

- [ ] **Step 4: Run tests + сборка**

Run: `go test ./pkg/docker/ ./pkg/lifecyclehooks/ ./cmd/afm/ -count=1 && go build ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/docker/ pkg/lifecyclehooks/ cmd/afm/
git commit -m "feat(docker): транспорт секретов lifecycle-хуков в контейнер без монтирования файлов"
```

---

### Task 6: Документация AGENTS.md (Phase 3) + финальная верификация

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Обновить раздел «Lifecycle hooks»** — дописать Phase 3: поле `env` (`env:`/`file:`), `inherit_env`, `secrets.env` слои (project>global>env процесса), `pkg/secrets`, минимальное окружение vs наследование (уточнение контракта!), `[REDACTED]`, Docker-транспорт (`AFM_HOOK_SECRET_*`, без монтирования файлов, фильтрация из агентов), fail-fast резолва до `flow_started`, «секреты не в payload/логах/argv». Пример YAML (telegram).

- [ ] **Step 2: Финальная верификация**

```bash
make lint
make test
GOOS=windows go build ./pkg/lifecyclehooks/
cd pkg/web/dashboard && npx vitest run
```
Expected: lint 0; Go зелёный (учесть известный флейк `TestDefaultExecFunc_ForwardsSignalToChild` в pkg/docker); windows-сборка чиста; frontend зелёный.

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md
git commit -m "docs: секреты и env lifecycle-хуков (Phase 3) в AGENTS.md"
```

---

## Self-Review + учёт ревью codex (2 CRITICAL + 6 MAJOR — все внесены)

- **Spec coverage:** env со ссылками env:/file: (T2 валидация, T3 резолв); вынос pkg/secrets + рефактор docker (T1); secrets.env приоритеты project>global>env (T3); имя переменной + AFM_-резерв + fail-fast до flow_started (T2/T3); minimal-by-default + inherit_env (T4, ПО СПЕКЕ); [REDACTED] в логах/dashboard (T4); Docker-транспорт без монтирования + фильтрация (T5); секреты не в payload/journal/argv/логах (T3/T4/T5); докум. (T6).
- **Внесённые фиксы codex:**
  - #1 CRIT (Docker имя не инъективно) → транспорт по индексам `AFM_HOOK_SECRET_<hookIdx>_<varIdx>`, не sanitize(id). T5.
  - #2 CRIT (изоляция транспорта) → ВСЕГДА `os.Unsetenv` транспортных in-container + `stripTransportVars` в наследуемом окружении агентов/скриптов/verify/inherit-хука. T4/T5.
  - #3 MAJOR (Combine до docker-ветки) → сборка слоёв+Combine ДО docker, в ReExec идут итоговые RegisteredHook. T5/T3.
  - #4 MAJOR (дефолт окружения) → принят spec-дефолт minimal-by-default (фича не релизнута → нет регрессии). T4, раздел «Контракт окружения».
  - #5 MAJOR (ошибка/finish мимо редактора) → finish и текст ошибки через редактор/`redactString`, ошибка санитизируется до OnError→notices.jsonl. T4.
  - #6 MAJOR (маркер содержит секрет) → `redactMarker` выбирает маркер без секрета, fallback — удаление. T4.
  - #7 MAJOR (AFM_ регистрозависимо) → резерв+уникальность по ASCII upper-case. T2.
  - #8 MAJOR (secrets.env для Phase 1-хуков) → слои грузятся только если хоть у одного хука непустой Env. T3.
- **Placeholder scan:** код для T1-T4 полный; T5 (Docker) на уровне дизайна + требование прочитать/зеркалить autoShim (launcher.go:391-424/wrapper.go).
- **Type consistency:** `secrets.ResolveRef/LoadSecrets/ExpandHome`, `Hook.Env map[string]SecretRef`/`InheritEnv bool`/`ResolvedEnv map[string]string`, `ResolveHookEnv(h,loaded)`, `buildEnv(h,cfg,p)`, `minimalBaseEnv()`, `stripTransportVars`, `newRedactingWriter(w,secrets)`/`redactAll(b,secrets,marker)`/`redactString`/`redactMarker`, `transportName(hookIdx,varIdx)` — единообразны.
- **Инвариант:** нерезолвнутый секрет = config-error fail-fast (до flow_started); ошибка самого хука не трогает FSM.
