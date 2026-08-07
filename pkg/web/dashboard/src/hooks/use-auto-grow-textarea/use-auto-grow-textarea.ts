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

  // Новый вызов callback-ref'а — это либо реально новый DOM-узел (открылась
  // форма другой строки), либо unmount. В обоих случаях состояние
  // lastAutoHeight/locked от ПРЕДЫДУЩЕГО узла бессмысленно для следующего —
  // без сброса лок с одной строки протекал бы на форму другой строки.
  const ref = useCallback((el: HTMLTextAreaElement | null) => {
    lastAutoHeight.current = null
    locked.current = false
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

    // Рост textarea может вытолкнуть то, что лежит под ней (например, кнопку
    // «Добавить»/«Отправить»), за пределы ближайшего скролл-контейнера
    // (#plan-content, #dialog-scroll) без явного признака, что там есть
    // скролл. 'nearest' докручивает минимально необходимо, не дёргая
    // страницу, если элемент и так виден.
    node.scrollIntoView({ block: 'nearest', inline: 'nearest' })
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
