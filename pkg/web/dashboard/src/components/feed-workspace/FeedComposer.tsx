import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { buildReplyBody } from '../../lib/quote'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { useMeshThread, type MeshRoster } from '../../hooks/use-mesh'
import { MentionMenu, mentionText } from '../assistants/AssistantMentionMenu'
import { filterMentionGroups } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { AttachAssistantDialog } from '../assistants'
import {
  genMeshRequestId,
  openMeshThread,
  sendMeshMessage,
  type MeshAttachAndSendResult,
  type MeshMessage,
} from '../../api/mesh-client'
import { buildMeshMentionGroups, type MeshMentionItem, type MeshMentionTarget } from './mesh-recipients'
import { findExistingDirectThread, senderLabel } from '../../lib/mesh-roster'
import { computeReceiptPhase, RECEIPT_LABEL } from './mesh-receipt'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { Stage } from '../../types'

// FeedComposerMeshProps (Agent Mesh Phase 7, T7.4) — when provided, the composer gains a Message/Revise mode
// toggle; the Message tab addresses the shared mesh (participants + attach-to-stage profiles) instead of the
// EXISTING note-to-agent path below. When omitted (every call site before T7.4, and every pre-existing test),
// the composer behaves EXACTLY as before: no tabs, the single Revise-equivalent note field, unchanged DOM.
export type FeedComposerMeshProps = {
  // roster — the SAME useMeshRoster poll App.tsx already owns, passed down (no second poller here).
  roster: MeshRoster
  // profiles — configured helper roles, for the "Attach to stage" mention group. Same useAssistantProfiles
  // poll App.tsx already owns.
  profiles: AssistantProfile[]
  // stages — for looking up a mentioned participant's OWN stage status (Paused · waiting for Continue).
  stages: Stage[]
}

type ComposerMode = 'message' | 'revise'

type FeedComposerProps = {
  // Стадия, живому агенту которой уйдёт заметка.
  stageId: string
  // onSend ОБЯЗАН отклоняться (reject) при неудаче доставки — тогда черновик
  // сохраняется. Резолв = заметка ушла, поле очищается.
  onSend: (text: string) => Promise<void>
  // Ответ на мысль агента: текст цитируемой мысли. Родитель (FeedWorkspace) деривит его из activeReply,
  // поэтому здесь достаточно строки/null.
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
  // paused — эта стадия сейчас на паузе И поставлена на неё нами (родитель считает status==='paused' &&
  // isOwned). Показывает янтарный баннер + тонирует строку ввода/кнопку.
  paused?: boolean
  // Фокус/blur поля ввода: родитель ставит/снимает паузу. onBlur сообщает,
  // остался ли непустой черновик (при нём паузу НЕ снимаем — Send сам возобновит).
  onFocus?: () => void
  onBlur?: (hasDraft: boolean) => void
  // «Resume now» на баннере — снять паузу немедленно, не отправляя заметку.
  onResumeNow?: () => void
  // mesh (T7.4) — см. FeedComposerMeshProps. undefined → ЛЕГАСИ-поведение, один режим, без табов.
  mesh?: FeedComposerMeshProps
}

// FeedComposer — messenger-поле ввода внизу ленты. Без mesh — ровно сегодняшнее поведение: заметка живому
// агенту идёт через существующий Revise (graceful-пауза + feedback.md). С mesh (T7.4) добавляется отдельный
// режим Message, адресующий общий mesh напрямую — это ДРУГОЙ путь доставки, Revise он не трогает.
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
  mesh,
}: FeedComposerProps): ReactElement {
  const [mode, setMode] = useState<ComposerMode>('revise')
  const [value, setValue] = useState('')
  const [sending, setSending] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)

  const empty = value.trim() === ''

  // Выбор мысли — переводим фокус в поле ввода, чтобы Cmd/Ctrl+Enter сразу отправлял ответ, не требуя
  // лишнего клика в textarea.
  useEffect(() => {
    if (replyKey === undefined) return
    rootRef.current?.querySelector<HTMLTextAreaElement>('.chat-composer-input')?.focus()
  }, [replyKey])

  async function submit(): Promise<void> {
    if (empty || sending) return
    const submitted = value // снимок на момент отправки
    const body = buildReplyBody(replyQuote, submitted)
    setSending(true)
    try {
      await onSend(body)
      // Очищаем ТОЛЬКО если пользователь не набрал новый текст поверх, пока шла отправка — иначе затрём
      // свежий черновик. Снимок сравниваем с сырым value (submitted), не с body.
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
      {mesh !== undefined && (
        <div className="feed-composer-modes" role="tablist" aria-label="Composer mode">
          <button
            type="button"
            role="tab"
            aria-selected={mode === 'message'}
            className={`feed-composer-mode${mode === 'message' ? ' active' : ''}`}
            onClick={() => setMode('message')}
          >
            Message
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === 'revise'}
            className={`feed-composer-mode${mode === 'revise' ? ' active' : ''}`}
            onClick={() => setMode('revise')}
          >
            Revise
          </button>
        </div>
      )}

      {mesh !== undefined && mode === 'message' ? (
        <MeshMessageComposer stageId={stageId} mesh={mesh} />
      ) : (
        <>
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
          {/* Полоса опций над строкой ввода: тумблер с подписью, прижат вправо. */}
          {onTogglePauseOnFocus !== undefined && (
            <div className="composer-opts">
              <label className="switch">
                <input
                  type="checkbox"
                  checked={pauseOnFocusEnabled}
                  onChange={(e) => onTogglePauseOnFocus(e.target.checked)}
                />
                <span className="track" aria-hidden="true" />
                <span className="switch-label">Hold the agent while you type</span>
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
          <div className={`chat-composer-row${paused ? ' paused' : ''}`}>
            <PasteableTextarea
              stageId={stageId}
              className="chat-composer-input"
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
            <ChatSendButton
              label={mesh !== undefined ? 'Apply Revise' : 'Send note to agent'}
              disabled={empty || sending}
              paused={paused}
              onClick={() => void submit()}
            />
          </div>
        </>
      )}
    </div>
  )
}

const MAX_MESSAGE_TEXTAREA_HEIGHT = 200

type MeshMessageComposerProps = {
  stageId: string
  mesh: FeedComposerMeshProps
}

// targetKey — a stable string identity for a MeshMentionTarget, used as a dependency/reset key: switching
// target must drop whatever thread/reply state the PREVIOUS target had, even though 'participant'/'profile'
// carry different fields.
function targetKey(target: MeshMentionTarget | null): string {
  if (target === null) return ''
  return target.kind === 'participant' ? `participant:${target.id}` : `profile:${target.profileId}`
}

// MeshMessageComposer — Message mode's own subtree (Agent Mesh Phase 7, T7.4): `@`-mention a recipient
// (reusing the shared MentionMenu popup — mesh-recipients.ts's grouped items, not a second filtering scheme),
// then Send addresses the mesh directly: an existing participant's thread (opening a NEW one the first time,
// per findExistingDirectThread's own "never widen a private thread" rule) or, for an "Attach to stage" pick,
// the T6.2 attachAndSendMesh composite. Free text here NEVER reaches the Revise/onSend path above — the two
// modes share a stageId and nothing else.
function MeshMessageComposer({ stageId, mesh }: MeshMessageComposerProps): ReactElement {
  const [draft, setDraft] = useState('')
  const [target, setTarget] = useState<MeshMentionTarget | null>(null)
  const [openedThreadId, setOpenedThreadId] = useState<string | null>(null)
  const [replyTo, setReplyTo] = useState<{ messageId: string; preview: string } | null>(null)
  const [sendError, setSendError] = useState<string | null>(null)
  // attachPreview — an "Attach to stage" pick is never sent straight away (T7.5): Send opens this preview
  // instead, and only the dialog's OWN "Attach and send" click actually calls attachAndSendMesh. Cancel clears
  // this with zero requests and leaves `draft` untouched so the user can edit and retry.
  const [attachPreview, setAttachPreview] = useState<{ profileId: string; instruction: string } | null>(null)
  // lastSend — the most recent send's receipt-tracking state: set (messageId: null) the instant Send fires,
  // filled in once the request resolves. null participantId before resolution (an "Attach to stage" pick only
  // learns the real participant id from the response) is fine — computeReceiptPhase's inFlight branch never
  // looks at it.
  const [lastSend, setLastSend] = useState<{ participantId: string; threadId: string; messageId: string | null } | null>(null)
  const [sending, setSending] = useState(false)


  const autoGrowRef = useAutoGrowTextarea(draft, MAX_MESSAGE_TEXTAREA_HEIGHT)
  const { nodeRef: caretNodeRef, replaceRange } = useCaretInsert(draft, setDraft)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const setRefs = useCallback(
    (node: HTMLTextAreaElement | null) => {
      textareaRef.current = node
      caretNodeRef.current = node
      autoGrowRef(node)
    },
    [autoGrowRef, caretNodeRef],
  )

  const groups = useMemo(
    () => buildMeshMentionGroups(mesh.roster.participants, mesh.profiles, stageId),
    [mesh.roster.participants, mesh.profiles, stageId],
  )
  const mention = useMentionMenu<MeshMentionItem>({
    matchesFor: (query) => filterMentionGroups(groups, query).flatMap((g) => g.items),
    textFor: mentionText,
    replaceRange,
    onPicked: (item) => setTarget(item.target),
  })
  const matchGroups = mention.query !== null ? filterMentionGroups(groups, mention.query) : []

  // Switching target drops whatever thread/reply context the previous one carried — addressing someone new
  // never inherits an unrelated open conversation.
  const key = targetKey(target)
  useEffect(() => {
    if (target?.kind === 'participant') {
      setOpenedThreadId(findExistingDirectThread(mesh.roster.threads, target.id)?.id ?? null)
    } else {
      setOpenedThreadId(null)
    }
    setReplyTo(null)
    setSendError(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- key IS the target identity; mesh.roster.threads
    // intentionally excluded so a later poll tick doesn't silently re-pick a thread mid-compose.
  }, [key])

  const threadMessages = useMeshThread(openedThreadId)

  function handleChange(e: ChangeEvent<HTMLTextAreaElement>): void {
    const v = e.target.value
    setDraft(v)
    mention.onInput(v, e.target.selectionStart ?? v.length)
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    if (mention.onKeyDown(e)) return
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      e.preventDefault()
      void submit()
    }
  }

  async function submit(): Promise<void> {
    const text = draft.trim()
    if (text === '' || sending || target === null) return
    // "Attach to stage" never sends straight away — Send opens the preview dialog below instead, and only
    // its OWN confirm click (handleAttachConfirmed) issues the attachAndSendMesh request. No network call,
    // no roster/feed mutation, happens on this path.
    if (target.kind === 'profile') {
      setAttachPreview({ profileId: target.profileId, instruction: text })
      return
    }
    setSending(true)
    setSendError(null)
    try {
      // Re-check at send time: the roster poll may have learned about a direct thread after the mention was picked.
      let threadId = openedThreadId ?? findExistingDirectThread(mesh.roster.threads, target.id)?.id ?? null
      setLastSend({ participantId: target.id, threadId: threadId ?? '', messageId: null })
      if (threadId === null) {
        const thread = await openMeshThread({
          requestId: genMeshRequestId('mesh-feed-open'),
          title: '',
          scope: target.scope,
          memberIds: [target.id],
        })
        threadId = thread.id
      }
      setOpenedThreadId(threadId)
      const receipt = await sendMeshMessage(threadId, {
        requestId: genMeshRequestId('mesh-feed-send'),
        recipientIds: [target.id],
        inReplyTo: replyTo?.messageId,
        body: text,
      })
      setLastSend({ participantId: target.id, threadId, messageId: receipt.message_id })
      setDraft('')
      setReplyTo(null)
    } catch (err) {
      setSendError(err instanceof Error ? err.message : 'request failed')
      setLastSend(null)
    } finally {
      setSending(false)
    }
  }

  // handleAttachConfirmed — the attach preview's "Attach and send" resolved: this is the ONLY place the
  // composite's result is applied, so nothing optimistic ever lands in the roster/feed before a real success.
  function handleAttachConfirmed(result: MeshAttachAndSendResult): void {
    setAttachPreview(null)
    setOpenedThreadId(result.thread_id)
    setLastSend({ participantId: result.participant_id, threadId: result.thread_id, messageId: result.message_id ?? null })
    setDraft('')
    setReplyTo(null)
  }

  const liveParticipant = lastSend === null ? undefined : mesh.roster.participants.find((p) => p.id === lastSend.participantId)
  const stagePaused =
    liveParticipant?.stage_id !== undefined && mesh.stages.find((s) => s.id === liveParticipant.stage_id)?.status === 'paused'
  const answered = lastSend !== null && lastSend.messageId !== null && threadMessages.some((m) => m.in_reply_to === lastSend.messageId)
  const receiptPhase =
    lastSend === null
      ? null
      : computeReceiptPhase({
          participant: liveParticipant,
          stagePaused,
          answered,
          inFlight: lastSend.messageId === null,
        })

  const empty = draft.trim() === '' || target === null

  return (
    <div className="mesh-message-composer">
      {attachPreview !== null && (
        <AttachAssistantDialog
          profileId={attachPreview.profileId}
          stageId={stageId}
          instruction={attachPreview.instruction}
          profiles={mesh.profiles}
          stages={mesh.stages.map((s) => ({ id: s.id, title: s.name }))}
          onCancel={() => setAttachPreview(null)}
          onConfirmed={handleAttachConfirmed}
        />
      )}
      {openedThreadId !== null && threadMessages.length > 0 && (
        <ul className="mesh-message-thread" aria-label="Conversation">
          {threadMessages.map((m: MeshMessage) => (
            <li key={m.id} className="mesh-message-thread-item">
              <span className="mesh-message-thread-sender">{senderLabel(m.sender_id, mesh.roster.participants)}</span>
              <span className="mesh-message-thread-body">{m.body}</span>
              <button
                type="button"
                className="mesh-message-thread-reply"
                onClick={() => setReplyTo({ messageId: m.id, preview: m.body.slice(0, 120) })}
              >
                Reply
              </button>
            </li>
          ))}
        </ul>
      )}

      {replyTo !== null && (
        <div className="feed-reply-chip">
          <span className="feed-reply-chip-label">↩ Reply</span>
          <span className="feed-reply-chip-src">{replyTo.preview}</span>
          <button type="button" className="feed-reply-chip-cancel" aria-label="Cancel reply to message" onClick={() => setReplyTo(null)}>
            ✕
          </button>
        </div>
      )}

      <div className="chat-composer-row">
        <textarea
          ref={setRefs}
          className="chat-composer-input"
          value={draft}
          onChange={handleChange}
          onKeyDown={onKeyDown}
          placeholder="Message someone… (@ to mention)"
          aria-label="Message the mesh"
          rows={1}
        />
        {mention.open && (
          <MentionMenu
            groups={matchGroups}
            highlightIndex={mention.highlightIndex}
            onHover={mention.setHighlightIndex}
            onSelect={mention.pick}
            ariaLabel="Mention a mesh participant or profile"
          />
        )}
        <ChatSendButton label="Send message" disabled={empty || sending} onClick={() => void submit()} />
      </div>

      {sendError !== null && (
        <div className="mesh-message-error" role="alert">
          {sendError}
        </div>
      )}
      {receiptPhase !== null && (
        <div className="mesh-message-receipt" role="status">
          {RECEIPT_LABEL[receiptPhase]}
        </div>
      )}
    </div>
  )
}
