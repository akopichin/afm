import { useEffect, useRef, useState, type ReactElement } from 'react'
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
  // --- Pause on focus (весь стейт живёт в usePauseOnFocus у родителя) ---
  // Состояние toggle «Pause on focus» и его переключатель.
  pauseOnFocusEnabled?: boolean
  onTogglePauseOnFocus?: (v: boolean) => void
  // paused — эта стадия сейчас на паузе И поставлена на неё нами (родитель
  // считает status==='paused' && isOwned). Показывает янтарный баннер + тонирует
  // строку ввода/кнопку.
  paused?: boolean
  // Фокус/blur поля ввода: родитель ставит/снимает паузу. onBlur сообщает,
  // остался ли непустой черновик (при нём паузу НЕ снимаем — Send сам возобновит).
  onFocus?: () => void
  onBlur?: (hasDraft: boolean) => void
  // «Resume now» на баннере — снять паузу немедленно, не отправляя заметку.
  onResumeNow?: () => void
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
export function FeedComposer({
  stageId,
  onSend,
  replyQuote = null,
  replyKey,
  onCancelReply,
  onSent,
  pauseOnFocusEnabled = false,
  onTogglePauseOnFocus,
  paused = false,
  onFocus,
  onBlur,
  onResumeNow,
}: FeedComposerProps): ReactElement {
  const [value, setValue] = useState('')
  const [sending, setSending] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)

  const empty = value.trim() === ''

  // Выбор мысли (клик по ↩) — переводим фокус в поле ввода, чтобы Cmd/Ctrl+Enter
  // сразу отправлял ответ, не требуя лишнего клика в textarea (иначе фокус
  // остаётся на кнопке ↩ и хоткей не доходит до обработчика textarea).
  useEffect(() => {
    if (replyKey === undefined) return
    rootRef.current?.querySelector<HTMLTextAreaElement>('.feed-composer-textarea')?.focus()
  }, [replyKey])

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
    <div className="feed-composer" ref={rootRef}>
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
      {/* Полоса опций над строкой ввода: подсказка слева, toggle справа. */}
      {onTogglePauseOnFocus !== undefined && (
        <div className="composer-opts">
          <span className="composer-opts-hint">Pause the agent while you type</span>
          <label className="switch">
            <input
              type="checkbox"
              checked={pauseOnFocusEnabled}
              onChange={(e) => onTogglePauseOnFocus(e.target.checked)}
            />
            <span className="track" aria-hidden="true" />
            <span className="switch-label">Pause on focus</span>
          </label>
        </div>
      )}
      {/* Янтарный баннер: стадия на паузе, снимется на отправке (или «Resume now»). */}
      {paused && (
        <div className="pause-banner" role="status">
          <span className="pause-banner-icon" aria-hidden="true">⏸</span>
          <span className="pause-banner-text">Stage paused. The agent is on hold — it'll resume when you send.</span>
          <button type="button" className="pause-banner-resume" onClick={onResumeNow}>
            Resume now
          </button>
        </div>
      )}
      <div className={`feed-composer-row${paused ? ' focused' : ''}`}>
        <PasteableTextarea
          stageId={stageId}
          className="feed-composer-textarea"
          value={value}
          onChange={setValue}
          placeholder="Note to agent…"
          allowFileReferences
          attachInline
          rows={1}
          onSubmit={() => void submit()}
          onFocus={onFocus}
          onBlur={() => onBlur?.(value.trim() !== '')}
          maxHeight={200}
        />
        <button
          type="button"
          className={`feed-composer-send${paused ? ' paused' : ''}`}
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
