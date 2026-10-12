// Общий поиск фокусируемых элементов для focus-trap модалок.

export const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])'

// visibleFocusables возвращает РЕАЛЬНО видимые фокусируемые элементы: FOCUSABLE_SELECTOR ловит и скрытые
// CSS-ом узлы, на которых .focus — no-op, и фокус остался бы снаружи модалки.
export function visibleFocusables(root: HTMLElement): HTMLElement[] {
  const all = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
  return all.filter((el) => (typeof el.checkVisibility === 'function' ? el.checkVisibility() : true))
}
