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
  // Ответ на мысль агента: текст цитируемой мысли (null/undefined — обычная
  // свободная заметка, чипа нет). Родитель (FeedWorkspace) деривит его из
  // activeReply, поэтому здесь достаточно строки/null.
  replyQuote?: string | null
  // Ключ цитируемой мысли — возвращается родителю в onSent, чтобы тот сбросил
  // reply-таргет race-safe (только если цель всё ещё та же).
  replyKey?: string
  // ✕ на чипе — отменить ответ (снять цитату). Родитель сбрасывает reply-таргет.
  onCancelReply?: () => void
  // Успешная доставка ответа: сообщаем родителю ключ отправленной мысли, чтобы
  // он сбросил reply-таргет ТОЛЬКО если он всё ещё указывает на неё (race-safe).
  onSent?: (key: string) => void
}

// buildReplyBody собирает тело Revise для ответа на мысль: цитата целиком как
// Markdown-blockquote (каждая строка с префиксом `> `, включая пустые и уже
// заквоченные/фенсовые), затем пустая строка и комментарий пользователя. Без
// цитаты (quote === null) — просто триммированный комментарий (сегодняшнее
// поведение свободной заметки). CRLF нормализуется в LF ДО префиксинга, иначе
// заквоченная строка получила бы хвостовой `\r`.
export function buildReplyBody(quote: string | null, comment: string): string {
  const c = comment.trim()
  if (quote === null) return c
  const q = quote
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((l) => `> ${l}`)
    .join('\n')
  return `${q}\n\n${c}`
}

// FeedComposer — messenger-поле ввода заметки живому агенту, прибитое к низу
// ленты. Растущая textarea + Attach (вставка/загрузка картинок, опционально
// файлы проекта в Docker) — всё из общего PasteableTextarea, как в
// AgentNoteModal. Отправка кнопкой ✈ или Cmd/Ctrl+Enter; пустой текст — no-op.
// Доставка идёт через существующий Revise (graceful-пауза + feedback.md) — сам
// вызов задаёт FeedWorkspace/App.
export function FeedComposer({ stageId, onSend, replyQuote = null, replyKey, onCancelReply, onSent }: FeedComposerProps): ReactElement {
  const [value, setValue] = useState('')
  const [sending, setSending] = useState(false)

  const empty = value.trim() === ''

  async function submit(): Promise<void> {
    if (empty || sending) return
    const submitted = value // снимок на момент отправки
    const body = buildReplyBody(replyQuote, submitted)
    setSending(true)
    try {
      await onSend(body)
      // Очищаем ТОЛЬКО если пользователь не набрал новый текст поверх, пока шла
      // отправка (поле остаётся редактируемым) — иначе затрём свежий черновик.
      // Снимок сравниваем с сырым value (submitted), не с body.
      setValue((cur) => (cur === submitted ? '' : cur))
      // Сообщаем родителю ключ отправленной мысли — он сбросит reply-таргет
      // только если цель всё ещё та же (race-safe, см. FeedWorkspace).
      if (replyKey !== undefined) onSent?.(replyKey)
    } catch {
      // Доставка не удалась (стадия ушла из running → 409, либо сеть) —
      // ОСТАВЛЯЕМ текст и чип (родитель не сбрасывается без onSent), пользователь
      // повторит.
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="feed-composer">
      {/* Quote-чип ответа на мысль агента — над строкой ввода, в стиле
          line-comment цитаты (левый акцент + приглушённый исходник, кламп 2
          строки). ✕ снимает цитату. */}
      {replyQuote !== null && (
        <div className="feed-reply-chip">
          <span className="feed-reply-chip-label">↩ In reply to</span>
          <span className="feed-reply-chip-src">{replyQuote}</span>
          <button
            type="button"
            className="feed-reply-chip-cancel"
            aria-label="Cancel reply to thought"
            onClick={onCancelReply}
          >
            ✕
          </button>
        </div>
      )}
      <div className="feed-composer-row">
        <PasteableTextarea
          stageId={stageId}
          className="feed-composer-textarea"
          value={value}
          onChange={setValue}
          placeholder="Note to agent…"
          allowFileReferences
          attachInline
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
    </div>
  )
}
