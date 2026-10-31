import type { ReactElement } from 'react'

type CountingRulesProps = { hasTokens: boolean; hasMoney: boolean }

// CountingRules — правила подсчёта свёрнуты по умолчанию: постоянная инструкция не нужна, а объяснение рядом.
export function CountingRules({ hasTokens, hasMoney }: CountingRulesProps): ReactElement {
  return (
    <div className="usage-footer">
      <details className="usage-method">
        <summary>How usage is counted</summary>
        <div>
          <p><strong>Agent job:</strong> one run of an agent process, from start to result, including its model requests and tool work. Every job is counted once. A helper owns its usage; its parent does not count it again.</p>
          <p><strong>Agent time</strong> adds job durations, including tools and retries. Parallel jobs can make it longer than the elapsed run time. Waiting for you and pauses are not agent time.</p>
          {hasTokens && <p><strong>Tokens</strong> separate uncached input, cache write, cache read and output. Reasoning is included in output. Select the token total to see its composition.</p>}
          {hasMoney && <p><strong>Estimated cost</strong> uses the configured prices. Jobs without a price make the total partial.</p>}
        </div>
      </details>
    </div>
  )
}
