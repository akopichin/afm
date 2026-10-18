import type { ReactElement } from 'react'
import { participantInitials } from '../../lib/mesh-roster'

export type MeshAvatarProps = {
  name: string
  // kind — mesh participant kind (stage_agent, run_agent, helper, side_agent, user); tints the avatar.
  kind: string
  size?: 'small' | 'large'
}

// MeshAvatar — квадратный аватар участника с инициалами (ростер, сообщения, шапка разговора, форма).
export function MeshAvatar({ name, kind, size }: MeshAvatarProps): ReactElement {
  return (
    <span className={`mesh-avatar${size !== undefined ? ` ${size}` : ''}`} data-kind={kind} aria-hidden="true">
      {participantInitials(name)}
    </span>
  )
}

// ThreadSymbol — значок группового разговора (шапка разговора и строка в списке разговоров).
export function ThreadSymbol({ size }: { size?: 'small' }): ReactElement {
  const px = size === 'small' ? 15 : 18
  return (
    <span className={`team-thread-symbol${size !== undefined ? ` ${size}` : ''}`} aria-hidden="true">
      <svg viewBox="0 0 24 24" width={px} height={px} fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
        <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
        <circle cx="9" cy="7" r="4" />
        <path d="M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" />
      </svg>
    </span>
  )
}
