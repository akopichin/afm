import { useCallback, useRef, useState, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { AssistantMentionMenu } from '../assistants/AssistantMentionMenu'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { detectMentionTrigger, filterMentionProfiles, type MentionTrigger } from '../assistants/mention-match'
import type { AssistantProfile } from '../../types/assistant-profile'

const MAX_TEXTAREA_HEIGHT = 200

export type SideAgentComposerProps = {
  draft: string
  onDraftChange: (value: string) => void
  // Отправить текущий черновик. Вызывается только при непустом (trim) тексте и
  // когда агент не занят (running → поле редактируемо, но Send недоступен).
  onSend: () => void
  // Прервать активный turn (кнопка Stop показывается только во время running).
  onStop: () => void
  running: boolean
  // quote — приложенные file-comments (file-browser "Without pause" → "Send to side agent"). Чип
  // рендерится только когда не null; сама цитата не делает сообщение отправляемым — draft всё равно
  // должен быть непустым.
  quote?: string | null
  // onCancelQuote — снять приложенную цитату (✕ на чипе).
  onCancelQuote?: () => void
  // assistantProfiles — роли, доступные для @mention (T4.2). Пустой список (в т.ч. когда collaboration
  // capability выключена — вызывающий просто не грузит профили) делает меню недостижимым: filterMentionProfiles
  // на пустом массиве всегда отдаёт [], так что AssistantMentionMenu никогда не рендерится — отдельного
  // флага "выключено" не нужно (TestMention_HiddenWhenCapabilityOff).
  assistantProfiles?: AssistantProfile[]
}

// SideAgentComposer — свой композер беседы. Поле редактируемо и во время работы агента; отправка
// следующего сообщения возможна только когда текущий turn завершён (Send disabled при running).
export function SideAgentComposer({
  draft,
  onDraftChange,
  onSend,
  onStop,
  running,
  quote = null,
  onCancelQuote,
  assistantProfiles = [],
}: SideAgentComposerProps): ReactElement {
  const autoGrowRef = useAutoGrowTextarea(draft, MAX_TEXTAREA_HEIGHT)
  const { nodeRef: caretNodeRef, replaceRange } = useCaretInsert(draft, onDraftChange)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const setRefs = useCallback(
    (node: HTMLTextAreaElement | null) => {
      textareaRef.current = node
      caretNodeRef.current = node
      autoGrowRef(node)
    },
    [autoGrowRef, caretNodeRef],
  )

  const [mention, setMention] = useState<MentionTrigger | null>(null)
  const [highlightIndex, setHighlightIndex] = useState(0)
  const matches = mention !== null ? filterMentionProfiles(assistantProfiles, mention.query) : []
  const menuOpen = matches.length > 0

  const canSend = !running && draft.trim() !== ''

  function submit(): void {
    if (canSend) onSend()
  }

  function selectMention(profile: AssistantProfile): void {
    if (mention === null) return
    const end = mention.start + 1 + mention.query.length // "@" + typed query so far
    replaceRange(mention.start, end, `@${profile.id} `)
    setMention(null)
  }

  function handleChange(e: ChangeEvent<HTMLTextAreaElement>): void {
    const value = e.target.value
    onDraftChange(value)
    const caret = e.target.selectionStart ?? value.length
    setMention(detectMentionTrigger(value, caret))
    setHighlightIndex(0)
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    if (menuOpen) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setHighlightIndex((i) => (i + 1) % matches.length)
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setHighlightIndex((i) => (i - 1 + matches.length) % matches.length)
        return
      }
      if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
        e.preventDefault()
        const profile = matches[highlightIndex]
        if (profile !== undefined) selectMention(profile)
        return
      }
      if (e.key === 'Escape') {
        // Первый Escape закрывает только список подсказок и не должен всплыть дальше — иначе он закрыл бы и
        // модалку беседы разом (TestMention_EscapeClosesSuggestionsThenModal). Второй Escape (список уже
        // закрыт) до этой ветки не доходит и свободно всплывает к обработчику модалки.
        e.preventDefault()
        e.stopPropagation()
        setMention(null)
        return
      }
    }

    // Cmd/Ctrl+Enter — отправка; обычный Enter оставляем textarea (новая строка).
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      e.preventDefault()
      submit()
    }
  }

  return (
    <div className="side-agent-composer">
      {quote !== null && (
        <div className="side-agent-quote-chip">
          <span className="side-agent-quote-chip-label">↩ File comments</span>
          <button
            type="button"
            className="side-agent-quote-chip-cancel"
            aria-label="Cancel attached file comments"
            onClick={onCancelQuote}
          >
            ✕
          </button>
          <pre className="side-agent-quote-chip-body">{quote}</pre>
        </div>
      )}
      <div className="chat-composer-row">
        <textarea
          ref={setRefs}
          className="chat-composer-input"
          value={draft}
          onChange={handleChange}
          onKeyDown={onKeyDown}
          placeholder="Message the agent…  (Cmd/Ctrl+Enter to send, @ to mention a role)"
          aria-label="Message the agent"
          rows={1}
        />
        {menuOpen && (
          <AssistantMentionMenu
            profiles={matches}
            highlightIndex={highlightIndex}
            onHover={setHighlightIndex}
            onSelect={selectMention}
          />
        )}
        {running && (
          <button type="button" className="chat-stop" aria-label="Stop" title="Stop" onClick={onStop}>
            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
              <rect x="5" y="5" width="14" height="14" rx="2" fill="currentColor" />
            </svg>
          </button>
        )}
        <ChatSendButton label="Send" disabled={!canSend} onClick={submit} />
      </div>
    </div>
  )
}
