import { useLayoutEffect, useRef, type ReactElement } from 'react'
import type { AssistantProfile } from '../../types/assistant-profile'
import { mentionOptionId } from './use-mention-menu'

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
  // id — the listbox id the composer's textarea points at (useMentionMenu's listboxId); options get derived ids.
  id?: string
}

const MENU_GAP = 6
const MIN_MENU_HEIGHT = 96
const MAX_MENU_HEIGHT = 280

// clippingAncestor — the nearest ancestor that clips its overflow (the menu cannot extend past it).
function clippingAncestor(el: HTMLElement): HTMLElement | null {
  for (let node = el.parentElement; node !== null; node = node.parentElement) {
    const { overflowY } = getComputedStyle(node)
    if (overflowY !== 'visible') return node
  }
  return null
}

// mentionText — the text written after `@` for an item: its handle, else its id.
export function mentionText(item: MentionItem): string {
  return item.handle !== undefined && item.handle !== '' ? item.handle : item.id
}

// MentionMenu — the ONE `@mention` suggestion popup (dev-plan.md T4.2, lifted into a shared primitive by
// T7.4): the side agent's own AssistantMentionMenu below wraps it with a single unlabeled group, and the feed
// composer's mesh mention menu (mesh-recipients.ts) passes several labeled groups straight through. Rendering
// lives here ONCE — no second popup component.
export function MentionMenu<T extends MentionItem>({ groups, highlightIndex, onHover, onSelect, ariaLabel, id }: MentionMenuProps<T>): ReactElement | null {
  const listRef = useRef<HTMLUListElement | null>(null)
  // The menu opens upward from the composer row; cap its height to the room actually left above the row
  // inside the nearest clipping ancestor (panels use overflow: hidden), so its top options never get cut off.
  useLayoutEffect(() => {
    const list = listRef.current
    const anchor = list?.parentElement
    if (list === null || anchor === null || anchor === undefined) return
    const clip = clippingAncestor(anchor)
    const room = anchor.getBoundingClientRect().top - (clip?.getBoundingClientRect().top ?? 0) - MENU_GAP
    list.style.maxHeight = `${Math.max(MIN_MENU_HEIGHT, Math.min(MAX_MENU_HEIGHT, room))}px`
  })
  const total = groups.reduce((n, g) => n + g.items.length, 0)
  if (total === 0) return null

  let cursor = -1
  return (
    <ul ref={listRef} id={id} className="assistant-mention-menu" role="listbox" aria-label={ariaLabel}>
      {groups.flatMap((group) => {
        const rows: ReactElement[] = []
        if (group.label !== '') {
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
              className={`assistant-mention-item${index === highlightIndex ? ' active' : ''}`}
            >
              <button
                type="button"
                className="assistant-mention-option"
                onMouseEnter={() => onHover(index)}
                // preventDefault on mousedown keeps focus (and the caret this selection relies on) in the
                // textarea; the selection itself happens on click, so Tab → Enter/Space works too.
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onSelect(item)}
              >
                <span className="assistant-mention-id">@{mentionText(item)}</span>
                <span className="assistant-mention-title">{item.title}</span>
                {item.unavailable === true && (
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
