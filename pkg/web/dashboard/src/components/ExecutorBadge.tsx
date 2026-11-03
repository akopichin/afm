import type { CSSProperties, ReactElement } from 'react'
import { executorLabel, executorTooltip, useUnknownCommandName } from '../lib/executor-label'

type ExecutorBadgeProps = {
  command: string
  commandName?: string
  // className — each surface keeps its own chip styling (and ellipsis).
  className?: string
  style?: CSSProperties
}

// ExecutorBadge — text chip with the readable executor name; the tooltip keeps the full name for a clipped
// label and the raw command. Rendered as text only.
export function ExecutorBadge({ command, commandName, className = 'executor-badge', style }: ExecutorBadgeProps): ReactElement | null {
  const executor = executorLabel(command, commandName, useUnknownCommandName())
  if (executor.label === '') return null
  return <span className={className} style={style} title={executorTooltip(executor)}>{executor.label}</span>
}
