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

// jsdom не реализует scrollIntoView (useAutoGrowTextarea зовёт его при
// каждом росте textarea, чтобы не прятать кнопку под ней) — без стаба любой
// такой рендер падал бы с "scrollIntoView is not a function". TS считает
// scrollIntoView всегда присутствующим в типе HTMLElement (lib.dom.d.ts),
// поэтому здесь достаточно безусловной no-op заглушки, без runtime-проверки.
HTMLElement.prototype.scrollIntoView = function (): void {
  /* no-op */
}

// jsdom does not implement URL.createObjectURL/revokeObjectURL (used by
// useImagePaste for an instant local thumbnail of a pasted screenshot before
// the upload resolves) — stub them so paste-related tests don't crash.
if (typeof URL.createObjectURL !== 'function') {
  URL.createObjectURL = (): string => 'blob:mock-url'
}
if (typeof URL.revokeObjectURL !== 'function') {
  URL.revokeObjectURL = (): void => {
    /* no-op */
  }
}
