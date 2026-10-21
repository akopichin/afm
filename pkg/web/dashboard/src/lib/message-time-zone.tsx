import { createContext, useContext, type ReactElement, type ReactNode } from 'react'

// Часовой пояс ХОСТА afm для отображения времени сообщений (лента, Team, история бокового агента).
// Источник — /api/status (time_zone/utc_offset_minutes); браузерная зона используется только как
// fallback для старого бэкенда, который не отдаёт ни одного из полей.
export type MessageTimeZone = {
  // IANA-имя зоны хоста; '' когда хост не смог его определить — тогда часы считаются по offset.
  timeZone: string
  // Подписанные минуты восточнее UTC — снапшот-fallback без учёта исторического DST.
  utcOffsetMinutes: number
}

// Browser-local значение: дефолт контекста вне провайдера и fallback для старого бэкенда.
function browserLocalZone(): MessageTimeZone {
  return {
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
  }
}

const MessageTimeZoneContext = createContext<MessageTimeZone | null>(null)

export function useMessageTimeZone(): MessageTimeZone {
  return useContext(MessageTimeZoneContext) ?? browserLocalZone()
}

// Монтируется в App снаружи SideAgentProvider, чтобы все рендереры сообщений видели одну и ту же зону.
export function MessageTimeZoneProvider({ timeZone, utcOffsetMinutes, children }: {
  timeZone: string
  // null — старый бэкенд без utc_offset_minutes (см. normalizeStatus).
  utcOffsetMinutes: number | null
  children: ReactNode
}): ReactElement {
  // Пустое имя при валидном offset — НЕ повод брать browser-зону: offset пришёл с хоста и задаёт часы.
  const value = timeZone === '' && utcOffsetMinutes === null
    ? browserLocalZone()
    : { timeZone, utcOffsetMinutes: utcOffsetMinutes ?? 0 }
  return <MessageTimeZoneContext.Provider value={value}>{children}</MessageTimeZoneContext.Provider>
}
