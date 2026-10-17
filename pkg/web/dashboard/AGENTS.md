# Dashboard

Поведение: [dashboard](../../../docs/dashboard.md). Проверки: `make web-test`
и `make web-typecheck` из корня; Vite build их не выполняет.

- `useWorkspaceView` владеет режимом workspace, `useSelectedStage` — выбором.
  Не размножай их состояние в App/компонентах. Список режимов и статусов бери
  из типов; stage statuses генерируются из Go (`make generate-check`).
- Новый attention не крадёт фокус во время редактирования или открытого overlay.
  Повторный attention различается по episode; helper selection не превращается
  в stage selection и не переключается автоматически после завершения helper.
- Auto-advance: `wasLive` живёт весь текущий selection. Когда выбранная стадия
  завершилась, поиск следующей активной повторяется при каждом poll; короткая
  script-стадия может целиком пройти между poll. Ручной выбор done не ставит флаг.
- `useFilePreview` инвалидирует ответы при каждом переключении файла/вкладки и
  unmount, даже при возвращении на тот же путь. Reload использует тот же путь.
  Image upload тоже защищён generation выбранной стадии.
- Таймеры используют server accumulator + anchor. Тик зависит от свежести
  `/api/status`, не WebSocket; при stale-ответе замирает, terminal фиксирует число.
- Общие textarea/Markdown/rendering-хелперы сохраняют единое поведение вложений,
  submit и экранирования. Исходник файла не передаётся Markdown-renderer;
  подсветка использует безопасный общий highlighter.
- Компоненты используют semantic tokens из `skins/base/tokens.css`. При смене
  дефолтной темы синхронно меняются HTML default, Go config и server replace-anchor.
- Capability гейтит UI и запросы: выключенный file browser не делает `/api/files/*`.
  План/диалог сверяются с серверными ShowPlan/ShowDialog, включая paused auto-стадии.
