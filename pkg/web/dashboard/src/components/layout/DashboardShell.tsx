import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type MouseEvent, type ReactNode, type ReactElement } from 'react'
import { trapTab, visibleFocusables } from '../../lib/focus-trap'
import { usePanelResize } from './use-panel-resize'

type DashboardShellProps = {
  rail: ReactNode
  // tabs — верхняя полоса вкладок воркспейса (Feed + контекстная attention).
  tabs: ReactNode
  // workspace — активный воркспейс (Feed / attention / history) под вкладками.
  workspace: ReactNode
}

// DashboardShell — оболочка тела дашборда: слева рейл стадий, справа единый воркспейс. Раскладка — CSS
// grid. Ширина рейла и мобильная шторка (≤900px) — тот же usePanelResize, что у People и Team: ширина
// клампится так, чтобы воркспейсу осталось место, и горизонтального скролла нет.
export function DashboardShell({ rail, tabs, workspace }: DashboardShellProps): ReactElement {
  const [railOpen, setRailOpen] = useState(false)
  const resize = usePanelResize({
    storageKey: 'afm.railWidth', defaultWidth: 300, container: 'self', direction: 1,
    drawerQuery: '(max-width: 900px)', toggleSelector: '.rail-toggle',
  })
  // Мобильный режим (шторка): закрытый рейл `inert`, на десктопе рейл всегда виден и интерактивен.
  const isMobile = resize.isDrawer
  const railRef = useRef<HTMLElement | null>(null)
  const toggleRef = useRef<HTMLButtonElement | null>(null)

  // Закрытая шторка на мобиле — `inert`: её кнопки не должны оставаться в tab-order за экраном. На
  // десктопе или при открытой шторке — интерактивна.
  useEffect(() => {
    const el = railRef.current
    if (el === null) return
    el.inert = isMobile && !railOpen
  }, [isMobile, railOpen])

  // Открытие шторки фокусирует первую стадию (до первого /api/status стадий нет — тогда первый контрол или
  // сам рейл), закрытие возвращает фокус на тумблер.
  const prevOpen = useRef(false)
  useEffect(() => {
    if (!isMobile) { prevOpen.current = railOpen; return }
    if (railOpen && !prevOpen.current) {
      const rail = railRef.current
      if (rail !== null) (rail.querySelector<HTMLElement>('.stage-row') ?? visibleFocusables(rail)[0] ?? rail).focus()
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

  // Открытая шторка перекрывает воркспейс: Tab и Shift+Tab ходят по кругу внутри неё.
  function onRailKeyDown(e: KeyboardEvent<HTMLElement>): void {
    if (railOpen && isMobile && railRef.current !== null) trapTab(e, railRef.current)
  }

  // Клик по строке стадии (или строке помощника) внутри шторки = выбор сделан → закрываем её. Клик по
  // развернуть-свернуть ветки помощников (.assistant-toggle — живёт и на самой стадии, и на любом узле-помощнике
  // с детьми) НЕ закрывает — иначе ветка схлопнулась бы вместе со шторкой.
  function onRailClickCapture(e: MouseEvent<HTMLElement>): void {
    if (!railOpen) return
    const target = e.target as HTMLElement
    if (target.closest('.assistant-toggle') !== null) return
    if (target.closest('[data-stage-id]') !== null || target.closest('[data-task-id]') !== null) {
      setRailOpen(false)
    }
  }

  return (
    <div
      className={`dashboard-body${railOpen ? ' rail-open' : ''}`}
      ref={resize.targetRef}
      style={{ '--rail-width': `${resize.effectiveWidth}px` } as CSSProperties}
    >
      {/* Скрим под шторкой (только <900px, виден лишь при .rail-open). */}
      <button
        type="button"
        className="rail-scrim"
        aria-label="Close stages"
        tabIndex={railOpen ? 0 : -1}
        onClick={() => setRailOpen(false)}
      />
      <aside className="rail-slot" id="rail-slot" ref={railRef} tabIndex={-1} onClickCapture={onRailClickCapture} onKeyDown={onRailKeyDown}>
        {rail}
      </aside>
      {/* Делитель между рейлом и воркспейсом. Скрыт на мобиле (рейл там — шторка). */}
      <div
        ref={resize.handleRef}
        className="rail-resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize stages panel"
        aria-valuenow={resize.effectiveWidth}
        aria-valuemin={200}
        aria-valuemax={resize.maxWidth}
        aria-hidden={resize.isDrawer ? true : undefined}
        tabIndex={resize.isDrawer ? -1 : 0}
        onMouseDown={resize.onMouseDown}
        onKeyDown={resize.onKeyDown}
        onDoubleClick={resize.onDoubleClick}
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
