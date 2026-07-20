import { Fragment, type ReactNode } from 'react'
import { Group, Panel, Separator, useDefaultLayout, type Layout } from 'react-resizable-panels'

type Props = {
  stages: ReactNode
  // Заголовок выбранной стадии (имя + статус-бейдж) над plan; null, когда стадия не выбрана.
  stageHeader: ReactNode
  // null — панель скрыта (автономная стадия для plan; недиалоговая для dialog).
  plan: ReactNode | null
  dialog: ReactNode | null
  log: ReactNode
  feed: ReactNode
}

// Локальный persistence-хук для одной группы панелей: восстанавливает layout
// из localStorage при монтировании и сохраняет его после ручного ресайза.
// Ключ для колонок фиксирован (afm-cols); для строк — зависит от набора
// видимых панелей: afm-rows-<ids> (напр. afm-rows-plan-dialog-log). react-
// resizable-panels v4 не имеет autoSaveId — persistence реализуется через
// useDefaultLayout({ id, storage }).
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

type RowId = 'plan' | 'dialog' | 'log'

// Доли строк для полного набора; при скрытии панелей берутся только присутствующие id.
const ROW_SHARES: Record<RowId, number> = { plan: 30, dialog: 45, log: 25 }

// Трёхколоночный resizable-лейаут: StagesList | центральная колонка (detail) | EventFeed.
// Центральная колонка — flex-col: detail-header сверху, под ним вертикальная Group
// (plan/dialog/log). Размеры колонок и строк сохраняются в localStorage.
//
// Внимание: API react-resizable-panels v4.x — Group/Panel/Separator + useDefaultLayout
// (в задаче упоминался v2-style autoSaveId/PanelResizeHandle, но установлен v4.12.2).
export function DashboardLayout({ stages, stageHeader, plan, dialog, log, feed }: Props) {
  const cols = useSavedLayout('afm-cols', DEFAULT_COLS)

  // Присутствующие строки в порядке plan → dialog → log. plan/dialog опускаются,
  // когда проп null (автономная/неинтерактивная стадия). log присутствует всегда.
  const rowPanels: Array<{ id: RowId; node: ReactNode; minSize: string }> = []
  if (plan !== null) rowPanels.push({ id: 'plan', node: plan, minSize: '15' })
  if (dialog !== null) rowPanels.push({ id: 'dialog', node: dialog, minSize: '15' })
  rowPanels.push({ id: 'log', node: log, minSize: '10' })

  // Storage-ключ и defaultLayout зависят от набора панелей: у каждого набора
  // своя сохранённая раскладка, ключи всегда совпадают с присутствующими id
  // (иначе react-resizable-panels распределяет доли по несуществующим панелям).
  const rowsStorageId = `afm-rows-${rowPanels.map((p) => p.id).join('-')}`
  const rowsFallback: Layout = Object.fromEntries(rowPanels.map((p) => [p.id, ROW_SHARES[p.id]]))
  const rows = useSavedLayout(rowsStorageId, rowsFallback)

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
            defaultLayout={rows.defaultLayout ?? rowsFallback}
            onLayoutChanged={rows.onLayoutChanged}
          >
            {rowPanels.map((p, i) => (
              <Fragment key={p.id}>
                {i > 0 && <Separator className="resize-handle resize-handle-h" />}
                <Panel id={p.id} minSize={p.minSize}>
                  {p.node}
                </Panel>
              </Fragment>
            ))}
          </Group>
        </div>
      </Panel>
      <Separator className="resize-handle resize-handle-v" />
      <Panel id="feed" minSize="12">{feed}</Panel>
    </Group>
  )
}
