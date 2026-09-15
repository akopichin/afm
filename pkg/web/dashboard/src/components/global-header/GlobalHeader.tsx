import type { ReactElement } from 'react'
import { useThemeMode } from '../../hooks/use-theme-mode'
import type { NotificationPermissionState } from '../../hooks/use-desktop-notifications'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'
import { useFileBrowser } from '../file-browser'
import { RunMetrics } from '../run-metrics'

type GlobalHeaderProps = {
  flowName: string
  description?: string
  connected: boolean
  attention?: boolean
  // Метрики прогона — прокидываются в центральный RunMetrics (см. App.tsx).
  startedAt: string
  elapsedMs: number
  idleMs: number
  backoffMs: number
  // Est. cost (Increment 2) — прокидываются в RunMetrics как есть; дефолты
  // здесь только на случай, если вызывающий код ещё не дошёл до полного
  // проброса из App.tsx (см. types/cost.ts).
  runCost?: CostSummary
  coverageIssues?: CoverageIssue[]
  accounting?: AccountingState
  onOpenCost?: (fromPopover: boolean) => void
  notificationsPermission?: NotificationPermissionState
  notificationsEnabled?: boolean
  onRequestEnableNotifications?: () => void
  onDisableNotifications?: () => void
  capabilities?: { fileBrowser: boolean }
}

// GlobalHeader — компактная шапка дашборда (пришла на смену FlowHeader+Footer).
// Слева бренд + имя/описание флоу; в центре метрики прогона (RunMetrics); справа
// действия: Files, dark/light, уведомления, состояние соединения. Данные и хуки
// те же, что были у FlowHeader/Footer — меняется только композиция. id header/
// flow-name/flow-description/ws-status сохранены для совместимости со скинами и
// проверками.
export function GlobalHeader({
  flowName,
  description,
  connected,
  attention = false,
  startedAt,
  elapsedMs,
  idleMs,
  backoffMs,
  runCost,
  coverageIssues = [],
  accounting = { supported: false },
  onOpenCost = () => {},
  notificationsPermission = 'unsupported',
  notificationsEnabled = false,
  onRequestEnableNotifications,
  onDisableNotifications,
  capabilities = { fileBrowser: false },
}: GlobalHeaderProps): ReactElement {
  const hasDescription = description !== undefined && description.trim() !== ''
  const { mode, toggle } = useThemeMode()

  return (
    <header id="header" className="global-header">
      <div className="gh-left">
        <span className="gh-mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.4">
            <polygon points="12,2 21,7 21,17 12,22 3,17 3,7" />
            <circle cx="12" cy="12" r="2.6" fill="currentColor" stroke="none" />
          </svg>
        </span>
        <span className="gh-wordmark">afm</span>
        <span className="gh-flow">
          <span id="flow-name" className="gh-flow-name">{flowName}</span>
          {hasDescription && <span id="flow-description" className="gh-flow-desc">{description}</span>}
        </span>
      </div>

      <div className="gh-center">
        <RunMetrics
          startedAt={startedAt}
          elapsedMs={elapsedMs}
          idleMs={idleMs}
          backoffMs={backoffMs}
          runCost={runCost}
          coverageIssues={coverageIssues}
          accounting={accounting}
          onOpenCost={onOpenCost}
        />
      </div>

      <div className="gh-right header-actions">
        {capabilities.fileBrowser && <OpenFileBrowserButton />}
        {/* Сегментированный переключатель темы (солнце | луна), как в макете. */}
        <div className="gh-theme-seg" role="group" aria-label="Theme mode">
          <button
            type="button"
            className={`gh-theme-seg-btn${mode === 'light' ? ' active' : ''}`}
            aria-pressed={mode === 'light'}
            aria-label="Light mode"
            onClick={() => { if (mode !== 'light') toggle() }}
          >
            {sunIcon()}
          </button>
          <button
            type="button"
            className={`gh-theme-seg-btn${mode === 'dark' ? ' active' : ''}`}
            aria-pressed={mode === 'dark'}
            aria-label="Dark mode"
            onClick={() => { if (mode !== 'dark') toggle() }}
          >
            {moonIcon()}
          </button>
        </div>
        {notificationsPermission !== 'unsupported' && (
          <button
            type="button"
            className={notificationsEnabled ? 'icon-btn icon-btn-on' : 'icon-btn'}
            onClick={notificationsEnabled ? onDisableNotifications : onRequestEnableNotifications}
            disabled={notificationsPermission === 'denied'}
            aria-label={notificationsEnabled ? 'Disable desktop notifications' : 'Enable desktop notifications'}
            title={notificationsPermission === 'denied' ? 'Notifications blocked in browser settings' : undefined}
          >
            {bellIcon(notificationsEnabled)}
          </button>
        )}
        <div
          id="ws-status"
          className={`ws-status gh-connection ${connected ? 'connected' : 'disconnected'}`}
          title="WebSocket connection"
        >
          <span className="gh-connection-dot" aria-hidden="true" />
          <span className="gh-connection-text">{connected ? 'Agent online' : 'Offline'}</span>
        </div>
        {attention && <span className="attention-dot" aria-label="Action needed" />}
      </div>
    </header>
  )
}

// Иконки шапки — инлайновый SVG на currentColor, 16px, единый набор.
function iconSvg(children: ReactElement): ReactElement {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  )
}
function sunIcon(): ReactElement {
  return iconSvg(<><circle cx="12" cy="12" r="4.2" /><path d="M12 2.5v2.2M12 19.3v2.2M4.2 4.2l1.6 1.6M18.2 18.2l1.6 1.6M2.5 12h2.2M19.3 12h2.2M4.2 19.8l1.6-1.6M18.2 5.8l1.6-1.6" /></>)
}
function moonIcon(): ReactElement {
  return iconSvg(<path d="M20 14.5A8 8 0 1 1 9.5 4 6.2 6.2 0 0 0 20 14.5Z" />)
}
function bellIcon(enabled: boolean): ReactElement {
  return iconSvg(
    <>
      <path d="M6 9a6 6 0 0 1 12 0c0 5 1.8 6.2 2.4 7.2a.6.6 0 0 1-.5.9H4.1a.6.6 0 0 1-.5-.9C4.2 15.2 6 14 6 9Z" fill={enabled ? 'currentColor' : 'none'} />
      <path d="M9.8 20.5a2.4 2.4 0 0 0 4.4 0" />
      {!enabled && <path d="M4 3.5l16 16" />}
    </>,
  )
}

// Отдельный подкомпонент — useFileBrowser() бросает вне FileBrowserProvider, а
// монтируется он только при capabilities.fileBrowser=true, поэтому сам
// GlobalHeader остаётся тестируемым без провайдера (та же причина, что была у
// FlowHeader.OpenFileBrowserButton).
function OpenFileBrowserButton(): ReactElement {
  const { openBrowser } = useFileBrowser()
  return (
    <button type="button" className="icon-btn gh-files-btn" aria-label="Open project files" onClick={openBrowser}>
      <span className="gh-files-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
          <path d="M3 7 h6 l2 2 h10 v10 a1 1 0 0 1 -1 1 H4 a1 1 0 0 1 -1 -1 Z" />
        </svg>
      </span>
      <span className="gh-files-label">Files</span>
    </button>
  )
}
