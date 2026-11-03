import { executorLabel, executorTooltip } from '../lib/executor-label'

// Типы фронтенда для профилей помощников (collaboration, Этап 4): источник — GET /api/assistant-profiles
// (pkg/server/assistants_profiles.go's assistantProfileView). Профиль — РОЛЬ (claude-рецепт/инструкция), не
// запущенный экземпляр: `@mention` адресует роль, обращение к уже работающей задаче — по task_id (T4.7).

export type AssistantProfileWire = {
  id: string
  title: string
  command: string
  command_name?: string
  command_source?: string
  runtime_context?: string
  available: boolean
  unavailable_reason?: string
}

export type AssistantProfile = {
  id: string
  title: string
  command: string
  // commandName — the server's readable name of command; absent for an older server (the label falls back).
  commandName?: string
  // commandSource — where the command comes from: "profile", "stage" or "client" (the run's default executor).
  // Optional so a profile built without it (a test fixture, an older server) still renders.
  commandSource?: string
  runtimeContext?: string
  available: boolean
  unavailableReason: string
}

// The server sends title "" for a role-only profile (no `title` in config); every consumer shows `title`
// as the role's name, so the id stands in here, once, instead of in each picker.
export function toAssistantProfile(raw: AssistantProfileWire): AssistantProfile {
  return {
    id: raw.id,
    title: raw.title !== '' ? raw.title : raw.id,
    command: raw.command,
    commandName: raw.command_name,
    commandSource: raw.command_source ?? '',
    runtimeContext: raw.runtime_context ?? '',
    available: raw.available,
    unavailableReason: raw.unavailable_reason ?? '',
  }
}

const COMMAND_SOURCE_LABELS: Record<string, string> = {
  profile: 'profile',
  stage: 'stage',
  client: 'run default',
}

// describeExecutor is the one-line "which executor, decided where" shown for a selected profile.
export function describeExecutor(profile: AssistantProfile, unknownPrefix?: string): string {
  const raw = profile.commandSource ?? ''
  const source = COMMAND_SOURCE_LABELS[raw] ?? raw
  const { label } = executorLabel(profile.command, profile.commandName, unknownPrefix)
  return source === '' ? label : `${label} (${source})`
}

// executorTitle is the tooltip of that line: the full name and, unless the skin hides it, the raw command.
export function executorTitle(profile: AssistantProfile, unknownPrefix?: string): string {
  return executorTooltip(executorLabel(profile.command, profile.commandName, unknownPrefix))
}

// describeUnavailable lists why profiles cannot be used, so an empty role list explains itself.
export function describeUnavailable(profiles: AssistantProfile[]): string {
  return profiles
    .filter((p) => !p.available)
    .map((p) => (p.unavailableReason === '' ? p.title : `${p.title}: ${p.unavailableReason}`))
    .join('; ')
}
