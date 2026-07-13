// Уровень важности записи операционного лога.
export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

// Строка операционного лога выбранной стадии.
// Источник — GET /api/stages/{id}/log: текст формата «HH:MM:SS  TYPE  detail»,
// хук оставляет только text-строки и разбирает их в эту структуру
// (соответствует фильтрации в renderLog текущего app.js).
export type LogEntry = {
  timestamp: string
  message: string
  level: LogLevel
}
