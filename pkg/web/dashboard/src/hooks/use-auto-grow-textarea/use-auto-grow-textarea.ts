import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

// Растит textarea по мере ввода текста (через scrollHeight) до maxHeightPx —
// дальше работает overflow-y из CSS. useLayoutEffect выполняется синхронно
// ДО отрисовки браузера: схлопывание в 'auto' и обратный рост до scrollHeight
// происходят за один кадр, поэтому курсор/вьюпорт не дёргаются.
//
// ResizeObserver ловит РУЧНОЙ ресайз через уголок textarea: браузер при
// перетаскивании сам выставляет el.style.height, поэтому сравнение текущего
// style.height с тем, что последним выставил сам хук (lastAutoHeight),
// однозначно отличает «потянул пользователь» от «выставили мы». Once
// detected, дальнейший авто-рост блокируется до следующего опустошения поля.
//
// ref — callback (не RefObject), по образцу useStickToBottom: textarea во
// всех трёх местах использования (PlanPanel/DialogChannel line-comment) может
// монтироваться ПОЗЖЕ первого рендера хука — форма комментария открывается
// условно, по клику на конкретную строку. useEffect с [] сработал бы один
// раз при пустом ref.current и больше никогда не переустановил бы
// ResizeObserver. Callback ref гарантирует переустановку эффектов именно в
// момент реального (пере)монтирования DOM-узла.
export function useAutoGrowTextarea(
  value: string,
  maxHeightPx: number,
): (node: HTMLTextAreaElement | null) => void {
  const [node, setNode] = useState<HTMLTextAreaElement | null>(null)
  const lastAutoHeight = useRef<number | null>(null)
  const locked = useRef(false)
  // Значение, которое хук УЖЕ обработал для текущего узла (null = свежий узел,
  // ещё не мерили). На монтировании textarea НЕ должна скроллить страницу к
  // себе: сам факт появления поля ввода не повод дёргать вьюпорт — иначе при
  // переходе из ленты к старому Q&A (DialogChannel монтирует attention-вид с
  // pending-textarea) её scrollIntoView перебивал прицельный скролл к вопросу.
  // Скроллим ТОЛЬКО когда value реально изменилось (пользователь печатает).
  // Сравнение по value, а не «первый прогон эффекта», устойчиво к повторному
  // прогону layout-эффекта в React StrictMode (dev): реплей идёт с тем же
  // value и тем же узлом → prevValue === value → скролла нет.
  const prevValue = useRef<string | null>(null)

  // Новый вызов callback-ref'а — это либо реально новый DOM-узел (открылась
  // форма другой строки), либо unmount. В обоих случаях состояние
  // lastAutoHeight/locked/prevValue от ПРЕДЫДУЩЕГО узла бессмысленно для
  // следующего — без сброса лок с одной строки протекал бы на форму другой.
  const ref = useCallback((el: HTMLTextAreaElement | null) => {
    lastAutoHeight.current = null
    locked.current = false
    prevValue.current = null
    setNode(el)
  }, [])

  useLayoutEffect(() => {
    if (node === null) return

    if (value === '') {
      locked.current = false
    }
    if (locked.current) return

    node.style.height = 'auto'
    const next = Math.min(node.scrollHeight, maxHeightPx)
    node.style.height = `${next}px`
    lastAutoHeight.current = next

    // Скроллим только при РЕАЛЬНОЙ смене value для этого узла (ввод). На
    // монтировании (prevValue === null) и на StrictMode-реплее (prevValue ===
    // value) — только меряем высоту, без scrollIntoView.
    const changed = prevValue.current !== null && prevValue.current !== value
    prevValue.current = value
    if (!changed) {
      return
    }

    // Рост textarea может вытолкнуть то, что лежит под ней (например, кнопку
    // «Добавить»/«Отправить»), за пределы ближайшего скролл-контейнера
    // (#plan-content, #dialog-scroll) без явного признака, что там есть
    // скролл. 'nearest' докручивает минимально необходимо, не дёргая
    // страницу, если элемент и так виден.
    //
    // Скроллим к СЛЕДУЮЩЕМУ элементу (кнопке), а не к самой textarea:
    // scrollIntoView(node) гарантирует видимость верха textarea, но никак не
    // гарантирует видимость того, что идёт после неё. Когда высокая textarea
    // (после роста) не помещается в видимую область скролл-контейнера вместе
    // с кнопкой, приоритет — показать кнопку (то, что реально нужно нажать),
    // а не верх уже введённого текста: пользователь обычно печатает в конце.
    // Фолбэк на node — на случай, если у textarea вообще нет соседа.
    const scrollTarget = node.nextElementSibling ?? node
    scrollTarget.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [value, maxHeightPx, node])

  useEffect(() => {
    if (node === null) return

    const obs = new ResizeObserver(() => {
      const current = parseFloat(node.style.height || '0')
      if (lastAutoHeight.current !== null && current !== lastAutoHeight.current) {
        locked.current = true
      }
    })
    obs.observe(node)
    return () => obs.disconnect()
  }, [node])

  return ref
}
