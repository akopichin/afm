// Общий поиск фокусируемых элементов для focus-trap модалок и общий жизненный цикл фокуса диалога.

import { createContext, useContext, useEffect, useLayoutEffect, useRef, type RefObject } from 'react'

export const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])'

// visibleFocusables возвращает РЕАЛЬНО видимые фокусируемые элементы: FOCUSABLE_SELECTOR ловит и скрытые
// CSS-ом узлы, на которых .focus — no-op, и фокус остался бы снаружи модалки.
export function visibleFocusables(root: HTMLElement): HTMLElement[] {
  const all = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
  return all.filter((el) => (typeof el.checkVisibility === 'function' ? el.checkVisibility() : true))
}

// DialogOpenContext — куда диалог сообщает «открыт/закрыт». App считает открытые диалоги и добавляет их к
// anyModalOpen: attention не уводит воркспейс из-под открытой формы, даже когда фокус на checkbox или кнопке.
// Сообщение идёт из layout-эффекта диалога: он выполняется раньше layout-синка attention в App того же
// commit, поэтому вопрос, пришедший одним batch с открытием диалога, уже видит диалог открытым.
export const DialogOpenContext = createContext<(open: boolean) => void>(() => {})

// useModalDialog — жизненный цикл фокуса модального диалога: фокус входит в первый элемент, Tab не выходит
// наружу, Escape вызывает onClose, на закрытии фокус возвращается туда, где его ждут. returnFocus находит цель
// в момент закрытия (например, карточку задачи по ключу, если доска её перерисовала); без неё — элемент,
// открывший диалог, если он ещё в документе.
export function useModalDialog(
  modalRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  returnFocus?: () => HTMLElement | null | undefined,
): void {
  const reportOpen = useContext(DialogOpenContext)
  const onCloseRef = useRef(onClose)
  const returnFocusRef = useRef(returnFocus)
  useEffect(() => {
    onCloseRef.current = onClose
    returnFocusRef.current = returnFocus
  })

  useLayoutEffect(() => {
    const opener = document.activeElement
    const modal = modalRef.current
    if (modal !== null) visibleFocusables(modal)[0]?.focus()
    reportOpen(true)

    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') {
        e.preventDefault()
        onCloseRef.current()
        return
      }
      const root = modalRef.current
      if (e.key !== 'Tab' || root === null) return
      const items = visibleFocusables(root)
      const first = items[0]
      const last = items[items.length - 1]
      if (first === undefined || last === undefined) return
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)

    return () => {
      document.removeEventListener('keydown', onKeyDown)
      reportOpen(false)
      const target = returnFocusRef.current?.() ?? (opener instanceof HTMLElement && opener.isConnected ? opener : null)
      target?.focus()
    }
  }, [modalRef, reportOpen])
}
