import type { ReactElement } from 'react'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import type { UsageAgent, UsageAnalytics, UsageModelRef, UsageRow } from '../../types/usage-analytics'
import { plural } from '../../lib/plural'
import { ModelMark } from './ModelMark'
import { SortHeader } from './SortHeader'
import { TimeCell, TokensCell, MoneyCell } from './usage-cells'

type BreakdownTableProps = {
  analytics: UsageAnalyticsController
  data: UsageAnalytics
  // Время, прошедшее после as_of: running-часть длительностей тикает на нём.
  sinceAsOfMs: number
}

const AGENT_KINDS: Record<string, string> = {
  stage: 'Stage agent',
  helper: 'Helper',
  run_agent: 'Run agent',
  verify: 'Verify',
  assistant: 'Assistant',
  run_overhead: 'Run overhead',
  unknown: 'Unattributed',
}

export function agentKindLabel(agent: UsageAgent): string {
  return AGENT_KINDS[agent.kind] ?? agent.kind
}

// Провайдер без заглавной в данных («anthropic») показываем с заглавной.
function providerLabel(channel: string): string {
  return channel.charAt(0).toUpperCase() + channel.slice(1)
}

// modelMeta — «Anthropic · 2 agents»; части, которых нет (канал пуст, сервер не прислал число), опускаются.
function modelMeta(row: UsageRow, model: UsageModelRef): string {
  const configured = row.modelSource === 'hint' ? 'configured' : ''
  const agents = row.agents === null ? '' : plural(row.agents, 'agent')
  return [providerLabel(model.channel), agents, configured].filter((part) => part !== '').join(' · ')
}

// rowMeta — вторая строка идентичности: у модели провайдер и происхождение названия, у агента роль и модель.
function rowMeta(row: UsageRow, byModel: boolean): string {
  const configured = row.modelSource === 'hint' ? ' · configured' : ''
  if (byModel) return row.model === null ? 'Model not reported' : modelMeta(row, row.model)
  const role = row.agent === null ? '' : agentKindLabel(row.agent)
  let model = 'Model unknown'
  if (row.modelState === 'single' && row.model !== null) model = `${row.model.model}${configured}`
  else if (row.modelState === 'mixed') model = 'Mixed models'
  return `${role} · ${model}`
}

// Цвет у модели и у агента с одной моделью; при смешанной или неизвестной модели метки нет, подпись объясняет.
function rowOrdinal(row: UsageRow): number | null | undefined {
  if (row.model === null) return undefined
  return row.model.colorOrdinal
}

export function BreakdownTable({ analytics, data, sinceAsOfMs }: BreakdownTableProps): ReactElement | null {
  if (data.breakdown === null) return null
  const byModel = data.breakdown.group === 'model'
  const hasTokens = analytics.metrics.includes('tokens') && data.metrics.includes('tokens')
  const hasMoney = analytics.metrics.includes('cost') && data.metrics.includes('cost')
  const sort = analytics.query.sort
  const moneyTotal = data.totals.money?.estimatedCostUsd ?? null

  return (
    <>
      <table className="usage-table usage-breakdown" aria-label={`Usage by ${data.breakdown.group}`}>
        <thead>
          <tr>
            <SortHeader label={byModel ? 'Model' : 'Agent'} sortKey="name" current={sort} onSort={analytics.sortBy} />
            <SortHeader label="Jobs" sortKey="jobs" current={sort} onSort={analytics.sortBy} numeric />
            <SortHeader label="Agent time" sortKey="time" current={sort} onSort={analytics.sortBy} numeric />
            {hasTokens && <SortHeader label="Tokens" sortKey="tokens" current={sort} onSort={analytics.sortBy} numeric />}
            {hasMoney && <SortHeader label="Est. cost" sortKey="cost" current={sort} onSort={analytics.sortBy} numeric />}
            <th scope="col" className="usage-drill-head"><span className="usage-sr-only">Details</span></th>
          </tr>
        </thead>
        <tbody>
          {data.breakdown.rows.map((row) => {
            const target = byModel ? { model: row.key } : { agent: row.agent?.id ?? row.key }
            const ordinal = rowOrdinal(row)
            return (
              <tr key={row.key}>
                <td>
                  <button type="button" className="usage-identity" onClick={() => analytics.drill(target)}>
                    <span className="usage-avatar" aria-hidden="true">{row.label.charAt(0).toUpperCase()}</span>
                    <span className="usage-identity-text">
                      <strong>{ordinal !== undefined && <ModelMark ordinal={ordinal} />}{row.label}</strong>{' '}
                      <small>{rowMeta(row, byModel)}</small>
                      {row.agent?.outsideRunTotal === true && <small className="usage-outside">Outside run total</small>}
                    </span>
                  </button>
                </td>
                <td className="usage-numeric" data-label="Jobs">
                  {row.jobs}
                  {row.activeJobs > 0 && <small>{row.activeJobs} running</small>}
                </td>
                <TimeCell metrics={row} sinceAsOfMs={sinceAsOfMs} label="Time" />
                {hasTokens && <TokensCell metrics={row} label="Tokens" />}
                {hasMoney && <MoneyCell metrics={row} total={row.agent?.outsideRunTotal === true ? null : moneyTotal} label="Est. cost" />}
                <td className="usage-drill-cell">
                  <button type="button" className="usage-drill" aria-label={`View agent jobs for ${row.label}`} onClick={() => analytics.drill(target)}>
                    <span className="usage-drill-arrow" aria-hidden="true" />
                  </button>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      <p className="usage-caption">Select a {byModel ? 'model' : 'agent'} to inspect its jobs.</p>
    </>
  )
}
