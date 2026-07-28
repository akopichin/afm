# Дизайн: единые константы имён тем скина и путей `.afm`/`runs`/`flows`

**Дата:** 2026-07-28
**Статус:** согласован, готов к плану реализации

## Контекст

Продолжение обзора кодовой базы на дублирование (см. закрытые спеки этой серии: `plan-version-scan-dedup`, `phase-filename-source-of-truth`, `claude-command-constants-dedup`). Приоритет 3 — два родственных, но независимых пункта из исходного обзора: имена тем скина (`goga`/`novacorps`/`coffee`) и путь `.afm`/`runs`/`flows`.

## Часть 1: имена тем скина

`pkg/config/config.go:253-255` определяет неэкспортируемые `themeGoga`/`themeNovacorps`/`themeCoffee`, используемые в `EffectiveTheme()` — нормализует **произвольное** значение `Theme` из YAML-конфига, с warning в stderr на неизвестное значение.

`pkg/server/server.go:25-28` независимо определяет те же 3 значения (+ свой `themeCustom`), используемые в `builtinSkinName()` — защитно нормализует значение, которое **уже пришло нормализованным** через `cfg.EffectiveTheme()` (единственный вызывающий — `cmd/afm/run.go:258`), без warning.

**Важно:** это не «один алгоритм в двух местах» (как было с `plan.vN.md`/список фаз в предыдущих спеках) — это два содержательно разных switch'а с разным поведением на неизвестном входе (warn vs. молча дефолтить). Объединять сами функции — рискованно и не нужно; дублируется только **набор из трёх строк-значений**, который должен совпадать. Решение — экспортировать сами константы, логику обоих switch'ей не трогать.

### Изменения

`pkg/config/config.go`:
```go
// Dashboard theme names returned by EffectiveTheme.
const (
	ThemeGoga      = "goga"
	ThemeNovacorps = "novacorps"
	ThemeCoffee    = "coffee"
)
```
(рескоуп видимости — было `themeGoga`/`themeNovacorps`/`themeCoffee`, значения не меняются). Обновить оба использования внутри `EffectiveTheme()` на новые имена.

`pkg/server/server.go`:
- Добавить импорт `"github.com/akopichin/afm/pkg/config"`.
- Убрать из локального блока констант (строка 24-29) `themeGoga`/`themeNovacorps`/`themeCoffee` — оставить только `themeCustom = "custom"` (уникальное для сервера понятие, в `pkg/config` не существует).
- Заменить 6 использований (`builtinSkinName()`: 2 case-значения + 2 return; `skinHrefFor(themeCoffee)`/`class="theme-"+themeCoffee` при подготовке index.html) на `config.ThemeGoga`/`config.ThemeNovacorps`/`config.ThemeCoffee`. Сама логика `builtinSkinName()` (switch с default-веткой, без warning) не меняется — меняется только источник трёх строк-значений.

`pkg/server` сегодня НЕ импортирует `pkg/config` (получает уже нормализованную строку через `Config.Theme string`, намеренная развязка) — этот пункт добавляет новую, безопасную (без риска цикла — `config` ничего из `afm` не импортирует) зависимость, аналогично прошлой спеке (`pkg/executor`→`pkg/config`).

## Часть 2: пути `.afm`/`runs`/`flows`

`cmd/afm/main.go` уже централизует путь `.afm` **внутри своего пакета** через `fmDir()` — хорошо. Но:

1. Литерал `".afm"` продублирован ещё в 2 местах **другого пакета**, не проходящих через `fmDir()` (он и не может — `fmDir()` привязан к `rootDir` флага `--dir`, а эти два случая — глобальный `~/.afm` и per-project `.afm`, не всегда совпадающие с текущим `rootDir` внутри `pkg/docker`):
   - `pkg/docker/secrets.go:107-108` — `filepath.Join(homeDir(), ".afm", "secrets.env")` и `filepath.Join(projectDir, ".afm", "secrets.env")`.
   - `cmd/afm/run.go:41` — `filepath.Join(home, ".afm")` (глобальный конфиг, отдельно от `fmDir()`).
2. Литерал `"runs"` — 5 мест в `cmd/afm` (`check.go:49`, `approve.go:20`, `retry.go:20`, `revise.go:37`, `run.go:407`), везде как `filepath.Join(fmDir(), "runs")` — нет обёртки-хелпера, хотя `fmDir()` этот паттерн уже устанавливает.
3. Литерал `"flows"` — 3 места в `cmd/afm` (`init.go:45`, `list.go:16`, `run.go:387,394,389,398` — 4 вхождения в одном файле), та же история.

### Изменения

`pkg/config/config.go`: новая экспортируемая константа рядом с существующими (`ClaudeCommand` и т.п.):
```go
// AfmDir — имя служебного каталога afm (и глобального ~/.afm, и per-project).
const AfmDir = ".afm"
```

`pkg/docker/secrets.go`: новый импорт `pkg/config` (пакет `pkg/docker` уже его использует в других файлах, но `secrets.go` — нет). Оба литерала `".afm"` (строки 107-108) → `config.AfmDir`.

`cmd/afm/main.go`: новый импорт `pkg/config`. `fmDir()` использует `config.AfmDir` вместо литерала. Добавляются два новых хелпера рядом с `fmDir()`, по тому же паттерну:
```go
// runsDir возвращает путь к каталогу с ранами внутри .afm.
func runsDir() string {
	return filepath.Join(fmDir(), "runs")
}

// flowsDir возвращает путь к каталогу с flow.yaml внутри .afm.
func flowsDir() string {
	return filepath.Join(fmDir(), "flows")
}
```

`cmd/afm/check.go`, `approve.go`, `retry.go`, `revise.go`, `run.go` (строка 407): `filepath.Join(fmDir(), "runs")` → `runsDir()` (5 мест).

`cmd/afm/list.go`: `filepath.Join(fmDir(), "flows")` → `flowsDir()`.

`cmd/afm/init.go`: `filepath.Join(fmDir(), "flows")` → `flowsDir()`. **Осторожно с именем**: текущий код объявляет ЛОКАЛЬНУЮ переменную `flowsDir := filepath.Join(fmDir(), "flows")`, которая после добавления пакетной функции с тем же именем будет её затенять (компилируется, но нечитаемо и означает, что фактически функция не используется). Локальная переменная переименовывается в `dir` (как уже сделано в `list.go`), значение берётся вызовом `flowsDir()`.

`cmd/afm/run.go`: 4 вхождения `"flows"` в `resolveFlowPath` (строки 387, 389, 394, 398) — первое и третье (`filepath.Join(fmDir(), "flows")`/`filepath.Join(fmDir(), "flows", e.Name())`) заменяются на `flowsDir()`/`filepath.Join(flowsDir(), e.Name())`; строки 389 и 398 — текстовые сообщения об ошибке (`"...и " + fmDir() + "/flows/ not found"`) — заменяются на `"...и " + flowsDir() + "/ not found"` (текст сообщения не меняется, меняется только то, как строится путь внутри него). Строка 41 (`filepath.Join(home, ".afm")`, глобальный конфиг) → `filepath.Join(home, config.AfmDir)`.

## Совместимость с существующими тестами

- `pkg/config`: `themeGoga`/`themeNovacorps`/`themeCoffee` не используются в `config_test.go` напрямую (чёрнобоксовый пакет, тесты идут через литералы/`EffectiveTheme()`) — изменений не требуется.
- `pkg/server`: `server_test.go` — белобоксовый (`package server`), использует `themeGoga`/`themeNovacorps` напрямую в 7 местах (`Config{Theme: themeGoga}` и т.п.) — после удаления локальных констант из `server.go` эти обращения перестанут компилироваться; заменяются на `config.ThemeGoga`/`config.ThemeNovacorps` (новый импорт `pkg/config` в тестовый файл).
- `cmd/afm`: тесты (если есть, использующие `fmDir()`) не ссылаются на литерал `".afm"` напрямую (по аналогии с предыдущими спеками этой серии) — проверить при реализации, но ожидается, что изменений не требуется.

## Тестирование

- Новый юнит-тест для `runsDir()`/`flowsDir()` в `cmd/afm` (если там уже есть тесты для `fmDir()` — добавить рядом; если нет ни одного — создать минимальный, подтверждающий `runsDir() == filepath.Join(fmDir(), "runs")` и аналогично для `flowsDir()`).
- Остальные существующие тесты должны продолжать проходить без функциональных изменений — это чистая консолидация констант/путей, поведение не меняется нигде.

## Out of scope

- Идентичный таймаут по умолчанию (30 мин), определённый в двух местах (`pkg/config/config.go` `Default()` и `pkg/executor/executor.go` `New()`) — отдельная, последняя оставшаяся спека из категории 1 исходного обзора.
- Фронтендовая часть обзора (`EventFeedPanel.tsx` hardcoded status-классы, CSS-дублирование статусов) — не Go, отдельный трек.
