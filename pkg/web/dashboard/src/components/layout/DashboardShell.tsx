import { useEffect, useState, type MouseEvent, type ReactNode, type ReactElement } from 'react'

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
export function DashboardShell({ rail, tabs, workspace }: DashboardShellProps): ReactElement {
  const [railOpen, setRailOpen] = useState(false)

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
      <aside className="rail-slot" id="rail-slot" onClickCapture={onRailClickCapture}>
        {rail}
      </aside>
      <section className="workspace" aria-label="Workspace">
        <div className="workspace-topbar">
          <button
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
