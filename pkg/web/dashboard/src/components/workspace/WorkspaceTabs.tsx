import { useRef, type ReactElement, type KeyboardEvent } from 'react'
import type { AttentionKind } from '../../hooks/use-workspace-view'

// WorkspaceTabDescriptor — одна вкладка воркспейса. kind/count/glow заполняются
// только у контекстной attention-вкладки (approval/question/paused/failed/
// hook_failed); у постоянной «Feed» их нет.
export type WorkspaceTabDescriptor = {
  id: string
  label: string
  kind?: AttentionKind
  count?: number
  glow?: boolean
}

type WorkspaceTabsProps = {
  tabs: WorkspaceTabDescriptor[]
  activeId: string
  onSelect: (id: string) => void
}

// WorkspaceTabs — верхние вкладки единого воркспейса (role=tablist). Тупой
// презентационный компонент: набор вкладок и активная вкладка вычисляются
// вызывающим (App.tsx). Постоянная «Feed» плюс контекстная вкладка выбранной
// стадии, которая при статусе, требующем действия, получает подпись по виду
// (Approval/Question/…), счётчик «· N» и glow-подсветку. Стрелки влево/вправо —
// перемещение фокуса и выбор между вкладками (план §11, a11y).
export function WorkspaceTabs({ tabs, activeId, onSelect }: WorkspaceTabsProps): ReactElement {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({})

  function onKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number): void {
    let nextIndex: number | null = null
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') nextIndex = (index + 1) % tabs.length
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') nextIndex = (index - 1 + tabs.length) % tabs.length
    if (nextIndex === null) return
    e.preventDefault()
    const next = tabs[nextIndex]
    if (!next) return
    refs.current[next.id]?.focus()
    onSelect(next.id)
  }

  return (
    <div className="workspace-tabs" role="tablist" aria-label="Workspace">
      {tabs.map((tab, i) => {
        const active = tab.id === activeId
        const isAttention = tab.kind !== undefined
        return (
          <button
            key={tab.id}
            ref={(el) => { refs.current[tab.id] = el }}
            type="button"
            role="tab"
            aria-selected={active}
            tabIndex={active ? 0 : -1}
            className={
              'workspace-tab' +
              (active ? ' active' : '') +
              (isAttention ? ' workspace-tab-attention' : '') +
              (tab.glow ? ' glow' : '')
            }
            data-kind={tab.kind}
            data-tab-id={tab.id}
            onClick={() => onSelect(tab.id)}
            onKeyDown={(e) => onKeyDown(e, i)}
          >
            {isAttention && <span className="attn-glyph" aria-hidden="true" />}
            <span className="workspace-tab-label">{tab.label}</span>
            {tab.count !== undefined && tab.count > 0 && <span className="attn-count">· {tab.count}</span>}
          </button>
        )
      })}
    </div>
  )
}
