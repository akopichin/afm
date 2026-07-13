import '@testing-library/jest-dom/vitest'

// react-resizable-panels v4 внутри mountGroup использует new ResizeObserver(...)
// для отслеживания размеров Group. jsdom его не реализует, поэтому в тестах
// подключаем noop-заглушку (наблюдать нечего — layout всё равно не пересчитывается).
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

