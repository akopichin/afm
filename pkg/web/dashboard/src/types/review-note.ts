// Заметка ревьюера, привязанная к строке/файлу проекта во время flow-wide
// review-pause (см. pkg/state.ReviewNote). Поля 1:1 соответствуют JSON-виду
// бэкенда — камелкейс не нужен, объект идёт напрямую в fetch-тело и обратно
// без промежуточного маппинга (в отличие от Stage/FlowStatus, где normalizeStatus
// приводит snake_case к camelCase для остального дашборда).
export type ReviewNote = {
  id: string
  root: string
  path: string
  display_path: string
  reference: string
  line: number | null
  orig_line_text: string | null
  content_sha: string
  text: string
  created_at: string
}
