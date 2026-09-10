import type { ReactNode, ReactElement } from 'react'

type DashboardShellProps = {
  rail: ReactNode
  // tabs — верхняя полоса вкладок воркспейса (Feed + контекстная attention).
  tabs: ReactNode
  // workspace — активный воркспейс (Feed / attention / history) под вкладками.
  workspace: ReactNode
}

// DashboardShell — нерезайзящаяся оболочка тела дашборда: слева рейл стадий,
// справа единый воркспейс (вкладки + активное содержимое). Пришла на смену
// трёхколоночному resizable DashboardLayout — план/диалог/лента больше не
// конкурируют в одновременных колонках, поэтому и ручки ресайза, и maximize
// не нужны (файловый браузер остаётся оверлеем, его внутренний делитель —
// отдельная история). Раскладка — CSS grid (skins/base/shell.css), без JS-стейта.
export function DashboardShell({ rail, tabs, workspace }: DashboardShellProps): ReactElement {
  return (
    <div className="dashboard-body">
      <aside className="rail-slot">{rail}</aside>
      <section className="workspace" aria-label="Workspace">
        {tabs}
        <div className="workspace-body">{workspace}</div>
      </section>
    </div>
  )
}
