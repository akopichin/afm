# Lifecycle-хуки Phase 3 — Implementation Plan (секреты и env)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Дать lifecycle-хукам безопасную доставку секретов/переменных: поле `env` со ссылками `env:`/`file:` (та же семантика, что `auth.from`), вынос резолвера в нейтральный `pkg/secrets`, минимальное окружение hook-процесса по умолчанию + `inherit_env`, редакцию `[REDACTED]` в логах/dashboard, приоритеты `secrets.env` (project > global > env процесса), и Docker-транспорт секретов без монтирования файлов в контейнер.

**Architecture:** Резолвер секретов (`ResolveAuthValue`/`LoadSecrets`/`LoadSecretLayers`/`expandHome`) переезжает из `pkg/docker` в нейтральный `pkg/secrets`; `pkg/docker` и lifecycle-хуки используют его. Хук получает `env map[string]SecretRef` и `inherit_env bool`. Секреты резолвятся ОДИН раз при сборке dispatcher (host, `cmd/afm/run.go`), до `flow_started`, fail-fast с указанием hook id + имени переменной; резолвнутые значения живут только в памяти (`RegisteredHook.ResolvedEnv`), не пишутся в payload/логи/journal. Runner строит окружение hook-процесса: минимальный базовый набор (PATH/HOME/locale/tmp/proxy-certs) + `AFM_*` + резолвнутый `env` (или полный `os.Environ()` при `inherit_env: true`), и оборачивает лог в редактирующий writer, заменяющий известные значения на `[REDACTED]`. В Docker-режиме host-launcher резолвит ссылки до `docker run`, передаёт значения транзиентными bare `-e AFM_SECRET_<hook>_<VAR>`; in-container резолвер читает их вместо файлов, transport-переменные фильтруются из окружения агентов/скриптов/других хуков.

**Tech Stack:** Go (stdlib), существующие `pkg/lifecyclehooks`, `pkg/docker` (`secrets.go`/`launcher.go`/`wrapper.go`), `pkg/config`, `pkg/flow`, `cmd/afm/run.go`, `schema/*.json`.

**Spec:** `docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md` — «Phase 3 — секреты и Docker», «Пользовательские переменные окружения и секреты (Phase 3)», «Поведение в Docker (Phase 3)».

## Global Constraints

- Все коммиты — на русском, БЕЗ Co-Authored-By.
- Не менять версию Go в go.mod.
- После каждой задачи: `go build ./... && go vet ./...` чисто; `golangci-lint run` без НОВЫХ issues vs baseline; `GOOS=windows go build ./pkg/lifecyclehooks/` зелёный (fsync/env — переносимо; process-group уже в runner_unix/windows).
- Новое YAML-поле в hook (`env`, `inherit_env`) → обновить `schema/config.schema.json` И `schema/flow.schema.json` (оба содержат `lifecycleHook`); guard `pkg/schemacheck` зелёный.
- **Секреты не протекают:** резолвнутые значения не в payload, не в `hook_events`/`deliveries` (их нет — Phase 2 отложена), не в `*.log` (кроме как через редакцию), не в аргументах `sh -c`/`docker run`, не в сообщениях об ошибках AFM. Только в окружении конкретного hook-процесса.
- **Инвариант Phase 1 сохраняется:** ошибка хука не трогает FSM. Но НЕразрешённый секрет — это **ошибка конфигурации** (fail-fast ДО `flow_started`), а не best-effort: ран не должен стартовать с хуком, чей секрет не резолвится.
- **Обратная совместимость:** хук без `env`/`inherit_env` ведёт себя как в Phase 1 (наследует окружение AFM). `env` пуст → минимальное окружение НЕ включается (чтобы не ломать Phase 1-хуки, полагающиеся на наследование) — см. Task 4 (минимальное окружение применяется ТОЛЬКО когда задан `env` или явно `inherit_env: false`; по умолчанию без `env` — наследование, как в Phase 1). Это уточнение спеки: минимальное окружение — свойство хука с секретами, не глобальная смена поведения.
- Префикс `AFM_` зарезервирован; имя целевой переменной — `[A-Za-z_][A-Za-z0-9_]*`.

## Уточнение контракта (решение по умолчанию окружения)

Спека говорит «по умолчанию (Phase 3) hook-процесс не наследует окружение целиком». Но Phase 1 уже релизнут с наследованием, и live-проверка полагалась на него. Во избежание тихой регрессии Phase 1-хуков принимается уточнение:

- Хук **без** `env` и без явного `inherit_env` → **наследует окружение AFM** (поведение Phase 1, неизменно).
- Хук с непустым `env` → **минимальное окружение** (PATH/HOME/locale/tmp/proxy-certs) + `AFM_*` + резолвнутый `env`, если не задан `inherit_env: true`.
- `inherit_env: true` → полное наследование + `AFM_*` + резолвнутый `env` (для legacy-скриптов, которым нужно всё окружение вместе с секретом).
- `inherit_env: false` явно → минимальное окружение даже без `env`.

Это сохраняет обратную совместимость и одновременно даёт изоляцию там, где появляются секреты. (Если требуется буквальная трактовка спеки — эскалировать; по умолчанию действуем так.)

## File Structure

Новые файлы:
- `pkg/secrets/secrets.go` — `ResolveRef` (бывш. ResolveAuthValue), `LoadSecrets`, `LoadSecretLayers`, `ExpandHome`; нейтрально (без зависимости на pkg/docker).
- `pkg/secrets/secrets_test.go` — перенос docker secrets-тестов.
- `pkg/lifecyclehooks/env.go` — `SecretRef`, `ResolveHookEnv`, построение окружения (`buildHookEnv`), редактирующий writer (`redactingWriter`).
- `pkg/lifecyclehooks/env_test.go`.

Модифицируемые:
- `pkg/docker/secrets.go` — стать тонкой обёрткой над `pkg/secrets` (или удалить, обновив call sites launcher.go/wrapper.go/run.go); `ResolveSystemPrompt` остаётся в docker (docker-специфичен) либо тоже переезжает — по месту.
- `pkg/docker/launcher.go` — переиспользовать `pkg/secrets`; добавить транспорт секретов хуков (`AFM_SECRET_<hook>_<VAR>`), `docker.UsesLifecycleSecrets`.
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
  - `Hook.Env map[string]SecretRef` (yaml `env,omitempty`), `Hook.InheritEnv *bool` (yaml `inherit_env,omitempty` — указатель, чтобы отличать «не задано» от `false`; см. уточнение контракта), `Hook.ResolvedEnv map[string]string` (yaml:"-", проставляется в рантайме — Task 3).
  - `ValidateLayer` дополнительно проверяет `env`: имя целевой переменной матчит `^[A-Za-z_][A-Za-z0-9_]*$`, не начинается с `AFM_`; источник начинается с `env:` или `file:` (непустой хвост). (Существование источника проверяется при резолве — Task 3, не здесь.)

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
	InheritEnv *bool                `yaml:"inherit_env,omitempty"`
	// ResolvedEnv — резолвнутые значения env (targetVar→value), проставляются
	// при сборке dispatcher (cmd/afm/run.go), в памяти. Не сериализуется, не
	// пишется в payload/логи. yaml:"-".
	ResolvedEnv map[string]string `yaml:"-"`
}
```

`validate.go` — в цикле по хукам добавить (после проверки command/events):

```go
	for varName, ref := range h.Env {
		if !envVarNameRe.MatchString(varName) {
			return fmt.Errorf("hooks[%d] (%s): env var name %q must match [A-Za-z_][A-Za-z0-9_]*", i, h.ID, varName)
		}
		if strings.HasPrefix(varName, "AFM_") {
			return fmt.Errorf("hooks[%d] (%s): env var name %q: prefix AFM_ is reserved", i, h.ID, varName)
		}
		s := string(ref)
		okEnv := strings.HasPrefix(s, "env:") && len(s) > len("env:")
		okFile := strings.HasPrefix(s, "file:") && len(s) > len("file:")
		if !okEnv && !okFile {
			return fmt.Errorf("hooks[%d] (%s): env %q source must be env:NAME or file:PATH, got %q", i, h.ID, varName, s)
		}
	}
```

с package-level `var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)` и импортом `regexp`/`strings`.

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

`cmd/afm/run.go` — в блоке сборки хуков, ПОСЛЕ `Combine`, ДО `New`:

```go
			// Секреты хуков: резолв один раз, до старта рана (спека). Слои:
			// project .afm/secrets.env > global ~/.afm/secrets.env > env процесса
			// (порядок обеспечивает secrets.ResolveRef: сначала loaded map, затем
			// os.Getenv; в loaded проектный слой перекрывает глобальный).
			hookSecrets, serr := loadHookSecretLayers(rootDir) // хелпер: LoadSecrets([global, project]) — project последним (приоритет)
			if serr != nil {
				return fmt.Errorf("lifecycle hooks: load secrets.env: %w", serr)
			}
			for i := range combined {
				resolved, rerr := lifecyclehooks.ResolveHookEnv(combined[i].Hook, hookSecrets)
				if rerr != nil {
					return fmt.Errorf("lifecycle hooks: %w", rerr) // fail-fast до flow_started
				}
				combined[i].Hook.ResolvedEnv = resolved
			}
```

`loadHookSecretLayers(afmRoot)` — хелпер рядом (в run.go или agent_environment.go): `secrets.LoadSecrets([]string{ filepath.Join(homeAfmDir,"secrets.env"), filepath.Join(afmRoot, ".afm/secrets.env") })` (project последним → приоритет). Свериться с существующим `docker.LoadSecretLayers` для путей.

(Docker-режим: в контейнере loaded-слои читаются из транзиентных транспортных переменных — см. Task 5; на этом шаге — host-путь.)

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
  - `buildEnv` учитывает режим окружения: без `Env` и без `InheritEnv` → `os.Environ()` (Phase 1); иначе минимальный набор (или полный при `InheritEnv==true`) + `AFM_*` + `ResolvedEnv`. `AFM_*` и `ResolvedEnv` всегда добавляются последними (перекрывают базовые).
  - `func minimalBaseEnv() []string` — PATH, HOME, locale (LANG/LC_*), TMPDIR, proxy/certs (HTTP(S)_PROXY/NO_PROXY/SSL_CERT_*), извлечённые из `os.Environ()`.
  - `type redactingWriter struct{...}` — io.Writer, заменяющий вхождения известных секретных значений на `[REDACTED]` перед записью в лог. Буферизует хвост длиной max(len(secret)) для стыков между Write-ами.

- [ ] **Step 1: Write the failing test**

```go
func TestBuildEnv_Modes(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("UNRELATED_KEY", "leak-me")
	cfg := DispatcherConfig{RunID: "r"}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted}, "id")

	// Phase 1: без Env/InheritEnv → наследование (UNRELATED виден)
	e1 := buildEnv(Hook{ID: "h"}, cfg, p)
	if !hasEnv(e1, "UNRELATED_KEY", "leak-me") {
		t.Fatal("no-env hook must inherit (Phase 1 compat)")
	}
	// с Env → минимальное окружение (UNRELATED НЕ виден), но PATH/AFM_*/resolved есть
	e2 := buildEnv(Hook{ID: "h", Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "secret"}}, cfg, p)
	if hasEnvKey(e2, "UNRELATED_KEY") {
		t.Fatal("env-hook must use minimal env (no unrelated leakage)")
	}
	if !hasEnvKey(e2, "PATH") || !hasEnv(e2, "AFM_HOOK_EVENT", "flow_started") || !hasEnv(e2, "T", "secret") {
		t.Fatalf("minimal must include PATH, AFM_*, resolved: %v", e2)
	}
	// inherit_env: true → наследование + resolved
	tru := true
	e3 := buildEnv(Hook{ID: "h", InheritEnv: &tru, Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "secret"}}, cfg, p)
	if !hasEnv(e3, "UNRELATED_KEY", "leak-me") || !hasEnv(e3, "T", "secret") {
		t.Fatal("inherit_env:true must inherit AND include resolved")
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
	switch {
	case len(h.Env) == 0 && h.InheritEnv == nil:
		env = os.Environ() // Phase 1 совместимость: нет секретов → наследуем
	case h.InheritEnv != nil && *h.InheritEnv:
		env = os.Environ() // явный inherit + секреты
	default:
		env = minimalBaseEnv() // есть Env или inherit_env:false → изоляция
	}
	env = append(env, afmVars(cfg, p)...) // AFM_* (вынести из старого buildEnv)
	for k, v := range h.ResolvedEnv {
		env = append(env, k+"="+v) // резолвнутые секреты — последними (перекрывают)
	}
	return env
}

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

`execOne` — обернуть лог в редактирующий writer, если у хука есть секреты:

```go
	logFile, logErr := openAttemptLog(logPath, p, attempt)
	if logErr == nil {
		var w io.WriteCloser = logFile
		if len(h.ResolvedEnv) > 0 {
			w = newRedactingWriter(logFile, secretValues(h)) // редакция stdout/stderr
		}
		cmd.Stdout = w
		cmd.Stderr = w
		// после Run: если w — редактор, Flush/Close его, затем закрыть logFile
		...
	}
```

(Аккуратно с закрытием: redactingWriter.Close() должен сбросить буферизованный хвост в logFile, затем закрыть logFile. Реализовать `redactingWriter` так, чтобы `Close()` флашил остаток и закрывал вложенный writer, ИЛИ разделить Flush и закрытие logFile. Учесть finish-строку `=== attempt N finished ===` — писать её в logFile напрямую ПОСЛЕ флаша редактора, чтобы не пропустить через редактор зря.)

`env.go`/`redact.go` — `redactingWriter`:

```go
type redactingWriter struct {
	w       io.Writer
	secrets []string
	maxLen  int
	tail    []byte // незаписанный хвост (возможный префикс секрета на стыке Write)
}

func newRedactingWriter(w io.Writer, secrets []string) *redactingWriter {
	max := 0
	nonEmpty := secrets[:0]
	for _, s := range secrets {
		if s == "" {
			continue
		}
		nonEmpty = append(nonEmpty, s)
		if len(s) > max {
			max = len(s)
		}
	}
	return &redactingWriter{w: w, secrets: nonEmpty, maxLen: max}
}

// Write редактирует известные секреты. Держит в буфере хвост длиной maxLen-1,
// чтобы поймать секрет, разорванный между Write-ами.
func (r *redactingWriter) Write(p []byte) (int, error) {
	n := len(p)
	buf := append(r.tail, p...)
	redacted := redactAll(buf, r.secrets)
	// оставить в хвосте последние maxLen-1 байт (могут быть началом секрета)
	keep := r.maxLen - 1
	if keep < 0 {
		keep = 0
	}
	if len(redacted) > keep {
		if _, err := r.w.Write(redacted[:len(redacted)-keep]); err != nil {
			return 0, err
		}
		r.tail = append(r.tail[:0], redacted[len(redacted)-keep:]...)
	} else {
		r.tail = append(r.tail[:0], redacted...)
	}
	return n, nil
}

func (r *redactingWriter) Close() error {
	if len(r.tail) > 0 {
		if _, err := r.w.Write(redactAll(r.tail, r.secrets)); err != nil {
			return err
		}
		r.tail = nil
	}
	return nil
}

func redactAll(b []byte, secrets []string) []byte {
	for _, s := range secrets {
		b = bytes.ReplaceAll(b, []byte(s), []byte("[REDACTED]"))
	}
	return b
}

func secretValues(h Hook) []string {
	out := make([]string, 0, len(h.ResolvedEnv))
	for _, v := range h.ResolvedEnv {
		out = append(out, v)
	}
	return out
}
```

(Важно: хвост-буфер может не поймать секрет, разорванный так, что часть уже записана в предыдущем Write за пределом keep — поэтому keep = maxLen-1 и мы НЕ пишем последние maxLen-1 байт до следующего Write/Close. Это гарантирует, что любое вхождение целиком проходит через `redactAll` на границе. Задокументировать ограничение: редакция — защита от `set -x`/`echo`, не абсолютная (скрипт может закодировать секрет).)

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
- Produces: в Docker-режиме секреты хуков резолвятся ХОСТОМ до `docker run` и передаются транзиентными bare `-e AFM_SECRET_HOOK_<hookEnvName>_<VAR>`; in-container `ResolveHookEnv`-путь берёт значения из этих переменных, а не из файлов/secrets.env; транспортные переменные фильтруются из окружения агентов/скриптов/других хуков.

> ВАЖНО: перед реализацией прочитать `pkg/docker/launcher.go` (ReExec, блок autoShim-секретов :391-424, dockerForwardEnvVars, сборку `-e`), `pkg/docker/wrapper.go` (`envName`, unset-паттерн) и docker-режим в `cmd/afm/run.go` (как флоу/конфиг доезжает в контейнер, `AFM_IN_DOCKER`). Реализация должна ЗЕРКАЛИТЬ существующий AFM_SECRET_<CMD>-паттерн, а не изобретать новый.

- [ ] **Step 1: Design + failing test**

Транспортное имя: `AFM_HOOK_SECRET_<sanitize(hookID)>__<VAR>` (двойное подчёркивание-разделитель; `sanitize` как `wrapper.envName` — верхний регистр, не-alnum→`_`). Хост:
1. Определить, использует ли флоу секреты хуков (`UsesLifecycleSecrets(cfg, flow)` — есть ли хоть один хук с непустым `env`). Если нет — ничего не делаем (существующее поведение).
2. На хосте загрузить `secrets.env`-слои + резолвить каждый хук (та же `ResolveHookEnv`), для каждой пары `os.Setenv(transportName, val)` + `args=append(args,"-e",transportName)` (bare, без значения в argv — как autoShim).
3. Не монтировать secret-файлы в контейнер.

In-container (`AFM_IN_DOCKER=1`): при сборке хуков вместо `loadHookSecretLayers` собрать `loaded` map из транспортных переменных: для каждой ожидаемой пары `AFM_HOOK_SECRET_<hook>__<VAR>` → положить значение под ключом, который `ResolveHookEnv` ищет. Проще: in-container резолвить `Hook.Env` НЕ через `secrets.ResolveRef` (файлов нет), а напрямую из транспортных переменных: `ResolvedEnv[VAR] = os.Getenv(transportName)`; отсутствие → ошибка (fail-fast). Затем `os.Unsetenv(transportName)` (чтобы не течь в агентов) ИЛИ отфильтровать транспортные `AFM_HOOK_SECRET_*` из окружения агентов/скриптов в executor/runner_factory.

Тест (host, unit — как существующие launcher-тесты): флоу с хуком `env: {TOK: "file:..."}` → `ReExec`-аргументы содержат `-e AFM_HOOK_SECRET_...__TOK`, значение выставлено в `os.Environ` процесса-хоста, но НЕ в argv; секрет-файл НЕ в списке `-v` монтирований.

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

- [ ] **Step 3: Implement** — по образцу autoShim-секретов (`launcher.go:391-424`). Развилка резолва host vs in-container вынести в `cmd/afm/run.go`: если `os.Getenv("AFM_IN_DOCKER")=="1"` → значения из транспортных переменных; иначе → `secrets.env`+файлы (Task 3). Фильтрацию транспортных переменных из окружения агентов сделать там, где формируется окружение агента/скрипта (executor/runner_factory) — вырезать `AFM_HOOK_SECRET_*` (и убедиться, что обычный `buildEnv` хука их тоже не тащит: хук берёт `ResolvedEnv`, а не транспортные напрямую — если минимальное окружение, они не попадут; при `inherit_env:true` — отфильтровать явно).

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

## Self-Review (выполнен автором плана)

- **Spec coverage:** env со ссылками env:/file: (T2 валидация, T3 резолв); вынос pkg/secrets + рефактор docker (T1); secrets.env приоритеты project>global>env (T3); имя переменной + AFM_-резерв + fail-fast до flow_started (T2/T3); минимальное окружение + inherit_env (T4, с уточнением контракта для Phase 1-совместимости); [REDACTED] в логах/dashboard (T4 — dashboard получает те же строки лога/notice, редакция на источнике); Docker-транспорт без монтирования + фильтрация (T5); секреты не в payload/journal/argv/логах (T3/T4/T5); докум. (T6).
- **Открытый вопрос (эскалирован в тексте):** спека требует «по умолчанию не наследовать окружение», но Phase 1 уже релизнут с наследованием. План принимает уточнение: изоляция включается наличием `env`/`inherit_env`, иначе Phase 1-совместимое наследование. Если нужна буквальная трактовка — менять дефолт в T4 и пометить как breaking для существующих хуков.
- **Placeholder scan:** код для T1-T4 полный; T5 (Docker) намеренно на уровне дизайна + требование прочитать и зеркалить существующий autoShim-паттерн (launcher.go:391-424/wrapper.go) — точные argv/`-v` конструкции берутся из реального кода, не из догадок.
- **Type consistency:** `secrets.ResolveRef/LoadSecrets/ExpandHome`, `Hook.Env map[string]SecretRef`/`InheritEnv *bool`/`ResolvedEnv map[string]string`, `ResolveHookEnv(h,loaded)`, `buildEnv(h,cfg,p)`, `minimalBaseEnv()`, `newRedactingWriter(w,secrets)`/`redactAll` — единообразны во всех задачах.
- **Инвариант:** нерезолвнутый секрет = config-error fail-fast (до flow_started), не best-effort; ошибка самого хука по-прежнему не трогает FSM.
