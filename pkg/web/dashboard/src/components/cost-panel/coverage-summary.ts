import type { AccountingState, Attribution, CoverageIssue } from '../../types/cost'

// Вынесено из RunMetrics.tsx (Task 11): и тайл Est. cost в шапке, и CostPanel
// строят строку покрытия по одним и тем же группам issues — единая логика
// живёт здесь, а не дублируется/не импортируется компонент-из-компонента.
const MAX_COVERAGE_GROUPS_SHOWN = 3

// summarizeCoverageGroups — общий ограниченный билдер сводки по пробелам
// покрытия: первые MAX_COVERAGE_GROUPS_SHOWN групп как есть, остаток сворачивается
// в одну фразу «and K more groups (M invocations)» (K — число скрытых групп,
// M — сумма их count, а не количество групп) — тайл/панель не должны раздуваться
// на десятки строк при большом числе разных пробелов.
export function summarizeCoverageGroups(issues: CoverageIssue[]): string {
  const shown = issues.slice(0, MAX_COVERAGE_GROUPS_SHOWN).map(describeCoverageIssue)
  const rest = issues.slice(MAX_COVERAGE_GROUPS_SHOWN)
  if (rest.length === 0) return shown.join('; ')
  const invocations = rest.reduce((sum, issue) => sum + issue.count, 0)
  return `${shown.join('; ')}; and ${rest.length} more groups (${invocations} invocations)`
}

// attributionLabel — owner of a coverage gap: a stage id, run overhead, or
// unknown when neither is resolvable. Codex#2/opus#2 review: the coverage line
// used to drop this entirely, leaving no way to tell "this stage's invocations
// aren't priced" from "run overhead isn't priced" — both rendered identically.
function attributionLabel(attribution: Attribution): string {
  switch (attribution.kind) {
    case 'stage':
      return attribution.stageId
    case 'run_overhead':
      return 'run overhead'
    case 'unknown':
      return 'unknown'
  }
}

function describeCoverageIssue(issue: CoverageIssue): string {
  const model = issue.model === '' ? 'unknown model' : issue.model
  const label = issue.kind === 'unmetered' ? 'unmetered' : 'unpriced'
  const attrLabel = attributionLabel(issue.attribution)
  const reasonSuffix = issue.reason !== '' ? ` — ${issue.reason}` : ''
  return `${label} — ${attrLabel}/${issue.phase} — ${model}${reasonSuffix} (×${issue.count})`
}

// costReason — составляет пояснение маркера по ДВУМ независимым осям (round-6
// #3 спеки): недоступность accounting-хранилища и пробелы покрытия. Если
// сработали обе — обе фразы идут подряд, ни одна не перекрывает другую (это
// разные факты: упавший writer и непрайсед модель).
export function costReason(coverageIssues: CoverageIssue[], accounting: AccountingState): string {
  const parts: string[] = []
  if (accounting.supported && accounting.health === 'unavailable') {
    parts.push('Cost accounting storage is unavailable right now — totals may be incomplete.')
  }
  if (coverageIssues.length > 0) {
    parts.push(`Coverage gaps: ${summarizeCoverageGroups(coverageIssues)}.`)
  }
  return parts.join(' ')
}
