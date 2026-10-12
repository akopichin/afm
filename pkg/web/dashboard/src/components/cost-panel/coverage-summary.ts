import type { AccountingState, Attribution, CoverageIssue } from '../../types/cost'

// Вынесено из RunMetrics.tsx: и тайл Est.
const MAX_COVERAGE_GROUPS_SHOWN = 3

// summarizeCoverageGroups ограничивает число групп; остаток суммирует как число скрытых групп и их
// invocations.
export function summarizeCoverageGroups(issues: CoverageIssue[]): string {
  const shown = issues.slice(0, MAX_COVERAGE_GROUPS_SHOWN).map(describeCoverageIssue)
  const rest = issues.slice(MAX_COVERAGE_GROUPS_SHOWN)
  if (rest.length === 0) return shown.join('; ')
  const invocations = rest.reduce((sum, issue) => sum + issue.count, 0)
  return `${shown.join('; ')}; and ${rest.length} more groups (${invocations} invocations)`
}

// attributionLabel — owner of a coverage gap: a stage id, run overhead, or unknown when neither is
// resolvable.
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

// costReason — составляет пояснение маркера по ДВУМ независимым осям: недоступность accounting-хранилища и
// пробелы покрытия.
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
