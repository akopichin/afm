import { useCallback, useEffect, useId, useMemo, useRef, useState, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { buildReplyBody } from '../../lib/quote'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import type { MeshRoster } from '../../hooks/use-mesh'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { MentionMenu, mentionText } from '../assistants/AssistantMentionMenu'
import { filterMentionGroups } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { AttachAssistantDialog } from '../assistants'
import { TabList, tabPanelProps } from '../tab-list'
import { MeshAvatar } from '../team/MeshAvatar'
import { sendMeshMessageTo, type MeshAttachAndSendResult, type MeshMessage, type MeshParticipant } from '../../api/mesh-client'
import { buildMeshMentionGroups, type MeshMentionItem } from './mesh-recipients'
import { composerRecipients, messageRoute, recipientFence, withoutRecipient, type ComposerRecipient, type MessageRoute } from './composer-recipients'
import { postStageDiscussionMessage } from '../../api/run-client'
import { participantLabel } from '../../lib/mesh-roster'
import { computeReceiptPhase, deliveryKey, RECEIPT_LABEL } from './mesh-receipt'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { Stage } from '../../types'

// FeedComposerMeshProps — when provided (the stage Feed with the mesh and collaboration on), the composer gains
// a Message/Revise mode toggle; Message writes to the shared mesh, a DIFFERENT delivery path from the Revise
// note below. When omitted (collaboration off), the composer is EXACTLY the plain note-to-agent field: no tabs,
// unchanged DOM.
export type FeedComposerMeshProps = {
  // roster — the SAME useMeshRoster poll App.tsx already owns, passed down (no second poller here).
  roster: MeshRoster
  // profiles — configured helper roles, for the "Attach to stage" mention group.
  profiles: AssistantProfile[]
  // stages — for the recipients' own stage status (paused, waiting for helpers).
  stages: Stage[]
  // pick — the stage's people panel picked someone (seq tells two clicks on the same person apart): the
  // composer switches to Message and adds them to the recipients. A pick made before this composer mounted
  // (another stage, no composer yet) is ignored.
  pick?: MeshPick | null
  // deliveries — the run's real delivery receipts (meshDeliveries over the run events), for "Delivered".
  deliveries?: ReadonlySet<string>
}

// MeshPick — someone to add to the recipients.
export type MeshPick = { participantId: string; seq: number }
// MeshReplyTarget — a conversation message the human answers in its thread ("↩ Reply in conversation").
export type MeshReplyTarget = { messageId: string; threadId: string; senderId: string; preview: string }

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
  // mesh — см. FeedComposerMeshProps. undefined → ЛЕГАСИ-поведение, один режим, без табов.
  mesh?: FeedComposerMeshProps
  // reviseAvailable — with mesh: the stage takes a Revise now (running, awaiting_approval, awaiting_assistants).
  // false hides the Revise tab; Message stays.
  reviseAvailable?: boolean
  // conversationMessages — the stage feed's conversation messages (FeedWorkspace's one poll), for the receipts.
  conversationMessages?: MeshMessage[]
  // meshReply — the conversation message being answered (one at a time, owned by FeedWorkspace); its sender
  // becomes a recipient. onCancelMeshReply — its ✕; onMeshReplySent — the answer went out (race-safe reset by id).
  meshReply?: MeshReplyTarget | null
  onCancelMeshReply?: () => void
  onMeshReplySent?: (messageId: string) => void
}

// FeedComposer — messenger-поле ввода внизу ленты. Без mesh — ровно прежнее поведение: заметка живому агенту
// идёт через существующий Revise (graceful-пауза + feedback.md). С mesh добавляется режим Message (по
// умолчанию), адресующий общий mesh напрямую — это ДРУГОЙ путь доставки, Revise он не трогает.
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
  reviseAvailable = true,
  conversationMessages = NO_MESSAGES,
  meshReply = null,
  onCancelMeshReply,
  onMeshReplySent,
}: FeedComposerProps): ReactElement {
  const [mode, setMode] = useState<ComposerMode>(mesh !== undefined ? 'message' : 'revise')
  // Without Revise the composer is Message only (with mesh; without it, it is never mounted then).
  const shownMode: ComposerMode = mesh !== undefined && !reviseAvailable ? 'message' : mode
  const [value, setValue] = useState('')
  const [sending, setSending] = useState(false)
  const revisePanelRef = useRef<HTMLDivElement | null>(null)
  const tabsId = useId()

  // A people-panel pick and a Reply in conversation are Message's; a reply to a thought is Revise's.
  const pickSeq = mesh?.pick?.seq
  const seenPickSeq = useRef(pickSeq ?? 0)
  useEffect(() => {
    if (pickSeq === undefined || pickSeq === seenPickSeq.current) return
    seenPickSeq.current = pickSeq
    setMode('message')
  }, [pickSeq])
  const meshReplyId = meshReply?.messageId
  useEffect(() => {
    if (meshReplyId !== undefined) setMode('message')
  }, [meshReplyId])

  const empty = value.trim() === ''

  // Выбор мысли — это ответ через Revise: показываем его поле и переводим туда фокус, чтобы Cmd/Ctrl+Enter
  // сразу отправлял ответ. Фокус — после того, как поле стало видимым (с mesh оно могло быть скрыто за Message).
  useEffect(() => {
    if (replyKey !== undefined) setMode('revise')
  }, [replyKey])
  useEffect(() => {
    if (replyKey === undefined || shownMode !== 'revise') return
    revisePanelRef.current?.querySelector<HTMLTextAreaElement>('.chat-composer-input')?.focus()
  }, [replyKey, shownMode])

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

  if (mesh === undefined) {
    return (
      <div className="feed-composer" ref={revisePanelRef}>
        {reviseComposer}
      </div>
    )
  }

  // Both panels stay mounted (the hidden one keeps its draft and recipients); only the shown one is visible.
  return (
    <div className="feed-composer">
      <TabList
        idPrefix={tabsId}
        ariaLabel="Composer mode"
        className="feed-composer-modes"
        tabClassName="feed-composer-mode"
        selected={shownMode}
        onSelect={setMode}
        tabs={[{ id: 'message', label: 'Message' }, ...(reviseAvailable ? [{ id: 'revise' as const, label: 'Revise' }] : [])]}
      />
      <div className="feed-composer-panel" {...tabPanelProps(tabsId, 'message')} hidden={shownMode !== 'message'}>
        <MeshMessageComposer
          stageId={stageId}
          mesh={mesh}
          conversationMessages={conversationMessages}
          reply={meshReply}
          onCancelReply={onCancelMeshReply}
          onReplySent={onMeshReplySent}
          onUseRevise={reviseAvailable ? () => setMode('revise') : undefined}
          shown={shownMode === 'message'}
        />
      </div>
      {reviseAvailable && (
        <div className="feed-composer-panel" ref={revisePanelRef} {...tabPanelProps(tabsId, 'revise')} hidden={shownMode !== 'revise'}>
          {reviseComposer}
        </div>
      )}
    </div>
  )
}

const NO_MESSAGES: MeshMessage[] = []
const MAX_MESSAGE_TEXTAREA_HEIGHT = 200

type MeshMessageComposerProps = {
  stageId: string
  mesh: FeedComposerMeshProps
  conversationMessages: MeshMessage[]
  reply: MeshReplyTarget | null
  onCancelReply?: () => void
  onReplySent?: (messageId: string) => void
  // onUseRevise — switch to Revise (offered when the stage agent reads a note only that way); undefined
  // while the stage takes no Revise.
  onUseRevise?: () => void
  // shown — the Message panel is visible (a hidden one cannot take the focus yet).
  shown: boolean
}

// Attaching — an "Attach to stage" profile picked with @: Send opens the attach preview instead of sending.
type Attaching = { profileId: string; title: string }

// LastSend — the most recent successful send, for its per-recipient receipts. messageId is null when an
// attach-and-send result carried no message id yet.
type LastSend = { messageId: string | null; recipientIds: string[] }

// MeshMessageComposer — Message mode: "To" chips (people picked in the People panel, the sender of the message
// being answered, `@handles` typed in the text, or — with nobody named — the stage agent), the text, Send. One
// request sends ONE message to all recipients, shown in this stage's feed (messageRoute picks the request: a new
// message to the stage agent is a post in its stage conversation, with "Interrupt agent"; anything else goes to
// exactly its recipients). Free text here NEVER reaches the Revise path.
function MeshMessageComposer({ stageId, mesh, conversationMessages, reply, onCancelReply, onReplySent, onUseRevise, shown }: MeshMessageComposerProps): ReactElement {
  const participants = mesh.roster.participants
  const [draft, setDraft] = useState('')
  // picked — people added from the People panel. While a message is answered they go to replyPicked instead:
  // the reply is addressed to that conversation's people only, and its additions end with it.
  const [picked, setPicked] = useState<string[]>([])
  const [replyPicked, setReplyPicked] = useState<string[]>([])
  const replyId = reply?.messageId
  useEffect(() => setReplyPicked([]), [replyId])
  const activePicked = reply !== null ? replyPicked : picked
  const setActivePicked = reply !== null ? setReplyPicked : setPicked
  const [attaching, setAttaching] = useState<Attaching | null>(null)
  // attachPreview — an "Attach to stage" pick is never sent straight away: Send opens this preview, and only
  // the dialog's own "Attach and send" click calls attachAndSendMesh. Cancel keeps the draft.
  const [attachPreview, setAttachPreview] = useState<{ profileId: string; instruction: string } | null>(null)
  const [lastSend, setLastSend] = useState<LastSend | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  // interrupt — "Interrupt agent": the running stage agent stops its turn to answer this post, then continues.
  // Chosen per send, offered only for a post to the stage agent while it runs.
  const [interrupt, setInterrupt] = useState(false)

  const { recipients, defaultProblem } = useMemo(
    () => composerRecipients({ participants, stages: mesh.stages, stageId, picked: activePicked, draft, replyTo: reply?.senderId ?? null }),
    [participants, mesh.stages, stageId, activePicked, draft, reply?.senderId],
  )
  const recipientIds = recipients.flatMap((r) => (r.participantId !== null ? [r.participantId] : [])).sort()
  const problems = recipients.flatMap((r) => (r.problem !== null ? [r.problem] : []))
  // submitter — a retry of the same message reuses its request id. Fenced by the recipient set, the thread and
  // the message answered: a send that finishes after any of them changed neither clears the new draft nor
  // shows a receipt for the old recipients.
  const fence = recipientFence(recipientIds, reply?.threadId ?? '', reply?.messageId ?? '')
  const submitter = useRetryableSubmit<{ recipientIds: string[]; inReplyTo?: string; body: string; route: MessageRoute; interrupt: boolean }>('mesh-feed-send', fence)
  const route = messageRoute(stageId, recipientIds, reply !== null)

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

  const groups = useMemo(() => buildMeshMentionGroups(participants, mesh.profiles, stageId), [participants, mesh.profiles, stageId])
  // A person picked with @ stays in the text as @handle (a typed recipient); a profile to attach becomes a chip.
  const mention = useMentionMenu<MeshMentionItem>({
    matchesFor: (query) => filterMentionGroups(groups, query).flatMap((g) => g.items),
    textFor: (item) => (item.target.kind === 'profile' ? null : mentionText(item)),
    replaceRange,
    onPicked: (item) => {
      if (item.target.kind === 'profile') setAttaching({ profileId: item.target.profileId, title: item.title })
    },
  })
  const matchGroups = mention.query !== null ? filterMentionGroups(groups, mention.query) : []

  // focusRequest — a recipient was just added from outside (People panel, Reply): the input takes the focus
  // once the panel is shown (the same pick switches the composer to Message).
  const [focusRequest, setFocusRequest] = useState(0)
  useEffect(() => {
    if (focusRequest > 0 && shown) textareaRef.current?.focus()
  }, [focusRequest, shown])
  const addRecipient = useCallback(
    (id: string): void => {
      setActivePicked((prev) => (prev.includes(id) ? prev : [...prev, id]))
      setFocusRequest((n) => n + 1)
    },
    [setActivePicked],
  )

  // A people-panel pick adds that person to the recipients.
  const pick = mesh.pick ?? null
  const seenPickSeq = useRef(pick?.seq ?? 0)
  useEffect(() => {
    if (pick === null || pick.seq === seenPickSeq.current) return
    seenPickSeq.current = pick.seq
    addRecipient(pick.participantId)
  }, [pick, addRecipient])

  // Answering a message makes its sender a recipient (composerRecipients' replyTo; the server keeps it in that
  // message's thread) and takes the focus.
  useEffect(() => {
    if (replyId !== undefined) setFocusRequest((n) => n + 1)
  }, [replyId])

  function removeRecipient(r: ComposerRecipient): void {
    const next = withoutRecipient(r, { picked: activePicked, draft })
    setActivePicked(next.picked)
    setDraft(next.draft)
  }

  // openPicker — "+" opens the same @ list at the caret.
  function openPicker(): void {
    const el = textareaRef.current
    const caret = el?.selectionStart ?? draft.length
    const before = draft.slice(0, caret)
    const insert = before !== '' && !/\s$/.test(before) ? ' @' : '@'
    replaceRange(caret, caret, insert)
    mention.onInput(before + insert + draft.slice(caret), caret + insert.length)
    el?.focus()
  }

  function handleChange(e: ChangeEvent<HTMLTextAreaElement>): void {
    const v = e.target.value
    setDraft(v)
    setLocalError(null)
    mention.onInput(v, e.target.selectionStart ?? v.length)
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    if (mention.onKeyDown(e)) return
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      e.preventDefault()
      submit()
    }
  }

  const explicit = recipients.filter((r) => r.source !== 'default')

  function submit(): void {
    const submitted = draft
    const text = submitted.trim()
    if (text === '') return
    if (attaching !== null) {
      // A new helper gets the text as its assignment, alone.
      if (explicit.length > 0) {
        setLocalError('An attached helper gets the text alone — remove the other recipients or the helper')
        return
      }
      setAttachPreview({ profileId: attaching.profileId, instruction: text })
      return
    }
    if (recipientIds.length === 0 || problems.length > 0) return
    const submittedReply = reply
    submitter.submit(
      { recipientIds, inReplyTo: reply?.messageId, body: text, route, interrupt: offerInterrupt && interrupt },
      (p, requestId) =>
        p.route.kind === 'stage'
          ? postStageDiscussionMessage(stageId, requestId, { body: p.body, mentions: p.route.mentions, interrupt: p.interrupt })
          : sendMeshMessageTo({ requestId, recipientIds: p.recipientIds, inReplyTo: p.inReplyTo, body: p.body, originStage: stageId }).then((r) => ({
              messageId: r.message_id,
            })),
      {
        onSuccess: (receipt) => {
          setLastSend({ messageId: receipt.messageId, recipientIds })
          setDraft((cur) => removeSubmittedText(cur, submitted))
          if (submittedReply !== null) onReplySent?.(submittedReply.messageId)
        },
        onFailure: () => setLastSend(null),
      },
    )
  }

  // handleAttachConfirmed — the attach preview's "Attach and send" resolved: the only place its result is
  // applied, so nothing optimistic shows before a real success.
  function handleAttachConfirmed(result: MeshAttachAndSendResult): void {
    setAttachPreview(null)
    setAttaching(null)
    setLastSend({ messageId: result.message_id ?? null, recipientIds: [result.participant_id] })
    setDraft('')
  }

  const stageStatus = (id: string | undefined): string | undefined => mesh.stages.find((s) => s.id === id)?.status
  const receipts =
    lastSend === null
      ? []
      : lastSend.recipientIds.map((id) => {
          const participant = participants.find((p) => p.id === id)
          const phase = computeReceiptPhase({
            participant,
            stagePaused: participant?.stage_id !== undefined && stageStatus(participant.stage_id) === 'paused',
            answered: lastSend.messageId !== null && conversationMessages.some((m) => m.in_reply_to === lastSend.messageId && m.sender_id === id),
            inFlight: lastSend.messageId === null,
            delivered: lastSend.messageId !== null && mesh.deliveries?.has(deliveryKey(lastSend.messageId, id)) === true,
          })
          return { id, name: participant !== undefined ? participantLabel(participant) : 'New agent', label: RECEIPT_LABEL[phase] }
        })

  const canSend = draft.trim() !== '' && (attaching !== null || (recipientIds.length > 0 && problems.length === 0))
  const offerInterrupt = attaching === null && route.kind === 'stage' && stageStatus(stageId) === 'running'
  // The stage agent waiting for its helpers reads only a Revise: offer the switch.
  const offerRevise = onUseRevise !== undefined && recipients.length === 0 && stageStatus(stageId) === 'awaiting_assistants'

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

      {reply !== null && (
        <div className="feed-reply-chip feed-reply-chip--conversation">
          <span className="feed-reply-chip-label">↩ Reply in conversation</span>
          <span className="feed-reply-chip-src">{reply.preview}</span>
          <button type="button" className="feed-reply-chip-cancel" aria-label="Cancel reply in conversation" onClick={onCancelReply}>
            ✕
          </button>
        </div>
      )}

      <div className="mesh-recipients" role="group" aria-label="Recipients">
        <span className="mesh-recipients-label">To</span>
        {recipients.map((r) => (
          <RecipientChip
            key={r.key}
            recipient={r}
            participants={participants}
            // The stage agent by default and the sender of the message answered (cancel the reply) have no ×.
            onRemove={r.source === 'default' || r.source === 'reply' ? undefined : () => removeRecipient(r)}
          />
        ))}
        {attaching !== null && (
          <span className="mesh-recipient-chip is-attach" title="A new helper of this stage; the text is its assignment">
            Attach helper: {attaching.title}
            <button type="button" className="mesh-recipient-remove" aria-label={`Don't attach ${attaching.title}`} onClick={() => setAttaching(null)}>
              ×
            </button>
          </span>
        )}
        {recipients.length === 0 && attaching === null && <span className="mesh-recipients-empty">{defaultProblem ?? 'Pick someone to write to'}</span>}
        <button type="button" className="mesh-recipients-add" aria-label="Add recipient" title="Add recipient (@)" onClick={openPicker}>
          +
        </button>
        {offerRevise && (
          <button type="button" className="mesh-recipients-use-revise" onClick={onUseRevise}>
            Use Revise
          </button>
        )}
        {offerInterrupt && (
          <label className="switch mesh-recipients-interrupt" title="The agent stops its current turn, answers this message, then continues its work">
            <input type="checkbox" checked={interrupt} onChange={(e) => setInterrupt(e.target.checked)} />
            <span className="track" aria-hidden="true" />
            <span className="switch-label">Interrupt agent</span>
          </label>
        )}
      </div>

      <div className="chat-composer-row">
        <textarea
          {...mention.inputAria}
          ref={setRefs}
          className="chat-composer-input"
          value={draft}
          onChange={handleChange}
          onKeyDown={onKeyDown}
          placeholder="Message… (@ to add someone)"
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
        <ChatSendButton label="Send message" disabled={!canSend} sending={submitter.pending} onClick={submit} />
      </div>

      {problems.length > 0 && (
        <ul className="mesh-message-error mesh-recipient-problems" role="alert">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      {(submitter.error ?? localError) !== null && (
        <div className="mesh-message-error" role="alert">
          {submitter.error ?? localError}
        </div>
      )}
      {submitter.pending ? (
        <div className="mesh-message-receipt" role="status">
          {RECEIPT_LABEL.awaiting}
        </div>
      ) : (
        receipts.length > 0 && (
          <ul className="mesh-message-receipt mesh-message-receipts" role="status" aria-label="Delivery">
            {receipts.map((r) => (
              <li key={r.id}>
                <span className="mesh-receipt-name">{r.name}</span> · {r.label}
              </li>
            ))}
          </ul>
        )
      )}
    </div>
  )
}

// RecipientChip — one "To" chip: avatar and @handle; the stage agent chosen by default says so; a recipient
// with a problem is marked (the problem is listed under the input).
function RecipientChip({ recipient, participants, onRemove }: { recipient: ComposerRecipient; participants: MeshParticipant[]; onRemove?: () => void }): ReactElement {
  const p = participants.find((x) => x.id === recipient.participantId)
  const name = p !== undefined ? participantLabel(p) : recipient.handle
  return (
    <span
      className={`mesh-recipient-chip${recipient.problem !== null ? ' has-problem' : ''}${recipient.source === 'default' ? ' is-default' : ''}`}
      title={recipient.problem ?? (recipient.source === 'default' ? 'Nobody named — the stage agent gets it' : name)}
    >
      {p !== undefined && <MeshAvatar name={name} kind={p.kind} size="small" />}@{recipient.handle}
      {recipient.source === 'default' && <span className="mesh-recipient-note">stage agent</span>}
      {onRemove !== undefined && (
        <button type="button" className="mesh-recipient-remove" aria-label={`Remove @${recipient.handle}`} onClick={onRemove}>
          ×
        </button>
      )}
    </span>
  )
}
