# Memory pipeline

Пользовательский контракт: [docs/agent-memory.md](../../docs/agent-memory.md).
Этот движок общий для `pkg/orchestrator/reflection.go` и `cmd/afm/memory_rebuild.go`.

- Capture собирает только reflect dataset и выполняется без memory-lock.
  Live capture идёт per-stage в фоне; Finalize запускается в конце рана после
  pending reflections. Не обещай перенос новых правил внутри текущего рана.
- Finalize подготавливает кандидаты, затем публикует их. Вызывающий обязан
  держать memory-lock до завершения finalize+commit. Общий порядок захвата:
  run-lock → memory-lock. CLI повторяет commit-preflight под memory-lock.
- Lock зависит от канонического абсолютного пути memory-dir с разрешением
  существующих symlink-предков; живёт вне каталога памяти и не зависит от его
  содержимого. Contention ожидается до освобождения или отмены context.
- Каждая попытка использует свежий staging directory. Все кандидаты готовятся
  до публикации; atomic write действует на отдельный файл, не на весь набор.
  Сохраняй backups и Published-маркеры manifest для частичного сбоя публикации.
- Offline capture пишет в attempt workspace. Только promotion обновляет
  canonical reflect datasets; `--dry-run` не публикует их и memory-файлы, не
  коммитит, но выполняет агентов и пишет staging/logs/manifest.
- CLI rebuild — strict: capture/finalize/commit/manifest ошибки дают non-zero
  exit. Live memory — best-effort notice без изменения FSM. Не уравнивай эти пути.
- MemoryUse управляет чтением, memory.mode — общим файлом, reflect.mode — файлом
  стадии. Инъекция передаёт пути существующих файлов, не содержимое.
- Shared reflect-файлы обрабатываются последовательно с передачей кандидата
  между стадиями; пустой dataset/отсутствие High не стирает прежние правила.
- Reflect учитывает прямой user-input наряду с логами. Не включай собственные
  memory-артефакты в SourceInventory. AFM-протокол исключён из project patterns.
- Агенты работают в свежем контексте без `AFM_STAGE_DIR`. Для тестов инъектируй
  AgentRunner через `WithRunner`; не запускай реальных агентов.
