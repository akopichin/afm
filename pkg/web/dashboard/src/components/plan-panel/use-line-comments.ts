import { useCallback, useLayoutEffect, useRef, useState } from 'react'

// useLineComments — слой ВЗАИМОДЕЙСТВИЯ поверх заякоренного markdown-рендера
// (renderAnchoredSections в markdown.ts). Владелец состояния (LineCommentedDocument)
// держит comments/activeCommentLine/rovingLine и вызывает этот хук; хук:
//   • привязывается к markdown-контейнеру через callback-ref (контейнер монтируется
//     условно — #plan-content/.dialog-question появляются только после загрузки),
//   • делегирует click/hover/keyboard на контейнер (scoped),
//   • держит роллинг tabindex по ВИДИМЫМ якорям (стрелки двигают, Enter/Space
//     открывают форму только когда фокус на самом якоре),
//   • навешивает императивный класс-слой (has-comment/is-active + roving tabindex)
//     через useLayoutEffect — идемпотентно, только атрибуты/классы, без вставок узлов.
// is-hover принадлежит pointer-хендлерам (НЕ императивному слою) — иначе при смене
// roving/state под неподвижным курсором подсветка пропала бы до следующего движения.

export type UseLineCommentsArgs = {
  comments: Record<number, string>
  activeCommentLine: number | null
  rovingLine: number | null
  setRovingLine: (line: number) => void
  // onActivate — клик по телу якоря (без drag/без выделения) ИЛИ Enter/Space на
  // самом якоре. Владелец решает, что делать (открыть/переключить/закрыть форму).
  onActivate: (line: number) => void
  // contentSignal меняется, когда меняется отрендеренный html (sections) — тогда
  // React заменяет innerHTML и императивные классы надо навесить заново.
  contentSignal: unknown
  // visibilitySignal меняется при сворачивании/разворачивании секций — влияет на
  // множество видимых якорей (roving их пропускает).
  visibilitySignal: unknown
}

export type UseLineComments = {
  containerRef: (node: HTMLElement | null) => void
  focusAnchor: (line: number) => void
}

// Порог, за которым движение указателя между pointerdown и pointerup считается
// перетаскиванием (выделением текста), а не кликом — тогда форму НЕ открываем.
const DRAG_THRESHOLD_PX = 6

function nearestAnchor(target: EventTarget | null, container: HTMLElement): HTMLElement | null {
  if (!(target instanceof Element)) return null
  const el = target.closest('[data-line]')
  if (el === null || !container.contains(el)) return null
  return el as HTMLElement
}

function anchorLine(anchor: HTMLElement): number | null {
  const raw = anchor.getAttribute('data-line')
  if (raw === null) return null
  const n = Number(raw)
  return Number.isFinite(n) ? n : null
}

// Клик по интерактивному потомку якоря (ссылка/кнопка/поле) — не открытие формы:
// пользователь взаимодействует со ссылкой, а не комментирует строку.
function isInteractiveDescendant(target: EventTarget | null, anchor: HTMLElement): boolean {
  if (!(target instanceof Element)) return false
  const interactive = target.closest('a,button,input,textarea')
  return interactive !== null && anchor.contains(interactive)
}

// Скрытый якорь — внутри свёрнутой секции (атрибут hidden на её теле). Такие
// исключаются из roving и не считаются целью навигации стрелками.
function isHidden(el: HTMLElement): boolean {
  return el.closest('[hidden]') !== null
}

function visibleAnchors(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('[data-line]')).filter((a) => !isHidden(a))
}

// selectionIntersectsAnchor — есть ли непустое выделение, пересекающее якорь. Если
// пользователь выделил текст внутри якоря (двойной клик/протяжка), клик, завершающий
// выделение, НЕ должен открывать форму.
function selectionIntersectsAnchor(anchor: HTMLElement): boolean {
  const sel = typeof window.getSelection === 'function' ? window.getSelection() : null
  if (sel === null || sel.isCollapsed || sel.rangeCount === 0) return false
  if (sel.toString() === '') return false
  const range = sel.getRangeAt(0)
  if (typeof range.intersectsNode === 'function') {
    try {
      return range.intersectsNode(anchor)
    } catch {
      // fall through к проверке через anchorNode
    }
  }
  const node = sel.anchorNode
  return node !== null && anchor.contains(node)
}

export function useLineComments(args: UseLineCommentsArgs): UseLineComments {
  const [node, setNode] = useState<HTMLElement | null>(null)

  // Свежие колбэки/значения для делегированных слушателей — чтобы не переустанавливать
  // их на каждый ре-рендер владельца (слушатели зависят только от node).
  const argsRef = useRef(args)
  argsRef.current = args

  const hoveredRef = useRef<HTMLElement | null>(null)
  const pointerRef = useRef({ x: 0, y: 0, dragging: false, suppressClick: false })

  const containerRef = useCallback((n: HTMLElement | null) => setNode(n), [])

  const focusAnchor = useCallback(
    (line: number) => {
      if (node === null) return
      const el = node.querySelector<HTMLElement>(`[data-line="${line}"]`)
      el?.focus()
    },
    [node],
  )

  // Делегированные слушатели: click/hover/keyboard/pointer — только по [node].
  useLayoutEffect(() => {
    if (node === null) return
    const container = node

    const clearHover = (): void => {
      if (hoveredRef.current !== null) {
        hoveredRef.current.classList.remove('is-hover')
        hoveredRef.current = null
      }
    }

    const onPointerOver = (e: Event): void => {
      const anchor = nearestAnchor((e as PointerEvent).target, container)
      if (anchor === hoveredRef.current) return
      clearHover()
      if (anchor !== null) {
        anchor.classList.add('is-hover')
        hoveredRef.current = anchor
      }
    }

    const onPointerOut = (e: Event): void => {
      const related = (e as PointerEvent).relatedTarget
      if (related instanceof Node && container.contains(related)) return // всё ещё внутри — onPointerOver ресинкнет
      clearHover()
    }

    const onPointerDown = (e: Event): void => {
      const pe = e as PointerEvent
      pointerRef.current = { x: pe.clientX, y: pe.clientY, dragging: false, suppressClick: false }
    }

    const onPointerMove = (e: Event): void => {
      const pe = e as PointerEvent
      const p = pointerRef.current
      if (Math.abs(pe.clientX - p.x) > DRAG_THRESHOLD_PX || Math.abs(pe.clientY - p.y) > DRAG_THRESHOLD_PX) {
        p.dragging = true
      }
    }

    const onPointerUp = (): void => {
      pointerRef.current.suppressClick = pointerRef.current.dragging
    }

    const onClick = (e: Event): void => {
      const target = e.target
      // Формы/индикаторы живут ВНУТРИ контейнера — исключаем их ПЕРВЫМИ.
      if (target instanceof Element && target.closest('[data-comment-ui]') !== null) return
      const anchor = nearestAnchor(target, container)
      if (anchor === null) return
      if (isInteractiveDescendant(target, anchor)) return
      // Открываем только на чистом клике: не после drag и не поверх выделения.
      if (pointerRef.current.suppressClick) {
        pointerRef.current.suppressClick = false
        return
      }
      if (selectionIntersectsAnchor(anchor)) return
      const line = anchorLine(anchor)
      if (line === null) return
      argsRef.current.onActivate(line)
    }

    const onKeyDown = (e: Event): void => {
      const ke = e as KeyboardEvent
      const target = ke.target
      if (target instanceof Element && target.closest('[data-comment-ui]') !== null) return
      const anchor = nearestAnchor(target, container)
      if (anchor === null) return

      if (ke.key === 'Enter' || ke.key === ' ' || ke.key === 'Spacebar') {
        // Только когда сфокусирован сам якорь — иначе Enter на вложенной ссылке
        // открыл бы И ссылку, И форму.
        if (target !== anchor) return
        ke.preventDefault()
        const line = anchorLine(anchor)
        if (line !== null) argsRef.current.onActivate(line)
        return
      }

      if (ke.key === 'ArrowDown' || ke.key === 'ArrowUp') {
        ke.preventDefault()
        const anchors = visibleAnchors(container)
        const idx = anchors.indexOf(anchor)
        if (idx === -1) return
        const nextIdx = ke.key === 'ArrowDown' ? Math.min(idx + 1, anchors.length - 1) : Math.max(idx - 1, 0)
        const next = anchors[nextIdx]
        if (next === undefined) return
        const line = anchorLine(next)
        if (line !== null) argsRef.current.setRovingLine(line)
        next.focus()
      }
    }

    container.addEventListener('pointerover', onPointerOver)
    container.addEventListener('pointerout', onPointerOut)
    container.addEventListener('pointerdown', onPointerDown)
    container.addEventListener('pointermove', onPointerMove)
    container.addEventListener('pointerup', onPointerUp)
    container.addEventListener('click', onClick)
    container.addEventListener('keydown', onKeyDown)
    return () => {
      container.removeEventListener('pointerover', onPointerOver)
      container.removeEventListener('pointerout', onPointerOut)
      container.removeEventListener('pointerdown', onPointerDown)
      container.removeEventListener('pointermove', onPointerMove)
      container.removeEventListener('pointerup', onPointerUp)
      container.removeEventListener('click', onClick)
      container.removeEventListener('keydown', onKeyDown)
      clearHover()
    }
  }, [node])

  // Императивный класс-слой: снять и заново навесить has-comment/is-active + roving
  // tabindex. is-hover НЕ трогаем (владение у pointer-хендлеров). useLayoutEffect
  // идёт ПОСЛЕ коммита → если React заменил innerHTML (сменился текст/сборка),
  // классы восстановятся до paint; идемпотентно (StrictMode-safe).
  useLayoutEffect(() => {
    if (node === null) return
    const anchors = Array.from(node.querySelectorAll<HTMLElement>('[data-line]'))
    const visible = anchors.filter((a) => !isHidden(a))

    let rovingTarget: HTMLElement | null = null
    if (args.rovingLine !== null) {
      rovingTarget = visible.find((a) => anchorLine(a) === args.rovingLine) ?? null
    }
    if (rovingTarget === null) rovingTarget = visible[0] ?? null

    for (const a of anchors) {
      const line = anchorLine(a)
      const hasComment = line !== null && args.comments[line] !== undefined
      a.classList.toggle('has-comment', hasComment)
      a.classList.toggle('is-active', line !== null && args.activeCommentLine === line)
      a.setAttribute('tabindex', a === rovingTarget ? '0' : '-1')
    }
  }, [node, args.comments, args.activeCommentLine, args.rovingLine, args.contentSignal, args.visibilitySignal])

  return { containerRef, focusAnchor }
}
