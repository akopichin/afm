import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import { TabList, tabPanelProps } from './TabList'

type Tab = 'one' | 'two' | 'three'

function Harness() {
  const [tab, setTab] = useState<Tab>('one')
  return (
    <>
      <TabList
        idPrefix="t"
        ariaLabel="Demo"
        className="tabs"
        tabClassName="tab"
        selected={tab}
        onSelect={setTab}
        tabs={[
          { id: 'one', label: 'One' },
          { id: 'two', label: 'Two' },
          { id: 'three', label: 'Three' },
        ]}
      />
      <div {...tabPanelProps('t', tab)}>panel {tab}</div>
    </>
  )
}

describe('TabList', () => {
  test('only the selected tab is in the Tab order and it controls the visible panel', () => {
    render(<Harness />)
    const one = screen.getByRole('tab', { name: 'One' })
    expect(one).toHaveAttribute('tabindex', '0')
    expect(screen.getByRole('tab', { name: 'Two' })).toHaveAttribute('tabindex', '-1')
    const panel = screen.getByRole('tabpanel', { name: 'One' })
    expect(one).toHaveAttribute('aria-controls', panel.id)
  })

  test('arrow keys move focus and selection, wrapping around; Home and End jump to the ends', () => {
    render(<Harness />)
    const one = screen.getByRole('tab', { name: 'One' })
    one.focus()
    fireEvent.keyDown(one, { key: 'ArrowRight' })
    const two = screen.getByRole('tab', { name: 'Two' })
    expect(two).toHaveFocus()
    expect(two).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tabpanel')).toHaveTextContent('panel two')

    fireEvent.keyDown(two, { key: 'End' })
    expect(screen.getByRole('tab', { name: 'Three' })).toHaveFocus()
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Three' }), { key: 'ArrowRight' })
    expect(one).toHaveFocus()
    fireEvent.keyDown(one, { key: 'ArrowLeft' })
    expect(screen.getByRole('tab', { name: 'Three' })).toHaveFocus()
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Three' }), { key: 'Home' })
    expect(one).toHaveAttribute('aria-selected', 'true')
  })
})
