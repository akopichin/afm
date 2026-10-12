import { useEffect, useState } from 'react'

// Типы input, которые являются click-control'ами, а не полем ввода: фокус на них НЕ считается
// редактированием.
const CLICK_CONTROL_INPUT_TYPES = new Set(['checkbox', 'radio', 'button', 'submit', 'reset', 'image'])

// contenteditable в jsdom не реализует HTMLElement.isContentEditable (всегда undefined) — поэтому
// обязателен fallback на атрибут, иначе ветка мертва и в тестах, и в проде.
function isContentEditableElement(el: Element): boolean {
  return (
    (el as HTMLElement).isContentEditable === true ||
    el.getAttribute?.('contenteditable') === '' ||
    el.getAttribute?.('contenteditable') === 'true'
  )
}

// isEditableElement — чистый предикат «этот элемент — редактируемое поле?». Принимает элемент аргументом
// (не читает document), поэтому тестируется без фокус-возни.
export function isEditableElement(el: Element | null): boolean {
  if (el === null) return false
  const tag = el.tagName
  if (tag === 'TEXTAREA') return true
  if (tag === 'INPUT') {
    const type = (el as HTMLInputElement).type?.toLowerCase() ?? ''
    return !CLICK_CONTROL_INPUT_TYPES.has(type)
  }
  return isContentEditableElement(el)
}

// isEditableFocused — сейчас в фокусе редактируемый элемент? Используется для suppression авто-открытия
// attention, чтобы не выдёргивать пользователя из набора текста (rule 9).
function isEditableFocused(): boolean {
  return isEditableElement(document.activeElement)
}

// useIsEditing — реактивный сигнал «сейчас в фокусе редактируемый элемент».
export function useIsEditing(): boolean {
  // Lazy-init фактическим состоянием фокуса, а не жёстким false — иначе remount
  // при уже сфокусированном поле создал бы окно ложного auto-open.
  const [editing, setEditing] = useState(() => isEditableFocused())

  // Смена фокуса: focusin/focusout не вызывают ре-рендер сами по себе, поэтому
  // слушаем их явно. Mount-only.
  useEffect(() => {
    const update = (): void => setEditing(isEditableFocused())
    document.addEventListener('focusin', update)
    document.addEventListener('focusout', update)
    return () => {
      document.removeEventListener('focusin', update)
      document.removeEventListener('focusout', update)
    }
  }, [])

  // Reconcile после каждого commit: если сфокусированный composer удаляется из DOM, focusout может не
  // выстрелить, и editing застрял бы в true навсегда.
  useEffect(() => {
    setEditing(isEditableFocused())
  })

  return editing
}
