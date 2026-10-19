# Agent processes

- Агент и его потомки живут в `processGroup`, лидер которой — якорь afm, а не агент.
  Якорь — сам бинарник afm со скрытым аргументом, без внешнего shell: поэтому
  `ServeProcessGroupAnchor` — первый вызов в `main` и в `TestMain` пакета executor.
  Не шли сигнал на сырой pgid после reap лидера: номер мог достаться чужой группе.
  Пока якорь не reap'нут, pgid наш; `release` убивает группу и только потом reap'ает
  якорь — в конце каждого хода, даже если потомки уже закрыли pipes. Если якорь не
  стартовал (тестовый бинарник другого пакета, бинарник удалён), агент сам лидер
  группы — запуск не должен падать из-за якоря.
- stdin/stdout/stderr принадлежат afm (`agentPipes`), не `exec.Cmd`: `Wait` не ждёт
  copy-горутин, промпт пишет своя горутина, её останавливает закрытие stdin после выхода.
- Pipe считается удержанным потомком, только если reader ждёт данных `pipeGracePeriod`
  после выхода; медленный callback — не тишина. Тогда группа убивается. Независимо от
  активности вывода группа убивается через `pipeDrainLimit` после выхода, а также при
  отмене ctx и idle timeout: пишущий без пауз потомок не держит ход вечно. После kill
  чтение длится не дольше `pipeFlushPeriod`, затем обрывается с предупреждением в
  stderr-лог.
- `Config.Dir` — CWD агента; абсолютный `Config.StageDir` передаётся как
  `AFM_STAGE_DIR`. Эти пути независимы.
- Verify и helper запуски используют свежую изолированную сессию без stage
  dialog-env и managed browser. Memory/JSON-fix тоже не наследуют `AFM_STAGE_DIR`.
- Сохраняй общий фильтр cross-agent transport secrets, включая shell-verify.
  Удаляй inherited `CODEX_VERIFY`; режим проверки задаёт executor.
- Bare Claude verifier сохраняет Claude-креды автора; не-Claude verifier их
  теряет. Recipe-verifier использует свой секрет. Не обобщай эти случаи в одно
  правило «срезать все креды» или «наследовать всё окружение».
- Read-only гарантия зависит от адаптера и окружения: Codex verify на хосте
  использует sandbox, собственный Docker-ран — доверие контейнеру. Не-Claude
  команды и bare Claude нельзя объявлять read-only только на основании промпта.
- MCP stdout предназначен только для JSON-RPC; диагностический вывод — stderr.

Контракты завершения и режимов: [prompts](../../docs/internals/prompts.md),
[verify](../../docs/verify.md). Browser-транспорт:
[browser/AGENTS.md](../browser/AGENTS.md).
