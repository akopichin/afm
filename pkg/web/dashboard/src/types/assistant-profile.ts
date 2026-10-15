// Типы фронтенда для профилей помощников (collaboration, Этап 4): источник — GET /api/assistant-profiles
// (pkg/server/assistants_profiles.go's assistantProfileView). Профиль — РОЛЬ (claude-рецепт/инструкция), не
// запущенный экземпляр: `@mention` адресует роль, обращение к уже работающей задаче — по task_id (T4.7).

export type AssistantProfileWire = {
  id: string
  title: string
  command: string
  available: boolean
  unavailable_reason?: string
}

export type AssistantProfile = {
  id: string
  title: string
  command: string
  available: boolean
  unavailableReason: string
}

export function toAssistantProfile(raw: AssistantProfileWire): AssistantProfile {
  return {
    id: raw.id,
    title: raw.title,
    command: raw.command,
    available: raw.available,
    unavailableReason: raw.unavailable_reason ?? '',
  }
}
