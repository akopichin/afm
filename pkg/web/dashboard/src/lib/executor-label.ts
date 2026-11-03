import { createContext, useContext } from 'react'

// Readable executor names. The server resolves `command_name` (config + built-in names); the dashboard keeps no
// name dictionary of its own and only numbers commands the server sent no name for.

export type ExecutorLabel = { label: string; raw: string }

const DEFAULT_UNKNOWN_PREFIX = 'Unknown'

// The prefix comes from /api/status (`unknown_command_name`); App provides it once for every surface.
export const UnknownCommandNameContext = createContext(DEFAULT_UNKNOWN_PREFIX)

export function useUnknownCommandName(): string {
  return useContext(UnknownCommandNameContext)
}

// Stable number per raw command for the whole page, so one command never gets two labels.
const unknownNumbers = new Map<string, number>()

// Test seam: the numbering is page-lifetime state.
export function resetUnknownCommandLabels(): void {
  unknownNumbers.clear()
}

// executorLabel is the only place that turns a command into visible text. A missing command_name is the
// emergency path (older server): the label is `<prefix>-N`, the raw command stays in the tooltip.
export function executorLabel(command: string, commandName: string | undefined, unknownPrefix = DEFAULT_UNKNOWN_PREFIX): ExecutorLabel {
  if (commandName !== undefined && commandName !== '') return { label: commandName, raw: command }
  if (command === '') return { label: '', raw: '' }
  let number = unknownNumbers.get(command)
  if (number === undefined) {
    number = unknownNumbers.size + 1
    unknownNumbers.set(command, number)
  }
  return { label: `${unknownPrefix}-${number}`, raw: command }
}

// A skin hides raw commands from every tooltip with `:root { --afm-raw-command-tooltip: none; }`: a native
// `title` cannot be hidden by a CSS selector, so the helpers read the variable instead.
export function rawCommandTooltipsEnabled(): boolean {
  return getComputedStyle(document.documentElement).getPropertyValue('--afm-raw-command-tooltip').trim() !== 'none'
}

// executorTooltip — the full display name (long labels are ellipsised in the UI) and, unless a skin hides it,
// the raw command.
export function executorTooltip({ label, raw }: ExecutorLabel): string {
  return raw !== '' && rawCommandTooltipsEnabled() ? `${label}\nCommand: ${raw}` : label
}

// stepTooltip — a Verify step's technical details: its number and, unless hidden, its raw command.
export function stepTooltip(step: number, raw: string): string {
  return raw !== '' && rawCommandTooltipsEnabled() ? `step ${step} · ${raw}` : `step ${step}`
}
