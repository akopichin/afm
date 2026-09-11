import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type MouseEvent, type ReactNode, type ReactElement } from 'react'

type DashboardShellProps = {
  rail: ReactNode
  // tabs — верхняя полоса вкладок воркспейса (Feed + контекстная attention).
  tabs: ReactNode
  // workspace — активный воркспейс (Feed / attention / history) под вкладками.
  workspace: ReactNode
}

// DashboardShell — оболочка тела дашборда: слева рейл стадий, справа единый
// воркспейс (вкладки + активное содержимое). Пришла на смену трёхколоночному
// resizable DashboardLayout — план/диалог/лента больше не конкурируют в
// одновременных колонках, поэтому ни ручки ресайза, ни maximize не нужны
// (файловый браузер остаётся оверлеем).
//
// Раскладка — CSS grid. На узких вьюпортах (<900px, см. layout.css) рейл
// перестаёт занимать колонку и превращается в слайд-овер (шторку) поверх
// воркспейса: тумблер ☰ слева от вкладок открывает его, скрим/Escape/выбор
// стадии — закрывают. Так на телефоне воркспейс получает всю ширину, а не ~150px.
const MOBILE_QUERY = '(max-width: 900px)'

// Ширина рейла стадий — растягивается делителем между рейлом и воркспейсом.
// Персистится, чтобы пережить перезагрузку. Клампится, чтобы рейл нельзя было
// сузить в ноль или растянуть на пол-экрана.
const RAIL_WIDTH_KEY = 'afm.railWidth'
const RAIL_MIN = 190
const RAIL_MAX = 560
const RAIL_DEFAULT = 300
const RAIL_STEP = 16 // шаг клавиатурного ресайза (стрелки)

function clampRail(w: number): number {
  return Math.max(RAIL_MIN, Math.min(w, RAIL_MAX))
}

function loadRailWidth(): number {
  try {
    const raw = window.localStorage.getItem(RAIL_WIDTH_KEY)
    if (raw !== null) {
      const n = Number(raw)
      if (Number.isFinite(n)) return clampRail(n)
    }
  } catch {
    // localStorage недоступен — просто дефолт
  }
  return RAIL_DEFAULT
}

export function DashboardShell({ rail, tabs, workspace }: DashboardShellProps): ReactElement {
  const [railOpen, setRailOpen] = useState(false)
  const [railWidth, setRailWidth] = useState<number>(loadRailWidth)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const draggingRef = useRef(false)
  // Мобильный режим (шторка) — по тому же брейкпоинту, что и CSS. Нужен в JS,
  // чтобы делать закрытый рейл `inert` ТОЛЬКО на мобиле; на десктопе рейл всегда
  // виден и интерактивен.
  const [isMobile, setIsMobile] = useState(false)
  const railRef = useRef<HTMLElement | null>(null)
  const toggleRef = useRef<HTMLButtonElement | null>(null)

  useEffect(() => {
    const mq = window.matchMedia(MOBILE_QUERY)
    const update = (): void => setIsMobile(mq.matches)
    update()
    mq.addEventListener('change', update)
    return () => mq.removeEventListener('change', update)
  }, [])

  // Закрытая шторка на мобиле — `inert`: её кнопки (строки стадий, кебабы) не
  // должны оставаться в tab-order за экраном (Finding #5 второго раунда). На
  // десктопе или при открытой шторке — интерактивна.
  useEffect(() => {
    const el = railRef.current
    if (el === null) return
    el.inert = isMobile && !railOpen
  }, [isMobile, railOpen])

  // Управление фокусом (Finding #5): открытие шторки переносит фокус на первую
  // строку стадии; закрытие возвращает фокус на тумблер, чтобы фокус не остался
  // на кнопке, уехавшей за экран.
  const prevOpen = useRef(false)
  useEffect(() => {
    if (!isMobile) { prevOpen.current = railOpen; return }
    if (railOpen && !prevOpen.current) {
      railRef.current?.querySelector<HTMLElement>('.stage-row')?.focus()
    } else if (!railOpen && prevOpen.current) {
      toggleRef.current?.focus()
    }
    prevOpen.current = railOpen
  }, [railOpen, isMobile])

  // Escape закрывает шторку (симметрично скриму). Слушатель живёт только пока
  // шторка открыта — на десктопе (рейл всегда виден) он не навешивается.
  useEffect(() => {
    if (!railOpen) return
    function onKey(e: globalThis.KeyboardEvent): void {
      if (e.key === 'Escape') setRailOpen(false)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [railOpen])

  // Персист ширины рейла (переживает reload/перезапуск).
  useEffect(() => {
    try {
      window.localStorage.setItem(RAIL_WIDTH_KEY, String(railWidth))
    } catch {
      // localStorage недоступен — ширина просто не сохранится
    }
  }, [railWidth])

  // Перетаскивание делителя: считаем ширину как (clientX - левый край тела).
  // Слушатели вешаем на document, чтобы тянуть можно было и вне узкой полоски.
  function onResizeStart(e: MouseEvent<HTMLDivElement>): void {
    e.preventDefault()
    draggingRef.current = true
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    function onMove(ev: globalThis.MouseEvent): void {
      if (!draggingRef.current || bodyRef.current === null) return
      const left = bodyRef.current.getBoundingClientRect().left
      setRailWidth(clampRail(ev.clientX - left))
    }
    function onUp(): void {
      draggingRef.current = false
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }

  // Клавиатурный ресайз (a11y): стрелки влево/вправо двигают делитель.
  function onResizeKey(e: KeyboardEvent<HTMLDivElement>): void {
    if (e.key === 'ArrowLeft') { e.preventDefault(); setRailWidth((w) => clampRail(w - RAIL_STEP)) }
    else if (e.key === 'ArrowRight') { e.preventDefault(); setRailWidth((w) => clampRail(w + RAIL_STEP)) }
  }

  // Клик по строке стадии внутри шторки = выбор сделан → закрываем её. Клик по
  // кебабу (и его меню) НЕ закрывает — иначе меню действий стадии схлопнулось бы
  // вместе со шторкой. Делегирование, чтобы не прокидывать колбэк в StagesList.
  function onRailClickCapture(e: MouseEvent<HTMLElement>): void {
    if (!railOpen) return
    const target = e.target as HTMLElement
    if (target.closest('[data-stage-id]') && target.closest('.stage-kebab-wrap') === null) {
      setRailOpen(false)
    }
  }

  return (
    <div
      className={`dashboard-body${railOpen ? ' rail-open' : ''}`}
      ref={bodyRef}
      style={{ '--rail-width': `${railWidth}px` } as CSSProperties}
    >
      {/* Скрим под шторкой (только <900px, виден лишь при .rail-open). */}
      <button
        type="button"
        className="rail-scrim"
        aria-label="Close stages"
        tabIndex={railOpen ? 0 : -1}
        onClick={() => setRailOpen(false)}
      />
      <aside className="rail-slot" id="rail-slot" ref={railRef} onClickCapture={onRailClickCapture}>
        {rail}
      </aside>
      {/* Делитель между рейлом и воркспейсом: тонкая полоса-хит-эриа на всю
          высоту, а видимая ручка-грип — маленькая по центру, появляется при
          наведении. Скрыт на мобиле (рейл там — слайд-овер). */}
      <div
        className="rail-resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize stages panel"
        aria-valuenow={railWidth}
        aria-valuemin={RAIL_MIN}
        aria-valuemax={RAIL_MAX}
        tabIndex={0}
        onMouseDown={onResizeStart}
        onKeyDown={onResizeKey}
        onDoubleClick={() => setRailWidth(RAIL_DEFAULT)}
      >
        <span className="rail-resizer-grip" aria-hidden="true" />
      </div>
      <section className="workspace" aria-label="Workspace">
        <div className="workspace-topbar">
          <button
            ref={toggleRef}
            type="button"
            className="rail-toggle"
            aria-label="Toggle stages"
            aria-expanded={railOpen}
            aria-controls="rail-slot"
            onClick={() => setRailOpen((open) => !open)}
          >
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
              <path d="M4 6h16M4 12h16M4 18h16" />
            </svg>
          </button>
          {tabs}
        </div>
        <div className="workspace-body">{workspace}</div>
      </section>
    </div>
  )
}
