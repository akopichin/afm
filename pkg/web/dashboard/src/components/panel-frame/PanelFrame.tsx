import { type ReactNode } from 'react'
import { useMaximize } from '../layout/Maximizable'

type PanelFrameProps = {
  title: string
  maximizeId?: string
  attention?: boolean
  actions?: ReactNode
  children: ReactNode
}

export function PanelFrame({ title, maximizeId, attention, actions, children }: PanelFrameProps) {
  const { maximizedKey, toggle } = useMaximize()
  const maximized = maximizeId !== undefined && maximizedKey === maximizeId
  return (
    <section
      className={`panel-frame${attention ? ' attention' : ''}`}
      data-panel={maximizeId ?? undefined}
    >
      <header className="panel-frame-header">
        <h3>{title}</h3>
        <div className="panel-frame-actions">
          {actions}
          {maximizeId !== undefined && (
            <button
              type="button"
              className="icon-btn"
              aria-label={maximized ? 'Collapse' : 'Expand'}
              onClick={() => toggle(maximizeId)}
            >
              {maximized ? '✕' : '⛶'}
            </button>
          )}
        </div>
      </header>
      <div className="panel-frame-body">{children}</div>
    </section>
  )
}
