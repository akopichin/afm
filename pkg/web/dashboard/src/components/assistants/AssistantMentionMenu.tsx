import { useLayoutEffect, useRef, type ReactElement } from 'react'
import type { AssistantProfile } from '../../types/assistant-profile'
import { mentionOptionId } from './use-mention-menu'
import { MeshAvatar } from '../team/MeshAvatar'

// MentionItem — the minimum shape the shared popup (MentionMenu) needs to render one option: an id/title pair
// plus an optional "why this can't be used" marker. mention-match.ts's MentionGroup<T> is keyed on the same
// {id, title} shape, so any T satisfying both can flow straight through filterMentionProfiles/
// filterMentionGroups into this component without a second adapter layer.
export type MentionItem = {
  id: string
  // handle — what follows `@` on screen and in the inserted text when it differs from id (a mesh
  // participant's short alias, e.g. `side` instead of `side:<run-id>`).
  handle?: string
  title: string
  unavailable?: boolean
  unavailableReason?: string
  description?: string
  kind?: string
}

export type MentionGroup<T extends MentionItem> = {
  // label — a section heading ("This run" / "This stage" / "Attach to stage", dev-plan.md T7.4). Empty
  // string renders no heading row at all.
  label: string
  items: T[]
}

export type MentionMenuProps<T extends MentionItem> = {
  groups: MentionGroup<T>[]
  // highlightIndex — a FLAT index across every item in every group, in render order. Callers already reduce
  // their matches to one flat array for keyboard Up/Down (see SideAgentComposer/FeedComposer), so this
  // component only needs to know which position in that same order is active.
  highlightIndex: number
  onHover: (index: number) => void
  onSelect: (item: T) => void
  ariaLabel: string
  heading?: string
  emptyMessage?: string
  showGroupLabels?: boolean
  // id — the listbox id the composer's textarea points at (useMentionMenu's listboxId); options get derived ids.
  id?: string
}

const MENU_GAP = 9
const VIEWPORT_MARGIN = 12
const MAX_MENU_HEIGHT = 380
const MENU_WIDTH = 360

// mentionText — the text written after `@` for an item: its handle, else its id.
export function mentionText(item: MentionItem): string {
  return item.handle !== undefined && item.handle !== '' ? item.handle : item.id
}

// MentionMenu — the ONE `@mention` suggestion popup (dev-plan.md T4.2, lifted into a shared primitive by
// T7.4): the side agent's own AssistantMentionMenu below wraps it with a single unlabeled group, and the feed
// composer's mesh mention menu (mesh-recipients.ts) passes several labeled groups straight through. Rendering
// lives here ONCE — no second popup component.
export function MentionMenu<T extends MentionItem>({ groups, highlightIndex, onHover, onSelect, ariaLabel, heading = ariaLabel, emptyMessage, showGroupLabels = true, id }: MentionMenuProps<T>): ReactElement | null {
  const listRef = useRef<HTMLUListElement | null>(null)
  // Fixed positioning escapes the scroll pane while keeping the menu in its modal's focus boundary.
  useLayoutEffect(() => {
    const list = listRef.current
    const anchor = list?.parentElement
    if (list === null || anchor === null || anchor === undefined) return
    const place = (): void => {
      const rect = anchor.getBoundingClientRect()
      const width = Math.min(MENU_WIDTH, window.innerWidth - VIEWPORT_MARGIN * 2)
      list.style.width = `${width}px`
      list.style.maxHeight = `${Math.max(0, Math.min(MAX_MENU_HEIGHT, rect.top - MENU_GAP - VIEWPORT_MARGIN))}px`
      list.style.left = `${Math.max(VIEWPORT_MARGIN, Math.min(rect.left, window.innerWidth - width - VIEWPORT_MARGIN))}px`
      list.style.top = `${Math.max(VIEWPORT_MARGIN, rect.top - list.getBoundingClientRect().height - MENU_GAP)}px`
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(place)
    observer?.observe(anchor)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
      observer?.disconnect()
    }
  })
  const total = groups.reduce((n, g) => n + g.items.length, 0)
  if (total === 0 && emptyMessage === undefined) return null

  let cursor = -1
  return (
    <ul ref={listRef} id={id} className="assistant-mention-menu" role="listbox" aria-label={ariaLabel}>
      <li className="assistant-mention-heading" role="presentation">{heading} · ↑ ↓ to choose · Enter to add</li>
      {total === 0 && <li className="assistant-mention-empty" role="presentation">{emptyMessage}</li>}
      {groups.flatMap((group) => {
        const rows: ReactElement[] = []
        if (showGroupLabels && group.label !== '') {
          rows.push(
            <li key={`${group.label}__hdr`} className="assistant-mention-group-label" role="presentation">
              {group.label}
            </li>,
          )
        }
        for (const item of group.items) {
          cursor += 1
          const index = cursor
          rows.push(
            <li
              key={item.id}
              id={id !== undefined ? mentionOptionId(id, index) : undefined}
              role="option"
              aria-selected={index === highlightIndex}
              aria-disabled={item.unavailable || undefined}
              className={`assistant-mention-item${index === highlightIndex ? ' active' : ''}${item.unavailable ? ' is-unavailable' : ''}`}
            >
              <button
                type="button"
                className="assistant-mention-option"
                disabled={item.unavailable}
                title={item.unavailableReason}
                onMouseEnter={() => onHover(index)}
                // preventDefault on mousedown keeps focus (and the caret this selection relies on) in the
                // textarea; the selection itself happens on click, so Tab → Enter/Space works too.
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onSelect(item)}
              >
                <MeshAvatar name={item.title} kind={item.kind ?? 'helper'} />
                <span className="assistant-mention-person">
                  <span className="assistant-mention-title">{item.title}</span>
                  {item.description && <span className="assistant-mention-description">{item.description}</span>}
                </span>
                <span className="assistant-mention-id">@{mentionText(item)}</span>
                {item.unavailable === true && !item.description && (
                  <span className="assistant-mention-unavailable" title={item.unavailableReason}>
                    Unavailable
                  </span>
                )}
              </button>
            </li>,
          )
        }
        return rows
      })}
    </ul>
  )
}

export type AssistantMentionMenuProps = {
  // profiles — already filtered by the caller (mention-match's filterMentionProfiles); this component only
  // renders and dispatches selection/hover, it does not know about the query text.
  profiles: AssistantProfile[]
  highlightIndex: number
  onHover: (index: number) => void
  onSelect: (profile: AssistantProfile) => void
  id?: string
}

// AssistantMentionMenu — the side agent's own `@mention` popup (dev-plan.md T4.2): a thin, single-group
// instantiation of the shared MentionMenu above. A mention always addresses a ROLE (profile), never a running
// task instance — see pkg/assistants.Registry.Resolve's doc comment; addressing an already-running task is a
// separate mechanism (task_id, T4.7).
export function AssistantMentionMenu({ profiles, highlightIndex, onHover, onSelect, id }: AssistantMentionMenuProps): ReactElement | null {
  const items = profiles.map((p) => ({ ...p, unavailable: !p.available, unavailableReason: p.unavailableReason }))
  return (
    <MentionMenu
      groups={[{ label: '', items }]}
      highlightIndex={highlightIndex}
      onHover={onHover}
      onSelect={(item) => {
        const original = profiles.find((p) => p.id === item.id)
        if (original !== undefined) onSelect(original)
      }}
      ariaLabel="Mention a role"
      id={id}
    />
  )
}
