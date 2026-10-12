import '@testing-library/jest-dom/vitest'

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
