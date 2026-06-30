# Встроенный Anthropic-совместимый прокси

**Дата:** 2026-06-29  
**Статус:** approved

## Цель

Встроить в FlowManager Go-прокси, который перехватывает HTTP-трафик между агентом (claude / wrapper-скрипт) и Anthropic-совместимым шлюзом. Прокси включён по умолчанию; конкретные трансформации (например, фикс для z.ai) применяются автоматически по upstream-хосту с возможностью явного override в конфиге.

Первая трансформация — **ZAI Transform**: конвертирует non-streaming запросы в streaming для шлюза `api.z.ai`, что устраняет 529-ошибки, возникающие при `thinking + stream=None` на моделях glm-5.x.

---

## Архитектура

```
flowmanager run
  │
  ├── читает proxy.upstream из конфига или ANTHROPIC_BASE_URL env
  ├── запускает pkg/proxy.Proxy на случайном порту (→ proxyAddr)
  ├── создаёт claude-шим в temp-dir (→ shimDir)
  │
  └── orchestrator.Options{ ProxyURL: proxyAddr, ProxyShimDir: shimDir }
           └── executor.Config{ ProxyURL, ProxyShimDir }
                    └── cmd.Env:
                          ANTHROPIC_BASE_URL=proxyAddr   (для command: claude)
                          FLOWMANAGER_PROXY_URL=proxyAddr
                          PATH=shimDir:...               (для wrapper-скриптов)

agent subprocess (claude / ai-free.claude-glm / ...)
  │  HTTP POST /v1/messages  → http://127.0.0.1:PORT
  ▼
pkg/proxy
  │  Match(upstream) → выбираем трансформацию
  │    ZAITransform  → non-streaming → streaming + SSE→JSON
  │    (нет match)   → passthrough
  ▼
upstream (api.z.ai / api.anthropic.com / ...)
```

### Поддержка wrapper-скриптов без правки вручную

Wrapper-скрипты (вроде `ai-free.claude-glm`) явно делают `export ANTHROPIC_BASE_URL=...`, перебивая env агента. Решение — **claude shim**:

FlowManager создаёт `/tmp/fm-proxy-shim-*/claude` — shell-скрипт, который вызывает настоящий claude с нашим URL:

```sh
#!/bin/sh
exec env ANTHROPIC_BASE_URL=http://127.0.0.1:PORT /usr/local/bin/claude "$@"
```

Temp-dir прописывается первым в `PATH` агента. Когда wrapper делает `exec claude`, находит шим. Используется абсолютный путь к реальному claude (через `exec.LookPath` до модификации PATH), рекурсии нет.

Два механизма в паре дают полное покрытие:

| Сценарий | Механизм |
|---|---|
| `command: claude` напрямую | `ANTHROPIC_BASE_URL` в env |
| `command: <wrapper>` | claude shim в PATH |

---

## Конфигурация

### config.yaml

```yaml
proxy:
  enabled: false              # по умолчанию true
  upstream: ""                # "" → берём из ANTHROPIC_BASE_URL env
  port: 0                     # 0 → случайный свободный порт
  transforms:
    zai: ~                    # ~ (null) → авто по хосту; true/false → override
```

Если `upstream` пуст и `ANTHROPIC_BASE_URL` не задан — прокси не стартует, трафик идёт напрямую (info-лог).

### Go: `pkg/config`

```go
type ProxyConfig struct {
    Enabled    *bool              `yaml:"enabled"`
    Upstream   string             `yaml:"upstream"`
    Port       int                `yaml:"port"`
    Transforms TransformOverrides `yaml:"transforms"`
}

type TransformOverrides struct {
    ZAI *bool `yaml:"zai"` // nil = авто-детект
}

// IsEnabled возвращает true по умолчанию (nil → true).
func (p ProxyConfig) IsEnabled() bool {
    return p.Enabled == nil || *p.Enabled
}
```

`Config` получает поле `Proxy ProxyConfig`. `mergeFile` мержит секцию по тем же правилам, что и остальные секции.

---

## pkg/proxy

### Структура пакета

```
pkg/proxy/
  proxy.go      — Proxy, Start, Shutdown, Addr
  transform.go  — Transform интерфейс, passthrough-хендлер
  zai.go        — ZAITransform + parseSSE
  shim.go       — CreateShim
```

### Transform интерфейс

```go
// Transform — трансформация HTTP-запроса/ответа для конкретного upstream.
type Transform interface {
    // Match возвращает true, если трансформация применима к данному upstream URL.
    Match(upstreamURL string) bool
    // ServeHTTP обрабатывает запрос. upstream — валидированный URL назначения.
    ServeHTTP(w http.ResponseWriter, r *http.Request, upstream string)
}
```

### Proxy

```go
type Proxy struct {
    upstream   string
    transforms []Transform
    srv        *http.Server
    addr       string
}

// New создаёт Proxy с заданным upstream и списком трансформаций.
func New(upstream string, transforms []Transform) *Proxy

// Start запускает HTTP-сервер на 127.0.0.1:port (0 = случайный порт).
// Возвращает "http://127.0.0.1:PORT".
func (p *Proxy) Start(port int) (addr string, err error)

// Addr возвращает адрес после Start.
func (p *Proxy) Addr() string

// Shutdown останавливает сервер.
func (p *Proxy) Shutdown(ctx context.Context) error

// BuildTransforms строит список трансформаций по upstream и override-флагам.
// zai: nil = авто-детект, true = всегда, false = никогда.
func BuildTransforms(upstream string, zai *bool) []Transform
```

`BuildTransforms`: если `zai == nil` — включаем `ZAITransform` когда `upstream` содержит `api.z.ai`; если явно `true` — всегда; если явно `false` — никогда.

Caller в `run.go` передаёт `cfg.Proxy.Transforms.ZAI` напрямую. `pkg/proxy` не импортирует `pkg/config`.

`ServeHTTP` у прокси: перебирает трансформации, первая с `Match(upstream)==true` обрабатывает запрос. Если ни одна не подошла — passthrough (reverse proxy через `net/http`).

### CreateShim

```go
// CreateShim создаёт temp-директорию с claude-шимом.
// proxyAddr — адрес прокси (http://127.0.0.1:PORT).
// Возвращает путь к директории; caller отвечает за defer os.RemoveAll.
// Если claude не найден в PATH — возвращает ошибку (non-fatal для caller).
func CreateShim(proxyAddr string) (shimDir string, err error)
```

---

## ZAI Transform

### Match

```go
func (t *ZAITransform) Match(upstreamURL string) bool {
    return strings.Contains(upstreamURL, "api.z.ai")
}
```

### Логика ServeHTTP

```
запрос с stream=true  → passthrough к upstream без изменений
запрос с stream=false/absent:
  1. добавляем "stream": true в тело
  2. шлём на upstream
  3. разбираем SSE-ответ:
       HTTP ≠ 200          → форвардим статус as-is
       event: error        → возвращаем 529 + JSON ошибки
       пустой SSE          → возвращаем 529, message "empty SSE response"
       нормальный контент  → собираем и возвращаем non-streaming JSON
```

### SSE-парсер: поддерживаемые event-типы

| Event | Действие |
|---|---|
| `message_start` | инициализируем message, usage |
| `content_block_start` | начинаем блок (text / thinking / tool_use) |
| `content_block_delta` | накапливаем text_delta, thinking_delta, input_json_delta, signature_delta |
| `content_block_stop` | для tool_use: JSON.parse накопленного input_json_delta |
| `message_delta` | сохраняем stop_reason, обновляем usage |
| `message_stop` | конец потока |
| `error` | → 529 |

### Результирующий JSON (non-streaming)

```json
{
  "id": "...",
  "type": "message",
  "role": "assistant",
  "model": "...",
  "content": [...],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": { "input_tokens": N, "output_tokens": N }
}
```

---

## Интеграция: run.go

```go
var proxyAddr, proxyShimDir string
if cfg.Proxy.IsEnabled() {
    upstream := cfg.Proxy.Upstream
    if upstream == "" {
        upstream = os.Getenv("ANTHROPIC_BASE_URL")
    }
    if upstream != "" {
        transforms := proxy.BuildTransforms(upstream, cfg.Proxy.Transforms.ZAI)
        p := proxy.New(upstream, transforms)
        addr, err := p.Start(cfg.Proxy.Port)
        if err != nil {
            return fmt.Errorf("start proxy: %w", err)
        }
        defer p.Shutdown(context.Background())
        proxyAddr = addr
        fmt.Printf("  proxy: %s → %s\n", addr, upstream)

        if shimDir, err := proxy.CreateShim(addr); err == nil {
            proxyShimDir = shimDir
            defer os.RemoveAll(shimDir)
        } else {
            fmt.Fprintf(os.Stderr, "warning: proxy shim: %v\n", err)
        }
    } else {
        fmt.Println("  proxy: skipped (no upstream configured)")
    }
}

orch := orchestrator.New(orchestrator.Options{
    // ... существующие поля ...
    ProxyURL:     proxyAddr,
    ProxyShimDir: proxyShimDir,
})
```

## Интеграция: orchestrator → executor

`orchestrator.Options` и `executor.Config` получают два поля:

```go
ProxyURL     string // → ANTHROPIC_BASE_URL в env агента
ProxyShimDir string // → prepend to PATH в env агента
```

## Интеграция: executor.go — сборка env

```go
env := os.Environ()
filtered := make([]string, 0, len(env)+3)
for _, kv := range env {
    switch {
    case strings.HasPrefix(kv, "CLAUDECODE="):
        // убираем для вложенных сессий
    case e.cfg.ProxyURL != "" && strings.HasPrefix(kv, "ANTHROPIC_BASE_URL="):
        // убираем — заменим адресом прокси ниже
    default:
        filtered = append(filtered, kv)
    }
}
if e.cfg.StageDir != "" {
    filtered = append(filtered, "FLOWMANAGER_STAGE_DIR="+e.cfg.StageDir)
}
if e.cfg.ProxyURL != "" {
    filtered = append(filtered, "ANTHROPIC_BASE_URL="+e.cfg.ProxyURL)
    filtered = append(filtered, "FLOWMANAGER_PROXY_URL="+e.cfg.ProxyURL)
}
if e.cfg.ProxyShimDir != "" {
    pathSet := false
    for i, kv := range filtered {
        if strings.HasPrefix(kv, "PATH=") {
            filtered[i] = "PATH=" + e.cfg.ProxyShimDir + ":" + kv[5:]
            pathSet = true
            break
        }
    }
    if !pathSet {
        filtered = append(filtered, "PATH="+e.cfg.ProxyShimDir+":"+os.Getenv("PATH"))
    }
}
cmd.Env = filtered
```

---

## Обработка ошибок

| Ситуация | Поведение |
|---|---|
| Прокси не смог занять порт | Hard error, `run` завершается |
| upstream пуст (нет конфига и нет env) | Прокси не стартует, info-лог, работаем напрямую |
| Шим не создан (нет `claude` в PATH) | Non-fatal warning, работаем без шима |
| z.ai вернул HTTP ≠ 200 на streaming-запрос | Форвардим статус as-is |
| SSE содержит `event: error` | Возвращаем 529 + JSON ошибки |
| SSE пришёл пустым | Возвращаем 529, `"empty SSE response"` |
| Соединение к upstream разорвано | Возвращаем 502 |

---

## Тестирование

### pkg/proxy — юнит-тесты

| Тест | Что проверяет |
|---|---|
| `TestZAITransform_Match` | авто-детект `api.z.ai`, игнор других хостов |
| `TestParseSSE_Text` | text блоки |
| `TestParseSSE_Thinking` | thinking блоки |
| `TestParseSSE_ToolUse` | tool_use + input_json_delta |
| `TestParseSSE_Error` | event: error → ошибка возвращается |
| `TestParseSSE_Empty` | пустой body → ошибка |
| `TestZAITransform_PassthroughStreaming` | stream=true → passthrough без изменений |
| `TestZAITransform_ConvertNonStreaming` | stream=false → streaming upstream → JSON |
| `TestZAITransform_SSEError_Returns529` | SSE error → 529 |
| `TestBuildTransforms_AutoDetect` | ZAI включается для `api.z.ai`, выключается для `api.anthropic.com` |
| `TestBuildTransforms_Override` | явный true/false из конфига |
| `TestProxy_Start` | прокси стартует, слушает, шатдаунится |

Upstream во всех тестах — `httptest.NewServer`, не реальный z.ai.

### pkg/config — юнит-тест

- `TestProxyConfig_IsEnabled`: nil → true, explicit false → false

### pkg/executor — расширение существующего теста

Существующий тест на env проверяет `FLOWMANAGER_STAGE_DIR`. Добавляем проверку:
- при `ProxyURL != ""`: `ANTHROPIC_BASE_URL` и `FLOWMANAGER_PROXY_URL` присутствуют в env, исходный `ANTHROPIC_BASE_URL` из env не дублируется
- при `ProxyShimDir != ""`: `PATH` начинается с shim dir

---

## Файлы, затрагиваемые реализацией

| Файл | Изменение |
|---|---|
| `pkg/config/config.go` | + `ProxyConfig`, `TransformOverrides`, мерж в `mergeFile` |
| `pkg/proxy/proxy.go` | новый файл |
| `pkg/proxy/transform.go` | новый файл |
| `pkg/proxy/zai.go` | новый файл |
| `pkg/proxy/shim.go` | новый файл |
| `cmd/flowmanager/run.go` | запуск прокси + шима, передача в `orchestrator.Options` |
| `pkg/orchestrator/orchestrator.go` | + `ProxyURL`, `ProxyShimDir` в `Options`, прокидка в executor |
| `pkg/executor/executor.go` | + `ProxyURL`, `ProxyShimDir` в `Config`, сборка env |
| `config.example.yaml` | + секция `proxy:` с комментариями |
