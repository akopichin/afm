// Вопрос в диалоговом канале стадии — с опциями либо ожидающий свободный ответ. Источник — GET
// /api/stages/{id}/dialog (последняя открытая запись с answer=null).
export type DialogQuestion = {
  id: string
  phase: string
  text: string
  options: string[]
  allowCustom: boolean
}
