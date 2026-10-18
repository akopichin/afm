import { useCallback, useRef, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { AssistantMentionMenu } from '../assistants/AssistantMentionMenu'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { filterMentionProfiles } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
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

  const mention = useMentionMenu<AssistantProfile>({
    matchesFor: (query) => filterMentionProfiles(assistantProfiles, query),
    textFor: (profile) => profile.id,
    replaceRange,
  })

  const canSend = !running && draft.trim() !== ''

  function submit(): void {
    if (canSend) onSend()
  }

  function handleChange(e: ChangeEvent<HTMLTextAreaElement>): void {
    const value = e.target.value
    onDraftChange(value)
    mention.onInput(value, e.target.selectionStart ?? value.length)
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    // The first Escape only closes the suggestions (the hook stops it from bubbling), so it does not close
    // the conversation modal too (TestMention_EscapeClosesSuggestionsThenModal); the second one bubbles.
    if (mention.onKeyDown(e)) return
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
        {mention.open && (
          <AssistantMentionMenu
            profiles={mention.matches}
            highlightIndex={mention.highlightIndex}
            onHover={mention.setHighlightIndex}
            onSelect={mention.pick}
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
