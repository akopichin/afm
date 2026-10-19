import { useCallback, useEffect, useId, useMemo, useRef, useState, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { buildReplyBody } from '../../lib/quote'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { useMeshThread, type MeshRoster } from '../../hooks/use-mesh'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { MentionMenu, mentionText } from '../assistants/AssistantMentionMenu'
import { draftMentions, filterMentionGroups } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { AttachAssistantDialog } from '../assistants'
import { TabList, tabPanelProps } from '../tab-list'
import {
  genMeshRequestId,
  openMeshThread,
  sendMeshMessage,
  type MeshAttachAndSendResult,
  type MeshMessage,
  type MeshThread,
} from '../../api/mesh-client'
import { buildMeshMentionGroups, participantMentionItem, type MeshMentionItem, type MeshMentionTarget } from './mesh-recipients'
import { findExistingDirectThread, senderLabel } from '../../lib/mesh-roster'
import { messageText } from '../../lib/agent-result'
import { AgentMessageBody } from '../agent-message/AgentMessageBody'
import { computeReceiptPhase, RECEIPT_LABEL, type ReceiptPhase } from './mesh-receipt'
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
  // localThreads — direct threads Message mode opened that the roster poll has not reported yet. Kept here
  // (not in MeshMessageComposer) so switching to Revise and back does not forget them and open a duplicate.
  const [localThreads, setLocalThreads] = useState<MeshThread[]>([])
  const [value, setValue] = useState('')
  const [sending, setSending] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const tabsId = useId()

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

  // reviseComposer — the note-to-agent field: the whole composer without mesh, the Revise tab with it.
  const reviseComposer = (
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
          disabled={empty}
          sending={sending}
          paused={paused}
          onClick={() => void submit()}
        />
      </div>
    </>
  )

  return (
    <div className="feed-composer" ref={rootRef}>
      {mesh !== undefined && (
        <TabList
          idPrefix={tabsId}
          ariaLabel="Composer mode"
          className="feed-composer-modes"
          tabClassName="feed-composer-mode"
          selected={mode}
          onSelect={setMode}
          tabs={[
            { id: 'message', label: 'Message' },
            { id: 'revise', label: 'Revise' },
          ]}
        />
      )}

      {mesh !== undefined ? (
        <div className="feed-composer-panel" {...tabPanelProps(tabsId, mode)}>
          {mode === 'message' ? (
            <MeshMessageComposer
              stageId={stageId}
              mesh={mesh}
              knownThreads={[...mesh.roster.threads, ...localThreads.filter((l) => !mesh.roster.threads.some((t) => t.id === l.id))]}
              onThreadCreated={(thread) => setLocalThreads((prev) => (prev.some((t) => t.id === thread.id) ? prev : [...prev, thread]))}
            />
          ) : (
            reviseComposer
          )}
        </div>
      ) : (
        reviseComposer
      )}
    </div>
  )
}

const MAX_MESSAGE_TEXTAREA_HEIGHT = 200

type MeshMessageComposerProps = {
  stageId: string
  mesh: FeedComposerMeshProps
  // knownThreads — the roster's threads plus ones opened here that the poll has not reported yet.
  knownThreads: MeshThread[]
  onThreadCreated: (thread: MeshThread) => void
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
function MeshMessageComposer({ stageId, mesh, knownThreads, onThreadCreated }: MeshMessageComposerProps): ReactElement {
  const [draft, setDraft] = useState('')
  const [target, setTarget] = useState<MeshMentionTarget | null>(null)
  // targetHandle — the `@handle` that picked target; deleting it from the draft un-addresses the target.
  const [targetHandle, setTargetHandle] = useState('')
  // openRequestIdRef — one id for opening the target's thread, reused by retries: a lost response must not
  // open a second thread. A new target gets a new id.
  const openRequestIdRef = useRef(genMeshRequestId('mesh-feed-open'))
  // openedThreadId — the conversation shown above the input. It is sticky: erasing the @mention un-addresses
  // the recipient but keeps the conversation in view; only picking someone else or its × replaces/closes it.
  const [openedThreadId, setOpenedThreadId] = useState<string | null>(null)
  // addressedRef — the last recipient addressed (targetKey), which the shown conversation belongs to.
  const addressedRef = useRef('')
  const [replyTo, setReplyTo] = useState<{ messageId: string; preview: string } | null>(null)
  // attachPreview — an "Attach to stage" pick is never sent straight away (T7.5): Send opens this preview
  // instead, and only the dialog's OWN "Attach and send" click actually calls attachAndSendMesh. Cancel clears
  // this with zero requests and leaves `draft` untouched so the user can edit and retry.
  const [attachPreview, setAttachPreview] = useState<{ profileId: string; instruction: string } | null>(null)
  // lastSend — the most recent successful send, for its receipt. messageId is null when an attach-and-send
  // result carried no message id yet.
  const [lastSend, setLastSend] = useState<{ participantId: string; threadId: string; messageId: string | null } | null>(null)
  // submitter — a retry of the same message reuses its request id. Fenced by the target: a send that
  // finishes after another recipient was picked neither clears the new draft nor writes its old thread id.
  const key = targetKey(target)
  const submitter = useRetryableSubmit<{ recipientId: string; inReplyTo?: string; body: string }>('mesh-feed-send', key)

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
    onPicked: (item) => {
      if (targetKey(item.target) !== addressedRef.current) addressNewTarget(item.target)
      setTarget(item.target)
      setTargetHandle(mentionText(item))
    },
  })
  const matchGroups = mention.query !== null ? filterMentionGroups(groups, mention.query) : []

  // addressNewTarget — picking someone new drops whatever thread/reply context the previous conversation
  // carried: addressing someone never inherits an unrelated open conversation. Re-picking the same recipient
  // after erasing its mention keeps the conversation and the reply.
  function addressNewTarget(next: MeshMentionTarget): void {
    addressedRef.current = targetKey(next)
    setOpenedThreadId(next.kind === 'participant' ? (findExistingDirectThread(knownThreads, next.id)?.id ?? null) : null)
    setReplyTo(null)
    openRequestIdRef.current = genMeshRequestId('mesh-feed-open')
  }

  // replyToMessage — Reply on a message of the shown conversation: answer it there, addressed back to its
  // sender, whose @handle goes into the draft (so the user never re-types the mention).
  function replyToMessage(m: MeshMessage): void {
    setReplyTo({ messageId: m.id, preview: messageText(m.body, m.sender_id !== 'user').slice(0, 120) })
    const sender = participantMentionItem(mesh.roster.participants, m.sender_id)
    if (sender === null) return
    const handle = mentionText(sender)
    addressedRef.current = targetKey(sender.target)
    setTarget(sender.target)
    setTargetHandle(handle)
    setDraft((cur) => (draftMentions(cur, handle) ? cur : `@${handle} ${cur}`))
    textareaRef.current?.focus()
  }

  function closeConversation(): void {
    setOpenedThreadId(null)
    setReplyTo(null)
  }

  const threadMessages = useMeshThread(openedThreadId)

  function handleChange(e: ChangeEvent<HTMLTextAreaElement>): void {
    const v = e.target.value
    setDraft(v)
    mention.onInput(v, e.target.selectionStart ?? v.length)
    if (target !== null && !draftMentions(v, targetHandle)) setTarget(null)
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    if (mention.onKeyDown(e)) return
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      e.preventDefault()
      submit()
    }
  }

  function submit(): void {
    const submitted = draft
    const text = submitted.trim()
    if (text === '' || target === null) return
    // "Attach to stage" never sends straight away — Send opens the preview dialog below instead, and only
    // its OWN confirm click (handleAttachConfirmed) issues the attachAndSendMesh request. No network call,
    // no roster/feed mutation, happens on this path.
    if (target.kind === 'profile') {
      setAttachPreview({ profileId: target.profileId, instruction: text })
      return
    }
    const recipient = target
    // Re-check at send time: the roster poll may have learned about a direct thread after the mention was picked.
    const knownThreadId = openedThreadId ?? findExistingDirectThread(knownThreads, recipient.id)?.id ?? null
    const submittedReplyTo = replyTo?.messageId
    submitter.submit(
      { recipientId: recipient.id, inReplyTo: submittedReplyTo, body: text },
      async (p, requestId) => {
        let threadId = knownThreadId
        if (threadId === null) {
          const thread = await openMeshThread({
            requestId: openRequestIdRef.current,
            title: '',
            scope: recipient.scope,
            memberIds: [recipient.id],
          })
          onThreadCreated(thread)
          threadId = thread.id
        }
        const receipt = await sendMeshMessage(threadId, { requestId, recipientIds: [p.recipientId], inReplyTo: p.inReplyTo, body: p.body })
        return { threadId, messageId: receipt.message_id }
      },
      {
        onSuccess: ({ threadId, messageId }) => {
          setOpenedThreadId(threadId)
          setLastSend({ participantId: recipient.id, threadId, messageId })
          setDraft((cur) => removeSubmittedText(cur, submitted))
          // A reply target picked while this message was sending belongs to the next message: keep it.
          setReplyTo((cur) => (cur?.messageId === submittedReplyTo ? null : cur))
        },
        onFailure: () => setLastSend(null),
      },
    )
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
  // receiptPhase — "awaiting" while a send to the current target is in flight, else the last send's receipt.
  const receiptPhase: ReceiptPhase | null = submitter.pending
    ? 'awaiting'
    : lastSend === null
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
        <div className="mesh-message-thread-frame">
          <div className="mesh-message-thread-header">
            <span>Conversation</span>
            <button type="button" className="mesh-message-thread-close" aria-label="Close conversation" onClick={closeConversation}>
              ✕
            </button>
          </div>
          <ul className="mesh-message-thread" aria-label="Conversation">
            {threadMessages.map((m: MeshMessage) => (
              <li key={m.id} className="mesh-message-thread-item">
                <span className="mesh-message-thread-sender">{senderLabel(m.sender_id, mesh.roster.participants)}</span>
                <span className="mesh-message-thread-body">
                  <AgentMessageBody body={m.body} fromAgent={m.sender_id !== 'user'} autoResult={m.auto_result === true} />
                </span>
                {/* Your own message has nobody to address back: Reply is offered on others' messages only. */}
                {m.sender_id !== 'user' && (
                  <button type="button" className="mesh-message-thread-reply" onClick={() => replyToMessage(m)}>
                    Reply
                  </button>
                )}
              </li>
            ))}
          </ul>
        </div>
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

      {target === null && <div className="mesh-message-route">Pick someone with @ or press Reply</div>}
      <div className="chat-composer-row">
        <textarea
          {...mention.inputAria}
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
            id={mention.listboxId}
            groups={matchGroups}
            highlightIndex={mention.highlightIndex}
            onHover={mention.setHighlightIndex}
            onSelect={mention.pick}
            ariaLabel="Mention a mesh participant or profile"
          />
        )}
        <ChatSendButton label="Send message" disabled={empty} sending={submitter.pending} onClick={submit} />
      </div>

      {submitter.error !== null && (
        <div className="mesh-message-error" role="alert">
          {submitter.error}
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
