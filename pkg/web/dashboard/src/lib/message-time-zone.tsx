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

export type HostTimeText = {
  // «17:02» — для строк, где день ясен из контекста (лента).
  clock: string
  // «Oct 23, 17:02» — абсолютное время, понятное и через сутки (ожидание лимита).
  short: string
  // «Oct 23, 2026, 17:02:06» — полная подпись для title/aria.
  full: string
}

function formatInZone(date: Date, timeZone: string): HostTimeText {
  const clockOptions = { timeZone, hourCycle: 'h23', hour: '2-digit', minute: '2-digit' } as const
  const format = (options: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat('en-US', { ...clockOptions, ...options }).format(date)
  return {
    clock: format({}),
    short: format({ month: 'short', day: 'numeric' }),
    full: format({ month: 'short', day: 'numeric', year: 'numeric', second: '2-digit' }),
  }
}

// formatHostTime форматирует момент в зоне хоста. IANA-имя остаётся источником истины для исторического DST;
// offset сервера — только fallback, когда у хоста нет IANA-имени или браузер не знает это имя.
export function formatHostTime(date: Date, { timeZone, utcOffsetMinutes }: MessageTimeZone): HostTimeText {
  if (timeZone !== '') {
    try {
      return formatInZone(date, timeZone)
    } catch (error) {
      if (!(error instanceof RangeError)) throw error
    }
  }
  return formatInZone(new Date(date.getTime() + utcOffsetMinutes * 60_000), 'UTC')
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
