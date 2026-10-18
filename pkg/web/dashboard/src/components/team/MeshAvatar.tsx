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
