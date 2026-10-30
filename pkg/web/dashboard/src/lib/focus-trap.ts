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

// trapTab замыкает Tab/Shift+Tab внутри root: с последнего элемента — на первый и обратно. Фокус на самом
// root или на нефокусируемом потомке уходит на край, пустой root фокус не отпускает.
export function trapTab(e: Pick<KeyboardEvent, 'key' | 'shiftKey' | 'preventDefault'>, root: HTMLElement): void {
  if (e.key !== 'Tab') return
  const items = visibleFocusables(root)
  const first = items[0]
  const last = items[items.length - 1]
  if (first === undefined || last === undefined) {
    e.preventDefault()
    return
  }
  const active = document.activeElement
  if (active instanceof HTMLElement && root.contains(active) && !items.includes(active)) {
    e.preventDefault()
    ;(e.shiftKey ? last : first).focus()
  } else if (e.shiftKey && active === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && active === last) {
    e.preventDefault()
    first.focus()
  }
}

// DialogOpenContext — куда диалог сообщает «открыт/закрыт». App считает открытые диалоги и добавляет их к
// anyModalOpen: attention не уводит воркспейс из-под открытой формы, даже когда фокус на checkbox или кнопке.
// Сообщение идёт из layout-эффекта диалога: он выполняется раньше layout-синка attention в App того же
// commit, поэтому вопрос, пришедший одним batch с открытием диалога, уже видит диалог открытым.
export const DialogOpenContext = createContext<(open: boolean) => void>(() => {})

// useModalDialog — жизненный цикл фокуса модального диалога: фокус входит в первый элемент, Tab не выходит
// наружу, Escape вызывает onClose, на закрытии фокус возвращается туда, где его ждут. returnFocus находит цель
// в момент закрытия (например, карточку задачи по ключу, если доска её перерисовала); без неё — элемент,
// открывший диалог, если он ещё в документе. active=false — вид диалога скрыт (Team остаётся смонтированным на
// других видах): диалог и его текст живут, но не держат фокус и клавиши и не считаются открытыми; при новом
// показе фокус снова входит в диалог.
export function useModalDialog(
  modalRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  returnFocus?: () => HTMLElement | null | undefined,
  active = true,
): void {
  const reportOpen = useContext(DialogOpenContext)
  const onCloseRef = useRef(onClose)
  const returnFocusRef = useRef(returnFocus)
  useEffect(() => {
    onCloseRef.current = onClose
    returnFocusRef.current = returnFocus
  })

  const activeRef = useRef(active)
  activeRef.current = active
  const openerRef = useRef<Element | null>(null)

  useLayoutEffect(() => {
    if (!active) return
    if (openerRef.current === null) openerRef.current = document.activeElement
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
      const active = document.activeElement
      // Some callers park initial/fallback focus on a non-tabbable element inside the modal (e.g. a
      // heading announcing an empty state) — it is in `root` but not in `items`, so neither branch below
      // would catch it and Tab would fall through to the browser's default, escaping the trap.
      if (active instanceof HTMLElement && root.contains(active) && !items.includes(active)) {
        e.preventDefault()
        ;(e.shiftKey ? last : first).focus()
        return
      }
      if (e.shiftKey && active === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && active === last) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)

    return () => {
      document.removeEventListener('keydown', onKeyDown)
      reportOpen(false)
      // Скрыт, а не закрыт: фокус остаётся у показанного вида.
      if (!activeRef.current) return
      const opener = openerRef.current
      const target = returnFocusRef.current?.() ?? (opener instanceof HTMLElement && opener.isConnected ? opener : null)
      target?.focus()
    }
  }, [modalRef, reportOpen, active])
}
