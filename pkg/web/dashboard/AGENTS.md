# Dashboard

Поведение: [dashboard](../../../docs/dashboard.md). Проверки: `make web-test`
и `make web-typecheck` из корня; Vite build их не выполняет.

- `useWorkspaceView` владеет режимом workspace, `useSelectedStage` — выбором.
  Не размножай их состояние в App/компонентах. Список режимов и статусов бери
  из типов; stage statuses генерируются из Go (`make generate-check`).
- Новый attention не крадёт фокус во время редактирования или открытого overlay.
  Модалки внутри видов берут фокус, Escape/Tab и возврат фокуса из `useModalDialog`
  (`lib/focus-trap`): он сообщает App об открытом диалоге из layout-эффекта, и синк
  attention того же commit читает это число сразу. Иначе фокус на checkbox/кнопке
  или вопрос в одном batch с открытием размонтируют вид вместе с черновиком.
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
- Время стадий вычисляется один раз в App общими `useStageClock`/`stageTimes`;
  rail и Cost получают один результат. Cost использует тот же Run elapsed, что шапка.
  Нет `timing` — время неизвестно при любом статусе: факт старта по статусу не
  выводится, Retry возвращает стадию в pending/ready.
- Слот `aboveFeed` (Stage note) FeedWorkspace рендерит внутри фрагмента: его `key` — id стадии, как и у композера
  (`noteTarget`); совпавшие ключи среди детей `section` оставляют осиротевший DOM заметки с живыми обработчиками.
- Общие textarea/Markdown/rendering-хелперы сохраняют единое поведение вложений,
  submit и экранирования. Исходник файла не передаётся Markdown-renderer;
  подсветка использует безопасный общий highlighter.
- Компоненты используют semantic tokens из `skins/base/tokens.css`. При смене
  дефолтной темы синхронно меняются HTML default, Go config и server replace-anchor.
- Capability гейтит UI и запросы: выключенный file browser не делает `/api/files/*`.
  Mesh-UI ленты стадии (беседы и их опрос, режим Message) требует mesh И
  collaboration; People требует также `collaboration_mode: team`. Team и Board
  требуют успешной mesh-пробы, run id и режима `team`; Board не опрашивается в
  `assistants`. Run conversations требует mesh-пробу, run id и режима `assistants`.
  Краткий сбой `/api/status` сохраняет последний режим и не размонтирует Team.
  План/диалог сверяются с серверными ShowPlan/ShowDialog, включая paused auto-стадии.
