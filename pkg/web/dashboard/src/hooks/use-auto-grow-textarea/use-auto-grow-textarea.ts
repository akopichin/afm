import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

// Растит textarea по мере ввода текста до maxHeightPx — дальше работает overflow-y из CSS. Once detected,
// дальнейший авто-рост блокируется до следующего опустошения поля.
export function useAutoGrowTextarea(
  value: string,
  maxHeightPx: number,
): (node: HTMLTextAreaElement | null) => void {
  const [node, setNode] = useState<HTMLTextAreaElement | null>(null)
  const lastAutoHeight = useRef<number | null>(null)
  const locked = useRef(false)
  // Значение, которое хук УЖЕ обработал для текущего узла. Скроллим ТОЛЬКО когда value реально изменилось
  // (пользователь печатает).
  const prevValue = useRef<string | null>(null)
  // needsMeasure — значение сменилось, пока поле было скрыто (display: none): мерить нечего, перемерим при показе.
  const needsMeasure = useRef(false)

  // Новый вызов callback-ref'а — это либо реально новый DOM-узел, либо unmount.
  const ref = useCallback((el: HTMLTextAreaElement | null) => {
    lastAutoHeight.current = null
    locked.current = false
    prevValue.current = null
    needsMeasure.current = false
    setNode(el)
  }, [])

  // measure — подгоняет высоту под текст (до maxHeightPx). false — поле скрыто (display: none): мерить нечего,
  // перемерим при показе (ResizeObserver ниже).
  const measure = useCallback((el: HTMLTextAreaElement): boolean => {
    if (el.scrollHeight === 0 && el.clientHeight === 0) {
      needsMeasure.current = true
      return false
    }
    needsMeasure.current = false
    el.style.height = 'auto'
    const next = Math.min(el.scrollHeight, maxHeightPx)
    el.style.height = `${next}px`
    lastAutoHeight.current = next
    return true
  }, [maxHeightPx])

  useLayoutEffect(() => {
    if (node === null) return

    if (value === '') {
      locked.current = false
    }
    if (locked.current) return

    const measured = measure(node)

    // Скроллим только при РЕАЛЬНОЙ смене value для этого узла (ввод). На монтировании (prevValue === null)
    // и на StrictMode-реплее (prevValue === value) — только меряем высоту, без scrollIntoView.
    const changed = prevValue.current !== null && prevValue.current !== value
    prevValue.current = value
    if (!changed || !measured) {
      return
    }

    // Рост textarea может вытолкнуть то, что лежит под ней, за пределы ближайшего скролл-контейнера без
    // явного признака, что там есть скролл.
    const scrollTarget = node.nextElementSibling ?? node
    scrollTarget.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [value, node, measure])

  useEffect(() => {
    if (node === null) return

    const obs = new ResizeObserver(() => {
      // Скрытое поле помечаем к перемеру; показанное снова (или изменившее ширину, пока было скрыто) меряем.
      if (node.scrollHeight === 0 && node.clientHeight === 0) {
        needsMeasure.current = true
        return
      }
      if (needsMeasure.current) {
        if (!locked.current) measure(node)
        return
      }
      const current = parseFloat(node.style.height || '0')
      if (lastAutoHeight.current !== null && current !== lastAutoHeight.current) {
        locked.current = true
      }
    })
    obs.observe(node)
    return () => obs.disconnect()
  }, [node, measure])

  return ref
}
