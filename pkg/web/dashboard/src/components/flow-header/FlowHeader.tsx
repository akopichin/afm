import type { ReactElement } from 'react'
import { useThemeMode } from '../../hooks/use-theme-mode'
import type { NotificationPermissionState } from '../../hooks/use-desktop-notifications'

type FlowHeaderProps = {
  flowName: string
  connected: boolean
  attention?: boolean
  description?: string
  notificationsPermission?: NotificationPermissionState
  notificationsEnabled?: boolean
  onRequestEnableNotifications?: () => void
  onDisableNotifications?: () => void
}

// Шапка дашборда: декоративный логотип, имя флоу и индикатор WebSocket-соединения.
// id flow-name и ws-status и классы ws-status connected/disconnected сохранены для
// совместимости со скинами (см. skins/base/header.css и skins/<name>/index.css).
// Когда attention=true — хотя бы одна стадия ждёт действия пользователя — рядом с
// именем флоу загорается пульсирующая amber-точка. Плюс переключатель dark/light —
// не зависит от пропсов компонента и активного скина.
// description — опциональный подзаголовок (описание флоу из его конфигурации),
// помогает отличить несколько параллельно запущенных пайплайнов друг от друга;
// рендерится второй строкой под именем флоу, не добавляя новую колонку в грид шапки.
// notificationsPermission/notificationsEnabled + on*Notifications — состояние и
// колбэки десктоп-уведомлений (useDesktopNotifications в App.tsx); кнопка вообще
// не рендерится при permission='unsupported' (браузер без Notification API).
export function FlowHeader({
  flowName,
  connected,
  attention = false,
  description,
  notificationsPermission = 'unsupported',
  notificationsEnabled = false,
  onRequestEnableNotifications,
  onDisableNotifications,
}: FlowHeaderProps): ReactElement {
  const hasDescription = description !== undefined && description.trim() !== ''
  const statusText = connected ? 'LINK' : 'OFFLINE'
  const statusClass = connected ? 'connected' : 'disconnected'
  const { mode, toggle } = useThemeMode()

  return (
    <header id="header">
      <span className="logo" aria-hidden="true">
        <span className="l-ring">
          <svg viewBox="0 0 24 24" fill="none" strokeWidth="1">
            <polygon points="12,1 22,7 22,17 12,23 2,17 2,7" />
          </svg>
        </span>
        <span className="l-arc">
          <svg viewBox="0 0 24 24" fill="none" strokeWidth="1" strokeLinecap="round">
            <path d="M 12 3 A 9 9 0 0 1 21 12" />
          </svg>
        </span>
        <span className="l-core">
          <svg viewBox="0 0 24 24" stroke="none">
            <circle cx="12" cy="12" r="2.4" />
          </svg>
        </span>
      </span>
      <h1>afm</h1>
      <div className="flow-name-wrap">
        <div id="flow-name" className="flow-name">{flowName}</div>
        {hasDescription && <div id="flow-description" className="flow-description">{description}</div>}
      </div>
      <div className="header-actions">
        {attention && <span className="attention-dot" aria-label="Action needed" />}
        <div id="ws-status" className={`ws-status ${statusClass}`} title="WebSocket">{statusText}</div>
        <button
          type="button"
          className="icon-btn"
          onClick={toggle}
          aria-label={mode === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
        >
          {mode === 'dark' ? '☾' : '☀'}
        </button>
        {notificationsPermission !== 'unsupported' && (
          <button
            type="button"
            className={notificationsEnabled ? 'icon-btn icon-btn-on' : 'icon-btn'}
            onClick={notificationsEnabled ? onDisableNotifications : onRequestEnableNotifications}
            disabled={notificationsPermission === 'denied'}
            aria-label={notificationsEnabled ? 'Disable desktop notifications' : 'Enable desktop notifications'}
            title={notificationsPermission === 'denied' ? 'Notifications blocked in browser settings' : undefined}
          >
            {notificationsEnabled ? '🔔' : '🔕'}
          </button>
        )}
      </div>
    </header>
  )
}
