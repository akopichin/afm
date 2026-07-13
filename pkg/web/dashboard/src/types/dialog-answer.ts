// Ответ пользователя на вопрос диалогового канала — выбор опции или свободный текст.
// Отправляется POST /api/stages/{id}/dialog/answer.
export type DialogAnswer = {
  questionId: string
  value: string
}
