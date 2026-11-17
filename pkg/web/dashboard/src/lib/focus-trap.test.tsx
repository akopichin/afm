import { act, render, waitFor } from '@testing-library/react'
import { useRef } from 'react'
import { afterEach, describe, expect, test } from 'vitest'
import { useModalDialog, visibleFocusables } from './focus-trap'

function Dialog({ returnFocus }: { returnFocus?: () => HTMLElement | null }) {
  const ref = useRef<HTMLDivElement | null>(null)
  useModalDialog(ref, () => {}, returnFocus)
  return (
    <div ref={ref}>
      <button>inside</button>
    </div>
  )
}

function button(label: string): HTMLButtonElement {
  const el = document.createElement('button')
  el.textContent = label
  document.body.append(el)
  return el
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('useModalDialog focus keeper', () => {
  test('re-resolves the target after the restored element unmounts, however late', async () => {
    const first = button('first')
    first.focus()
    const { unmount } = render(<Dialog returnFocus={() => document.getElementById('card')} />)
    const card = button('card')
    card.id = 'card'
    unmount()
    expect(card).toHaveFocus()
    const replacement = button('replacement')
    replacement.id = 'card'
    await act(async () => {
      await new Promise((r) => setTimeout(r, 3500))
      card.remove()
    })
    await waitFor(() => expect(replacement).toHaveFocus())
  })

  test('falls back to the workspace when nothing else is left', async () => {
    const main = document.createElement('main')
    main.id = 'main'
    main.tabIndex = -1
    document.body.append(main)
    const opener = button('opener')
    opener.focus()
    const { unmount } = render(<Dialog />)
    unmount()
    expect(opener).toHaveFocus()
    opener.remove()
    await waitFor(() => expect(main).toHaveFocus())
  })

  test('stops once the user moved focus elsewhere', async () => {
    const opener = button('opener')
    const other = button('other')
    opener.focus()
    const { unmount } = render(<Dialog />)
    unmount()
    other.focus()
    opener.remove()
    await Promise.resolve()
    expect(other).toHaveFocus()
  })
})

describe('visibleFocusables', () => {
  test('skips tabindex=-1 buttons so roving satellites are not trap boundaries', () => {
    const root = document.createElement('div')
    root.innerHTML = '<button id="a">a</button><button id="b" tabindex="-1">b</button><button id="c" tabindex="0">c</button>'
    document.body.append(root)
    expect(visibleFocusables(root).map((el) => el.id)).toEqual(['a', 'c'])
  })
})
