import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react'

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
export function useAutoGrowTextarea(value: string, maxHeightPx: number): RefObject<HTMLTextAreaElement> {
  const ref = useRef<HTMLTextAreaElement>(null)
  const lastAutoHeight = useRef<number | null>(null)
  const locked = useRef(false)

  useLayoutEffect(() => {
    const el = ref.current
    if (el === null) return

    if (value === '') {
      locked.current = false
    }
    if (locked.current) return

    el.style.height = 'auto'
    const next = Math.min(el.scrollHeight, maxHeightPx)
    el.style.height = `${next}px`
    lastAutoHeight.current = next
  }, [value, maxHeightPx])

  useEffect(() => {
    const el = ref.current
    if (el === null) return

    const obs = new ResizeObserver(() => {
      const current = parseFloat(el.style.height || '0')
      if (lastAutoHeight.current !== null && current !== lastAutoHeight.current) {
        locked.current = true
      }
    })
    obs.observe(el)
    return () => obs.disconnect()
  }, [ref.current])

  return ref
}
