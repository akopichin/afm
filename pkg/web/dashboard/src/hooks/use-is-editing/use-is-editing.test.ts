import { act, render, renderHook } from '@testing-library/react'
import { createElement } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { isEditableElement, useIsEditing } from './use-is-editing'

// Собрать INPUT с заданным type (пустой аргумент — атрибут не ставим вовсе).
function input(type?: string): HTMLInputElement {
  const el = document.createElement('input')
  if (type !== undefined) el.setAttribute('type', type)
  return el
}

describe('isEditableElement (чистый предикат, DENYLIST)', () => {
  // Редактируемые: textarea, contenteditable и всё, что НЕ click-control input.
  const editableCases: Array<[string, Element]> = [
    ['textarea', document.createElement('textarea')],
    ['contenteditable="true"', (() => { const d = document.createElement('div'); d.setAttribute('contenteditable', 'true'); return d })()],
    ['contenteditable=""', (() => { const d = document.createElement('div'); d.setAttribute('contenteditable', ''); return d })()],
    ['input text', input('text')],
    ['input search', input('search')],
    ['input email', input('email')],
    ['input url', input('url')],
    ['input tel', input('tel')],
    ['input password', input('password')],
    ['input number', input('number')],
    ['input date', input('date')],
    ['input time', input('time')],
    ['input range', input('range')],
    ['input color', input('color')],
    ['input file', input('file')],
    ['input без type', input()],
    ['input type=""', input('')],
    ['input неизвестный type', input('quux')],
  ]
  it.each(editableCases)('%s → true', (_label, el) => {
    expect(isEditableElement(el)).toBe(true)
  })

  // Click-control input'ы, select, null — не редактирование.
  const nonEditableCases: Array<[string, Element | null]> = [
    ['input checkbox', input('checkbox')],
    ['input radio', input('radio')],
    ['input button', input('button')],
    ['input submit', input('submit')],
    ['input reset', input('reset')],
    ['input image', input('image')],
    ['input CHECKBOX (case-insensitive)', input('CHECKBOX')],
    ['select', document.createElement('select')],
    ['null', null],
  ]
  it.each(nonEditableCases)('%s → false', (_label, el) => {
    expect(isEditableElement(el)).toBe(false)
  })
})

describe('useIsEditing (реальный document.activeElement)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('фокус на textarea → true, blur → false', () => {
    const ta = document.createElement('textarea')
    document.body.appendChild(ta)
    const { result } = renderHook(() => useIsEditing())
    expect(result.current).toBe(false)

    act(() => ta.focus())
    expect(result.current).toBe(true)

    act(() => ta.blur())
    expect(result.current).toBe(false)
  })

  it('фокус на checkbox → false (click-control не редактирование)', () => {
    const cb = document.createElement('input')
    cb.type = 'checkbox'
    document.body.appendChild(cb)
    const { result } = renderHook(() => useIsEditing())

    act(() => cb.focus())
    // В jsdom клик не гарантирует фокус — проверяем, что фокус реально наступил.
    expect(document.activeElement).toBe(cb)
    expect(result.current).toBe(false)
  })

  it('lazy-init: textarea в фокусе ДО renderHook → true сразу', () => {
    const ta = document.createElement('textarea')
    document.body.appendChild(ta)
    ta.focus()
    expect(document.activeElement).toBe(ta)

    const { result } = renderHook(() => useIsEditing())
    // true на первом же рендере — отличает lazy-init от useState(false),
    // который дал бы true только после focusin.
    expect(result.current).toBe(true)
  })

  it('focus transfer editable→checkbox→editable не оставляет stale-значение', () => {
    const ta = document.createElement('textarea')
    const cb = document.createElement('input')
    cb.type = 'checkbox'
    document.body.appendChild(ta)
    document.body.appendChild(cb)
    const { result } = renderHook(() => useIsEditing())

    act(() => ta.focus())
    expect(result.current).toBe(true)

    act(() => cb.focus())
    expect(result.current).toBe(false)

    act(() => ta.focus())
    expect(result.current).toBe(true)
  })

  it('reconcile-after-unmount: удаление сфокусированного textarea → false без blur', () => {
    // Компонент, который сам использует хук и условно рендерит focusable textarea.
    function Harness({ show }: { show: boolean }) {
      const editing = useIsEditing()
      return createElement(
        'div',
        null,
        createElement('span', { 'data-testid': 'editing' }, String(editing)),
        show ? createElement('textarea', { 'data-testid': 'ta' }) : null,
      )
    }

    const { rerender, getByTestId } = render(createElement(Harness, { show: true }))
    const ta = getByTestId('ta') as HTMLTextAreaElement

    act(() => ta.focus())
    expect(getByTestId('editing').textContent).toBe('true')

    // Убираем textarea из DOM БЕЗ ручного .blur() — focusout может не выстрелить,
    // но reconcile-эффект после commit сверится с фактическим activeElement.
    rerender(createElement(Harness, { show: false }))
    expect(getByTestId('editing').textContent).toBe('false')
  })
})
