import { handleTextareaSubmit } from '../../lib/textarea-submit'
import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactElement } from 'react'
import { createPortal } from 'react-dom'
import { PasteableTextarea } from '../pasteable-textarea'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { ComposerCard, ComposerHint, ComposerRecipients } from '../chat-composer/ComposerCard'
import { buildReplyBody } from '../../lib/quote'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import type { MeshRoster } from '../../hooks/use-mesh'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { MentionMenu, mentionText } from '../assistants/AssistantMentionMenu'
import { filterMentionGroups } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { AttachAssistantDialog } from '../assistants'
import { TabList, tabPanelProps } from '../tab-list'
import { addMeshLoop, listMeshLoops, sendMeshMessageTo, stopMeshLoop, type MeshAttachAndSendResult, type MeshMessage, type MeshParticipant } from '../../api/mesh-client'
import { buildLoopMentionGroups, buildMeshMentionGroups, type MeshMentionItem } from './mesh-recipients'
import { isLoopCommandDraft, LOOP_INTERVAL_EXAMPLES, LOOP_SYNTAX, loopIntervalSlot, loopSubcommand, parseLoopCommand, resolveLoopTarget, type IntervalSlot, type LoopCommand, type LoopTarget } from './loop-command'
import { LoopOutcomeView, type LoopOutcome } from './LoopResult'
import { composerRecipients, messageRoute, recipientFence, withoutRecipient, type ComposerRecipient, type MessageRoute } from './composer-recipients'
import { postStageDiscussionMessage } from '../../api/run-client'
import { mentionHandles, participantLabel } from '../../lib/mesh-roster'
import { computeReceiptPhase, deliveryKey, sideAgentOutcome, RECEIPT_LABEL } from './mesh-receipt'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { Stage } from '../../types'
import type { ImagePasteDraft, AttachmentActions } from '../../hooks/use-image-paste'

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
  sideFailures?: ReadonlySet<string>
}

// MeshPick — someone to add to the recipients.
export type MeshPick = { participantId: string; seq: number }
// MeshReplyTarget — a conversation message the human answers in its thread ("↩ Reply in conversation").
// members — the members of that message's conversation: the reply stays in it, so only they can get it.
export type MeshReplyTarget = { messageId: string; threadId: string; senderId: string; preview: string; members: string[] }

type ComposerMode = 'message' | 'revise'

// Drafts belong to the run, so navigation never discards a reply or an uploaded image.
export type ComposerDraft = {
  mode: ComposerMode
  modeChosen: boolean
  revise: string
  message: string
  picked: string[]
  thoughtReply?: { key: string; text: string } | null
  meshReply?: MeshReplyTarget | null
  replyMessage?: string
  replyPicked?: string[]
  reviseAttachments?: ImagePasteDraft
  messageAttachments?: ImagePasteDraft
  replyAttachments?: ImagePasteDraft
}

export const emptyComposerDraft = (): ComposerDraft => ({ mode: 'message', modeChosen: false, revise: '', message: '', picked: [] })

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
  // draft — this stage's saved draft to start from; onDraftChange — every change of it, to save (see ComposerDraft).
  draft?: ComposerDraft
  onDraftChange?: (patch: Partial<ComposerDraft>) => void
  onReplyAttachmentsChange?: (replyId: string, attachments: ImagePasteDraft) => void
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
  draft,
  onDraftChange,
  onReplyAttachmentsChange,
}: FeedComposerProps): ReactElement {
  const [mode, setModeState] = useState<ComposerMode>(draft?.mode ?? (mesh !== undefined ? 'message' : 'revise'))
  // modeChosen — the human (or an action of theirs: a pick, a reply) chose the mode; a late mesh never overrides it.
  const modeChosen = useRef(draft?.modeChosen ?? false)
  const setMode = useCallback((m: ComposerMode): void => {
    modeChosen.current = true
    setModeState(m)
  }, [])
  // Without Revise the composer is Message only (with mesh; without it, it is never mounted then).
  const shownMode: ComposerMode = mesh !== undefined && !reviseAvailable ? 'message' : mode
  const [value, setValue] = useState(draft?.revise ?? '')
  const [sending, setSending] = useState(false)
  const reviseAttachments = useRef<AttachmentActions | null>(null)

  // The mesh can arrive after the composer mounted (the roster probe answers after the first render): its first
  // activation opens Message, the default with the mesh — unless the human already chose a mode or is writing a
  // Revise note.
  const meshActive = mesh !== undefined
  const wasMeshActive = useRef(meshActive)
  useEffect(() => {
    if (meshActive && !wasMeshActive.current && !modeChosen.current && value.trim() === '') setModeState('message')
    wasMeshActive.current = meshActive
  }, [meshActive, value])

  // The saved draft: every change is reported, merged with the Message part MeshMessageComposer reports.
  const reportDraft = useRef(onDraftChange)
  reportDraft.current = onDraftChange
  const updateDraft = useCallback((patch: Partial<ComposerDraft>): void => {
    reportDraft.current?.(patch)
  }, [])
  useEffect(() => updateDraft({ mode, modeChosen: modeChosen.current }), [mode, updateDraft])
  useEffect(() => updateDraft({ revise: value }), [value, updateDraft])
  const revisePanelRef = useRef<HTMLDivElement | null>(null)
  const tabsId = useId()
  const [modeOptions, setModeOptions] = useState<HTMLDivElement | null>(null)

  // A people-panel pick and a Reply in conversation are Message's; a reply to a thought is Revise's.
  const pickSeq = mesh?.pick?.seq
  const seenPickSeq = useRef(pickSeq ?? 0)
  useEffect(() => {
    if (pickSeq === undefined || pickSeq === seenPickSeq.current) return
    seenPickSeq.current = pickSeq
    setMode('message')
  }, [pickSeq])
  const meshReplyId = meshReply?.messageId
  const previousMeshReply = useRef(draft?.meshReply?.messageId)
  useEffect(() => {
    if (meshReplyId !== undefined && meshReplyId !== previousMeshReply.current) setMode('message')
    previousMeshReply.current = meshReplyId
  }, [meshReplyId])

  const empty = value.trim() === ''
  // A slash command runs in Message: as a stage Revise it would put the literal text into the agent's feedback.
  const slashInRevise = mesh !== undefined && isLoopCommandDraft(value)

  // Выбор мысли — это ответ через Revise: показываем его поле и переводим туда фокус, чтобы Enter
  // сразу отправлял ответ. Фокус — после того, как поле стало видимым (с mesh оно могло быть скрыто за Message).
  const previousThoughtReply = useRef(draft?.thoughtReply?.key)
  useEffect(() => {
    if (replyKey !== undefined && replyKey !== previousThoughtReply.current) setMode('revise')
    previousThoughtReply.current = replyKey
  }, [replyKey])
  useEffect(() => {
    if (replyKey === undefined || shownMode !== 'revise') return
    revisePanelRef.current?.querySelector<HTMLTextAreaElement>('.chat-composer-input')?.focus()
  }, [replyKey, shownMode])

  async function submit(): Promise<void> {
    if (empty || sending || slashInRevise) return
    const submitted = value // снимок на момент отправки
    const body = buildReplyBody(replyQuote, submitted)
    const attachmentActions = reviseAttachments.current
    const sentAttachments = submittedAttachments(attachmentActions, submitted)
    setSending(true)
    try {
      await onSend(body)
      attachmentActions?.discard(sentAttachments)
      setValue((cur) => removeSubmittedText(cur, submitted))
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

  const pauseOption = onTogglePauseOnFocus !== undefined && (
    <label className={`switch${mesh !== undefined ? ' feed-composer-mode-option' : ''}`}>
      <input type="checkbox" checked={pauseOnFocusEnabled} onChange={(e) => onTogglePauseOnFocus(e.target.checked)} />
      {mesh === undefined && <span className="track" aria-hidden="true" />}
      <span className="switch-label">Hold the agent while you type</span>
    </label>
  )
  const reviseAgent = mesh?.roster.participants.find((p) => p.kind === 'stage_agent' && p.stage_id === stageId)
  const reviseSend = (
    <ChatSendButton
      label={mesh !== undefined ? 'Send revision' : 'Send note to agent'}
      visibleLabel={mesh !== undefined ? 'Send revision' : undefined}
      disabled={empty || slashInRevise}
      sending={sending}
      paused={paused}
      onClick={() => void submit()}
    />
  )

  // The legacy note keeps its inline controls; mesh modes share the card and toolbar layout.
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
      {mesh === undefined && pauseOption && <div className="composer-opts">{pauseOption}</div>}
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
      <ComposerCard card={mesh !== undefined} paused={paused}>
        {mesh !== undefined && (
          <ComposerRecipients>
            {reviseAgent !== undefined ? (
              <RecipientChip recipient={loopTargetChip(reviseAgent, mesh.roster.participants)} participants={mesh.roster.participants} />
            ) : <span className="mesh-recipient-chip chat-composer-recipient">@{stageId}</span>}
          </ComposerRecipients>
        )}
        <PasteableTextarea
          stageId={stageId}
          attachmentDraft={draft?.reviseAttachments}
          attachmentActionsRef={reviseAttachments}
          onAttachmentDraftChange={(reviseAttachments) => updateDraft({ reviseAttachments })}
          className="chat-composer-input"
          value={value}
          onChange={setValue}
          placeholder={mesh !== undefined ? 'Describe what the agent should change…' : 'Note to agent…'}
          textareaProps={mesh !== undefined ? { 'aria-label': 'Revision instructions' } : undefined}
          allowFileReferences
          attachInline
          toolbar={mesh !== undefined ? reviseSend : undefined}
          rows={1}
          onSubmit={() => void submit()}
          onFocus={onFocus}
          onBlur={() => onBlur?.(value.trim() !== '')}
          maxHeight={200}
        />
        {mesh === undefined && reviseSend}
      </ComposerCard>
      {mesh !== undefined && <ComposerHint note={COMPOSER_HINT.revise} />}
      {slashInRevise && (
        <div className="mesh-message-error" role="alert">
          {SLASH_IN_REVISE}
        </div>
      )}
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
    <div className="feed-composer feed-composer--mesh">
      <div className="feed-composer-header">
        <TabList
          idPrefix={tabsId}
          ariaLabel="Composer mode"
          className="feed-composer-modes"
          tabClassName="feed-composer-mode"
          selected={shownMode}
          onSelect={setMode}
          tabs={[{ id: 'message', label: <><ComposerModeIcon mode="message" />Message</> }, ...(reviseAvailable ? [{ id: 'revise' as const, label: <><ComposerModeIcon mode="revise" />Revise</> }] : [])]}
        />
        <div className="composer-opts" ref={setModeOptions}>
          {shownMode === 'revise' && pauseOption}
        </div>
      </div>
      <div className="feed-composer-panel" {...tabPanelProps(tabsId, 'message')} hidden={shownMode !== 'message'}>
        <MeshMessageComposer
          stageId={stageId}
          mesh={mesh}
          initialMessage={draft?.message ?? ''}
          initialPicked={draft?.picked ?? NO_PICKS}
          savedDraft={draft}
          onReplyAttachmentsChange={onReplyAttachmentsChange}
          onDraftChange={updateDraft}
          conversationMessages={conversationMessages}
          reply={meshReply}
          onCancelReply={onCancelMeshReply}
          onReplySent={onMeshReplySent}
          onUseRevise={reviseAvailable ? () => setMode('revise') : undefined}
          shown={shownMode === 'message'}
          modeOptions={modeOptions}
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

function ComposerModeIcon({ mode }: { mode: ComposerMode }): ReactElement {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {mode === 'message' ? <path d="M4 4h16v12H9l-5 4V4Z" /> : (
        <><path d="M20 10a8 8 0 0 0-14-5L3 8m0-5v5h5" /><path d="M4 14a8 8 0 0 0 14 5l3-3m0 5v-5h-5" /></>
      )}
    </svg>
  )
}

const COMPOSER_HINT: Record<ComposerMode, string> = {
  message: 'Only the recipients above receive this message.',
  revise: 'The executor starts a revision with your instructions.',
}

const SLASH_IN_REVISE = 'Slash commands run in Message'
const NO_MESSAGES: MeshMessage[] = []
const NO_PICKS: string[] = []
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
  modeOptions: HTMLDivElement | null
  // initialMessage/initialPicked — the stage's saved Message draft; onDraftChange — report its changes.
  initialMessage: string
  initialPicked: string[]
  savedDraft?: ComposerDraft
  onReplyAttachmentsChange?: (replyId: string, attachments: ImagePasteDraft) => void
  onDraftChange: (patch: Partial<ComposerDraft>) => void
}

// LoopPayload — a /loop command as the intent a retry is recognised by: plain JSON data, with the target already
// resolved to its participant id.
type LoopPayload = { kind: 'add'; target: string; every: string; prompt: string; untilStageEnd: boolean } | { kind: 'stop'; loopId: string } | { kind: 'list' }

// runLoopCommand — the one request a /loop command is. originStage is the Feed it runs in: a new loop shows there,
// and --until-stage-end ends it with that stage.
function runLoopCommand(p: LoopPayload, requestId: string, originStage: string): Promise<LoopOutcome> {
  switch (p.kind) {
    case 'add':
      return addMeshLoop({ requestId, target: p.target, every: p.every, prompt: p.prompt, originStage, ...(p.untilStageEnd ? { untilStage: originStage } : {}) }).then(
        (loop) => ({ kind: 'created', loop }),
      )
    case 'stop':
      return stopMeshLoop({ requestId, loopId: p.loopId }).then((loop) => ({ kind: 'stopped', loop }))
    case 'list':
      return listMeshLoops().then(({ loops }) => ({ kind: 'list', loops }))
  }
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
function MeshMessageComposer({
  stageId,
  mesh,
  conversationMessages,
  reply,
  onCancelReply,
  onReplySent,
  onUseRevise,
  shown,
  modeOptions,
  initialMessage,
  initialPicked,
  savedDraft,
  onReplyAttachmentsChange,
  onDraftChange,
}: MeshMessageComposerProps): ReactElement {
  const participants = mesh.roster.participants
  // A reply has its own draft (like its own picks below): what was being written to others neither becomes the
  // reply's text and @recipients nor is lost — it comes back when the reply ends.
  const [ordinaryDraft, setOrdinaryDraft] = useState(initialMessage)
  const [replyDraft, setReplyDraft] = useState(savedDraft?.replyMessage ?? '')
  // picked — people added from the People panel. While a message is answered they go to replyPicked instead:
  // the reply is addressed to that conversation's people only, and its additions end with it.
  const [picked, setPicked] = useState<string[]>(initialPicked)
  useEffect(() => onDraftChange({ message: ordinaryDraft }), [ordinaryDraft, onDraftChange])
  useEffect(() => onDraftChange({ picked }), [picked, onDraftChange])
  const [replyPicked, setReplyPicked] = useState<string[]>(savedDraft?.replyPicked ?? [])
  const replyId = reply?.messageId
  const activeReplyId = useRef(replyId)
  activeReplyId.current = replyId
  const previousReply = useRef(replyId)
  useEffect(() => {
    if (previousReply.current === replyId) return
    previousReply.current = replyId
    setReplyPicked([])
    setReplyDraft('')
  }, [replyId])
  useEffect(() => onDraftChange({ replyMessage: replyDraft }), [replyDraft, onDraftChange])
  useEffect(() => onDraftChange({ replyPicked }), [replyPicked, onDraftChange])
  const activePicked = reply !== null ? replyPicked : picked
  const setActivePicked = reply !== null ? setReplyPicked : setPicked
  const draft = reply !== null ? replyDraft : ordinaryDraft
  const setDraft = reply !== null ? setReplyDraft : setOrdinaryDraft
  const [attaching, setAttaching] = useState<Attaching | null>(null)
  // attachPreview — an "Attach to stage" pick is never sent straight away: Send opens this preview, and only
  // the dialog's own "Attach and send" click calls attachAndSendMesh. Cancel keeps the draft.
  const [attachPreview, setAttachPreview] = useState<{ profileId: string; instruction: string; submitted: string; actions: AttachmentActions | null; attachments: ImagePasteDraft } | null>(null)
  const [lastSend, setLastSend] = useState<LastSend | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  // interrupt — "Interrupt agent": the running stage agent stops its turn to answer this post, then continues.
  // Chosen per send, offered only for a post to the stage agent while it runs.
  const [interrupt, setInterrupt] = useState(false)

  const { recipients, defaultProblem } = useMemo(
    () => composerRecipients({ participants, stages: mesh.stages, stageId, picked: activePicked, draft, replyTo: reply?.senderId ?? null, replyMembers: reply?.members ?? null }),
    [participants, mesh.stages, stageId, activePicked, draft, reply?.senderId, reply?.members],
  )
  const recipientIds = recipients.flatMap((r) => (r.participantId !== null ? [r.participantId] : [])).sort()
  const problems = recipients.flatMap((r) => (r.problem !== null ? [r.problem] : []))
  // submitter — a retry of the same message reuses its request id. Fenced by the recipient set, the thread and
  // the message answered: a send that finishes after any of them changed neither clears the new draft nor
  // shows a receipt for the old recipients.
  const fence = recipientFence(recipientIds, reply?.threadId ?? '', reply?.messageId ?? '')
  const submitter = useRetryableSubmit<{ recipientIds: string[]; inReplyTo?: string; body: string; route: MessageRoute; interrupt: boolean }>('mesh-feed-send', fence)
  const route = messageRoute(stageId, recipientIds, reply !== null)

  // loopMode — the draft is a /loop command: an operation of its own, never a message. The reply, the picked
  // people, the stage agent and an attaching profile take no part in it (they come back when the draft stops being one).
  const loopMode = isLoopCommandDraft(draft)
  const loopParse = loopMode ? parseLoopCommand(draft) : null
  const loopCommand = loopParse?.ok === true ? loopParse.command : null
  // loopTargetToken — a resolved `@target` shows its row even while the rest of the command (e.g. the prompt)
  // is still being typed: parseLoopCommand carries it on its one failure that can follow a valid target.
  const loopTargetToken = loopCommand?.kind === 'add' ? loopCommand.target : (loopParse !== null && !loopParse.ok ? loopParse.target : undefined)
  const loopTarget = loopTargetToken !== undefined ? resolveLoopTarget(loopTargetToken, participants, stageId) : null
  const loopProblem = loopTarget !== null && 'problem' in loopTarget ? loopTarget.problem : null
  // Malformed syntax keeps Send clickable so mouse users can reveal its error.
  // An unusable resolved target already explains its error and disables Send.
  const loopCanSend = loopProblem === null
  const intervalSlot = loopMode ? loopIntervalSlot(draft) : null
  const loopHintId = useId()
  // loopOutcome — the result of the last /loop command, shown until the next edit or Escape. Its own submitter: a
  // loop command and a message never share a pending state, an error or a request id.
  const [loopOutcome, setLoopOutcome] = useState<LoopOutcome | null>(null)
  const loopSubmitter = useRetryableSubmit<LoopPayload>('mesh-feed-loop', stageId)

  const messageAttachments = useRef<AttachmentActions | null>(null)
  const latestDraft = useRef(draft)
  latestDraft.current = draft
  const { nodeRef: caretNodeRef, replaceRange } = useCaretInsert(draft, setDraft)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const setRefs = useCallback(
    (node: HTMLTextAreaElement | null) => {
      textareaRef.current = node
      caretNodeRef.current = node
    },
    [caretNodeRef],
  )

  const messageGroups = useMemo(() => buildMeshMentionGroups(participants, mesh.profiles, stageId), [participants, mesh.profiles, stageId])
  const loopGroups = useMemo(() => buildLoopMentionGroups(participants, stageId), [participants, stageId])
  const groups = loopMode ? loopGroups : messageGroups
  // A person picked with @ stays in the text as @handle (a typed recipient); a profile to attach becomes a chip.
  const mention = useMentionMenu<MeshMentionItem>({
    matchesFor: (query) => filterMentionGroups(groups, query).flatMap((g) => g.items),
    showEmpty: true,
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

  function handleChange(v: string): void {
    setDraft(v)
    setLocalError(null)
    setLoopOutcome(null)
    mention.onInput(v, textareaRef.current?.selectionStart ?? v.length)
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    if (mention.onKeyDown(e)) return
    if (e.key === 'Escape') setLoopOutcome(null)
    handleTextareaSubmit(e, submit, canSend && !(loopMode ? loopSubmitter.pending : submitter.pending))
  }

  const explicit = recipients.filter((r) => r.source !== 'default')

  // pickInterval — an interval example replaces the interval typed so far; the caret stays in the draft.
  function pickInterval(slot: IntervalSlot, example: string): void {
    replaceRange(slot.start, slot.end, `${slot.lead}${example} `)
    textareaRef.current?.focus()
  }

  // submitLoop — a malformed command is rejected visibly with the draft kept; a target problem is already shown.
  // A command that ran clears only the text it was made of; one that failed keeps the draft and shows why.
  function submitLoop(): void {
    if (loopParse === null) return
    if (!loopParse.ok) {
      setLocalError(loopParse.error)
      return
    }
    if (loopProblem !== null) return
    const submitted = draft
    setLocalError(null)
    setLoopOutcome(null)
    loopSubmitter.submit(
      loopPayload(loopParse.command, loopTarget),
      (p, requestId) => runLoopCommand(p, requestId, stageId),
      {
        onSuccess: (outcome) => {
          setLoopOutcome(outcome)
          setDraft((cur) => removeSubmittedText(cur, submitted))
        },
        onFailure: setLocalError,
      },
    )
  }

  function submit(): void {
    if (loopMode) {
      submitLoop()
      return
    }
    const submitted = draft
    const text = submitted.trim()
    if (text === '') return
    if (attaching !== null) {
      // A new helper gets the text as its assignment, alone.
      if (explicit.length > 0) {
        setLocalError('An attached helper gets the text alone — remove the other recipients or the helper')
        return
      }
      setAttachPreview({ profileId: attaching.profileId, instruction: text, submitted, actions: messageAttachments.current, attachments: submittedAttachments(messageAttachments.current, submitted) })
      return
    }
    if (recipientIds.length === 0 || problems.length > 0) return
    const submittedReply = reply
    const attachmentActions = messageAttachments.current
    const sentAttachments = submittedAttachments(attachmentActions, submitted)
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
          attachmentActions?.discard(sentAttachments)
          setLastSend({ messageId: receipt.messageId, recipientIds })
          // Interrupting is chosen per send: it does not carry over to the next message (a failure keeps it for the retry).
          setInterrupt(false)
          if (submittedReply === null || activeReplyId.current === submittedReply.messageId) setDraft((cur) => removeSubmittedText(cur, submitted))
          if (submittedReply !== null && activeReplyId.current === submittedReply.messageId && removeSubmittedText(latestDraft.current, submitted).trim() === '' && (attachmentActions?.snapshot().length ?? 0) === 0) onReplySent?.(submittedReply.messageId)
        },
        onFailure: () => setLastSend(null),
      },
    )
  }

  // handleAttachConfirmed — the attach preview's "Attach and send" resolved: the only place its result is
  // applied, so nothing optimistic shows before a real success.
  function handleAttachConfirmed(result: MeshAttachAndSendResult): void {
    if (attachPreview !== null) {
      attachPreview.actions?.discard(attachPreview.attachments)
      setDraft((current) => removeSubmittedText(current, attachPreview.submitted))
    }
    setAttachPreview(null)
    setAttaching(null)
    setLastSend({ messageId: result.message_id ?? null, recipientIds: [result.participant_id] })
  }

  const stageStatus = (id: string | undefined): string | undefined => mesh.stages.find((s) => s.id === id)?.status
  const receipts =
    lastSend === null
      ? []
      : lastSend.recipientIds.map((id) => {
          const participant = participants.find((p) => p.id === id)
          const sideOutcome = lastSend.messageId === null ? null : sideAgentOutcome(
            { id: lastSend.messageId, recipient_ids: lastSend.recipientIds }, conversationMessages, mesh.sideFailures ?? new Set(), id,
          )
          const phase = sideOutcome ?? computeReceiptPhase({
            participant,
            stagePaused: participant?.stage_id !== undefined && stageStatus(participant.stage_id) === 'paused',
            answered: lastSend.messageId !== null && conversationMessages.some((m) => m.in_reply_to === lastSend.messageId && m.sender_id === id),
            inFlight: lastSend.messageId === null,
            delivered: lastSend.messageId !== null && mesh.deliveries?.has(deliveryKey(lastSend.messageId, id)) === true,
          })
          return { id, name: participant !== undefined ? participantLabel(participant) : 'New agent', label: RECEIPT_LABEL[phase] }
        })

  const canSend = loopMode ? loopCanSend : draft.trim() !== '' && (attaching !== null || (recipientIds.length > 0 && problems.length === 0))
  const offerInterrupt = !loopMode && attaching === null && route.kind === 'stage' && stageStatus(stageId) === 'running'
  // The stage agent waiting for its helpers reads only a Revise: offer the switch.
  const offerRevise = !loopMode && onUseRevise !== undefined && recipients.length === 0 && stageStatus(stageId) === 'awaiting_assistants'

  return (
    <div className="mesh-message-composer">
      {shown && offerInterrupt && modeOptions !== null && createPortal(
        <label className="feed-composer-mode-option" title="The agent stops its current turn, answers this message, then continues its work">
          <input type="checkbox" checked={interrupt} onChange={(e) => setInterrupt(e.target.checked)} />
          <span>Interrupt agent</span>
        </label>,
        modeOptions,
      )}
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

      {reply !== null && !loopMode && (
        <div className="feed-reply-chip feed-reply-chip--conversation">
          <span className="feed-reply-chip-label">↩ Reply in conversation</span>
          <span className="feed-reply-chip-src">{reply.preview}</span>
          <button type="button" className="feed-reply-chip-cancel" aria-label="Cancel reply in conversation" onClick={onCancelReply}>
            ✕
          </button>
        </div>
      )}

      <ComposerCard>
        {loopMode && (
          <ComposerRecipients label="Loop" ariaLabel="Loop target" className="mesh-loop-target">
            {loopTarget !== null && 'participant' in loopTarget ? (
              <RecipientChip recipient={loopTargetChip(loopTarget.participant, participants)} participants={participants} />
            ) : loopCommand !== null && loopCommand.kind !== 'add' ? (
              <span className="mesh-recipients-empty">Runs for the whole run — no recipient</span>
            ) : (
              <span className="mesh-recipients-empty">Name one target with @agent</span>
            )}
          </ComposerRecipients>
        )}

        {!loopMode && (
          <ComposerRecipients>
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
              <span className="mesh-recipient-chip chat-composer-recipient is-attach" title="A new helper of this stage; the text is its assignment">
                Attach helper: {attaching.title}
                <button type="button" className="mesh-recipient-remove chat-composer-recipient-remove" aria-label={`Don't attach ${attaching.title}`} onClick={() => setAttaching(null)}>
                  ×
                </button>
              </span>
            )}
            {recipients.length === 0 && attaching === null && <span className="mesh-recipients-empty">{defaultProblem ?? 'Pick someone to write to'}</span>}
            <button type="button" className="mesh-recipients-add chat-composer-add" aria-label="Add recipient" title="Add recipient (@)" onClick={openPicker}>
              <span aria-hidden="true">+</span> Add recipient
            </button>
            {offerRevise && (
              <button type="button" className="mesh-recipients-use-revise" onClick={onUseRevise}>
                Use Revise
              </button>
            )}
          </ComposerRecipients>
        )}

        <PasteableTextarea
          key={reply?.messageId ?? 'ordinary'}
          stageId={stageId}
          maxHeight={MAX_MESSAGE_TEXTAREA_HEIGHT}
          attachmentActionsRef={messageAttachments}
          attachmentDraft={reply !== null ? savedDraft?.replyAttachments : savedDraft?.messageAttachments}
          onAttachmentDraftChange={(attachments) => {
            if (replyId === undefined) onDraftChange({ messageAttachments: attachments })
            else if (onReplyAttachmentsChange !== undefined) onReplyAttachmentsChange(replyId, attachments)
            else if (activeReplyId.current === replyId) onDraftChange({ replyAttachments: attachments })
          }}
          textareaProps={{ ...mention.inputAria, 'aria-label': 'Message the mesh', 'aria-describedby': loopMode ? loopHintId : undefined }}
          textareaRef={setRefs}
          allowFileReferences
          attachInline
          toolbar={(
            <>
              <button type="button" className="chat-composer-mention" aria-label="Mention someone" title="Mention someone (@)" onClick={openPicker}>@</button>
              <ChatSendButton
                label={loopMode ? loopSendLabel(loopSubcommand(draft)) : 'Send message'}
                visibleLabel={loopMode ? loopSendLabel(loopSubcommand(draft)) : 'Send'}
                disabled={!canSend}
                sending={loopMode ? loopSubmitter.pending : submitter.pending}
                onClick={submit}
              />
            </>
          )}
          className="chat-composer-input"
          value={draft}
          onChange={handleChange}
          onKeyDown={onKeyDown}
          placeholder="Write a message, or type @ to bring someone in…"
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
            heading="Mention a participant"
            emptyMessage="No matching participant. Try a name or @alias."
            showGroupLabels={false}
          />
        )}
      </ComposerCard>
      {!loopMode && <ComposerHint note={COMPOSER_HINT.message} />}
      {loopMode && (
        <div id={loopHintId} className="mesh-loop-hint">
          {LOOP_SYNTAX}
        </div>
      )}
      {intervalSlot !== null && (
        <div className="mesh-loop-intervals" role="group" aria-label="Interval examples">
          {LOOP_INTERVAL_EXAMPLES.map((example) => (
            <button key={example} type="button" className="mesh-loop-interval" aria-describedby={loopHintId} onClick={() => pickInterval(intervalSlot, example)}>
              {example}
            </button>
          ))}
        </div>
      )}

      {loopProblem !== null && (
        <div className="mesh-message-error" role="alert">
          {loopProblem}
        </div>
      )}
      {!loopMode && problems.length > 0 && (
        <ul className="mesh-message-error mesh-recipient-problems" role="alert">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      {(loopMode ? localError : (submitter.error ?? localError)) !== null && (
        <div className="mesh-message-error" role="alert">
          {loopMode ? localError : (submitter.error ?? localError)}
        </div>
      )}
      {loopOutcome !== null && <LoopOutcomeView outcome={loopOutcome} participants={participants} stages={mesh.stages} />}
      {loopMode ? null : submitter.pending ? (
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

// loopPayload — the request intent of a valid command; the target of an add is the participant it resolved to.
function loopPayload(command: LoopCommand, target: LoopTarget | null): LoopPayload {
  switch (command.kind) {
    case 'add':
      return { kind: 'add', target: target !== null && 'participant' in target ? target.participant.id : command.target, every: command.every, prompt: command.prompt, untilStageEnd: command.untilStageEnd }
    case 'stop':
      return { kind: 'stop', loopId: command.loopId }
    case 'list':
      return { kind: 'list' }
  }
}

// loopSendLabel distinguishes creating a loop from running list or stop, even during an incomplete draft.
function loopSendLabel(kind: 'add' | 'list' | 'stop' | null): string {
  return kind === 'list' || kind === 'stop' ? 'Run' : 'Start loop'
}

// loopTargetChip — the loop target as a chip of the "To" row's own kind.
function loopTargetChip(p: MeshParticipant, participants: MeshParticipant[]): ComposerRecipient {
  const handle = mentionHandles(participants).get(p.id) ?? p.id
  return { key: p.id, handle, participantId: p.id, source: 'typed', picked: false, typedTokens: [], problem: null, note: null }
}

// RecipientChip — one "To" chip: @handle; the stage agent chosen by default says so; a recipient
// with a problem is marked (the problem is listed under the input).
function RecipientChip({ recipient, participants, onRemove }: { recipient: ComposerRecipient; participants: MeshParticipant[]; onRemove?: () => void }): ReactElement {
  const p = participants.find((x) => x.id === recipient.participantId)
  const name = p !== undefined ? participantLabel(p) : recipient.handle
  return (
    <span
      className={`mesh-recipient-chip chat-composer-recipient${recipient.problem !== null ? ' has-problem' : ''}${recipient.source === 'default' ? ' is-default' : ''}`}
      title={recipient.problem ?? (recipient.source === 'default' ? 'Nobody named — the stage agent gets it' : name)}
    >
      <span className="mesh-recipient-handle">@{recipient.handle}</span>
      {recipient.source === 'default' && <span className="mesh-recipient-note">stage agent</span>}
      {recipient.note !== null && <span className="mesh-recipient-note">{recipient.note}</span>}
      {onRemove !== undefined && (
        <button type="button" className="mesh-recipient-remove chat-composer-recipient-remove" aria-label={`Remove @${recipient.handle}`} onClick={onRemove}>
          ×
        </button>
      )}
    </span>
  )
}

function submittedAttachments(actions: AttachmentActions | null, text: string): ImagePasteDraft {
  return actions?.sentIn(text) ?? []
}
