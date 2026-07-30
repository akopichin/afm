# Docker: поддержка `codex` + `type: codex` autoShim-рецепт

**Контекст:** afm Docker-mode (CLAUDE.md → «Docker Mode»); существующий autoShim (`docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`, `pkg/docker/wrapper.go`) уже поддерживает `type: openai`/`type: cursor`.
**Цель:** (1) `command: codex-as-claude` работает в Docker-режиме так же, как локально; (2) новый `type: codex` autoShim-рецепт для конфигурируемой через `docker.agents.codex` модели, без монтирования хостового бинарника.
**Авторизация:** ChatGPT-plan OAuth (`~/.codex/auth.json`), а не API-ключ — принципиально другой механизм, чем `AFM_SECRET_<CMD>` (см. «`~/.codex` — copy, не mount» ниже). Изучен пример goga (`~/.codex/auth.json` монтируется `:ro`), но выбран более безопасный вариант — copy-on-container-start вместо прямого read-only bind-mount хостового каталога.

## Почему нельзя просто смонтировать хостовый бинарник

`codex` — нативный бинарник (Mach-O на macOS), в отличие от `glm51`/кастомных bash-скриптов он не портируется в Linux-контейнер простым `-v host:container:ro`. Поэтому, в отличие от общего механизма `ScanCommands` (mount произвольной команды из PATH), для codex единственный рабочий путь — установить `codex` **внутри** образа, как уже сделано для `claude` (`Dockerfile.runtime:56`).

Второе следствие: `pkg/executor` умеет парсить только claude `stream-json` (`pkg/executor/executor.go:152` `parseStreamEvent`) — сырой `codex exec --json` не годится ни в Docker, ни локально. Нужен транслятор `codex-as-claude.sh`, который у пользователя уже есть и используется локально (`command: codex-as-claude` в flow.yaml) — этот скрипт вендорится в afm, аналогично `openai-as-claude.sh`/`cursor-as-claude.sh`.

## Часть 1 — базовая поддержка в образе

- `Dockerfile.runtime`: `RUN npm install -g @openai/codex` рядом с существующей строкой `npm install -g @anthropic-ai/claude-code` (строка 56).
- Вендорим `codex-as-claude.sh` (источник — `~/work/ralphex/scripts/codex-as-claude/codex-as-claude.sh`) в `afm/scripts/codex-as-claude.sh`:
  - убираем ralphex-специфичный блок `<<<RALPHEX:REVIEW_DONE>>>`/`adapter_text` (строки 50-58 оригинала) — у afm нет такого сигнала;
  - единственное содержательное изменение: `codex "${codex_args[@]}"` → `"${CODEX_BIN:-codex}" "${codex_args[@]}"` (см. «Часть 3» — нужно для autoShim, чтобы враппер с именем `codex` не рекурсировал сам в себя через PATH).
- `COPY scripts/codex-as-claude.sh /usr/local/bin/codex-as-claude` + `chmod +x` в `Dockerfile.runtime`, рядом с блоками `openai-as-claude`/`cursor-as-claude` (строки 68-74).
- Итог: `command: codex-as-claude` в flow.yaml работает в Docker **без изменений** в flow.yaml относительно локального запуска.

## Часть 2 — `~/.codex` (OAuth-состояние): copy, не mount

goga монтирует `~/.codex/auth.json` напрямую read-only. Для afm выбран другой вариант — по аналогии с тем, как уже устроено `~/.claude` (примонтирован в контейнер), но с дополнительным шагом изоляции: codex может рефрешить OAuth-токен (переписать `auth.json`) во время работы, и мы не хотим, чтобы это когда-либо просочилось обратно на хост или столкнулось с одновременно работающим локальным `codex login`.

- `ReExec` (`pkg/docker/launcher.go`) монтирует `~/.codex` **read-only** во временный путь контейнера: `-v $HOME/.codex:/tmp/host-codex:ro`.
- `docker-entrypoint.sh`, ещё под root (до `gosu`), при наличии `/tmp/host-codex`:
  ```sh
  if [ -d /tmp/host-codex ]; then
    mkdir -p /home/afm/.codex
    cp -a /tmp/host-codex/. /home/afm/.codex/
    chown -R "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm/.codex
  fi
  ```
- Внутри контейнера `$HOME/.codex` (= `containerHome/.codex`) — обычная **writable** копия: codex читает/обновляет её свободно. Контейнер эфемерный (`docker run --rm`) — копия исчезает вместе с ним; хостовый `~/.codex` никогда не изменяется.
- **Gating** (не монтируем без необходимости — то же least-privilege правило, что уже применяется к recipe-секретам через `UsedRecipes`): монтируем `~/.codex` только если флоу реально использует codex — глобальная `client.command` или команда стадии равна `codex-as-claude`, либо среди используемых recipe есть `type: codex`. Реализуется новым хелпером `docker.UsesCodex(f *flow.Flow, globalCmd string, usedRecipes map[string]config.AgentRecipe) bool` в `pkg/docker/launcher.go`, вызывается из `run.go` рядом с уже существующим построением `cmds`/`recipes`.
- Если `~/.codex` не существует на хосте — мount пропускается молча (как и для остальных best-effort путей в этом коде), `codex-as-claude` внутри контейнера упадёт с ошибкой codex CLI об отсутствии авторизации — это нормальная, понятная ошибка, специально ловить её в afm не нужно.

## Часть 3 — `type: codex` autoShim-рецепт

```yaml
docker:
  autoShim: true
  agents:
    codex:
      type: codex
      model: gpt-5.1-codex   # опц.; "" или "default" → CODEX_MODEL не выставляется, решает сам codex/config.toml
```

- `pkg/config/config.go`: новая константа `RecipeTypeCodex = "codex"`, добавляется в switch допустимых значений `AgentRecipe.Type` (`Validate()`, строки 127-131).
- **`auth` не обязателен для `type: codex`.** Это не костыль, а отражение реального устройства: авторизация идёт через смонтированную (Часть 2) `~/.codex`, а не через одно-значный секрет `AFM_SECRET_<CMD>`, который резолвит `ResolveAuthValue`. `Validate()` пропускает проверку `auth.to` (строка 135-137 текущего кода), если `r.Type == RecipeTypeCodex` — если пользователь всё-таки укажет `auth` (задел на будущее — API-key поддержка), она валидируется как у `openai`/`cursor` (`env:` префикс, без ограничения `ClaudeAuthEnvVars`).
- `url` для `type: codex` не используется (codex сам решает провайдера через `~/.codex/config.toml`) — не обязателен, в отличие от `openai`/`cursor`.

### Обход рекурсии через PATH: `CODEX_BIN`

Recipe-ключ — `codex` (по явному запросу пользователя), а значит и сгенерированный враппер называется `codex` и лежит в wrapper-dir, который prepend'ится в `PATH` дочернего процесса. Настоящий codex CLI, установленный `npm install -g` (Часть 1), **тоже** называется `codex`. Раз `codex-as-claude.sh` внутри себя вызывает `codex` по голому имени (строка 87 оригинала), а wrapper-dir стоит в PATH раньше, — без доп. мер получилась бы бесконечная рекурсия: враппер `codex` → `codex-as-claude` → голый `codex` → снова враппер `codex`.

Решение — то же самое, что уже используется для `claude` (`realClaude`, резолвится `exec.LookPath` **до** того, как wrapper-dir лёг в PATH), но проброшенное во второй уровень через env, а не через inline-абсолютный путь в самом враппере:

- `CreateWrappers` (`pkg/docker/wrapper.go`) резолвит `exec.LookPath("codex")` **один раз**, до генерации враппера с именем `codex` — аналогично существующему условному резолву `realClaude` (строки 56-66), но независимо от него (флоу может использовать только codex, без единого claude-типа рецепта).
- Генерируемый враппер (`WrapperSpec.Type == RecipeTypeCodex`):
  ```sh
  #!/bin/sh
  export CODEX_BIN="/usr/lib/node_modules/@openai/codex/bin/codex"   # abs-путь, разрешён ДО prepend wrapper-dir
  export CODEX_MODEL="gpt-5.1-codex"                                  # опущено, если model пуст/"default"
  exec /usr/local/bin/codex-as-claude "$@"
  ```
- `codex-as-claude.sh` меняется на `"${CODEX_BIN:-codex}" "${codex_args[@]}"` — при запуске из-под враппера использует abs-путь (без обращения к PATH вообще), при прямом локальном запуске (`CODEX_BIN` не выставлен) — как раньше, голый `codex`.
- Сам `/usr/local/bin/codex-as-claude` враппером **не** экранируется (имя не совпадает с `codex`) — раньше существующий трюк с абсолютным путём для него не требуется, `exec /usr/local/bin/codex-as-claude "$@"` работает напрямую.

## Данные потоки (сводно)

```
HOST (launcher, ReExec)                                  CONTAINER (run.go после re-exec)
UsesCodex(f, ...) → mount ~/.codex:ro в /tmp/host-codex   entrypoint (root): cp -a + chown → $HOME/.codex (writable)
ScanCommands(skip generated codex)                        cfg.Docker.Agents читается из смонтированного config.yaml
                                                           CreateWrappers:
                                                             realCodexBin = LookPath("codex")  (если есть codex-recipe)
                                                             WrapperSpec{Type:"codex", Model, CODEX_BIN=realCodexBin}
                                                             → exec /usr/local/bin/codex-as-claude "$@"
```

Секретов через `AFM_SECRET_*`/`AFM_SYSPROMPT_*` для `type: codex` в базовом сценарии нет (auth не обязателен) — если пользователь всё же задаст `auth` (задел на будущее API-key режима), работает тот же transient-механизм, что и для `openai`/`cursor`.

## Файлы

| Файл | Изменение |
|------|-----------|
| `Dockerfile.runtime` | `npm install -g @openai/codex`; `COPY scripts/codex-as-claude.sh /usr/local/bin/codex-as-claude` + chmod |
| `scripts/codex-as-claude.sh` (новый, вендор) | Портировано из ralphex; убран `<<<RALPHEX:...>>>`-блок; `codex` → `"${CODEX_BIN:-codex}"` |
| `docker-entrypoint.sh` | Блок copy+chown `/tmp/host-codex` → `/home/afm/.codex`, до `gosu` |
| `pkg/config/config.go` | `RecipeTypeCodex = "codex"`; `Validate()` — codex не требует `auth`; codex не требует `url` |
| `pkg/docker/wrapper.go` | Ветка `RecipeTypeCodex` в `generateWrapper`; резолв `realCodexBin` в `CreateWrappers` (условно, как `realClaude`); `WrapperSpec` не меняет форму (переиспользует `AuthTo`/`Model`, новое поле не нужно — `CODEX_BIN` строится из уже резолвленного `realCodexBin`, аналогично `realClaude`) |
| `pkg/docker/launcher.go` | Новый хелпер `UsesCodex(f, globalCmd, usedRecipes) bool`; в `ReExec` — доп. mount `~/.codex:ro` в `/tmp/host-codex`, гейтед `UsesCodex` |
| `cmd/afm/run.go` | Вызов `UsesCodex` рядом с построением `recipes`/`cmds`, проброс решения в `docker.ReExecConfig` (новое поле, например `MountCodexState bool`) |

## Edge cases

- `~/.codex` отсутствует на хосте → mount пропускается, `codex-as-claude` упадёт с ошибкой codex CLI об авторизации (не обрабатываем специально).
- `codex` не установлен в образе (сборка без обновления Dockerfile) → `CreateWrappers` возвращает hard error при резолве `realCodexBin`, аналогично существующей ошибке для отсутствующего `claude`.
- `type: codex` recipe с заполненным `auth` — валидируется как `openai`/`cursor` (`env:` префикс), но это не основной сценарий (auth опционален).
- Флоу использует codex БЕЗ recipe (просто `command: codex-as-claude`, нет `docker.agents.codex`) — `UsesCodex` всё равно должен сработать по факту использования команды `codex-as-claude`, не только по наличию recipe: `~/.codex` монтируется в обоих случаях.
- autoShim off, но `command: codex-as-claude` используется — работает напрямую из baked-в-образ `/usr/local/bin/codex-as-claude` (Часть 1 не зависит от autoShim). Если у пользователя на хосте **тоже** есть свой `codex-as-claude.sh` на PATH, `ScanCommands` (не видит `generated`, т.к. autoShim off) смонтирует его поверх того же пути — безобидно, тот же контракт. `~/.codex` монтируется через `UsesCodex` независимо от autoShim.

## Тесты

- **Unit `pkg/config`:** `type: codex` — `auth` не обязателен; `url` не обязателен; неизвестный тип по-прежнему ошибка.
- **Unit `pkg/docker/wrapper.go`:** `generateWrapper` для `type: codex` — `CODEX_BIN=<abs>`, `CODEX_MODEL` эмитится только при непустом/не-`"default"` model, `exec /usr/local/bin/codex-as-claude "$@"`; `CreateWrappers` резолвит `realCodexBin` только при наличии codex-спеки (аналог существующего теста `TestCreateWrappers_OpenAINoClaudeRequired` — codex не требует `claude` на PATH, и наоборот, `claude`-тип не требует `codex`).
- **Unit `pkg/docker/launcher.go`:** `UsesCodex` — true для recipe `type: codex` (использованного), true для голой команды `codex-as-claude` без recipe, false когда ни то ни другое не задействовано.
- **Integration `ReExec`:** codex используется → присутствует `-v $HOME/.codex:/tmp/host-codex:ro`; не используется → монтирования нет.
- **Shell-тест `docker-entrypoint.sh`:** при наличии `/tmp/host-codex` — копия появляется в `$HOME/.codex` с правильным владельцем, при отсутствии — блок пропускается без ошибки.
- **Shell-тест `codex-as-claude.sh`:** stub `codex` печатает `$0`/argv → при выставленном `CODEX_BIN` вызывается abs-путь, без — голое имя `codex` (регресс на локальный сценарий).
