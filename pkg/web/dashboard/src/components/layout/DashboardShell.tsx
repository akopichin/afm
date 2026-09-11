import { useEffect, useRef, useState, type MouseEvent, type ReactNode, type ReactElement } from 'react'

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

export function DashboardShell({ rail, tabs, workspace }: DashboardShellProps): ReactElement {
  const [railOpen, setRailOpen] = useState(false)
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
    function onKey(e: KeyboardEvent): void {
      if (e.key === 'Escape') setRailOpen(false)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [railOpen])

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
    <div className={`dashboard-body${railOpen ? ' rail-open' : ''}`}>
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
