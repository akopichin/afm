# Дизайн: дефолт open_browser=false + печать URL

- Дата: 2026-07-09
- Статус: одобрен, ожидает плана реализации
- Ветка: theme

## Контекст

Параметр `server.open_browser` уже существует (`pkg/config/config.go:29`,
`ServerConfig.OpenBrowser *bool`), default `true` (`IsOpenBrowser()` возвращает
`true` при nil). При `true` afm после старта дашборда зовёт `openBrowser(dashURL)`
(`cmd/afm/run.go:223`) — `exec.Command("open"|"xdg-open", url).Start()`.

**Гипотеза пользователя** (не подтверждена): «косяки с подписанием бинарника —
потому что он сразу открывает браузер через system.exec». Технически это **не
верно**: «косяк подписания» на macOS 26 (Darwin 25+) — это SIGKILL неподписанного
бинарника `afm` (`Code Signature Invalid`) при запуске любого subcommand; лечится
ad-hoc codesign (`make install`, `codesign -f -s -`, см. memory `afm-binary-update`
и Makefile:44-49). `exec.Command` внешнего процесса не ломает подпись `afm`.

**Однако само авто-открытие браузера часто нежелательно** (CI, SSH, remote,
несколько запусков подряд, silent-режим). Цель: default `false` — браузер не
открывается, URL печатается в лог; `true` — открывать как сейчас.

**Выбор подхода:** сменить дефолт существующего `server.open_browser` на `false`
(не добавлять `open_viewer`, не вводить env-override или алиасы).

## Решение

Сменить default `server.open_browser`: `true` → `false`. При `false` — не звать
`openBrowser`, а печатать в лог URL дашборда с подсказкой открыть вручную. При
`true` — открывать браузер как сейчас. Работает одинаково для локального запуска
и Docker-режима.

## Детали

### 1. Config (`pkg/config/config.go`)

- `Default()` (стр.152): `openBrowser := true` → `false`.
- `IsOpenBrowser()` (стр.33-37): при `OpenBrowser == nil` возвращать `false`
  (было `true`). Иначе `*OpenBrowser` без изменений.
- `mergeFile` — без изменений (nil-паттерн: только явное значение перетирает).

### 2. run.go — печать URL и условие открытия (`cmd/afm/run.go:217, 221-223`)

Сейчас:
```go
fmt.Printf("  dashboard: %s\n", dashURL)
...
if cfg.Server.IsOpenBrowser() && os.Getenv("AFM_IN_DOCKER") != "1" {
    openBrowser(dashURL)
}
```

Стать:
```go
fmt.Printf("  dashboard: %s\n", dashURL)
if cfg.Server.IsOpenBrowser() {
    // Локально — openBrowser; в Docker — хост-side opener уже запущен (run.go:78).
    if os.Getenv("AFM_IN_DOCKER") != "1" {
        openBrowser(dashURL)
    }
} else {
    fmt.Printf("  → open this URL in your browser to follow the run\n")
}
```

Логика:
- `dashboard: <URL>` печатается **всегда** (и Docker, и локально).
- Браузер открывается при `open_browser=true`: локально через `openBrowser`,
  в Docker — через `launchHostBrowserOpener` на хосте (уже gated на
  `IsOpenBrowser()` в `run.go:78`).
- При `open_browser=false` — нигде не открывается, печатается подсказка.
- `AFM_IN_DOCKER != "1"` влияет **только на сам exec** `openBrowser` (в
  Linux-контейнере нет `xdg-open`), на печать URL не влияет.

`launchHostBrowserOpener` (run.go:78 `if port > 0 && cfg.Server.IsOpenBrowser()`)
— без изменений: при `false` host-opener не запускается.

### 3. Тесты (`pkg/config/config_test.go`)

- `TestServerConfigDefaults` (стр.78): инвертировать —
  `if cfg.Server.IsOpenBrowser() { t.Error("default open_browser should be false") }`.
- По образцу `TestProxyConfig_IsEnabled` добавить табличный тест `TestServerConfig_IsOpenBrowser`:
  `nil` → `false`, `&true` → `true`, `&false` → `false`.

### 4. example config (`config.example.yaml`)

Добавить закомментированную секцию `server:` рядом с `theme:` для документации
нового дефолта:
```yaml
# server:
#   # Открывать браузер с дашбордом автоматически при запуске.
#   # Default: false — URL печатается в лог, откройте вручную.
#   # open_browser: true
#   # port: 9876
```

### 5. Обработка / крайние случаи

- `server.port == 0` (дашборд выключен): блок открытия/печати URL не выполняется
  (`run.go:188 if cfg.Server.GetPort() > 0`) — без изменений.
- Docker + `open_browser=false`: host-opener не запускается; URL
  `http://localhost:<port>` печатается в лог контейнера (виден на хосте через
  проброс `-p <port>:<port>`).
- Локально + `open_browser=false`: URL печатается, `exec` не зовётся.
- Неизвестные/env — нет, всё через конфиг.

### 6. Что НЕ делаем (YAGNI)

- Не добавляем `open_viewer`, env `AFM_OPEN_BROWSER`, deprecated-алиасы.
- Не трогаем `launchHostBrowserOpener`, `browserCmd`, Docker-логику re-exec.
- Не меняем `openBrowser`/`exec.Command` — остаётся, вызывается реже.
- Не связываем с подписанием бинарника (это отдельная проблема codesign).

## Файлы для изменения

| Файл | Изменение |
|---|---|
| `pkg/config/config.go` | `Default()` openBrowser=false; `IsOpenBrowser()` nil→false |
| `pkg/config/config_test.go` | инвертировать `TestServerConfigDefaults`; добавить `TestServerConfig_IsOpenBrowser` |
| `cmd/afm/run.go` | печать подсказки при `!IsOpenBrowser()`; условие открытия |
| `config.example.yaml` | закомментированная секция `server:` с `open_browser` |
