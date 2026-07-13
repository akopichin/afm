import { type ReactNode } from 'react'
import { Group, Panel, Separator, useDefaultLayout, type Layout } from 'react-resizable-panels'

type Props = {
  stages: ReactNode
  // Заголовок выбранной стадии (имя + статус-бейдж) над plan; null, когда стадия не выбрана.
  stageHeader: ReactNode
  plan: ReactNode
  dialog: ReactNode
  log: ReactNode
  feed: ReactNode
}

// Локальный persistence-хук для одной группы панелей: восстанавливает layout
// из localStorage (afm-cols / afm-rows) при монтировании и сохраняет его после
// ручного ресайза. react-resizable-panels v4 не имеет autoSaveId — persistence
// реализуется через useDefaultLayout({ id, storage }).
//
// ВАЖНО: при пустом storage useDefaultLayout всё равно возвращает вычисленный
// layout (равные доли), поэтому по самому объекту не отличить «свечая загрузка»
// от «пользователь сохранял». Различаем по наличию ключа в storage: нет ключа →
// свежая загрузка → fallback с начальными пропорциями.
function useSavedLayout(id: string, fallback: Layout) {
  const saved = useDefaultLayout({ id, storage: localStorage })
  const hasStoredLayout = localStorage.getItem(id) !== null
  return {
    defaultLayout: hasStoredLayout ? saved.defaultLayout : fallback,
    onLayoutChanged: saved.onLayoutChanged,
  }
}

// Layout в v4 — это map Panel-id → flexGrow (доли). Это и начальные пропорции:
// stages:detail:feed = 15:60:25, plan:dialog:log = 30:45:25 (диалогу — больше).
// Применяется только когда в storage нет сохранённого layout (свежая загрузка);
// после ручного ресайза восстанавливается сохранённое значение.
const DEFAULT_COLS: Layout = { stages: 15, detail: 60, feed: 25 }
const DEFAULT_ROWS: Layout = { plan: 30, dialog: 45, log: 25 }

// Трёхколоночный resizable-лейаут: StagesList | центральная колонка (detail) | EventFeed.
// Центральная колонка — flex-col: detail-header сверху, под ним вертикальная Group
// (plan/dialog/log). Размеры колонок и строк сохраняются в localStorage.
//
// Внимание: API react-resizable-panels v4.x — Group/Panel/Separator + useDefaultLayout
// (в задаче упоминался v2-style autoSaveId/PanelResizeHandle, но установлен v4.12.2).
export function DashboardLayout({ stages, stageHeader, plan, dialog, log, feed }: Props) {
  const cols = useSavedLayout('afm-cols', DEFAULT_COLS)
  const rows = useSavedLayout('afm-rows', DEFAULT_ROWS)

  return (
    <Group
      orientation="horizontal"
      className="dashboard-cols"
      defaultLayout={cols.defaultLayout ?? DEFAULT_COLS}
      onLayoutChanged={cols.onLayoutChanged}
    >
      <Panel id="stages" minSize="12">{stages}</Panel>
      <Separator className="resize-handle resize-handle-v" />
      <Panel id="detail">
        <div className="detail-column">
          <div id="detail-header" className="detail-header">{stageHeader}</div>
          <Group
            orientation="vertical"
            className="detail-rows"
            defaultLayout={rows.defaultLayout ?? DEFAULT_ROWS}
            onLayoutChanged={rows.onLayoutChanged}
          >
            <Panel id="plan" minSize="15">{plan}</Panel>
            <Separator className="resize-handle resize-handle-h" />
            <Panel id="dialog" minSize="15">{dialog}</Panel>
            <Separator className="resize-handle resize-handle-h" />
            <Panel id="log" minSize="10">{log}</Panel>
          </Group>
        </div>
      </Panel>
      <Separator className="resize-handle resize-handle-v" />
      <Panel id="feed" minSize="12">{feed}</Panel>
    </Group>
  )
}
