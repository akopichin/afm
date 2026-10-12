// Заметка ревьюера, привязанная к строке/файлу проекта во время flow-wide review-pause.
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
