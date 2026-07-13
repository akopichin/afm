// Вопрос в диалоговом канале стадии — с опциями либо ожидающий свободный ответ.
// Источник — GET /api/stages/{id}/dialog (последняя открытая запись с answer=null).
//   id          — идентификатор вопроса (поле id сервера);
//   phase       — фаза диалога (поле phase), для группировки истории;
//   text        — текст вопроса (поле question сервера);
//   options     — предопределённые опции ответа (массив строк сервера); [] — только свободный текст;
//   allowCustom — разрешён ли свободный ответ (поле allow_custom сервера).
export type DialogQuestion = {
  id: string
  phase: string
  text: string
  options: string[]
  allowCustom: boolean
}
