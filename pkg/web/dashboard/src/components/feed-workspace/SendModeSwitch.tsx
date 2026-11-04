import type { ReactElement } from 'react'
import type { ComposerMode } from './composer-draft'

type SendModeSwitchProps = {
  mode: ComposerMode
  // restartShown — Restart with note is offered while the stage takes it, and stays while it is the chosen mode.
  restartShown: boolean
  onSelect: (mode: ComposerMode) => void
}

// SendModeSwitch — "Send as": how Send delivers the one input. Choosing a mode sends nothing and leaves the
// keyboard focus on the button; there are no panels, so it is a pressed-button group, not tabs.
export function SendModeSwitch({ mode, restartShown, onSelect }: SendModeSwitchProps): ReactElement {
  return (
    <div className="feed-composer-modes" role="group" aria-label="Send as">
      <ModeButton mode="chat" chosen={mode} onSelect={onSelect} />
      {restartShown && <ModeButton mode="restart" chosen={mode} onSelect={onSelect} />}
    </div>
  )
}

const MODE_LABEL: Record<ComposerMode, string> = { chat: 'Chat', restart: 'Restart with note' }

function ModeButton({ mode, chosen, onSelect }: { mode: ComposerMode; chosen: ComposerMode; onSelect: (mode: ComposerMode) => void }): ReactElement {
  const pressed = mode === chosen
  return (
    <button type="button" className="feed-composer-mode" aria-pressed={pressed} onClick={() => onSelect(mode)}>
      <ModeIcon mode={mode} />
      {MODE_LABEL[mode]}
    </button>
  )
}

function ModeIcon({ mode }: { mode: ComposerMode }): ReactElement {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {mode === 'chat' ? <path d="M4 4h16v12H9l-5 4V4Z" /> : (
        <><path d="M20 10a8 8 0 0 0-14-5L3 8m0-5v5h5" /><path d="M4 14a8 8 0 0 0 14 5l3-3m0 5v-5h-5" /></>
      )}
    </svg>
  )
}
