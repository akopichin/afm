# Agent jobs journal

Журнал `agent-jobs.jsonl` (границы AI-subprocess: кто и когда). Токены и деньги
остаются в `usage.jsonl`. Пакет — leaf: не импортирует accounting, executor,
state, orchestrator.

- Первая строка — `header` (`recording_started_at`, базовый offset): пишется один
  раз при создании файла (fsync файла и каталога) и не переписывается. Файл без
  годного заголовка — unavailable, а не пустой журнал; 0 jobs доказан только
  через `CoverageGaps`.
- Один writer. `Open` режет torn tail перед append, `Load` только читает через
  тот же `replay`. `Open` всегда возвращает рабочий `Store`: при ошибке он
  unavailable, а ход агента не падает.
- Повтор факта молча игнорируется; другой факт того же job — `ErrConflictingRecord`
  и диагностика без замены. `Revision` растёт только от принятой записи и новой
  причины health.
- `started` несёт `Segment` (якорь и база шкалы активного времени); `finished` —
  только `job_id`, исход и длительность. В записях нет argv, env и текстов
  сообщений: `Title` и `Reason` кладёт вызывающий и редактирует заранее.
