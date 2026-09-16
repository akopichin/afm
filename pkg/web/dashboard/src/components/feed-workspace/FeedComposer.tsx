import { useState, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'

type FeedComposerProps = {
  // Стадия, живому агенту которой уйдёт заметка. Также key этого компонента в
  // FeedWorkspace — смена стадии даёт свежий пустой черновик (черновик не
  // «переедет» в другую стадию) и инвалидирует зависшие attach-колбэки.
  stageId: string
  // onSend ОБЯЗАН отклоняться (reject) при неудаче доставки — тогда черновик
  // сохраняется. Резолв = заметка ушла, поле очищается.
  onSend: (text: string) => Promise<void>
}

// FeedComposer — messenger-поле ввода заметки живому агенту, прибитое к низу
// ленты. Растущая textarea + Attach (вставка/загрузка картинок, опционально
// файлы проекта в Docker) — всё из общего PasteableTextarea, как в
// AgentNoteModal. Отправка кнопкой ✈ или Cmd/Ctrl+Enter; пустой текст — no-op.
// Доставка идёт через существующий Revise (graceful-пауза + feedback.md) — сам
// вызов задаёт FeedWorkspace/App.
export function FeedComposer({ stageId, onSend }: FeedComposerProps): ReactElement {
  const [value, setValue] = useState('')
  const [sending, setSending] = useState(false)

  const empty = value.trim() === ''

  async function submit(): Promise<void> {
    if (empty || sending) return
    const submitted = value // снимок на момент отправки
    setSending(true)
    try {
      await onSend(submitted.trim())
      // Очищаем ТОЛЬКО если пользователь не набрал новый текст поверх, пока шла
      // отправка (поле остаётся редактируемым) — иначе затрём свежий черновик.
      setValue((cur) => (cur === submitted ? '' : cur))
    } catch {
      // Доставка не удалась (стадия ушла из running → 409, либо сеть) —
      // ОСТАВЛЯЕМ текст, пользователь повторит.
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="feed-composer">
      <PasteableTextarea
        stageId={stageId}
        className="feed-composer-textarea"
        value={value}
        onChange={setValue}
        placeholder="Note to agent…"
        allowFileReferences
        onSubmit={() => void submit()}
        maxHeight={200}
      />
      <button
        type="button"
        className="feed-composer-send"
        aria-label="Send note to agent"
        disabled={empty || sending}
        onClick={() => void submit()}
      >
        <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M22 2 11 13" />
          <path d="M22 2 15 22l-4-9-9-4 20-7Z" />
        </svg>
      </button>
    </div>
  )
}
