import { executorLabel, type ExecutorLabel } from './executor-label'

export type VerifyStepInfo = {
  step: number
  command: string
  kind?: string
  name?: string
  commandName?: string
}

export type VerifyStepPresentation = {
  // subject — what the row names: the check's name, else the shell command or the AI step's executor (legacy).
  subject: string
  named: boolean
  // executor — the AI step's executor; a shell step has none.
  executor: ExecutorLabel | null
}

// presentVerifyStep is the one place that decides how a Verify step is named, for the Feed rows and the rail.
export function presentVerifyStep(info: VerifyStepInfo, unknownPrefix?: string): VerifyStepPresentation {
  const executor = info.kind === 'shell' ? null : executorLabel(info.command, info.commandName, unknownPrefix)
  const name = info.name ?? ''
  return {
    subject: name !== '' ? name : (executor?.label ?? info.command),
    named: name !== '',
    executor,
  }
}
