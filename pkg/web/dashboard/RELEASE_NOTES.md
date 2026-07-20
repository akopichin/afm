# Release Notes — afm dashboard

## 2026-07-20

- Синхронизированы `CODEMANIFEST` всех 14 клеток `src/` с фактическим кодом: исправлены задвоенные пути
  импортов (`pkg/web/dashboard/src/...` → `src/...`), устранены расхождения манифестов с реализацией.
- Обновлены cell-level `.usages/*.md` файлы (`src/types`, `src/hooks/use-status`,
  `src/components/flow-header`, `src/components/dialog-channel`, `src/app`) — приведены в соответствие с
  текущим кодом.
- Расширено тестовое покрытие: 53 → 115 тестов (21 test file, `npx vitest run` — все проходят), закрыты
  критические пробелы покрытия в 7 клетках (типы, хуки `use-elapsed`/`use-event-feed`/`use-stage-log`/
  `use-status`, компоненты `Footer`/`FlowHeader`/`StagesList`/`EventFeedPanel`/`LogPanel`/`DialogChannel`/
  `PlanPanel`/`App`).
- `goga lint` (14 клеток) и `npx tsc --noEmit` — без ошибок.

### Известные проблемы

- `src/components/log-panel/LogPanel.tsx` не различает визуально уровень записи лога (`LogEntry.level`),
  хотя манифест это требует — баг зафиксирован, не исправлялся в рамках этой приёмки (см.
  `docs/tasks/web-dash.md`, раздел Acceptance Status).
