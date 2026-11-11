import '@testing-library/jest-dom/vitest'
import { createElement, forwardRef, useContext, useState } from 'react'
import { vi } from 'vitest'
import { listCalls } from './list-spy'

// react-resizable-panels v4 внутри mountGroup использует new ResizeObserver для отслеживания размеров
// Group.
class ResizeObserverStub {
  observe(): void {
    /* no-op */
  }
  unobserve(): void {
    /* no-op */
  }
  disconnect(): void {
    /* no-op */
  }
}

if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = ResizeObserverStub
}

// jsdom не реализует scrollIntoView — без стаба любой такой рендер падал бы с "scrollIntoView is not a
// function".
HTMLElement.prototype.scrollIntoView = function (): void {
  /* no-op */
}

// jsdom does not implement element scrolling either: the virtual list moves its scroller with scrollBy when the
// head of the list is evicted.
for (const method of ['scrollBy', 'scrollTo'] as const) {
  if (typeof Element.prototype[method] !== 'function') Element.prototype[method] = (): void => {}
}

// jsdom does not implement URL.createObjectURL/revokeObjectURL — stub them so paste-related tests don't
// crash.
if (typeof URL.createObjectURL !== 'function') {
  URL.createObjectURL = (): string => 'blob:mock-url'
}
if (typeof URL.revokeObjectURL !== 'function') {
  URL.revokeObjectURL = (): void => {
    /* no-op */
  }
}

// jsdom не реализует window.matchMedia — no-op заглушка: всегда desktop, с полным набором методов, чтобы
// addEventListener не падал.
if (typeof window.matchMedia !== 'function') {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: (): void => {},
      removeEventListener: (): void => {},
      addListener: (): void => {},
      removeListener: (): void => {},
      dispatchEvent: (): boolean => false,
    }) as unknown as MediaQueryList
}

// jsdom has no layout, so react-virtuoso would render no rows: unless a test sets its own VirtuosoMockContext (a small
// viewport to check that rows are bounded), the list gets a viewport that fits every row of a test. The initial
// position is dropped: under the mock context a list opened at a later row renders nothing.
vi.mock('react-virtuoso', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-virtuoso')>()
  const Virtuoso = forwardRef(function MockedVirtuoso(props: Record<string, unknown>, ref) {
    const own = useContext(actual.VirtuosoMockContext)
    const { initialTopMostItemIndex: initial, ...rest } = props
    useState(() => listCalls.mounts.push(initial))
    // The handle is patched once: every scrollToIndex of the feed is recorded before it reaches the list.
    const spiedRef = (handle: { scrollToIndex: (location: unknown) => void; __spied?: boolean } | null): void => {
      if (handle !== null && handle.__spied !== true) {
        const original = handle.scrollToIndex.bind(handle)
        handle.__spied = true
        handle.scrollToIndex = (location) => {
          listCalls.scrolls.push(location)
          original(location)
        }
      }
      if (typeof ref === 'function') ref(handle)
      else if (ref !== null) ref.current = handle
    }
    const list = createElement(actual.Virtuoso as never, { ...rest, ref: spiedRef } as never)
    return own !== undefined ? list : createElement(actual.VirtuosoMockContext.Provider, { value: { viewportHeight: 20000, itemHeight: 20 } }, list)
  })
  return { ...actual, Virtuoso }
})
