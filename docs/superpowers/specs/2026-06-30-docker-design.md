# Docker Mode для afm

**Дата:** 2026-06-30  
**Статус:** approved

## Цель

Запускать `afm` и агентов (`claude` + кастомные команды) внутри Docker-контейнера.  
Пользователь ничего не меняет в командах — `afm run flow.yaml` работает как раньше, но при включённом Docker-режиме процесс автоматически перезапускается внутри контейнера.

---

## Архитектура: Self-Re-Exec

```
Хост:  afm run flow.yaml
         │
         ▼ (docker.enabled=true && AFM_IN_DOCKER не выставлен)
       pkg/docker/launcher.go
         │  1. Сканирует flow.yaml → находит нестандартные command
         │  2. Строит docker run с монтированиями
         │  3. syscall.Exec (replace process, не fork)
         │
         ▼
Docker:  afm run flow.yaml   ← те же аргументы
           (AFM_IN_DOCKER=1, пропускает Docker-логику)
           │
           ├── claude  (агент, из образа)
           ├── glm51   (пример кастомной команды, смонтирована из хоста)
           └── /project/.afm/runs/...  (пишет в смонтированную папку)
```

Ключевые детали:
- `syscall.Exec` заменяет текущий процесс — сигналы (Ctrl+C) корректно доходят до контейнера
- `AFM_IN_DOCKER=1` выставляется внутри контейнера как защита от рекурсии
- `.afm/` пишется в смонтированную папку проекта — артефакты прогона видны на хосте

---

## Конфиг

### `pkg/config/config.go`

```go
type DockerConfig struct {
    Enabled *bool  `yaml:"enabled"` // nil = смотрим AFM_USE_DOCKER
    Image   string `yaml:"image"`   // default: "akopichin/afm:latest"
}
```

Добавляется поле `Docker DockerConfig` в `Config`.  
Метод `IsDockerEnabled()` — аналог `IsEnabled()` у прокси:
- `true` если `Enabled == true` или `AFM_USE_DOCKER=1`
- `false` если `Enabled == false`
- `false` если `AFM_IN_DOCKER=1` (уже внутри контейнера)

### `config.example.yaml`

```yaml
# docker:
#   enabled: true                    # nil/absent → смотрим $AFM_USE_DOCKER
#   image: akopichin/afm:latest      # переопределить образ (например, локальный)
```

### Переменные окружения

| Переменная | Назначение |
|-----------|------------|
| `AFM_USE_DOCKER` | Включить Docker mode (`1` или `true`) |
| `AFM_IN_DOCKER` | Выставляется внутри контейнера — предотвращает рекурсию |
| `AFM_DOCKER_IMAGE` | Переопределить образ без правки конфига |

---

## Пакет `pkg/docker`

### `launcher.go`

**`ScanCommands(flowPath string) []CommandMount`**
- Парсит flow YAML (только flow YAML, не конфиг — в конфиге нет per-stage переопределений)
- Собирает `client.command` из корня flow + per-stage переопределений (если поддержаны в flow.Stage)
- Фильтрует `claude` и пустые значения
- Для каждого уникального бинарника вызывает `exec.LookPath` на хосте
- Возвращает `[]CommandMount{HostPath, ContainerPath}`

**`ReExec(cfg ReExecConfig) error`**
- Принимает: image, projectDir, flowPath, os.Args
- Вызывает `ScanCommands`
- Строит аргументы `docker run`:

```
docker run --rm
  -v /abs/project:/project
  -v ~/.claude:/root/.claude
  -v ~/.afm:/root/.afm
  -v /usr/local/bin/glm51:/usr/local/bin/glm51:ro   ← кастомные команды
  -w /project
  -e AFM_IN_DOCKER=1
  -e ANTHROPIC_API_KEY        (pass-through если выставлен)
  -e ANTHROPIC_BASE_URL       (pass-through если выставлен)
  [-it если os.Stdin — TTY]
  akopichin/afm:latest
  afm run flow.yaml [остальные флаги]
```

- Вызывает `syscall.Exec(dockerPath, args, env)`

### Интеграция с `run.go`

В самом начале `RunE`, до любой другой логики:

```go
if cfg.Docker.IsDockerEnabled() {
    return docker.ReExec(docker.ReExecConfig{
        Image:      cfg.Docker.GetImage(),
        ProjectDir: rootDir,
        FlowPath:   flowPath,
        Args:       os.Args,
    })
}
```

---

## Монтирования

| Хост | Контейнер | Назначение |
|------|-----------|------------|
| `$(pwd)` | `/project` | Проект + `.afm/` (runs, flows, config) |
| `~/.claude/` | `/root/.claude` | Auth, skills, память |
| `~/.afm/` | `/root/.afm` | Глобальный конфиг afm |
| `/path/to/cmd` | `/usr/local/bin/cmd` (`:ro`) | Нестандартные агенты из flow |

---

## Dockerfile.runtime

Новый файл для публичного образа Docker Hub (существующий `Dockerfile` для CI не трогаем):

```dockerfile
# Stage 1: build afm
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /afm ./cmd/afm

# Stage 2: runtime
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y \
      curl git ca-certificates gnupg \
      python3 python3-pip python3-venv \
      build-essential && \
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y nodejs && \
    curl -fsSL https://go.dev/dl/go1.26.linux-amd64.tar.gz | \
      tar -C /usr/local -xz && \
    rm -rf /var/lib/apt/lists/*

ENV PATH="/usr/local/go/bin:$PATH"

RUN npm install -g @anthropic-ai/claude-code

COPY --from=builder /afm /usr/local/bin/afm

WORKDIR /project
ENTRYPOINT ["/usr/local/bin/afm"]
```

---

## Публикация

### Makefile

```makefile
DOCKER_IMAGE := akopichin/afm
DOCKER_TAG   := latest

docker-build:
	docker build -f Dockerfile.runtime -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push: docker-build
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-run:
	docker run --rm -it \
	  -v $(PWD):/project \
	  -v ~/.claude:/root/.claude \
	  -v ~/.afm:/root/.afm \
	  -e ANTHROPIC_API_KEY \
	  $(DOCKER_IMAGE):$(DOCKER_TAG) $(ARGS)
```

### `.goreleaser.yml`

```yaml
dockers:
  - image_templates:
      - "akopichin/afm:{{ .Tag }}"
      - "akopichin/afm:latest"
    dockerfile: Dockerfile.runtime
    build_flag_templates:
      - "--platform=linux/amd64"
```

---

## Документация

### CLAUDE.md — новая секция "Docker Mode"

Описывает:
- Как включить (`docker.enabled` в конфиге или `AFM_USE_DOCKER=1`)
- Что монтируется автоматически
- Таблицу env vars
- Отладку: как проверить что контейнер запущен, как переопределить образ

### README — секция "Running in Docker"

Пример для пользователей без локальной установки afm:

```bash
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/root/.claude \
  -v ~/.afm:/root/.afm \
  -e ANTHROPIC_API_KEY \
  akopichin/afm:latest \
  run flow.yaml
```

---

## Тестирование

- Unit-тест `ScanCommands` — проверяет парсинг flow YAML и фильтрацию `claude`
- Unit-тест `ReExec` — проверяет построение аргументов `docker run` (через мок `syscall.Exec`)
- Интеграционный тест — `make docker-build && docker run ... afm --help` (smoke test образа)
