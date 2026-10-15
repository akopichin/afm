import type { ReactElement } from 'react'
import type { AssistantTask } from '../../types/assistant-task'

type AssistantBreadcrumbProps = {
  // chain — root-first, INCLUDING the currently selected task itself (AssistantBreadcrumb is a dumb renderer
  // of whatever buildBreadcrumbChain already computed — see its own doc comment for why a root-only task
  // yields a chain of length 1).
  chain: AssistantTask[]
  // maxDepth — the run's effective delegation-depth ceiling (GET /api/assistant-profiles, resolved once per
  // run, not per task) — null when collaboration is off or the value has not loaded yet, in which case only
  // the chain/current depth renders, no "of N".
  maxDepth: number | null
}

// AssistantBreadcrumb (T4a.8) shows a delegated task's full ancestry — "Frontend → QA → Mobile" — plus its
// numeric depth (and the run's max_depth, once known) and highlights the immediate parent (the second-to-last
// link) distinctly from the rest of the chain. Renders nothing for a root task (chain length 1): a
// breadcrumb with a single crumb carries no information a root task's own header does not already show.
export function AssistantBreadcrumb({ chain, maxDepth }: AssistantBreadcrumbProps): ReactElement | null {
  if (chain.length <= 1) return null

  const current = chain[chain.length - 1] as AssistantTask
  const immediateParent = chain[chain.length - 2] as AssistantTask
  const depth = current.depth

  return (
    <nav className="assistant-breadcrumb" aria-label="Delegation chain">
      <ol className="assistant-breadcrumb-chain">
        {chain.map((t, i) => {
          const label = t.title !== '' ? t.title : t.profileId
          const isImmediateParent = t.id === immediateParent.id
          const isCurrent = i === chain.length - 1
          return (
            <li
              key={t.id}
              className="assistant-breadcrumb-item"
              data-immediate-parent={isImmediateParent ? 'true' : undefined}
              data-current={isCurrent ? 'true' : undefined}
            >
              {i > 0 && (
                <span className="assistant-breadcrumb-sep" aria-hidden="true">
                  →
                </span>
              )}
              <span>{label}</span>
            </li>
          )
        })}
      </ol>
      <span className="assistant-breadcrumb-depth">
        Depth {depth}
        {maxDepth !== null ? ` of ${maxDepth}` : ''}
      </span>
    </nav>
  )
}
