import { handleTextareaSubmit } from '../../lib/textarea-submit'
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { ComposerCard, ComposerHint, ComposerRecipients } from '../chat-composer/ComposerCard'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import type { MeshRoster } from '../../hooks/use-mesh'
import { errorMessage, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { MentionMenu, mentionText } from '../assistants/AssistantMentionMenu'
import { filterMentionGroups } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { AttachAssistantDialog } from '../assistants'
import {
  addMeshLoop, attachAndSendMesh, genMeshRequestId, listMeshLoops, sendMeshMessageTo, stopMeshLoop,
  type AttachAndSendMeshRequest, type MeshAttachAndSendResult, type MeshMessage, type MeshParticipant,
} from '../../api/mesh-client'
import { buildLoopMentionGroups, buildMeshMentionGroups, type MeshMentionItem } from './mesh-recipients'
import { LOOP_INTERVAL_EXAMPLES, LOOP_SYNTAX, loopIntervalSlot, loopSubcommand, parseLoopCommand, resolveLoopTarget, type IntervalSlot, type LoopCommand, type LoopTarget } from './loop-command'
import { LoopOutcomeView, type LoopOutcome } from './LoopResult'
import { awaitingAgentNeedsRestart, messageRoute, withoutRecipient, type ComposerRecipient, type ComposerRecipients as Recipients } from './composer-recipients'
import { postStageDiscussionMessage } from '../../api/run-client'
import { mentionHandles, participantLabel } from '../../lib/mesh-roster'
import { computeReceiptPhase, deliveryKey, sideAgentOutcome, RECEIPT_LABEL } from './mesh-receipt'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { Stage } from '../../types'
import type { AttachmentActions, ImagePasteDraft } from '../../hooks/use-image-paste'
import {
  EMPTY_COMPOSER_DRAFT, buildSubmission, cancelContext, chatIntentKey, draftRecipients, hasInput, nextLockToken, offersInterrupt,
  patchDraft, pickRecipient, setMode, snapshotSend, startChat, takeLock, withDefaultMode,
  type ChatIntent, type ComposerDraft, type ComposerMode, type SendSnapshot, type SubmissionEnv,
} from './composer-draft'
import { createComposerDraftStore, settleFailure, settleSuccess, useComposerDraft, type ComposerDraftStore } from './composer-draft-store'
import { SendModeSwitch } from './SendModeSwitch'
import { ReplyContextChip } from './ReplyContextChip'

export type { MeshReplyTarget } from './composer-draft'

// FeedComposerMeshProps — the mesh with collaboration on: Chat joins Restart with note. Without it the composer
// only restarts the stage agent with the note.
export type FeedComposerMeshProps = {
  // roster — the SAME useMeshRoster poll App.tsx already owns, passed down (no second poller here).
  roster: MeshRoster
  // profiles — configured helper roles, for the "Attach to stage" mention group.
  profiles: AssistantProfile[]
  // stages — for the recipients' own stage status (paused, waiting for helpers).
  stages: Stage[]
  // pick — the stage's people panel picked someone (seq tells two clicks on the same person apart): the
  // composer switches to Chat and adds them to the recipients. A pick made before this composer mounted
  // (another stage, no composer yet) is ignored.
  pick?: MeshPick | null
  // deliveries — the run's real delivery receipts (meshDeliveries over the run events), for "Delivered".
  deliveries?: ReadonlySet<string>
  sideFailures?: ReadonlySet<string>
}

// MeshPick — someone to add to the recipients.
export type MeshPick = { participantId: string; seq: number }

type FeedComposerProps = {
  stageId: string
  // onSend — Restart with note. It MUST reject when the note was not delivered: the draft is then kept.
  onSend: (text: string) => Promise<void>
  // draftStore — the run's drafts (App owns it, so a draft outlives this view); without it the composer keeps its own.
  draftStore?: ComposerDraftStore
  // focusRequest — changes when an action asks for the input (↩ on a thought, Reply in conversation); the value
  // seen at mount is ignored, so a restored draft never takes the focus (and never starts Hold) by itself.
  focusRequest?: number
  // conversationClosed — the conversation of the message being answered has been closed.
  conversationClosed?: boolean
  // holdActive — Hold has an operation on this stage: like started input, a late mesh keeps the mode then.
  holdActive?: boolean
  // --- Hold the agent while you type (its state lives in usePauseOnFocus in App) ---
  pauseOnFocusEnabled?: boolean
  onTogglePauseOnFocus?: (v: boolean) => void
  // paused — the stage is paused and we paused it: the amber banner and a tinted input row.
  paused?: boolean
  // onFocus asks for the pause (Restart only); onBlur tells whether a draft remains (an empty field resumes).
  onFocus?: () => void
  onBlur?: (hasDraft: boolean) => void
  onResumeNow?: () => void
  mesh?: FeedComposerMeshProps
  // reviseAvailable — the stage takes a Restart with note now (running, awaiting_approval, awaiting_assistants).
  reviseAvailable?: boolean
  // conversationMessages — the stage feed's conversation messages (FeedWorkspace's one poll), for the receipts.
  conversationMessages?: MeshMessage[]
}

// FeedComposer — the one input of a stage Feed. Its draft (text, attachments, reply context) lives in the run's
// store, so Chat and Restart with note send the same input and the draft outlives the view.
export function FeedComposer({
  stageId,
  onSend,
  draftStore,
  focusRequest = 0,
  conversationClosed = false,
  holdActive = false,
  pauseOnFocusEnabled = false,
  onTogglePauseOnFocus,
  paused = false,
  onFocus,
  onBlur,
  onResumeNow,
  mesh,
  reviseAvailable = true,
  conversationMessages = NO_MESSAGES,
}: FeedComposerProps): ReactElement {
  const [ownStore] = useState(createComposerDraftStore)
  const store = draftStore ?? ownStore
  const hasMesh = mesh !== undefined
  const stored = useComposerDraft(store, stageId)
  // shown — a stage nobody wrote to yet shows its default mode before the layout effect below stores it.
  const shown = useCallback((d: ComposerDraft) => (d === EMPTY_COMPOSER_DRAFT ? withDefaultMode(d, { hasMesh, holdActive: false }) : d), [hasMesh])
  const draft = shown(stored)
  const mode = draft.mode
  const edit = useCallback((fn: (d: ComposerDraft) => ComposerDraft) => store.update(stageId, fn), [store, stageId])

  // The mesh can arrive after the composer mounted: an untouched draft follows it, started input or an active
  // Hold keep the mode shown. A stage nobody wrote to has no mode in effect yet, so it just takes the default.
  const holdRef = useRef(holdActive)
  holdRef.current = holdActive
  useLayoutEffect(() => {
    store.update(stageId, (d) => withDefaultMode(d, { hasMesh, holdActive: d !== EMPTY_COMPOSER_DRAFT && holdRef.current }))
  }, [store, stageId, hasMesh])

  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  // The attachments' owner while this composer is mounted: a late success discards through it.
  const attachmentActions = useRef<AttachmentActions | null>(null)
  useLayoutEffect(() => store.registerAttachments(stageId, attachmentActions), [store, stageId])

  const participants = mesh?.roster.participants ?? NO_PARTICIPANTS
  const stages = mesh?.stages ?? NO_STAGES
  const stageStatus = stages.find((s) => s.id === stageId)?.status
  const restartBlock = reviseAvailable ? null : restartUnavailableReason(stageStatus)
  const recipientsFor = useCallback(
    (d: ComposerDraft): Recipients => (hasMesh ? draftRecipients(d, { participants, stages, stageId }) : NO_RECIPIENTS),
    [hasMesh, participants, stages, stageId],
  )
  const recipientState = useMemo(() => recipientsFor(draft), [recipientsFor, draft])
  const envWith = (recipients: Recipients): SubmissionEnv => ({
    stageId, hasMesh, restartBlock, recipients, conversationClosed, stageRunning: stageStatus === 'running',
  })
  const submission = buildSubmission(draft, envWith(recipientState))
  const pending = draft.inFlight !== null
  const chatMode = hasMesh && mode === 'chat'
  const loopMode = submission.kind === 'loop'

  const { recipients, defaultProblem } = recipientState
  const recipientIds = recipients.flatMap((r) => (r.participantId !== null ? [r.participantId] : [])).sort()
  const problems = recipients.flatMap((r) => (r.problem !== null ? [r.problem] : []))
  const route = messageRoute(stageId, recipientIds, draft.context?.kind === 'conversation')
  const offerInterrupt = chatMode && !loopMode && draft.attaching === null && offersInterrupt(route, stageStatus === 'running')
  // The stage agent waiting for its helpers reads a note only through Restart: say so where its problem is.
  const awaitingAgent = chatMode && !loopMode && awaitingAgentNeedsRestart({ recipients, defaultProblem, stageId, stages })

  // loopMode — the draft is a /loop command: an operation of its own, never a message. The reply, the picked
  // people, the stage agent and an attaching profile take no part in it.
  const loopParse = loopMode ? parseLoopCommand(draft.text) : null
  const loopCommand = loopParse?.ok === true ? loopParse.command : null
  // loopTargetToken — a resolved `@target` shows its row even while the rest of the command is still typed.
  const loopTargetToken = loopCommand?.kind === 'add' ? loopCommand.target : (loopParse !== null && !loopParse.ok ? loopParse.target : undefined)
  const loopTarget = loopTargetToken !== undefined ? resolveLoopTarget(loopTargetToken, participants, stageId) : null
  const loopProblem = loopTarget !== null && 'problem' in loopTarget ? loopTarget.problem : null
  const intervalSlot = loopMode ? loopIntervalSlot(draft.text) : null
  const loopHintId = useId()
  const reasonId = useId()
  // loopOutcome — the result of the last /loop command, shown until the next edit or Escape. Its own submitter
  // keeps the loop's request ids; the shared lock still keeps the same input from going out another way.
  const [loopOutcome, setLoopOutcome] = useState<LoopOutcome | null>(null)
  const loopSubmitter = useRetryableSubmit<LoopPayload>('mesh-feed-loop', stageId)
  const [localError, setLocalError] = useState<string | null>(null)
  const [lastSend, setLastSend] = useState<LastSend | null>(null)
  // attachPreview — an "Attach to stage" pick is never sent straight away: Send opens this preview with the text
  // it was opened for, and only its own "Attach and send" sends. Cancel keeps the draft.
  const [attachPreview, setAttachPreview] = useState<{ profileId: string; instruction: string; text: string } | null>(null)

  const setText = useCallback((text: string) => edit((d) => patchDraft(d, { text })), [edit])
  const { nodeRef: caretNodeRef, replaceRange } = useCaretInsert(draft.text, setText)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const setRefs = useCallback(
    (node: HTMLTextAreaElement | null) => {
      textareaRef.current = node
      caretNodeRef.current = node
    },
    [caretNodeRef],
  )

  const messageGroups = useMemo(() => buildMeshMentionGroups(participants, mesh?.profiles ?? NO_PROFILES, stageId), [participants, mesh?.profiles, stageId])
  const loopGroups = useMemo(() => buildLoopMentionGroups(participants, stageId), [participants, stageId])
  const groups = loopMode ? loopGroups : messageGroups
  // A person picked with @ stays in the text as @handle (a typed recipient); a profile to attach becomes a chip.
  const mention = useMentionMenu<MeshMentionItem>({
    matchesFor: (query) => filterMentionGroups(groups, query).flatMap((g) => g.items),
    showEmpty: true,
    textFor: (item) => (item.target.kind === 'profile' ? null : mentionText(item)),
    replaceRange,
    onPicked: (item) => {
      if (item.target.kind !== 'profile') return
      const attaching = { profileId: item.target.profileId, title: item.title }
      edit((d) => patchDraft(d, { attaching }))
    },
  })
  const matchGroups = mention.query !== null ? filterMentionGroups(groups, mention.query) : []

  // Focus only on an action that asks for it, never because a draft with a reply was restored.
  const seenFocus = useRef(focusRequest)
  useEffect(() => {
    if (focusRequest === seenFocus.current) return
    seenFocus.current = focusRequest
    textareaRef.current?.focus()
  }, [focusRequest])

  // A people-panel pick writes to that person: Chat, with them added, and the input focused.
  const pick = mesh?.pick ?? null
  const seenPickSeq = useRef(pick?.seq ?? 0)
  useEffect(() => {
    if (pick === null || pick.seq === seenPickSeq.current) return
    seenPickSeq.current = pick.seq
    edit((d) => pickRecipient(d, pick.participantId))
    textareaRef.current?.focus()
  }, [pick, edit])

  // Hold: only a focus while Restart is chosen pauses the agent; leaving an empty field resumes it in any mode.
  function handleFocus(): void {
    if (shown(store.get(stageId)).mode === 'restart') onFocus?.()
  }
  function handleBlur(): void {
    onBlur?.(hasInput(store.get(stageId)))
  }

  function handleChange(text: string): void {
    setText(text)
    setLocalError(null)
    setLoopOutcome(null)
    if (chatMode) mention.onInput(text, textareaRef.current?.selectionStart ?? text.length)
  }

  function removeRecipient(r: ComposerRecipient): void {
    edit((d) => {
      const next = withoutRecipient(r, { picked: d.picked, draft: d.text })
      return patchDraft(d, { picked: next.picked, text: next.draft })
    })
  }

  // openPicker — "+" opens the same @ list at the caret.
  function openPicker(): void {
    const el = textareaRef.current
    const caret = el?.selectionStart ?? draft.text.length
    const before = draft.text.slice(0, caret)
    const insert = before !== '' && !/\s$/.test(before) ? ' @' : '@'
    replaceRange(caret, caret, insert)
    mention.onInput(before + insert + draft.text.slice(caret), caret + insert.length)
    el?.focus()
  }

  // pickInterval — an interval example replaces the interval typed so far; the caret stays in the draft.
  function pickInterval(slot: IntervalSlot, example: string): void {
    replaceRange(slot.start, slot.end, `${slot.lead}${example} `)
    textareaRef.current?.focus()
  }

  // submit — Send and Enter do what the chosen mode says, from the draft as it is now.
  function submit(): void {
    const current = shown(store.get(stageId))
    const next = buildSubmission(current, envWith(recipientsFor(current)))
    switch (next.kind) {
      case 'blocked':
        return
      case 'loop':
        submitLoop(current.text)
        return
      case 'attach':
        setAttachPreview({ profileId: next.profileId, instruction: next.instruction, text: current.text })
        return
      case 'restart':
        sendRestart(next.body)
        return
      case 'chat':
        sendChat(next.intent)
    }
  }

  // Each send takes the shared lock before its request and settles in the request's own promise chain, through
  // the store it was sent from: the result applies even after this composer unmounted.
  function sendRestart(body: string): void {
    const token = nextLockToken()
    const locked = edit((d) => takeLock(d, 'restart', token))
    if (locked.inFlight?.token !== token) return
    const snap = snapshotSend(locked, 'restart', token)
    onSend(body).then(
      () => settleSuccess(store, stageId, snap),
      () => settleFailure(store, stageId, snap, ''),
    )
  }

  function sendChat(intent: ChatIntent): void {
    const token = nextLockToken()
    const locked = edit((d) => startChat(d, chatIntentKey(intent), token, genMeshRequestId('mesh-feed-send')))
    if (locked.inFlight?.token !== token || locked.unsettled === null) return
    const requestId = locked.unsettled.requestId
    const snap = snapshotSend(locked, 'chat', token, requestId)
    const request: Promise<{ messageId: string }> =
      intent.route.kind === 'stage'
        ? postStageDiscussionMessage(stageId, requestId, { body: intent.body, mentions: intent.route.mentions, interrupt: intent.interrupt })
        : sendMeshMessageTo({ requestId, recipientIds: intent.recipientIds, inReplyTo: intent.inReplyTo ?? undefined, body: intent.body, originStage: stageId }).then((r) => ({
            messageId: r.message_id,
          }))
    request.then(
      (receipt) => succeedWithReceipt(snap, { messageId: receipt.messageId, recipientIds: intent.recipientIds }),
      (err: unknown) => {
        settleFailure(store, stageId, snap, errorMessage(err))
        if (mounted.current) setLastSend(null)
      },
    )
  }

  // succeedWithReceipt — receipts belong to this composer and to the reply context the request was sent in.
  function succeedWithReceipt(snap: SendSnapshot, receipt: LastSend): void {
    const sameContext = store.get(stageId).contextGeneration === snap.contextGeneration
    settleSuccess(store, stageId, snap)
    if (mounted.current && sameContext) setLastSend(receipt)
  }

  // submitLoop — a malformed command is rejected visibly with the draft kept; a target problem is already shown.
  function submitLoop(text: string): void {
    const parse = parseLoopCommand(text)
    if (!parse.ok) {
      setLocalError(parse.error)
      return
    }
    const target = parse.command.kind === 'add' ? resolveLoopTarget(parse.command.target, participants, stageId) : null
    if ((target !== null && 'problem' in target) || loopSubmitter.pending) return
    const token = nextLockToken()
    const locked = edit((d) => takeLock(d, 'loop', token))
    if (locked.inFlight?.token !== token) return
    const snap = snapshotSend(locked, 'loop', token)
    setLocalError(null)
    setLoopOutcome(null)
    loopSubmitter.submit(
      loopPayload(parse.command, target),
      (p, requestId) =>
        runLoopCommand(p, requestId, stageId).then(
          (outcome) => {
            settleSuccess(store, stageId, snap)
            return outcome
          },
          (err: unknown) => {
            settleFailure(store, stageId, snap, errorMessage(err))
            throw err
          },
        ),
      { onSuccess: setLoopOutcome, onFailure: setLocalError },
    )
  }

  // sendAttach — the attach preview's request, under the shared lock: the dialog keeps its own request id and
  // error, the composer applies the result to the draft it was opened for.
  function sendAttach(req: AttachAndSendMeshRequest): Promise<MeshAttachAndSendResult> {
    const token = nextLockToken()
    const locked = edit((d) => takeLock(d, 'attach', token))
    if (locked.inFlight?.token !== token) return Promise.reject(new Error('This text is still being sent — wait for that send to finish'))
    const snap = snapshotSend({ ...locked, text: attachPreview?.text ?? locked.text }, 'attach', token)
    return attachAndSendMesh(req).then(
      (result) => {
        succeedWithReceipt(snap, { messageId: result.message_id ?? null, recipientIds: [result.participant_id] })
        return result
      },
      (err: unknown) => {
        settleFailure(store, stageId, snap, errorMessage(err))
        throw err
      },
    )
  }

  const canSend = loopMode ? loopProblem === null : submission.kind !== 'blocked'
  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    if (chatMode && mention.onKeyDown(e)) return
    if (e.key === 'Escape') setLoopOutcome(null)
    handleTextareaSubmit(e, submit, canSend && !pending)
  }

  const blocked = submission.kind === 'blocked' && submission.reason !== null ? submission : null
  const sendLabel = loopMode ? loopSendLabel(loopSubcommand(draft.text)) : mode === 'restart' ? 'Restart with note' : 'Send message'
  const sendVisibleLabel = loopMode || mode === 'restart' ? sendLabel : 'Send'
  const chatError = chatMode && !loopMode ? (draft.unsettled?.error ?? localError) : localError
  const receipts = deliveryReceipts(lastSend, participants, stages, conversationMessages, mesh)

  const stageAgentChip = participants.find((p) => p.kind === 'stage_agent' && p.stage_id === stageId)
  const pauseOption = onTogglePauseOnFocus !== undefined && (
    <label className={`switch${hasMesh ? ' feed-composer-mode-option' : ''}`}>
      <input type="checkbox" checked={pauseOnFocusEnabled} onChange={(e) => onTogglePauseOnFocus(e.target.checked)} />
      {!hasMesh && <span className="track" aria-hidden="true" />}
      <span className="switch-label">Hold the agent while you type</span>
    </label>
  )
  const modeOption = mode === 'restart' ? pauseOption : offerInterrupt && (
    <label className="feed-composer-mode-option" title="The agent stops its current turn, answers this message, then continues its work">
      <input type="checkbox" checked={draft.interrupt} onChange={(e) => edit((d) => patchDraft(d, { interrupt: e.target.checked }))} />
      <span>Interrupt agent</span>
    </label>
  )

  return (
    <div className={`feed-composer${hasMesh ? ' feed-composer--mesh' : ''}`}>
      {attachPreview !== null && mesh !== undefined && (
        <AttachAssistantDialog
          profileId={attachPreview.profileId}
          stageId={stageId}
          instruction={attachPreview.instruction}
          profiles={mesh.profiles}
          stages={mesh.stages.map((s) => ({ id: s.id, title: s.id }))}
          send={sendAttach}
          onCancel={() => setAttachPreview(null)}
          onConfirmed={() => setAttachPreview(null)}
        />
      )}
      {hasMesh ? (
        <div className="feed-composer-header">
          <SendModeSwitch mode={mode} restartShown={reviseAvailable || mode === 'restart'} onSelect={(m) => edit((d) => setMode(d, m))} />
          {modeOption && <div className="composer-opts">{modeOption}</div>}
        </div>
      ) : modeOption && <div className="composer-opts">{modeOption}</div>}
      {paused && (
        <div className="pause-banner" role="status">
          <span className="pause-banner-icon" aria-hidden="true">⏸</span>
          <span className="pause-banner-text">{mode === 'chat' ? PAUSED_IN_CHAT : PAUSED_IN_RESTART}</span>
          <button type="button" className="pause-banner-resume" onClick={onResumeNow}>
            Resume now
          </button>
        </div>
      )}
      {draft.context !== null && !loopMode && (
        <ReplyContextChip
          context={draft.context}
          mode={mode}
          closed={conversationClosed}
          canUseRestart={reviseAvailable}
          onRemove={() => edit(cancelContext)}
          onUseRestart={() => edit((d) => setMode(d, 'restart'))}
        />
      )}

      <ComposerCard paused={paused}>
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
        {chatMode && !loopMode && (
          <ComposerRecipients>
            {recipients.map((r) => (
              <RecipientChip
                key={r.key}
                recipient={r}
                participants={participants}
                // The stage agent by default and the recipient a reply requires (remove the reply) have no ×.
                onRemove={r.source === 'default' || r.source === 'reply' ? undefined : () => removeRecipient(r)}
              />
            ))}
            {draft.attaching !== null && (
              <span className="mesh-recipient-chip chat-composer-recipient is-attach" title="A new helper of this stage; the text is its assignment">
                Attach helper: {draft.attaching.title}
                <button type="button" className="mesh-recipient-remove chat-composer-recipient-remove" aria-label={`Don't attach ${draft.attaching.title}`} onClick={() => edit((d) => patchDraft(d, { attaching: null }))}>
                  ×
                </button>
              </span>
            )}
            {recipients.length === 0 && draft.attaching === null && <span className="mesh-recipients-empty">{defaultProblem ?? 'Pick someone to write to'}</span>}
            <button type="button" className="mesh-recipients-add chat-composer-add" aria-label="Add recipient" title="Add recipient (@)" onClick={openPicker}>
              <span aria-hidden="true">+</span> Add recipient
            </button>
          </ComposerRecipients>
        )}
        {hasMesh && mode === 'restart' && (
          // Restart with note goes to the stage agent only; Chat's recipients wait for the way back.
          <ComposerRecipients>
            {stageAgentChip !== undefined ? (
              <RecipientChip recipient={loopTargetChip(stageAgentChip, participants)} participants={participants} />
            ) : <span className="mesh-recipient-chip chat-composer-recipient">@{stageId}</span>}
          </ComposerRecipients>
        )}

        <PasteableTextarea
          stageId={stageId}
          maxHeight={MAX_TEXTAREA_HEIGHT}
          attachmentDraft={draft.attachments}
          attachmentActionsRef={attachmentActions}
          onAttachmentDraftChange={(attachments: ImagePasteDraft) => store.update(stageId, (d) => patchDraft(d, { attachments }))}
          textareaProps={{
            ...(chatMode ? mention.inputAria : {}),
            'aria-label': mode === 'chat' ? 'Message the mesh' : 'Restart instructions',
            'aria-describedby': loopMode ? loopHintId : undefined,
          }}
          textareaRef={setRefs}
          allowFileReferences
          attachInline
          toolbar={(
            <>
              {chatMode && <button type="button" className="chat-composer-mention" aria-label="Mention someone" title="Mention someone (@)" onClick={openPicker}>@</button>}
              <ChatSendButton
                label={sendLabel}
                visibleLabel={sendVisibleLabel}
                disabled={!canSend}
                sending={pending}
                paused={paused}
                describedBy={blocked !== null ? reasonId : undefined}
                onClick={submit}
              />
            </>
          )}
          className="chat-composer-input"
          value={draft.text}
          onChange={handleChange}
          onKeyDown={onKeyDown}
          onFocus={handleFocus}
          onBlur={handleBlur}
          placeholder={mode === 'chat' ? 'Ask or tell — type @ to bring someone in…' : 'New instructions — the agent restarts this phase with them…'}
          rows={1}
        />
        {chatMode && mention.open && (
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
      {!loopMode && <ComposerHint note={composerHint(mode, stageStatus, awaitingAgent)} />}
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

      {blocked !== null && (
        <div id={reasonId} className="feed-composer-blocked" role="status">
          <span>{blocked.reason}</span>
          {blocked.switchTo !== undefined && (
            <SwitchModeButton mode={blocked.switchTo} onSelect={(m) => edit((d) => setMode(d, m))} />
          )}
        </div>
      )}
      {loopProblem !== null && (
        <div className="mesh-message-error" role="alert">
          {loopProblem}
        </div>
      )}
      {chatMode && !loopMode && problems.length > 0 && (
        <ul className="mesh-message-error mesh-recipient-problems" role="alert">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      {awaitingAgent && reviseAvailable && <SwitchModeButton mode="restart" onSelect={(m) => edit((d) => setMode(d, m))} />}
      {chatError !== null && (
        <div className="mesh-message-error" role="alert">
          {chatError}
        </div>
      )}
      {loopOutcome !== null && <LoopOutcomeView outcome={loopOutcome} participants={participants} />}
      {hasMesh && !loopMode && (draft.inFlight?.op === 'chat' ? (
        <div className="mesh-message-receipt" role="status">
          {RECEIPT_LABEL.awaiting}
        </div>
      ) : (
        chatMode && receipts.length > 0 && (
          <ul className="mesh-message-receipt mesh-message-receipts" role="status" aria-label="Delivery">
            {receipts.map((r) => (
              <li key={r.id}>
                <span className="mesh-receipt-name">{r.name}</span> · {r.label}
              </li>
            ))}
          </ul>
        )
      ))}
    </div>
  )
}

// SwitchModeButton — the explicit way to the other mode when the chosen one cannot send.
function SwitchModeButton({ mode, onSelect }: { mode: ComposerMode; onSelect: (mode: ComposerMode) => void }): ReactElement {
  return (
    <button type="button" className="mesh-recipients-use-revise" onClick={() => onSelect(mode)}>
      {mode === 'chat' ? 'Use Chat' : 'Use Restart with note'}
    </button>
  )
}

const PAUSED_IN_RESTART = "Stage paused. The agent is on hold — it'll resume when you send."
const PAUSED_IN_CHAT = "Stage paused by you — Chat messages don't resume it."

// composerHint — what Send does in the chosen mode, without promising work a finished agent will not do.
function composerHint(mode: ComposerMode, stageStatus: string | undefined, awaitingAgent: boolean): string {
  if (mode === 'restart') return 'The agent stops and restarts its current phase with your note.'
  if (stageStatus === 'done' || stageStatus === 'failed') return "Only the recipients above receive this. This stage has finished — its agent won't read it."
  if (awaitingAgent) return 'Only the recipients above receive this.'
  return 'Only the recipients above receive this. The agent keeps working on its task.'
}

// restartUnavailableReason — why a stage that is shown takes no Restart with note now (App's reviseAvailable gate).
function restartUnavailableReason(status: string | undefined): string {
  if (status === undefined) return 'the stage is not running'
  if (status === 'revising') return 'the agent is already restarting with a note'
  if (status === 'done' || status === 'failed') return 'the stage has finished'
  if (status === 'running' || status === 'awaiting_approval' || status === 'awaiting_assistants') return 'the run is paused'
  return `the stage is ${status.replace(/_/g, ' ')}`
}

const NO_MESSAGES: MeshMessage[] = []
const NO_PARTICIPANTS: MeshParticipant[] = []
const NO_STAGES: Stage[] = []
const NO_PROFILES: AssistantProfile[] = []
const NO_RECIPIENTS: Recipients = { recipients: [], defaultProblem: null }
const MAX_TEXTAREA_HEIGHT = 200

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

// LastSend — the most recent successful send, for its per-recipient receipts. messageId is null when an
// attach-and-send result carried no message id yet.
type LastSend = { messageId: string | null; recipientIds: string[] }

// deliveryReceipts — each recipient of the last send with its own state, "Answered" once that one replied.
function deliveryReceipts(
  lastSend: LastSend | null,
  participants: MeshParticipant[],
  stages: Stage[],
  conversationMessages: MeshMessage[],
  mesh: FeedComposerMeshProps | undefined,
): { id: string; name: string; label: string }[] {
  if (lastSend === null) return []
  const stageStatus = (id: string | undefined): string | undefined => stages.find((s) => s.id === id)?.status
  return lastSend.recipientIds.map((id) => {
    const participant = participants.find((p) => p.id === id)
    const sideOutcome = lastSend.messageId === null ? null : sideAgentOutcome(
      { id: lastSend.messageId, recipient_ids: lastSend.recipientIds }, conversationMessages, mesh?.sideFailures ?? new Set(), id,
    )
    const phase = sideOutcome ?? computeReceiptPhase({
      participant,
      stagePaused: participant?.stage_id !== undefined && stageStatus(participant.stage_id) === 'paused',
      answered: lastSend.messageId !== null && conversationMessages.some((m) => m.in_reply_to === lastSend.messageId && m.sender_id === id),
      inFlight: lastSend.messageId === null,
      delivered: lastSend.messageId !== null && mesh?.deliveries?.has(deliveryKey(lastSend.messageId, id)) === true,
    })
    return { id, name: participant !== undefined ? participantLabel(participant) : 'New agent', label: RECEIPT_LABEL[phase] }
  })
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

// loopTargetChip — a single participant (the loop target, the stage agent of a restart) as a "To" chip.
function loopTargetChip(p: MeshParticipant, participants: MeshParticipant[]): ComposerRecipient {
  const handle = mentionHandles(participants).get(p.id) ?? p.id
  return { key: p.id, handle, participantId: p.id, source: 'typed', picked: false, typedTokens: [], problem: null, note: null }
}

// RecipientChip — one "To" chip: @handle; the stage agent chosen by default says so, the recipient a reply
// requires says "in reply"; a recipient with a problem is marked (the problem is listed under the input).
function RecipientChip({ recipient, participants, onRemove }: { recipient: ComposerRecipient; participants: MeshParticipant[]; onRemove?: () => void }): ReactElement {
  const p = participants.find((x) => x.id === recipient.participantId)
  const name = p !== undefined ? participantLabel(p) : recipient.handle
  const note = recipient.note ?? (recipient.source === 'reply' ? 'in reply' : null)
  const title = recipient.problem ?? (recipient.source === 'default' ? 'Nobody named — the stage agent gets it' : recipient.source === 'reply' ? 'Gets this reply — remove the reply to drop them' : name)
  return (
    <span className={`mesh-recipient-chip chat-composer-recipient${recipient.problem !== null ? ' has-problem' : ''}${recipient.source === 'default' ? ' is-default' : ''}`} title={title}>
      <span className="mesh-recipient-handle">@{recipient.handle}</span>
      {recipient.source === 'default' && <span className="mesh-recipient-note">stage agent</span>}
      {note !== null && <span className="mesh-recipient-note">{note}</span>}
      {onRemove !== undefined && (
        <button type="button" className="mesh-recipient-remove chat-composer-recipient-remove" aria-label={`Remove @${recipient.handle}`} onClick={onRemove}>
          ×
        </button>
      )}
    </span>
  )
}
