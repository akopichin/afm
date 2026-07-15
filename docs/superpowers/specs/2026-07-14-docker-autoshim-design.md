# Docker: `autoShim` — автогенерация claude-совместимых врапперов по recipe

**Контекст:** afm Docker-mode (CLAUDE.md → «Docker Mode», «Built-in Reverse Proxy»).
**Предыдущий вариант:** `docs/…/2026-07-06-docker-agent-wrappers-design.md` (recipe без мастер-флага).
**Цель:** по флагу `docker.autoShim: true` afm **полностью генерирует** claude-совместимый враппер внутри Docker — без монтирования хост-бинарника и без `docker.extra_mounts`. Реальные обёртки (`glm51`, `glm52`) — это «`model`+`url`+`auth`+sysprompt → `exec claude`», поэтому описываются recipe и регенерируются в контейнере.

**Главное отличие от 2026-07-06:** добавлен мастер-флаг `autoShim` (opt-in), а внутренний data flow упрощён — `url`/`model`/`auth.to`/путь-к-sysprompt контейнер читает прямо из смонтированного `config.yaml`, поэтому transient-переменная `AFM_URL_<CMD>` убрана. Осталось две transient-переменные: `AFM_SECRET_<CMD>` (секрет) и опц. `AFM_SYSPROMPT_<CMD>` (контент).

## Режимы

- **generated** (`autoShim: true` И у команды есть recipe) — afm генерирует wrapper `exec claude`, хост-бинарник **не монтируется**.
- **нет recipe** (при `autoShim: true`) → fallback на текущее поведение (`ScanCommands` mount `:ro`, запуск напрямую). Backward compat.
- **`autoShim` off/отсутствует** → `docker.agents` полностью игнорируется, поведение строго как сегодня.
- **wrap-binary / `converter`** (не-claude агенты) — extension-point, **не реализуется в этом проходе**. Если встретится в конфиге — игнорировать с warn.

## Семантика флага `autoShim`

- `docker.autoShim: true` — **opt-in, default off, Docker-only**. На хосте (вне re-exec) флаг ничего не делает: реальные `glm51`/`glm52` и так в PATH.
- **Мастер-переключатель.** Recipe (`docker.agents.*`) вступают в силу **только** когда флаг on. Без флага — игнорируются, бинарники монтируются `:ro` как сегодня.
- Применяется ко всем командам из flow (+ global `client.command`), у которых есть recipe.
- Bonus: recipe может описать команду, которой **вообще нет на хосте** (docker-only агент) — `ScanCommands` пропускает generated-команды, монтировать нечего.

## Recipe schema

```yaml
docker:
  autoShim: true                              # мастер-флаг (НОВОЕ)
  secrets_file: ~/.afm/secrets.env            # опц.; дефолт — global+project layers (см. ниже)
  agents:
    glm51:
      model: glm-5.1                          # ОБЯЗАТЕЛЕН → ANTHROPIC_DEFAULT_{HAIKU,SONNET,OPUS}_MODEL (все 3 тира)
      url: https://api.z.ai/api/anthropic     # опц.; gateway агента (literal | env:X)
      system_prompt: file:~/.ai-free/claude-glm/system-prompt.md   # опц.; контент файла → --append-system-prompt-file
      auth:
        from: file:~/.ai-free/claude-glm/token   # ГДЕ afm читает ЗНАЧЕНИЕ на хосте (env:X | file:P)
        to:   env:ANTHROPIC_AUTH_TOKEN            # ОБЯЗАТЕЛЕН env: один из ANTHROPIC_AUTH_TOKEN/ANTHROPIC_API_KEY/CLAUDE_CODE_OAUTH_TOKEN
    glm52:
      model: glm-5.2
      url: https://api.z.ai/api/anthropic
      auth:
        from: file:~/.ai-free/claude-glm/token
        to:   env:ANTHROPIC_AUTH_TOKEN
```

**Формы значений:** `env:VAR` | `file:~/path` | `file:/abs/path`. `url` — literal или `env:`. `~` в `from`/`system_prompt` раскрывается по **хосту** (читаем значение/контент).

**Валидация (ошибки конфига):** `model` отсутствует; `auth` отсутствует; `auth.to` не `env:`; `auth.to` не из списка claude auth-envvars.

**Built-in `claude` (нулевая конфигурация):** recipe для `claude` не нужен. afm знает, что `claude` принимает `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` (первый непустой) — хардкод `claudeAuthEnvVars` в `pkg/docker/launcher.go:46` заменяется getter'ом из recipe-слоя.

**`secrets_file`:** дефолт — два слоя с merge'ем (как `config.yaml`): глобальный `~/.afm/secrets.env` + проектный `.afm/secrets.env` (проектный важнее). `docker.secrets_file` (строка) переопределяет. Грузится **только на хосте** (launcher). Формат — `KEY=VALUE` построчно → map; `auth.from: env:X` ищет `X` в map (fallback `os.Getenv`), `auth.from: file:P` читает файл `P` на хосте. `afm-init` добавляет `secrets.env` в `.gitignore` (проектный и/или глобальный).

## Data flow (упрощённый)

`config.yaml` монтируется в контейнер по тому же абсолютному пути (проект через `-v ProjectDir:ProjectDir`, `~/.afm` → `/home/afm/.afm`), поэтому `LoadFrom` в контейнере даёт **тот же** `cfg.Docker.Agents`. Контейнер читает `url`/`model`/`auth.to`/путь-к-sysprompt прямо из cfg — на хосте резолвятся **только** значения, которых в контейнере нет: секрет и контент sysprompt (лежат в host-only файлах вроде `~/.ai-free/…`).

```
HOST (launcher, ReExec)                              CONTAINER (run.go после re-exec)
load secrets_file (global ~/.afm + project .afm)     read cfg.Docker.Agents  ← config смонтирован
для каждой command с recipe:                         H_p = host(proxy.upstream) если proxy on; иначе ""
  auth.from      → value    → os.Setenv AFM_SECRET_..   для каждой reciped command:
  system_prompt  → content  → os.Setenv AFM_SYSPROMPT_..   BaseURL = host-match(host(cfg.url), H_p)
ScanCommands(skip generated) → NO -v binary              WrapperSpec{Command,RealClaude,AuthTo,BaseURL,
                                                           Model,HasSysPrompt}  ← поля из cfg
                                                         CreateWrappers(specs) → wrapperDir
                                                           → Options.ProxyShimDir + GeneratedAgents
```

- `url`/`model` — в cfg, bake'ятся в скрипт литералом (не секрет, не transient).
- `auth` value и `system_prompt` content — через transient env (`AFM_SECRET_<CMD>` / `AFM_SYSPROMPT_<CMD>`), секрет **не пишется в файл скрипта**.
- transient `AFM_SECRET_*` / `AFM_SYSPROMPT_*` передаются bare-form: launcher `os.Setenv` → `docker run` наследует `os.Environ()` (как существующий паттерн `launcher.go:225`), секрет не светится в argv `docker run`. Wrapper `unset`'ит их до `exec` агента.
- `AFM_URL_<CMD>` **убран** (url берётся из cfg) — отличие от спека 2026-07-06.

## Generated wrapper (точный шаблон)

glm51 (`url=z.ai`, host-match → proxy; proxy on):

```sh
#!/bin/sh
export ANTHROPIC_AUTH_TOKEN="$AFM_SECRET_GLM51"       # <auth.to> = transient value с хоста
unset AFM_SECRET_GLM51
export ANTHROPIC_BASE_URL="http://127.0.0.1:39217"    # baked: адрес proxy при host(url)==upstream — ZAI сохранён
export ANTHROPIC_DEFAULT_HAIKU_MODEL="glm-5.1"        # из recipe.model — маппит все 3 тира
export ANTHROPIC_DEFAULT_SONNET_MODEL="glm-5.1"
export ANTHROPIC_DEFAULT_OPUS_MODEL="glm-5.1"
if [ -n "$AFM_SYSPROMPT_GLM51" ]; then
  _sp=$(mktemp); printf '%s' "$AFM_SYSPROMPT_GLM51" > "$_sp"; chmod 600 "$_sp"; unset AFM_SYSPROMPT_GLM51
  set -- "$@" --append-system-prompt-file "$_sp"
fi
exec /usr/local/bin/claude "$@"                        # absolute realClaude — обходит proxy-shim на PATH, без рекурсии
```

**Всегда:** `export <auth.to>="$AFM_SECRET_<CMD>"; unset AFM_SECRET_<CMD>`; bake'нутый `export ANTHROPIC_BASE_URL=<BaseURL>` (если `url` задан); три `export ANTHROPIC_DEFAULT_*_MODEL=<model>`; опц. sysprompt-блок (iff `system_prompt` задан); `exec <abs realClaude> "$@"`.

- realClaude — abs-путь, разрешённый afm через `exec.LookPath("claude")` **до** prepend'а wrapper-dir к PATH (→ не уходит в рекурсию, не попадает в proxy-shim).
- afm **не передаёт** агенту `--model` и не ставит `ANTHROPIC_*_MODEL` через executor (executor зовёт только `cfg.Command` + stream-json флаги). `model` выставляет только wrapper.

## claude proxy-shim — частный случай WrapperSpec

`pkg/proxy/shim.go:CreateShim` (одиночный `claude`-скрипт `exec env ANTHROPIC_BASE_URL=<proxy> <realClaude>`) поглощается `CreateWrappers`: claude-shim = WrapperSpec с `Command="claude"`, `BaseURL=proxyAddr`, `AuthTo=""`, `Model=""` (→ эмитится «короткий» шаблон: только `export ANTHROPIC_BASE_URL=<proxy>` + `exec <abs realClaude>`). Создаётся когда proxy on. Таким образом **один wrapper-dir на PATH** содержит и generated-врапперы, и claude-shim — все комбинации (proxy off+autoShim on / proxy on+autoShim off / оба on).

## Proxy host-match (P2′) — решается на генерации в run.go

`H_p` = host(proxy.upstream) если proxy on (upstream из `cfg.Proxy.Upstream`, fallback `ANTHROPIC_BASE_URL`); иначе `""`.

- proxy off / `H_p==""` → `BaseURL = cfg.url` (direct)
- proxy on и `host(cfg.url) == H_p` → `BaseURL = <proxyAddr>` (через proxy → ZAI 529-защита)
- proxy on и `host(cfg.url) != H_p` → `BaseURL = cfg.url` (direct; кросс-gateway)

Host извлекается экспортируемым хелпером `proxy.HostOf(url) string` (inline-извлечение в `ZAITransform.Match` рефакторится на вызов `HostOf`); `pkg/docker/wrapper.go` импортирует `pkg/proxy` и переиспользует `HostOf`. Цикла нет: `pkg/proxy` нижестоящий и не импортирует `pkg/docker`. z.ai-агенты сохраняют ZAI, deepseek-агент в том же flow идёт напрямую.

## Файлы

| Файл | Изменение |
|------|-----------|
| `pkg/config/config.go` | `AgentRecipe{Model, URL, SystemPrompt, Auth{From, To}}`; `DockerConfig.AutoShim *bool`, `.Agents map[string]AgentRecipe`, `.SecretsFile string`. merge в `mergeFile` (per-key overlay для `Agents` + валидация). Built-in claude auth-list в getter'е. |
| `pkg/docker/wrapper.go` (новый) | `CreateWrappers([]WrapperSpec) (wrapperDir string, err error)`; `WrapperSpec{Command, RealClaude, AuthTo, BaseURL, Model, HasSysPrompt}`; два шаблона (агент — при `Model!=""`; claude-shim — при `Model==""`); host-match helper; sanitize env-имени (`<NAME>`: uppercase, всё не-`[A-Z0-9_]` → `_`; `glm51`→`GLM51`, `deepseek-v4`→`DEEPSEEK_V4`, `ai-free.claude-glm`→`AI_FREE_CLAUDE_GLM`). Поглощает логику `proxy.CreateShim`. |
| `pkg/docker/secrets.go` (новый) | Парсинг `secrets.env` (`KEY=VALUE`) + merge слоёв (global `~/.afm` + project `.afm`, проектный важнее). |
| `pkg/docker/launcher.go` | `ReExecConfig` получает `Recipes map[string]config.AgentRecipe` и `SecretsFile string` (run.go пробрасывает из `cfg.Docker`). Load `secrets_file`(и); для каждой command с recipe: резолв `auth.from`/`system_prompt` на хосте → transient `os.Setenv` + bare `-e AFM_SECRET_<CMD>`/`-e AFM_SYSPROMPT_<CMD>`. `ScanCommands(f, globalCmd, generated)` — skip generated. `CheckClaudeDockerAuth` — built-in из getter'а вместо хардкода `claudeAuthEnvVars` (`launcher.go:46`). |
| `pkg/orchestrator/orchestrator.go` | `Options.GeneratedAgents map[string]bool` (ключи `cfg.Docker.Agents` при autoShim on; пусто на хост-пути). `proxyForCmd` (уже существует, `orchestrator.go:234`) → проверяет `generated[cmd]`: `true` → `("", shimDir)` (self-route через baked BASE_URL, wrapper-dir на PATH); `claude` → `("","")`; иначе без изменений. |
| `cmd/afm/run.go` | Если `cfg.Docker.AutoShim`: read recipes из cfg; `buildSpecs` (host-match → `BaseURL` из `cfg.url` + `H_p`); `CreateWrappers` (claude-shim если proxy on + generated specs) → единый `Options.ProxyShimDir` + `Options.GeneratedAgents`. Создание wrapper-dir **выносится из proxy-only блока** (`run.go:154`) — теперь работает и без proxy. Грузит secrets_file launcher на хосте (run.go в контейнере читает cfg). |
| `pkg/proxy/shim.go` | `CreateShim` переносится в `pkg/docker/wrapper.go` (claude-shim = WrapperSpec). Файл `shim.go` удаляется; `shim_test.go` переносится/обновляется под `CreateWrappers`. |
| `pkg/executor/executor.go` | Без правок. `Config.ProxyShimDir` семантически = «wrapper-dir, prepended к PATH». |

**Срез существующего кода (proxy-пламбинг):** все 4 вызова `executor.New` (`orchestrator.go:115`, `:250`, `:275`, `:293`) уже ходят через `proxyForCmd` → generated-логика добавляется в одном месте. `proxyForCmd(generated=true)` возвращает `("", shimDir)`: `ProxyURL=""` (executor НЕ инжектит `ANTHROPIC_BASE_URL` — wrapper bake'ит свой), `ProxyShimDir=wrapperDir` (prepended к PATH, чтобы команда `glm51` разрешалась в generated-враппер). `proxyForCmd(claude)` → `("","")` (claude идёт напрямую через OAuth). `proxyForCmd(<mounted>)` → `(proxyURL, shimDir)` без изменений.

## Edge-cases / валидация

- **Нет секрета** (`from` пуст / файл отсутствует) → **fail fast** на re-exec: `agent <cmd>: secret not found (checked: .afm/secrets.env, ~/.afm/secrets.env, env)`.
- **`claude` не на PATH** при генерации → hard error (как `CreateShim`).
- **Конфиг-валидация:** см. Recipe schema → ошибки конфига.
- **`system_prompt` файл отсутствует** → warn + skip sysprompt (не валить).
- **`url` пустой** → wrapper не ставит `ANTHROPIC_BASE_URL` (наследует env).
- **autoShim on + команда без recipe** → mount `:ro` как сегодня (backward compat).
- **`autoShim` off + заполненный `docker.agents`** → `docker.agents` игнорируется, mount `:ro`.
- **Backward compat:** `docker.extra_mounts` не удаляется; конфиги без `docker.agents`/`autoShim` работают как раньше.

## Тесты

- **Unit `pkg/config`:** парсинг `docker.agents`/`autoShim`/`secrets_file` (`model`/`system_prompt`/`auth`); валидация (`model` обязателен; `auth` обязателен; `auth.to` не env → error; `auth.to` не из claude-списка → error); merge слоёв (global+project, проектный важнее); built-in `claude`.
- **Unit `CreateWrappers`:** assert содержимого скрипта — `export <AuthTo>="$AFM_SECRET_<CMD>"` + `unset`; bake'нутый `ANTHROPIC_BASE_URL`; три `ANTHROPIC_DEFAULT_*_MODEL=<model>`; sysprompt-блок эмитится iff `HasSysPrompt`; `exec <abs RealClaude> "$@"`; sanitize env-имени; claude-shim spec (`Model==""`) → короткий шаблон (только BASE_URL).
- **Unit host-match:** `host(url)==H_p` → `BaseURL=<proxy>`; `≠` → `BaseURL=<url>`; proxy off / `H_p==""` → `BaseURL=<url>`.
- **Unit резолв секрета/sysprompt (host):** `from: env:X` (map + fallback `os.Getenv`), `from: file:P` (host read), `system_prompt` контент, `~` по хосту.
- **Shell-тест wrapper'а:** stub realClaude (печатает env) → `AFM_SECRET_*` **отсутствует** в окружении exec'нутого процесса, `ANTHROPIC_AUTH_TOKEN` set, model vars set, sysprompt-файл создан `0600` + `--append-system-prompt-file` передан, `BASE_URL` = proxy или url по host-match.
- **Integration `launcher`:** generated-recipe → нет `-v /usr/local/bin/<cmd>`, есть transient `-e AFM_SECRET_<CMD>`/`-e AFM_SYSPROMPT_<CMD>` (через `SetExecFunc`, как в `launcher_test.go`); нет `-e AFM_URL_<CMD>`.
- **Integration `run.go`/`buildSpecs`:** recipe+`model`+`autoShim` → generated `WrapperSpec`; host-match → правильный `BaseURL`; `Options.GeneratedAgents` заполнен.
- **`proxyForCmd`:** `generated[cmd]=true` → `("", shimDir)`; `claude` → `("","")`; mounted → `(proxyURL, shimDir)`.
- **Edge:** нет секрета → fail fast (текст); `claude` не на PATH → hard error; валидации конфига; `system_prompt` отсутствует → warn+skip; autoShim off + agents → игнор; backward compat (конфиг с `extra_mounts`/без `agents` работает).
