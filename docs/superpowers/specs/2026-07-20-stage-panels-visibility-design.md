# Скрытие panel/dialog по типу стейджа

**Дата:** 2026-07-20
**Ветка:** ux-improvements
**Область:** `pkg/server` (backend `/api/status`), `pkg/web/dashboard` (frontend), `cmd/afm/run.go` (проводка)

## Проблема

В деталях выбранного стейджа всегда показываются оба раздела — **plan** и **dialog** —
даже когда они не имеют смысла:

1. **Автономный** стейдж (супервизор выбрал autonomous-трек) пропускает planning →
   у него нет `plan.md`. Раздел plan для него бессмысленный.
2. Стейдж с `interactive: false` (и не ушедший в autonomous-трек) не может иметь
   диалога — раздел dialog для него бессмысленный.

## Цель

Скрывать раздел для выбранного стейджа, когда он невозможен, — но только для этого
стейджа (для остальных разделы остаются).

## Правила отображения (для выбранного стейджа)

- **Plan** — показывать iff `!autonomous`.
- **Dialog** — показывать iff `interactive || autonomous`.

Обоснование правила dialog: автономный трек **всегда диалоговый** (`runAutonomousAgent`
строит промпт с `Interactive: true`) и выбирается супервизором независимо от
`stage.Interactive`. Поэтому `interactive:false`-стейдж, ушедший в autonomous, имеет
реальный диалог — прятать его нельзя. Диалог скрывается только когда он точно
невозможен: `!interactive && !autonomous`.

Обоснование правила plan: autonomous-трек не создаёт `plan.md`; не-автономные стейджи
по валидации флоу (`flow.go`: `Plan != "" || HasAgent(planning) || Interactive`) всегда
имеют план. Значит достаточно `!autonomous`.

## Дизайн

### Backend: два per-stage флага в `/api/status`

Сейчас `handleStatus` (`pkg/server/handlers.go`) кодирует голый снапшот
`s.store.Snapshot()`. Расширяем ответ двумя параллельными картами (в духе
существующей `stage_names`):

- `stage_interactive: map[id]bool` — **статический** конфиг из flow. Сервер его
  сейчас не знает → добавить поле `StageInteractive map[string]bool` в
  `server.Config` и `Server`. Заполняется в `cmd/afm/run.go` при создании сервера из
  `f.Stages` (там `Interactive` уже имеет финальное значение — строки 218–221 гасят
  его, если дашборд выключен).
- `stage_autonomous: map[id]bool` — **рантайм**: сервер делает
  `os.Stat(filepath.Join(runDir, id, "autonomous.flag"))` для каждого стейджа из
  снапшота (тот же авторитетный сигнал, что `orchestrator.isAutonomousStage`).

`handleStatus` кодирует обёртку:

```go
type statusResponse struct {
    state.RunState
    StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
    StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
}
```

`StageInteractive` берётся из `s.stageInteractive`; `StageAutonomous` вычисляется по
`autonomous.flag`. Встраивание `state.RunState` сохраняет существующие поля (flow_name,
stage_order, stage_names, stages, …) без изменений — обратная совместимость.

### Frontend: типы, парсинг, условный рендер

- `Stage` (`src/types/stage.ts`) получает поля `interactive: boolean` и
  `autonomous: boolean`.
- `useStatus` (`src/hooks/use-status/use-status.ts`): `normalizeStatus` читает
  `stage_interactive` / `stage_autonomous` (объекты по id), `toStage` проставляет
  флаги (дефолт `false`, если карта/ключ отсутствуют).
- `App` (`src/app/App.tsx`) для выбранного стейджа вычисляет:
  - `showPlan = selectedStage === null || !selectedStage.autonomous`
  - `showDialog = selectedStage === null || selectedStage.interactive || selectedStage.autonomous`
  - (когда стадия не выбрана — обе панели показываются, нейтральное состояние)
  - передаёт `plan={showPlan ? <PlanPanel …/> : null}` и
    `dialog={showDialog ? <DialogChannel …/> : null}`.
- `DashboardLayout` (`src/components/layout/DashboardLayout.tsx`): условно рендерит
  `<Panel id="plan">` и `<Panel id="dialog">` (со своими `<Separator>`) только когда
  соответствующий проп не `null`. `defaultLayout` строки собирается **только из
  присутствующих** панелей (иначе react-resizable-panels v4 распределит пропорции по
  ключам, часть которых относится к отсутствующим панелям).

### Нюанс лейаута (обязательная проверка)

`react-resizable-panels` v4 хранит layout по id панелей в localStorage (`afm-rows`).
При скрытии панели:
- `defaultLayout` для вертикальной группы должен содержать только id присутствующих
  панелей (`plan`/`dialog`/`log` в нужной комбинации), иначе пропорции «поедут».
- Разделители (`Separator`) между панелями рендерятся только между присутствующими
  панелями (нет висящего разделителя сверху/снизу).

Проверяется тестом `DashboardLayout` (рендер сокращённого набора) и визуальной
проверкой в браузере.

### Тайминг

До отработки супервизора стейдж ещё не помечен `autonomous.flag` — plan будет виден,
затем (при появлении флага, поллинг статуса 3с) скроется. Приемлемо, специальной
обработки не требуется.

## Тесты

**Backend** (`pkg/server/handlers_test.go`):
- `handleStatus` возвращает `stage_interactive` из сконфигурированной карты и
  `stage_autonomous=true` для стейджа, в чьей директории есть `autonomous.flag`
  (и `false`/отсутствие — когда флага нет).
- Существующие поля ответа (stages, stage_names, stage_order) сохранены.

**Frontend:**
- `use-status`: `normalizeStatus` проставляет `interactive`/`autonomous` из карт;
  дефолт `false` при отсутствии.
- `App`: plan скрыт при `autonomous`, показан иначе; dialog скрыт при
  `!interactive && !autonomous`, показан при `interactive` или `autonomous`.
- `DashboardLayout`: корректный рендер при отсутствии plan / отсутствии dialog /
  отсутствии обоих (панели и разделители соответствуют присутствующим).
- Визуальная проверка в обеих темах (novacorps + goga).

## Вне области

- Логика выбора трека супервизором, запись `autonomous.flag`, содержимое
  `plan.md`/диалога — не меняются.
- Прочие панели (stages, log, feed) и заголовок стейджа — без изменений.
- Поведение при `interactive:true` (диалог всегда показан) — без изменений.
