# Agent processes

- Завершай всю process group, не только прямого потомка: внуки могут удерживать
  stdout-pipe и навсегда заблокировать reader/shutdown. Устанавливай группу до Start.
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
