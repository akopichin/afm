import { useRef, type KeyboardEvent, type ReactElement, type ReactNode } from 'react'

export type TabItem<T extends string> = { id: T; label: ReactNode }

export type TabListProps<T extends string> = {
  // idPrefix — unique per tab list (useId()), ties each tab to its panel through ids.
  idPrefix: string
  tabs: TabItem<T>[]
  selected: T
  onSelect: (id: T) => void
  ariaLabel: string
  className: string
  // tabClassName — each tab's class; the selected one also gets "active".
  tabClassName: string
}

function tabId(idPrefix: string, id: string): string {
  return `${idPrefix}-tab-${id}`
}

function panelId(idPrefix: string, id: string): string {
  return `${idPrefix}-panel-${id}`
}

// tabPanelProps — the attributes of the panel that shows tab `id` (only the selected panel is rendered).
export function tabPanelProps(idPrefix: string, id: string): { role: 'tabpanel'; id: string; 'aria-labelledby': string } {
  return { role: 'tabpanel', id: panelId(idPrefix, id), 'aria-labelledby': tabId(idPrefix, id) }
}

// nextTabIndex — where an arrow, Home or End key moves focus in a list of `count` tabs (wrapping around), or
// null for any other key.
function nextTabIndex(key: string, current: number, count: number): number | null {
  switch (key) {
    case 'ArrowRight':
      return (current + 1) % count
    case 'ArrowLeft':
      return (current - 1 + count) % count
    case 'Home':
      return 0
    case 'End':
      return count - 1
    default:
      return null
  }
}

// TabList — the one WAI-ARIA tab list (Team tabs, the feed composer's Message/Revise): roving tabindex so Tab
// enters the list at the selected tab, arrows/Home/End move between tabs and select them (automatic
// activation), and each tab points at its panel via aria-controls.
export function TabList<T extends string>({ idPrefix, tabs, selected, onSelect, ariaLabel, className, tabClassName }: TabListProps<T>): ReactElement {
  const buttonsRef = useRef<(HTMLButtonElement | null)[]>([])

  function onKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number): void {
    const next = nextTabIndex(e.key, index, tabs.length)
    const tab = next === null ? undefined : tabs[next]
    if (next === null || tab === undefined) return
    e.preventDefault()
    buttonsRef.current[next]?.focus()
    onSelect(tab.id)
  }

  return (
    <div className={className} role="tablist" aria-label={ariaLabel}>
      {tabs.map((tab, index) => {
        const active = tab.id === selected
        return (
          <button
            key={tab.id}
            ref={(node) => {
              buttonsRef.current[index] = node
            }}
            type="button"
            role="tab"
            id={tabId(idPrefix, tab.id)}
            aria-selected={active}
            // Only the selected panel is in the DOM, so only its tab may point at it.
            aria-controls={active ? panelId(idPrefix, tab.id) : undefined}
            tabIndex={active ? 0 : -1}
            className={`${tabClassName}${active ? ' active' : ''}`}
            onClick={() => onSelect(tab.id)}
            onKeyDown={(e) => onKeyDown(e, index)}
          >
            {tab.label}
          </button>
        )
      })}
    </div>
  )
}
