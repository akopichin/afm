import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { cancelNotes, continueStage, pauseStage, reviseStage } from '../api/run-client'
import { GlobalHeader } from '../components/global-header'
import { StagesList, canAttachHelper } from '../components/stages-list'
import { AssistantTaskView, AttachHelperDialog, AddStageDialog } from '../components/assistants'
import { StagePeoplePanel, STAGE_PEOPLE_ID, STAGE_PEOPLE_DRAWER_QUERY, TeamView } from '../components/team'
import { BoardLoadState } from '../components/team/BoardLoadState'
import { BoardView } from '../components/team/BoardView'
import { teamStagesOf } from '../components/team/RunTeamList'
import { StageNote } from '../components/stage-note'
import { ReviewNotesModal } from '../components/review-notes-modal'
import { PlanPanel } from '../components/plan-panel'
import { DialogChannel } from '../components/dialog-channel'
import { FeedWorkspace, meshDeliveries, type ComposerDraft, type MeshPick } from '../components/feed-workspace'
import { emptyComposerDraft } from '../components/feed-workspace/FeedComposer'
import { sideAgentFailures } from '../components/feed-workspace/mesh-receipt'
import { CostPanel } from '../components/cost-panel'
import { MaximizeProvider } from '../components/layout/Maximizable'
import { DashboardShell } from '../components/layout/DashboardShell'
import { WorkspaceTabs, WorkspaceHeader, AttentionBanner, type WorkspaceTabDescriptor } from '../components/workspace'
import { createStageActions, StageActionsContext } from '../hooks/use-stage-retry'
import { CommentProvider } from '../components/comments/CommentProvider'
import { CommentRecipientsContext } from '../components/comments/comment-recipients'
import { FileBrowserProvider } from '../components/file-browser'
import { SideAgentProvider } from '../components/side-agent'
import { MessageTimeZoneProvider } from '../lib/message-time-zone'
import { DialogOpenContext } from '../lib/focus-trap'
import { ReviewBanner } from '../components/review-banner'
import { useStatus } from '../hooks/use-status'
import { useMediaQuery } from '../hooks/use-media-query'
import { useSelectedStage } from '../hooks/use-selected-stage'
import { useAssistantMaxDepth, useAssistantProfiles } from '../hooks/use-assistant-profiles'
import { useStageAssistants } from '../hooks/use-stage-assistants'
import { useMeshBoard, useMeshRoster } from '../hooks/use-mesh'
import { mergeById } from '../lib/mesh-roster'
import { baselineTeamUnread } from '../lib/team-unread'
import type { MeshParticipant, MeshThread, MeshThreadSummaries } from '../api/mesh-client'
import { RUN_CONVERSATIONS_ID, RunConversationsPanel, runConversationThreads } from '../components/run-conversations/RunConversationsPanel'
import { useRunConversationsSeen } from '../components/run-conversations/run-conversations-seen'
import { useReviewNoteCount } from '../hooks/use-review-note-count'
import { useEventFeed } from '../hooks/use-event-feed'
import { useStageEvents } from '../hooks/use-stage-events'
import { useRunEpoch } from '../hooks/use-run-epoch'
import { useElapsed } from '../hooks/use-elapsed'
import { useStageClock } from '../hooks/use-stage-clock'
import { hasOpenTiming, stageTimes } from '../lib/stage-timing'
import { useIdleMs } from '../hooks/use-idle-ms'
import { useBackoffMs } from '../hooks/use-backoff-ms'
import { anyAwaiting } from '../hooks/use-attention'
import { attentionKindForStatus, countByKind, sig, useWorkspaceView, type AttentionKind } from '../hooks/use-workspace-view'
import { useTitleFlash } from '../hooks/use-title-flash'
import { useFaviconPulse } from '../hooks/use-favicon-pulse'
import { useDesktopNotifications } from '../hooks/use-desktop-notifications'
import { usePauseOnFocus } from '../hooks/use-pause-on-focus'
import { useIsEditing } from '../hooks/use-is-editing'
import { SIGNIFICANT_EVENT_TYPES } from '../types'

// Подписи контекстной вкладки воркспейса по виду attention (Approval/Question/…).
const ATTENTION_TAB_LABEL: Record<AttentionKind, string> = {
  approval: 'Approval',
  question: 'Question',
  failed: 'Failed',
  hook_failed: 'Hook failed',
  paused: 'Paused',
  assistants: 'Helpers',
}

// Подпись ждущего действия в шапке файлового оверлея.
const ATTENTION_SHORTCUT_LABEL: Record<AttentionKind, string> = {
  approval: 'Approval waiting',
  question: 'Question waiting',
  failed: 'Stage failed',
  hook_failed: 'Hook failed',
  paused: 'Stage paused',
  assistants: 'Helper decision needed',
}

// REVISE_STATUSES — the statuses POST /api/stages/{id}/revise accepts (pkg/server handleRevise).
const REVISE_STATUSES: ReadonlySet<string> = new Set(['running', 'awaiting_approval', 'awaiting_assistants'])
// NO_DELIVERIES — no delivery receipts (the stage Feed's mesh UI is off).
const NO_DELIVERIES: ReadonlySet<string> = new Set()
// NO_PARTICIPANTS — no locally added run agents in the current run.
const NO_PARTICIPANTS: MeshParticipant[] = []
// NO_SUMMARIES — no thread summaries (the roster has not reported any).
const NO_SUMMARIES: MeshThreadSummaries = {}
const NO_THREADS: MeshThread[] = []

// Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер.
export function App(): ReactElement {
  const {
    flowName, stages, startedAt, endedAt, elapsedAccumulatedMs, elapsedSince,
    description, version, idleAccumulatedMs, idleSince, backoffAccumulatedMs,
    backoffOpenSince, capabilities, graphRevision, runId, flowPauseState, flowPausedStages, collaborationMode, runCost,
    runOverheadCost, coverageIssues, accounting, statusAvailable, refresh, runStatus,
    timeZone, utcOffsetMinutes,
  } = useStatus()
  const runEpoch = useRunEpoch(runId)
  const stageActions = useMemo(createStageActions, [runEpoch])

  // Модалка ревью-раунда — открывается по клику ReviewBanner's "Send notes".
  const [reviewModalOpen, setReviewModalOpen] = useState(false)

  const [attachHelperStageId, setAttachHelperStageId] = useState<string | null>(null)
  const [addStageOpen, setAddStageOpen] = useState(false)

  // «Pause on focus» — при включённом toggle фокус поля заметки ставит running- стадию на паузу, а
  // отправка сперва снимает паузу, затем доставляет заметку.
  const pauseOnFocus = usePauseOnFocus({ pauseStage, continueStage, reviseStage, runEpoch })

  const reviewNoteCount = useReviewNoteCount(flowPauseState === 'paused')

  // Отмена ревью не требует целевой стадии; fire-and-forget вызов обязательно обрабатывает reject.
  function handleReviewCancel(): void {
    cancelNotes().catch((err: unknown) => {
      console.error('Failed to cancel review notes:', err)
    })
  }

  // Открываем ReviewNotesModal для выбора целевой стадии перед injectNotes.
  function handleReviewSend(): void {
    setReviewModalOpen(true)
  }

  const wsUrl = buildWebSocketUrl()
  const { events, connected, activity: liveActivity } = useEventFeed(wsUrl, runId)

  // Единый workspace-view-редьюсер — источник истины для того, ЧТО показано справа от рейла: Feed,
  // контекстный attention или read-only просмотр истории плана/диалога.
  const [filesOpen, setFilesOpen] = useState(false)
  // sideAgentOpen — видимость «бокового» агента. Входит в anyModalOpen, чтобы attention других стадий не
  // авто-открывался за открытой беседой (только сигналил).
  const [sideAgentOpen, setSideAgentOpen] = useState(false)
  // openDialogs — диалоги внутри видов (New task, Task details, Add run agent), открытые через useModalDialog:
  // они сообщают о себе через DialogOpenContext и тоже входят в anyModalOpen. openDialogsRef — то же число
  // сразу, без ожидания render: синк attention того же commit читает его через overlayOpenNow.
  const [openDialogs, setOpenDialogs] = useState(0)
  const openDialogsRef = useRef(0)
  const reportDialogOpen = useCallback((open: boolean) => {
    openDialogsRef.current += open ? 1 : -1
    setOpenDialogs(openDialogsRef.current)
  }, [])
  const dialogOpenNow = useCallback(() => openDialogsRef.current > 0, [])
  const otherModalsOpen = filesOpen || reviewModalOpen || attachHelperStageId !== null || addStageOpen || openDialogs > 0
  const anyModalOpen = otherModalsOpen || sideAgentOpen
  // selfOwnedActive — у какой-то стадии есть незавершённая «Pause on focus»-операция.
  const selfOwnedActive = stages.some((s) => pauseOnFocus.shouldSuppressAttention(s.id))
  const editing = useIsEditing() || anyModalOpen || selfOwnedActive
  // ownedHandledSigs — подписи НАШИХ же paused-эпизодов: их auto-open подавляется навсегда, а не
  // откладывается.
  const ownedHandledSigs = stages
    .filter((s) => s.status === 'paused' && pauseOnFocus.shouldSuppressAttention(s.id))
    .map((s) => sig({ stageId: s.id, kind: 'paused', episode: s.updatedAt }))
  const { state: wsState, activeItem: attnItem, openFeed, openCost, openAttention, openHistory, openAssistant, openTeam, openBoard } = useWorkspaceView(stages, editing, ownedHandledSigs, dialogOpenNow)

  // meshRoster (Agent Mesh, T7.3) — the ONE useMeshRoster poll for the whole dashboard; there is no
  // status.capabilities.mesh field (unlike collaboration/addStage/sideAgent below), so this hook's own
  // probe-the-first-fetch result IS the capability signal the Team tab gates on (see use-mesh-roster.ts's own
  // comment). TeamView receives the SAME roster as a prop rather than polling it a second time.
  const meshRoster = useMeshRoster(runId)
  // teamOpenedRun — the run whose Team was opened in this page; from then on that Team stays mounted (hidden on
  // other views). A new run mounts its Team only once Team is opened in it.
  const [teamOpenedRun, setTeamOpenedRun] = useState<string | null>(null)
  useEffect(() => {
    if (wsState.view === 'team') setTeamOpenedRun(runId)
  }, [wsState.view, runId])
  useEffect(() => {
    if (runId !== '' && meshRoster.runId === runId && meshRoster.enabled) {
      baselineTeamUnread(runId, meshRoster.summaries ?? {})
    }
  }, [runId, meshRoster])
  // teamViewsAvailable — Team/Board data is wanted only with a successful mesh probe, a known run and the `team`
  // collaboration mode. collaborationMode stays at its last resolved value through a status outage (unlike
  // statusAvailable), so a temporary /api/status failure does not stop the board poll.
  const teamViewsAvailable = meshRoster.enabled && runId !== '' && collaborationMode === 'team'
  // meshBoard — the ONE useMeshBoard poll: Team's Board tab and the run-wide Board tab read the same value, so
  // switching between them never refetches. It starts only once teamViewsAvailable.
  const meshBoard = useMeshBoard(teamViewsAvailable, runId)
  // runConversationsAvailable — the right-hand Run conversations panel replaces Team in `assistants` mode: the
  // same mesh probe and run id, the other mode. Its open state lives here because the opener sits in the tab strip.
  const runConversationsAvailable = meshRoster.enabled && runId !== '' && collaborationMode === 'assistants'
  const [runConversationsOpen, setRunConversationsOpen] = useState(false)
  const closeRunConversations = useCallback(() => setRunConversationsOpen(false), [])
  // The stage Feed's people panel: the wide panel (open by default) and the narrow drawer (closed) keep separate
  // flags, so resizing never opens the drawer or forgets the wide choice. peoplePick: the last person picked there.
  const peopleDrawer = useMediaQuery(STAGE_PEOPLE_DRAWER_QUERY)
  const [peopleShown, setPeopleShown] = useState({ wide: true, drawer: false })
  const peopleOpen = peopleDrawer ? peopleShown.drawer : peopleShown.wide
  // Changes only the layout in effect now.
  const setPeopleOpen = useCallback(
    (open: boolean) => setPeopleShown((shown) => (peopleDrawer ? { ...shown, drawer: open } : { ...shown, wide: open })),
    [peopleDrawer],
  )
  const [feedReplies, setFeedReplies] = useState<{ stageId: string | null; count: number }>({ stageId: null, count: 0 })
  const reportFeedReplies = useCallback((stageId: string | null, count: number) => setFeedReplies({ stageId, count }), [])
  const [peoplePick, setPeoplePick] = useState<MeshPick | null>(null)
  const closePeople = useCallback(() => setPeopleOpen(false), [setPeopleOpen])
  // addedParticipants — run agents added from the people panel that the roster poll has not reported yet.
  // The panel AND the Feed composer use the merged list, so a click on a just-added agent addresses it. They
  // belong to the run they were added in: another run starts without them.
  const [added, setAdded] = useState<{ runId: string; participants: MeshParticipant[] }>({ runId, participants: [] })
  const addedParticipants = added.runId === runId ? added.participants : NO_PARTICIPANTS
  const meshParticipants = useMemo(() => mergeById(meshRoster.participants, addedParticipants), [meshRoster.participants, addedParticipants])
  const mergedRoster = useMemo(() => ({ ...meshRoster, participants: meshParticipants }), [meshRoster, meshParticipants])
  // runSeen — which run conversations hold an agent message to the human that was not seen yet. App owns it
  // because the opener (tab strip) and the panel show the same state; the cursor survives a reload.
  const runConversations = useMemo(
    () => (runConversationsAvailable ? runConversationThreads(meshRoster.threads, meshParticipants) : NO_THREADS),
    [runConversationsAvailable, meshRoster.threads, meshParticipants],
  )
  const runSeen = useRunConversationsSeen(runId, runConversations, meshRoster.summaries ?? NO_SUMMARIES)
  const runUnreadCount = runSeen.unreadThreadIds.size
  const addParticipant = useCallback(
    (p: MeshParticipant) => setAdded((prev) => ({ runId, participants: prev.runId === runId ? [...prev.participants, p] : [p] })),
    [runId],
  )

  // Забываем Pause-on-focus ownership после внешнего Continue или завершения стадии, чтобы Send не вызвал
  // лишний Continue.
  useEffect(() => {
    pauseOnFocus.reconcile(stages.map((s) => ({ id: s.id, status: s.status })))
  }, [stages, pauseOnFocus.reconcile])
  const [selection, setSelection] = useSelectedStage(
    stages,
    wsState.view === 'attention' ? attnItem?.stageId ?? null : null,
  )
  // selectedStageId — какая стадия подсвечена активной в рейле, ВНЕ зависимости от того, смотрит ли
  // пользователь на саму стадию или на один из её помощников (Selection.stageId несёт родителя в обоих
  // случаях). selectedTaskId — только когда выбран конкретный помощник.
  const selectedStageId = selection?.stageId ?? null
  const selectedTaskId = selection?.kind === 'assistant' ? selection.taskId : null

  // assistantsByStage — дерево задач помощников КАЖДОЙ стадии, из ОТДЕЛЬНОГО поллинга (useStageAssistants),
  // а не из /api/status (AGENTS.md: статус не раздувается). Капабилити выключена → хук не делает запросов.
  const assistantsByStage = useStageAssistants(stages.map((s) => s.id), capabilities.collaboration)
  const selectedTaskStageTasks = selection?.kind === 'assistant' ? assistantsByStage[selection.stageId]?.tasks ?? [] : []
  const selectedTask =
    selection?.kind === 'assistant'
      ? selectedTaskStageTasks.find((t) => t.id === selection.taskId) ?? null
      : null
  // assistantMaxDepth — the run's effective delegation-depth ceiling (T4a.8's breadcrumb "depth N of max_depth"),
  // independent of assistantsByStage's own per-stage poll (a run-wide, rarely-changing number).
  const assistantMaxDepth = useAssistantMaxDepth(capabilities.collaboration)
  // assistantProfiles — configured helper roles, for the "Attach helper…" dialog. Same collaboration gate as
  // the rest; empty list when the capability is off (no network calls).
  const assistantProfiles = useAssistantProfiles(capabilities.collaboration)

  // If collaboration is disabled, a previously selected helper view returns to Feed.
  useEffect(() => {
    if (!capabilities.collaboration && wsState.view === 'assistant') openFeed()
  }, [capabilities.collaboration, wsState.view])

  // Защита: мэш-капабилити определяется пробным запросом (meshRoster.enabled), а не статическим полем
  // статуса — тот же паттерн, что и у collaboration/accounting выше: застрявший на 'team' вид (капабилити
  // отключили на бэкенде между опросами) уводим в Feed.
  useEffect(() => {
    if (meshRoster.probed && !meshRoster.enabled && (wsState.view === 'team' || wsState.view === 'board')) openFeed()
  }, [meshRoster.probed, meshRoster.enabled, wsState.view])

  // Тот же уход в Feed, когда режим сменился на `assistants`: Team/Board там не существуют. Условие на mode, а не
  // `!teamViewsAvailable` — перепроба мэша временно сбрасывает meshRoster.enabled и не должна выкидывать из Team.
  useEffect(() => {
    if (collaborationMode === 'assistants' && (wsState.view === 'team' || wsState.view === 'board')) openFeed()
  }, [collaborationMode, wsState.view])

  // Фокус стоял на Team/Board/People-контроле, который пропал при смене режима на `assistants`, — браузер уронил
  // его на body: возвращаем на вкладку Feed. teamUiShown помнит, что эти контролы действительно были на экране, чтобы
  // не отбирать фокус на первом же статусе.
  const teamUiShown = useRef(false)
  useEffect(() => {
    if (collaborationMode === 'assistants' && teamUiShown.current && document.activeElement === document.body) {
      document.querySelector<HTMLElement>('.workspace-tabs [data-tab-id="feed"]')?.focus()
    }
    teamUiShown.current = teamViewsAvailable
  }, [collaborationMode, teamViewsAvailable])

  // Клик по строке помощника в рейле: выбираем задачу и открываем её ленту.
  function handleSelectAssistantTask(stageId: string, taskId: string): void {
    setSelection({ kind: 'assistant', stageId, taskId })
    setScrollToDialogTarget(null)
    openAssistant()
  }

  // После активации Cost из поповера переносим фокус на вкладку Cost; inline-клик сохраняет свой фокус.
  const [costFocusPending, setCostFocusPending] = useState(false)
  function handleOpenCost(fromPopover: boolean): void {
    openCost()
    if (fromPopover) setCostFocusPending(true)
  }
  useEffect(() => {
    if (!costFocusPending || wsState.view !== 'cost') return
    document.querySelector<HTMLElement>('.workspace-tabs [data-tab-id="cost"]')?.focus()
    setCostFocusPending(false)
  }, [costFocusPending, wsState.view])

  // Стадия, о которой сейчас говорит воркспейс: в attention-режиме — активный элемент очереди; иначе —
  // выбранная в рейле стадия.
  const workspaceStageId = wsState.view === 'attention' && attnItem !== null ? attnItem.stageId : selectedStageId
  const workspaceStage = stages.find((stage) => stage.id === workspaceStageId) ?? null

  // Keep Hold's resume route until polling leaves the paused episode, even after its request settles.
  const heldPauseEpisodes = useMemo(() => new Map<string, string>(), [runId])
  for (const stage of stages) {
    if (stage.status !== 'paused') heldPauseEpisodes.delete(stage.id)
    else if (pauseOnFocus.hasActiveOp(stage.id)) heldPauseEpisodes.set(stage.id, stage.updatedAt)
  }
  const heldWorkspacePause = workspaceStage !== null && (pauseOnFocus.hasActiveOp(workspaceStage.id) ||
    (workspaceStage.status === 'paused' && heldPauseEpisodes.get(workspaceStage.id) === workspaceStage.updatedAt))

  const heldContinue = heldWorkspacePause && workspaceStage !== null ? () => pauseOnFocus.resumeNow(workspaceStage.id) : undefined

  // Feed follows the selected stage, using its replayed events as well as live updates.
  const stageEvents = useStageEvents(workspaceStage?.id ?? null, events, runId)

  // Клик по стадии в рейле: выбираем её и открываем подходящий вид — attention, если стадия ждёт действия;
  // иначе Feed для завершённых/ещё не начавшихся стадий; историю живой стадии — если есть.
  function handleSelectStage(stageId: string): void {
    setSelection({ kind: 'stage', stageId })
    // Ручной выбор другой стадии инвалидирует прицельный скролл к Q&A, заданный кликом по элементу ленты —
    // иначе, если у новой стадии случайно совпадёт phase/id, DialogChannel проскроллит не туда.
    setScrollToDialogTarget(null)
    const stage = stages.find((s) => s.id === stageId) ?? null
    const kind = stage === null ? null : attentionKindForStatus(stage)
    if (kind !== null) {
      openAttention(stageId)
      return
    }
    // Нет ожидающего действия.
    if (wsState.view === 'feed' || wsState.view === 'cost' || wsState.view === 'team' || wsState.view === 'board') return
    if (stage?.status === 'done' || stage?.status === 'pending') openFeed()
    else if (stage?.showDialog === true) openHistory('dialog-history')
    else if (stage?.showPlan === true) openHistory('plan-history')
    else openFeed()
  }

  // Прицельный переход из ленты по клику на dialog_question/dialog_answer элемент: выбираем стадию,
  // открываем диалог и запоминаем, к какому именно Q&A прокрутить.
  const [scrollToDialogTarget, setScrollToDialogTarget] = useState<{ stageId: string; phase: string; id: string } | null>(null)
  function handleOpenDialogFromFeed(stageId: string, phase: string, id: string): void {
    setSelection({ kind: 'stage', stageId })
    const stage = stages.find((s) => s.id === stageId) ?? null
    if (stage?.status === 'awaiting_user_input') openAttention(stageId)
    else openHistory('dialog-history')
    setScrollToDialogTarget({ stageId, phase, id })
  }

  // Вызывается DialogChannel'ом сразу после того, как scrollToDialogTarget был успешно применён — родитель
  // обязан сбросить цель, иначе она переживёт своё назначение.
  const handleDialogTargetConsumed = useCallback((): void => {
    setScrollToDialogTarget(null)
  }, [])

  // anyAttention (любая стадия прогона ждёт) — пульс favicon (шапочная точка
  // была убрана в этой ветке — пульс остался единственным глобальным сигналом).
  const anyAttention = anyAwaiting(stages)
  // Title flash завязан на ГЛОБАЛЬНОЕ непросмотренное ожидание, а не только на текущую workspace-стадию:
  // мигаем, когда в очереди есть ожидание, но воркспейс сейчас НЕ в attention-виде.
  useTitleFlash(wsState.items.length > 0 && wsState.view !== 'attention')
  useFaviconPulse(anyAttention)
  const {
    enabled: notificationsEnabled,
    permission: notificationsPermission,
    requestEnable: onRequestEnableNotifications,
    disable: onDisableNotifications,
  } = useDesktopNotifications(stages, handleSelectStage, flowName)

  // Заголовок вкладки берём из description флоу, иначе имя флоу, иначе дефолт.
  useEffect(() => {
    document.title = (description ?? '').trim() || flowName || 'afm Dashboard'
  }, [description, flowName])

  // showPlan/showDialog capabilities are computed server-side per stage — the client only adds the
  // "nothing selected → show both, neutral state" rule on top.
  const showPlan = workspaceStage === null || workspaceStage.showPlan
  const showDialog = workspaceStage === null || workspaceStage.showDialog

  // noteTarget — стадия, живому агенту которой можно послать заметку из messenger-поля ленты: running И
  // не скрипт, ЛИБО есть незавершённая Pause-on-focus операция (hasActiveOp) — композер остаётся
  // смонтированным на всём цикле pause→набор→send→resume, иначе поле и черновик исчезли бы, когда стадия
  // мигнула в paused/resuming. Прочие статусы/скрипты и flowPauseState!=='none' → null (поле не показываем).
  const legacyNoteTarget =
    workspaceStage !== null &&
    flowPauseState === 'none' &&
    !workspaceStage.isScript &&
    (workspaceStage.status === 'running' || pauseOnFocus.hasActiveOp(workspaceStage.id))
      ? workspaceStage.id
      : null

  // stageMesh — the stage Feed's mesh UI (People panel, its conversations, the Message composer mode). It needs
  // both the mesh and collaboration: with collaboration off the stage Feed is the plain pre-mesh Feed. (The
  // Team tab keeps its own gate, the mesh probe alone.)
  const stageMesh = meshRoster.enabled && capabilities.collaboration
  // commentRoster — the roster the file-comment recipient picker reads: the same poll as the Feed, with the same
  // gate; without the mesh only the Assistant (side-agent capability alone) can receive comments.
  const commentRoster = useMemo(
    () => ({ runId, participants: stageMesh ? meshParticipants : NO_PARTICIPANTS, stages }),
    [runId, stageMesh, meshParticipants, stages],
  )
  // meshDeliveryReceipts — the run's real per-recipient delivery receipts, for the composer's "Delivered".
  // composerDrafts — every stage's Feed composer draft for this page session (FeedWorkspace's composerDrafts).
  // A new run starts without them: the same stage ID in the next run is another conversation.
  const composerDrafts = useRef({ runEpoch, drafts: new Map<string, ComposerDraft>() })
  const [, refreshComposerDrafts] = useState(0)
  if (composerDrafts.current.runEpoch !== runEpoch) composerDrafts.current = { runEpoch, drafts: new Map() }
  const draftPreviews = useMemo(() => new Set<string>(), [runEpoch])
  useEffect(() => () => { draftPreviews.forEach((url) => URL.revokeObjectURL(url)) }, [draftPreviews])
  const updateComposerDraft = useCallback((stageId: string, patch: Partial<ComposerDraft>) => {
    for (const attachments of [patch.reviseAttachments, patch.messageAttachments, patch.replyAttachments]) {
      attachments?.forEach((attachment) => draftPreviews.add(attachment.previewUrl))
    }
    if (composerDrafts.current.runEpoch !== runEpoch) return
    const previous = composerDrafts.current.drafts.get(stageId) ?? emptyComposerDraft()
    if (Object.entries(patch).every(([key, value]) => previous[key as keyof ComposerDraft] === value)) return
    composerDrafts.current = { runEpoch, drafts: new Map(composerDrafts.current.drafts).set(stageId, { ...previous, ...patch }) }
    // Text is rendered locally; saving it must not repaint the feed on every keystroke.
    if (Object.keys(patch).some((key) => !['revise', 'message', 'replyMessage'].includes(key))) refreshComposerDrafts((n) => n + 1)
  }, [runEpoch, draftPreviews])
  // stageNoteDrafts — each stage's unsaved Stage note text for this page session. Separate from the composer drafts:
  // it is another input with another destination. A new run starts without them. Typing only writes the ref.
  const stageNoteDrafts = useRef({ runEpoch, drafts: new Map<string, string>() })
  if (stageNoteDrafts.current.runEpoch !== runEpoch) stageNoteDrafts.current = { runEpoch, drafts: new Map() }
  const updateStageNoteDraft = useCallback((stageId: string, text: string | null) => {
    if (stageNoteDrafts.current.runEpoch !== runEpoch) return
    const drafts = new Map(stageNoteDrafts.current.drafts)
    if (text === null) drafts.delete(stageId)
    else drafts.set(stageId, text)
    stageNoteDrafts.current = { runEpoch, drafts }
  }, [runEpoch])
  const meshDeliveryReceipts = useMemo(() => (stageMesh ? meshDeliveries(events) : NO_DELIVERIES), [stageMesh, events])
  const meshSideFailures = useMemo(() => (stageMesh ? sideAgentFailures(events) : NO_DELIVERIES), [stageMesh, events])
  // With the mesh the gate is split: Message — any non-script stage while the run is alive; Revise — only the
  // statuses the Revise API accepts (or a Pause-on-focus operation still in flight), outside a review pause.
  const runAlive = runStatus !== 'finished' && runStatus !== 'failed' && runStatus !== 'interrupted'
  const reviseAvailable =
    workspaceStage !== null &&
    flowPauseState === 'none' &&
    (REVISE_STATUSES.has(workspaceStage.status) || pauseOnFocus.hasActiveOp(workspaceStage.id))
  const noteTarget = stageMesh
    ? workspaceStage !== null && !workspaceStage.isScript && runAlive
      ? workspaceStage.id
      : null
    : legacyNoteTarget
  // showPeople — the stage Feed shows who this stage can talk to.
  const showPeople = stageMesh && collaborationMode === 'team' && workspaceStage !== null && wsState.view === 'feed'

  const stageNowMs = useStageClock(statusAvailable && runAlive && hasOpenTiming(stages))
  // A stale status freezes the clock, preserving the last displayed open interval in both views.
  const times = stageTimes(stages, stageNowMs, runAlive)
  const statusLive = statusAvailable
  const elapsedMs = useElapsed(startedAt, endedAt, statusLive, elapsedAccumulatedMs, elapsedSince)
  const idleMs = useIdleMs(idleAccumulatedMs, idleSince, statusLive)
  const backoffMs = useBackoffMs(backoffAccumulatedMs, backoffOpenSince, statusLive)

  const lastRefreshedEvent = useRef<object | null>(null)

  // WebSocket как канал обновления: значимое событие → ре-запрос состояния флоу.
  useEffect(() => {
    if (events.length === 0) return

    // Сравниваем ссылку на последнее событие: длина ленты перестаёт меняться после MAX_EVENTS.
    const latest = events[events.length - 1]
    if (latest === undefined || latest === lastRefreshedEvent.current) return

    lastRefreshedEvent.current = latest
    if (SIGNIFICANT_EVENT_TYPES.has(latest.type)) {
      refresh()
    }
  }, [events, refresh])

  // Очередь attention и позиция активного элемента — для навигации prev/next между несколькими стадиями,
  // ждущими действия. Очередь ведёт сам редьюсер (wsState.items).
  const attnItems = wsState.items
  const attnIndex = attnItem === null ? -1 : attnItems.findIndex((it) => it.stageId === attnItem.stageId)
  function goAttention(delta: number): void {
    if (attnItems.length === 0) return
    const base = attnIndex >= 0 ? attnIndex : 0
    const next = attnItems[(base + delta + attnItems.length) % attnItems.length]
    if (next) {
      setSelection({ kind: 'stage', stageId: next.stageId })
      openAttention(next.stageId)
    }
  }

  const inAttention = wsState.view === 'attention'
  const contextKind = workspaceStage === null ? null : attentionKindForStatus(workspaceStage)

  // Первый нерешённый элемент очереди (для маяка/шортката). В attention-виде это
  // активный элемент; иначе — голова очереди.
  const attentionTabItem = attnItem ?? attnItems[0] ?? null

  // Клик по первому нерешённому ожиданию закрывает Files и открывает attention.
  const attentionShortcut = attentionTabItem !== null
    ? {
        label: ATTENTION_SHORTCUT_LABEL[attentionTabItem.kind],
        onActivate: (): void => {
          setSelection({ kind: 'stage', stageId: attentionTabItem.stageId })
          openAttention(attentionTabItem.stageId)
        },
      }
    : null

  // Вкладки: Feed + контекстная detail-вкладка + отдельный маяк ожидания.
  const isGlobalView = wsState.view === 'feed' || wsState.view === 'cost' || wsState.view === 'team' || wsState.view === 'board'
  // В глобальном виде detail-таб выбранной стадии дублирует glow-маяк, если оба ведут в ОДНО и то же
  // ожидание — т.е. у стадии есть своё ожидание и маяк указывает на неё же.
  const duplicatesBeacon =
    isGlobalView &&
    contextKind !== null &&
    workspaceStage !== null &&
    attentionTabItem?.stageId === workspaceStage.id
  // Cost — постоянная вкладка: глобальный отчёт по затратам, не привязанный к выбранной стадии. Никогда не
  // auto-open'ится (в отличие от контекстных attention-вкладок ниже).
  const tabs: WorkspaceTabDescriptor[] = [{ id: 'feed', label: 'Feed' }]
  if (workspaceStage !== null) {
    if (inAttention && contextKind !== null) {
      // Тело показывает attention самой стадии → detail = attention (active, glow).
      tabs.push({
        id: 'detail',
        label: ATTENTION_TAB_LABEL[contextKind],
        kind: contextKind,
        count: countByKind(attnItems, contextKind),
        glow: true,
      })
    } else if (!isGlobalView || (wsState.view === 'feed' && workspaceStage.status !== 'pending' && !duplicatesBeacon && (workspaceStage.showPlan || workspaceStage.showDialog))) {
      // Тело показывает историю/детали выбранной стадии — вкладка это и отражает. В history-виде
      // (!isGlobalView) таб нужен всегда — он и есть активная вкладка истории.
      tabs.push({
        id: 'detail',
        label: workspaceStage.name !== '' ? workspaceStage.name : workspaceStage.id,
      })
    }
  }
  // Отдельный маяк ожидания — когда очередь непуста и мы НЕ в attention-виде
  // (в attention-виде ожидание уже показано detail-вкладкой). Никогда не active.
  const showBeacon = !inAttention && attentionTabItem !== null
  if (showBeacon && attentionTabItem !== null) {
    tabs.push({
      id: 'beacon',
      label: ATTENTION_TAB_LABEL[attentionTabItem.kind],
      kind: attentionTabItem.kind,
      count: countByKind(attnItems, attentionTabItem.kind),
      glow: true,
    })
  }
  // Team — ГЛОБАЛЬНАЯ вкладка ростера/бесед Agent Mesh (T7.3), не привязана к выбранной стадии (как Cost
  // ниже), видна когда meshRoster.enabled (пробный запрос — см. его комментарий, у мэша нет своего поля в
  // status.capabilities).
  // Team and Board also wait for the run id: it keys their state, so a first status arriving after the mesh
  // probe must not recreate an already opened Team.
  if (teamViewsAvailable) {
    tabs.push({ id: 'team', label: 'Team' }, { id: 'board', label: 'Board' })
  }
  // Cost всегда последней — после detail/beacon, а не второй вкладкой сразу после Feed.
  tabs.push({ id: 'cost', label: 'Cost' })
  const activeTabId = wsState.view === 'feed' ? 'feed' : wsState.view === 'cost' ? 'cost' : wsState.view === 'team' ? 'team' : wsState.view === 'board' ? 'board' : 'detail'
  function onSelectTab(id: string): void {
    if (id === 'feed') { openFeed(); return }
    if (id === 'cost') { openCost(); return }
    if (id === 'team') { openTeam(); return }
    if (id === 'board') { openBoard(); return }
    if (id === 'beacon') {
      // Явный переход к ждущему действию.
      if (attentionTabItem !== null) {
        setSelection({ kind: 'stage', stageId: attentionTabItem.stageId })
        openAttention(attentionTabItem.stageId)
      }
      return
    }
    // detail — контекст выбранной стадии. Помощник — ручной read-only вид (как history), переоткрываем его
    // же, а не теряем выбор клика по уже активной вкладке.
    if (selection?.kind === 'assistant') { openAssistant(); return }
    if (workspaceStage === null) return
    if (contextKind !== null) openAttention(workspaceStage.id)
    else if (workspaceStage.showDialog) openHistory('dialog-history')
    else if (workspaceStage.showPlan) openHistory('plan-history')
    else openFeed()
  }

  // Единственная панель детали — по одной за раз, на всю доступную высоту.
  const attentionBanner = inAttention && contextKind !== null ? <AttentionBanner kind={contextKind} /> : undefined
  let detailPanel: ReactElement | null = null
  if (workspaceStage !== null) {
    if (wsState.view === 'attention') {
      detailPanel = contextKind === 'question'
        ? <DialogChannel key="dialog" stage={workspaceStage} attention banner={attentionBanner} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} />
        : <PlanPanel key="plan" stage={workspaceStage} attention={contextKind === 'approval'} banner={attentionBanner} statusAvailable={statusAvailable} onContinue={heldContinue} />
    } else if (wsState.view === 'plan-history') {
      detailPanel = showPlan
        ? <PlanPanel key="plan" stage={workspaceStage} attention={false} statusAvailable={statusAvailable} onContinue={heldContinue} />
        : showDialog ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} /> : null
    } else if (wsState.view === 'dialog-history') {
      detailPanel = showDialog
        ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} />
        : showPlan ? <PlanPanel key="plan" stage={workspaceStage} attention={false} statusAvailable={statusAvailable} onContinue={heldContinue} /> : null
    }
  }

  return (
    // MessageTimeZoneProvider стоит СНАРУЖИ SideAgentProvider: Feed, Team и история бокового агента
    // должны видеть одну и ту же хост-зону для времени сообщений.
    <MessageTimeZoneProvider timeZone={timeZone} utcOffsetMinutes={utcOffsetMinutes}>
    <DialogOpenContext.Provider value={reportDialogOpen}>
    {/* SideAgentProvider MUST wrap FileBrowserProvider, not the other way around: FileBrowserProvider calls
        useSideAgentContext() (file-browser → side-agent, via its "Send to assistant" no-pause flow) and
        needs a real provider above it in the tree. Reversing this order silently breaks that flow — no
        compile error, no runtime error, just a default/no-op context. */}
    <SideAgentProvider
      runId={runId}
      enabled={capabilities.sideAgent}
      blocked={otherModalsOpen}
      onOpenChange={setSideAgentOpen}
      attentionShortcut={attentionShortcut}
      currentStageId={selectedStageId ?? ''}
      stages={stages.map((s) => ({ id: s.id, title: s.name }))}
      mentionsEnabled={capabilities.collaboration}
    >
      {/* CommentProvider выше FileBrowserProvider, PlanPanel и DialogChannel: черновики и отправка
          живут весь ран и переживают размонтирование Files. */}
      <StageActionsContext.Provider value={stageActions}>
      <CommentProvider runId={runId}>
      <CommentRecipientsContext.Provider value={commentRoster}>
      <FileBrowserProvider flowName={flowName} startedAt={startedAt} enabled={capabilities.fileBrowser} onOpenChange={setFilesOpen} attentionShortcut={attentionShortcut}>
      <GlobalHeader
        flowName={flowName}
        description={description}
        connected={connected}
        startedAt={startedAt}
        elapsedMs={elapsedMs}
        idleMs={idleMs}
        backoffMs={backoffMs}
        runCost={runCost}
        coverageIssues={coverageIssues}
        accounting={accounting}
        onOpenCost={handleOpenCost}
        notificationsPermission={notificationsPermission}
        notificationsEnabled={notificationsEnabled}
        onRequestEnableNotifications={onRequestEnableNotifications}
        onDisableNotifications={onDisableNotifications}
        capabilities={capabilities}
      />

      <ReviewBanner
        flowPauseState={flowPauseState}
        noteCount={reviewNoteCount}
        onSend={handleReviewSend}
        onCancel={handleReviewCancel}
      />

      <main id="main">
        <MaximizeProvider>
          <DashboardShell
            rail={
              <StagesList
                stages={stages}
                times={times}
                selectedStageId={workspaceStageId}
                onSelect={handleSelectStage}
                progressDone={stages.filter((s) => s.status === 'done').length}
                progressTotal={stages.length}
                accounting={accounting}
                version={version}
                assistants={assistantsByStage}
                selectedTaskId={selectedTaskId}
                onSelectTask={handleSelectAssistantTask}
                canAddStage={capabilities.addStage}
                onAddStage={() => setAddStageOpen(true)}
                statusAvailable={statusAvailable}
              />
            }
            tabs={
              <>
                <WorkspaceTabs tabs={tabs} activeId={activeTabId} onSelect={onSelectTab} />
                {runConversationsAvailable && (
                  <>
                  <button
                    type="button"
                    className={`run-conversations-toggle${runUnreadCount > 0 ? ' has-unread' : ''}${runUnreadCount > 0 && !runConversationsOpen ? ' is-pulsing' : ''}`}
                    aria-label={runUnreadCount > 0 ? `Run conversations, ${runUnreadCount} with new messages` : 'Run conversations'}
                    aria-controls={RUN_CONVERSATIONS_ID}
                    aria-expanded={runConversationsOpen}
                    onClick={() => setRunConversationsOpen((open) => !open)}
                  >
                    <span aria-hidden="true">💬</span>
                    <span className="run-conversations-toggle-label" aria-hidden="true">Conversations</span>
                    {runUnreadCount > 0 && <span className="run-conversations-toggle-count" aria-hidden="true">{runUnreadCount}</span>}
                  </button>
                  {/* The button's name is not announced when it changes; this live region announces a new message. */}
                  <span className="run-conversations-announce" role="status">
                    {runUnreadCount > 0 ? `${runUnreadCount} ${runUnreadCount === 1 ? 'conversation' : 'conversations'} with new messages` : ''}
                  </span>
                  </>
                )}
              </>
            }
            workspace={
              // The split and its primary column only lay out in `assistants` (Run conversations beside the
              // views); elsewhere both are `display: contents`, so Team and the views keep their layout. They
              // are always rendered, so toggling the mode never remounts the views.
              <div className={`workspace-split${runConversationsAvailable ? ' with-run-conversations' : ''}`}>
              <div className="workspace-primary">
                {/* Контекст стадии воркспейса (имя + статус) — общая шапка и для
                    Feed, и для деталей (мокап показывает контекст стадии над
                    лентой). Cost/Team — глобальные виды, не привязанные к стадии:
                    оставлять над ними имя/статус последней выбранной стадии
                    подразумевало бы несуществующий пер-стейдж фильтр. */}
                {workspaceStage !== null && wsState.view !== 'cost' && wsState.view !== 'team' && wsState.view !== 'board' && (
                  <WorkspaceHeader
                    stage={workspaceStage}
                    connected={connected}
                    statusAvailable={statusAvailable}
                    showControls={wsState.view === 'feed' && selection?.kind === 'stage'}
                    showStageButtons={selection?.kind === 'stage'}
                    controlsEnabled={flowPauseState === 'none' && (!pauseOnFocus.hasActiveOp(workspaceStage.id) || (workspaceStage.status === 'paused' && pauseOnFocus.isOwned(workspaceStage.id)))}
                    onContinue={heldContinue}
                    replyNeeded={wsState.view === 'feed' && feedReplies.stageId === workspaceStage.id ? feedReplies.count : 0}
                    attentionNav={
                      inAttention && attnIndex >= 0 && attnItems.length > 1
                        ? { pos: attnIndex + 1, total: attnItems.length, onPrev: () => goAttention(-1), onNext: () => goAttention(1) }
                        : undefined
                    }
                    actions={
                      showPeople ? (
                        <button
                          type="button"
                          className="team-icon-btn stage-people-toggle"
                          aria-label="Show people"
                          title="People"
                          aria-controls={STAGE_PEOPLE_ID}
                          aria-expanded={peopleOpen}
                          onClick={() => setPeopleOpen(!peopleOpen)}
                        >
                          <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <circle cx="9" cy="8" r="3" /><path d="M3 21v-3a6 6 0 0 1 12 0v3M16 5a3 3 0 0 1 0 6m2 4a5 5 0 0 1 3 5" />
                          </svg>
                          <span>People</span>
                        </button>
                      ) : canAttachHelper(workspaceStage, capabilities.collaboration) ? (
                        // Без People-панели (assistants, collaboration без mesh) это единственное место прикрепить helper.
                        <button type="button" className="workspace-action" onClick={() => setAttachHelperStageId(workspaceStage.id)}>
                          Attach helper…
                        </button>
                      ) : undefined
                    }
                  />
                )}
                {wsState.view === 'cost' ? (
                  <CostPanel
                    stages={stages}
                    times={times}
                    elapsedMs={elapsedMs}
                    runCost={runCost}
                    runOverheadCost={runOverheadCost}
                    coverageIssues={coverageIssues}
                    accounting={accounting}
                  />
                ) : wsState.view === 'team' ? (
                  // Team is global, like Cost: must be checked BEFORE the
                  // `workspaceStage === null` Feed fallback below, since Team has no selected-stage
                  // dependency at all (a never-selected stage must still show Team, not Feed).
                  // Rendered below, outside this chain: Team stays mounted once opened (see teamOpenedRun).
                  null
                ) : wsState.view === 'board' && teamViewsAvailable ? (
                  // Board — global like Team, fed by the same meshBoard; BoardLoadState separates loading, unavailable
                  // and stale data from "no tasks".
                  <BoardLoadState board={meshBoard}>
                    <BoardView key={runId} board={meshBoard} participants={meshParticipants} stages={teamStagesOf(stages)} />
                  </BoardLoadState>
                ) : wsState.view === 'feed' || wsState.view === 'board' || workspaceStage === null ? (
                  <div className="stage-feed-layout">
                    <FeedWorkspace
                      key={runId}
                      events={stageEvents}
                      stageId={workspaceStage?.id ?? null}
                      emptyHint={workspaceStage === null ? 'Select a stage to see its feed' : 'No events for this stage yet'}
                      onOpenDialog={handleOpenDialogFromFeed}
                      noteTarget={noteTarget}
                      composerDrafts={composerDrafts.current.drafts}
                      onComposerDraftChange={updateComposerDraft}
                      onSendNote={pauseOnFocus.handleSend}
                      pauseOnFocusEnabled={pauseOnFocus.enabled}
                      onTogglePauseOnFocus={pauseOnFocus.setEnabled}
                      composerPaused={
                        workspaceStage !== null &&
                        workspaceStage.status === 'paused' &&
                        pauseOnFocus.isOwned(workspaceStage.id)
                      }
                      onComposerFocus={() =>
                        workspaceStage !== null && pauseOnFocus.onFocus(workspaceStage.id, workspaceStage.status)
                      }
                      onComposerBlur={(hasDraft) =>
                        workspaceStage !== null && pauseOnFocus.onBlur(workspaceStage.id, hasDraft)
                      }
                      onComposerResumeNow={() =>
                        workspaceStage !== null && pauseOnFocus.resumeNow(workspaceStage.id)
                      }
                      // mesh (Agent Mesh Phase 7, T7.4) — same roster/profiles App.tsx already owns for Team,
                      // only with stageMesh: undefined gives the plain Feed (no conversations, no poll of them)
                      // and the plain note-to-agent composer (no Message/Revise tabs).
                      mesh={stageMesh ? { roster: mergedRoster, profiles: assistantProfiles, stages, pick: peoplePick, deliveries: meshDeliveryReceipts, sideFailures: meshSideFailures } : undefined}
                      reviseAvailable={reviseAvailable}
                      paused={workspaceStage?.status === 'paused' || flowPauseState !== 'none'}
                      onReplyNeededChange={reportFeedReplies}
                      aboveFeed={
                        wsState.view === 'feed' && selection?.kind === 'stage' && workspaceStage !== null ? (
                          <StageNote
                            key={workspaceStage.id}
                            stage={workspaceStage}
                            initialDraft={stageNoteDrafts.current.drafts.get(workspaceStage.id)}
                            onDraftChange={updateStageNoteDraft}
                            onChanged={refresh}
                          />
                        ) : undefined
                      }
                    />
                    {showPeople && workspaceStage !== null && (
                      <StagePeoplePanel
                        stageId={workspaceStage.id}
                        helperTasks={Object.values(assistantsByStage).flatMap((entry) => entry.tasks)}
                        participants={meshParticipants}
                        stages={stages}
                        profiles={assistantProfiles}
                        onAgentAdded={addParticipant}
                        onAttachHelper={canAttachHelper(workspaceStage, capabilities.collaboration) ? setAttachHelperStageId : undefined}
                        open={peopleOpen}
                        drawer={peopleDrawer}
                        onClose={closePeople}
                        // The click adds a recipient in the Feed composer, which exists only while noteTarget is set.
                        onPick={
                          noteTarget !== null
                            ? (p) => setPeoplePick((prev) => ({ participantId: p.id, seq: (prev?.seq ?? 0) + 1 }))
                            : undefined
                        }
                      />
                    )}
                  </div>
                ) : wsState.view === 'assistant' ? (
                  <AssistantTaskView
                    task={selectedTask}
                    allTasks={selectedTaskStageTasks}
                    maxDepth={assistantMaxDepth}
                    runId={runId}
                    liveActivity={liveActivity}
                    connected={connected}
                  />
                ) : detailPanel === null ? (
                  <div className="detail-empty empty-hint">Nothing to show for this stage</div>
                ) : (
                  <div className="detail-panels">
                    {/* Шапка-баннер контекстной вкладки («Plan needs your
                        approval» / «Agent needs your input») теперь рендерится
                        ВНУТРИ скролл-области панели (см. проп banner у
                        PlanPanel/DialogChannel), а не здесь над ней — чтобы она
                        уезжала со скроллом и не занимала высоту постоянно. */}
                    {/* Переключатель истории (Finding #2 второго раунда): у стадии,
                        задававшей вопрос во время planning/implementation, есть и
                        план, и диалог — иначе план стал бы недоступен навсегда
                        (клик всегда открывал dialog-history). Показываем сегмент
                        Plan | Dialog только в history-виде, когда доступны оба. */}
                    {!inAttention && showPlan && showDialog && (
                      <div className="history-switch" role="group" aria-label="History view">
                        <button
                          type="button"
                          className={`history-switch-btn${wsState.view === 'plan-history' ? ' active' : ''}`}
                          aria-pressed={wsState.view === 'plan-history'}
                          onClick={() => openHistory('plan-history')}
                        >
                          Plan
                        </button>
                        <button
                          type="button"
                          className={`history-switch-btn${wsState.view === 'dialog-history' ? ' active' : ''}`}
                          aria-pressed={wsState.view === 'dialog-history'}
                          onClick={() => openHistory('dialog-history')}
                        >
                          Dialog
                        </button>
                      </div>
                    )}
                    {detailPanel}
                  </div>
                )}
                {/* Team stays mounted after it is first opened and is only hidden on other views: its drafts,
                    in-flight sends (with their idempotency keys), selection and reading position live in it. */}
                {teamOpenedRun === runId && teamViewsAvailable && (
                  <div style={{ display: wsState.view === 'team' ? 'contents' : 'none' }}>
                    <TeamView key={runId} runId={runId} active={wsState.view === 'team'} roster={mergedRoster} onParticipantAdded={addParticipant} board={meshBoard} stages={stages} events={events} profiles={assistantProfiles} />
                  </div>
                )}
              </div>
              {/* Stays mounted while closed (slid out and inert): its conversation and draft survive view and
                  stage changes, and usePanelResize keeps measuring a stable parent. */}
              {runConversationsAvailable && (
                <RunConversationsPanel
                  key={runId}
                  open={runConversationsOpen}
                  onClose={closeRunConversations}
                  participants={meshParticipants}
                  threads={meshRoster.threads}
                  summaries={meshRoster.summaries ?? NO_SUMMARIES}
                  unreadThreadIds={runSeen.unreadThreadIds}
                  seenSeqs={runSeen.seenSeqs}
                  onSeen={runSeen.markSeen}
                />
              )}
              </div>
            }
          />
        </MaximizeProvider>
      </main>

      {reviewModalOpen && (
        <ReviewNotesModal pausedStages={flowPausedStages} onClose={() => setReviewModalOpen(false)} attentionShortcut={attentionShortcut} />
      )}

      {attachHelperStageId !== null && (
        <AttachHelperDialog
          stageId={attachHelperStageId}
          stageTitle={stages.find((s) => s.id === attachHelperStageId)?.name || attachHelperStageId}
          profiles={assistantProfiles}
          onAttached={() => setAttachHelperStageId(null)}
          onCancel={() => setAttachHelperStageId(null)}
        />
      )}

      {addStageOpen && (
        <AddStageDialog
          capabilityEnabled={capabilities.addStage}
          graphRevision={graphRevision}
          stages={stages.map((s) => ({ id: s.id, title: s.name || s.id }))}
          onCommitted={() => { setAddStageOpen(false); refresh() }}
          onCancel={() => setAddStageOpen(false)}
        />
      )}
      </FileBrowserProvider>
      </CommentRecipientsContext.Provider>
      </CommentProvider>
      </StageActionsContext.Provider>
    </SideAgentProvider>
    </DialogOpenContext.Provider>
    </MessageTimeZoneProvider>
  )
}

function buildWebSocketUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

  return `${protocol}//${window.location.host}/ws`
}
