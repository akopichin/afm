import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { cancelNotes, continueStage, pauseStage, reviseStage, setStageNote, triggerStageButton } from '../api/run-client'
import { GlobalHeader } from '../components/global-header'
import { StagesList, canAttachHelper } from '../components/stages-list'
import { AssistantTaskView, AttachHelperDialog, AddStageDialog } from '../components/assistants'
import { StagePeoplePanel, STAGE_PEOPLE_ID, TeamView } from '../components/team'
import { AgentNoteModal } from '../components/agent-note-modal'
import { ReviewNotesModal } from '../components/review-notes-modal'
import { PlanPanel } from '../components/plan-panel'
import { DialogChannel } from '../components/dialog-channel'
import { FeedWorkspace, meshDeliveries, type ComposerDraft, type MeshPick } from '../components/feed-workspace'
import { sideAgentFailures } from '../components/feed-workspace/mesh-receipt'
import { CostPanel } from '../components/cost-panel'
import { MaximizeProvider } from '../components/layout/Maximizable'
import { DashboardShell } from '../components/layout/DashboardShell'
import { WorkspaceTabs, WorkspaceHeader, AttentionBanner, type WorkspaceTabDescriptor } from '../components/workspace'
import { FileBrowserProvider } from '../components/file-browser'
import { SideAgentProvider } from '../components/side-agent'
import { MessageTimeZoneProvider } from '../lib/message-time-zone'
import { ReviewBanner } from '../components/review-banner'
import { useStatus } from '../hooks/use-status'
import { useSelectedStage } from '../hooks/use-selected-stage'
import { useAssistantMaxDepth, useAssistantProfiles } from '../hooks/use-assistant-profiles'
import { useStageAssistants } from '../hooks/use-stage-assistants'
import { useMeshRoster } from '../hooks/use-mesh'
import { mergeById } from '../lib/mesh-roster'
import type { MeshParticipant } from '../api/mesh-client'
import { useReviewNoteCount } from '../hooks/use-review-note-count'
import { useEventFeed } from '../hooks/use-event-feed'
import { useStageEvents } from '../hooks/use-stage-events'
import { useElapsed } from '../hooks/use-elapsed'
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
  assistants: 'Assistants',
}

// Подпись ждущего действия в шапке файлового оверлея.
const ATTENTION_SHORTCUT_LABEL: Record<AttentionKind, string> = {
  approval: 'Approval waiting',
  question: 'Question waiting',
  failed: 'Stage failed',
  hook_failed: 'Hook failed',
  paused: 'Stage paused',
  assistants: 'Assistant decision needed',
}

// REVISE_STATUSES — the statuses POST /api/stages/{id}/revise accepts (pkg/server handleRevise).
const REVISE_STATUSES: ReadonlySet<string> = new Set(['running', 'awaiting_approval', 'awaiting_assistants'])
// NO_DELIVERIES — no delivery receipts (the stage Feed's mesh UI is off).
const NO_DELIVERIES: ReadonlySet<string> = new Set()

// Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер.
export function App(): ReactElement {
  const {
    flowName, stages, startedAt, endedAt, elapsedAccumulatedMs, elapsedSince,
    description, version, idleAccumulatedMs, idleSince, backoffAccumulatedMs,
    backoffOpenSince, capabilities, graphRevision, runId, flowPauseState, flowPausedStages, runCost,
    runOverheadCost, coverageIssues, accounting, statusAvailable, refresh, runStatus,
    timeZone, utcOffsetMinutes,
  } = useStatus()

  // Модалка ревью-раунда — открывается по клику ReviewBanner's "Send notes".
  const [reviewModalOpen, setReviewModalOpen] = useState(false)

  // Стадия, для которой открыта модалка pre-note; null — модалка скрыта.
  const [preNoteModalStageId, setPreNoteModalStageId] = useState<string | null>(null)
  const [attachHelperStageId, setAttachHelperStageId] = useState<string | null>(null)
  const [addStageOpen, setAddStageOpen] = useState(false)

  async function handleSubmitPreNote(note: string): Promise<void> {
    if (preNoteModalStageId === null) return
    try {
      await setStageNote(preNoteModalStageId, note)
      setPreNoteModalStageId(null)
    } catch (err) {
      // Как handleSubmitNote: не закрываем молча — оставляем модалку открытой с введённым текстом, чтобы
      // пользователь мог повторить (стадия могла уже стартовать, либо сеть отвалилась).
      console.error('Failed to save pre-note:', err)
    }
  }

  // «Pause on focus» — при включённом toggle фокус поля заметки ставит running- стадию на паузу, а
  // отправка сперва снимает паузу, затем доставляет заметку.
  const pauseOnFocus = usePauseOnFocus({ pauseStage, continueStage, reviseStage })

  function handlePause(stageId: string): void {
    // onPause вызывается без await из onClick; catch нужен для ошибок сети и сменившегося статуса стадии.
    pauseStage(stageId).catch((err: unknown) => {
      console.error('Failed to pause stage:', err)
    })
  }

  function handleButton(stageId: string, name: string): void {
    // Fire-immediately, без модалки: StagesList закрывает кебаб синхронно ДО вызова onButton.
    triggerStageButton(stageId, name).catch((err: unknown) => {
      console.error('Failed to trigger stage button:', err)
    })
  }

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
  const { events, connected } = useEventFeed(wsUrl)

  // Единый workspace-view-редьюсер — источник истины для того, ЧТО показано справа от рейла: Feed,
  // контекстный attention или read-only просмотр истории плана/диалога.
  const [filesOpen, setFilesOpen] = useState(false)
  // sideAgentOpen — видимость «бокового» агента. Входит в anyModalOpen, чтобы attention других стадий не
  // авто-открывался за открытой беседой (только сигналил).
  const [sideAgentOpen, setSideAgentOpen] = useState(false)
  const otherModalsOpen = filesOpen || preNoteModalStageId !== null || reviewModalOpen || attachHelperStageId !== null || addStageOpen
  const anyModalOpen = otherModalsOpen || sideAgentOpen
  // selfOwnedActive — у какой-то стадии есть незавершённая «Pause on focus»-операция.
  const selfOwnedActive = stages.some((s) => pauseOnFocus.shouldSuppressAttention(s.id))
  const editing = useIsEditing() || anyModalOpen || selfOwnedActive
  // ownedHandledSigs — подписи НАШИХ же paused-эпизодов: их auto-open подавляется навсегда, а не
  // откладывается.
  const ownedHandledSigs = stages
    .filter((s) => s.status === 'paused' && pauseOnFocus.shouldSuppressAttention(s.id))
    .map((s) => sig({ stageId: s.id, kind: 'paused', episode: s.updatedAt }))
  const { state: wsState, activeItem: attnItem, openFeed, openCost, openFullFeed, openAttention, openHistory, openAssistant, openTeam } = useWorkspaceView(stages, editing, ownedHandledSigs)

  // meshRoster (Agent Mesh, T7.3) — the ONE useMeshRoster poll for the whole dashboard; there is no
  // status.capabilities.mesh field (unlike collaboration/addStage/sideAgent below), so this hook's own
  // probe-the-first-fetch result IS the capability signal the Team tab gates on (see use-mesh-roster.ts's own
  // comment). TeamView receives the SAME roster as a prop rather than polling it a second time.
  const meshRoster = useMeshRoster()
  // The stage Feed's people panel: its narrow-screen drawer, and the last person picked there for the Feed
  // composer (seq makes a second click on the same person a new pick).
  const [peopleOpen, setPeopleOpen] = useState(false)
  const [peoplePick, setPeoplePick] = useState<MeshPick | null>(null)
  const closePeople = useCallback(() => setPeopleOpen(false), [])
  // addedParticipants — run agents added from the people panel that the roster poll has not reported yet.
  // The panel AND the Feed composer use the merged list, so a click on a just-added agent addresses it.
  const [addedParticipants, setAddedParticipants] = useState<MeshParticipant[]>([])
  const meshParticipants = useMemo(() => mergeById(meshRoster.participants, addedParticipants), [meshRoster.participants, addedParticipants])
  const addParticipant = useCallback((p: MeshParticipant) => setAddedParticipants((prev) => [...prev, p]), [])

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

  // Защита: если collaboration выключена на бэкенде, а вид застрял на 'assistant' — уводим в Feed (тот же
  // паттерн, что и у accounting/'cost' ниже).
  useEffect(() => {
    if (!capabilities.collaboration && wsState.view === 'assistant') openFeed()
  }, [capabilities.collaboration, wsState.view])

  // Защита: мэш-капабилити определяется пробным запросом (meshRoster.enabled), а не статическим полем
  // статуса — тот же паттерн, что и у collaboration/accounting выше: застрявший на 'team' вид (капабилити
  // отключили на бэкенде между опросами) уводим в Feed.
  useEffect(() => {
    if (meshRoster.probed && !meshRoster.enabled && wsState.view === 'team') openFeed()
  }, [meshRoster.probed, meshRoster.enabled, wsState.view])

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
  // Защита: если accounting выключён на бэкенде, а вид застрял на 'cost' — уводим в Feed.
  useEffect(() => {
    if (!accounting.supported && wsState.view === 'cost') openFeed()
  }, [accounting.supported, wsState.view])

  // Стадия, о которой сейчас говорит воркспейс: в attention-режиме — активный элемент очереди; иначе —
  // выбранная в рейле стадия.
  const workspaceStageId = wsState.view === 'attention' && attnItem !== null ? attnItem.stageId : selectedStageId
  const workspaceStage = stages.find((stage) => stage.id === workspaceStageId) ?? null

  // stageEvents — лента вкладки Feed: только события ТЕКУЩЕЙ стадии воркспейса, а не весь событийный поток
  // флоу. Full feed (глобальный вид) продолжает читать сырые `events` напрямую.
  const stageEvents = useStageEvents(workspaceStage?.id ?? null, events)

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
    if (wsState.view === 'feed' || wsState.view === 'cost' || wsState.view === 'full-feed' || wsState.view === 'team') return
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
  // meshDeliveryReceipts — the run's real per-recipient delivery receipts, for the composer's "Delivered".
  // composerDrafts — every stage's Feed composer draft for this page session (FeedWorkspace's composerDrafts).
  const composerDrafts = useRef(new Map<string, ComposerDraft>())
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
  const showPeople = stageMesh && workspaceStage !== null && wsState.view === 'feed'

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
  const isGlobalView = wsState.view === 'feed' || wsState.view === 'cost' || wsState.view === 'full-feed' || wsState.view === 'team'
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
  if (meshRoster.enabled) {
    tabs.push({ id: 'team', label: 'Team' })
  }
  // Cost всегда последней — после detail/beacon, а не второй вкладкой сразу после Feed.
  if (accounting.supported) {
    tabs.push({ id: 'cost', label: 'Cost' })
  }
  // Full feed — постоянная ГЛОБАЛЬНАЯ вкладка, читает весь событийный поток флоу. Всегда ПОСЛЕДНЯЯ в
  // списке — правее Cost, а не второй сразу после Feed (тот же порядок, что и у Cost выше).
  tabs.push({ id: 'full-feed', label: 'Full feed' })
  const activeTabId = wsState.view === 'feed' ? 'feed' : wsState.view === 'cost' ? 'cost' : wsState.view === 'full-feed' ? 'full-feed' : wsState.view === 'team' ? 'team' : 'detail'
  function onSelectTab(id: string): void {
    if (id === 'feed') { openFeed(); return }
    if (id === 'cost') { openCost(); return }
    if (id === 'full-feed') { openFullFeed(); return }
    if (id === 'team') { openTeam(); return }
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
        : <PlanPanel key="plan" stage={workspaceStage} attention={contextKind === 'approval'} banner={attentionBanner} />
    } else if (wsState.view === 'plan-history') {
      detailPanel = showPlan
        ? <PlanPanel key="plan" stage={workspaceStage} attention={false} />
        : showDialog ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} /> : null
    } else if (wsState.view === 'dialog-history') {
      detailPanel = showDialog
        ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} />
        : showPlan ? <PlanPanel key="plan" stage={workspaceStage} attention={false} /> : null
    }
  }

  return (
    // MessageTimeZoneProvider стоит СНАРУЖИ SideAgentProvider: Feed, Team и история бокового агента
    // должны видеть одну и ту же хост-зону для времени сообщений.
    <MessageTimeZoneProvider timeZone={timeZone} utcOffsetMinutes={utcOffsetMinutes}>
    {/* SideAgentProvider MUST wrap FileBrowserProvider, not the other way around: FileBrowserProvider calls
        useSideAgentContext() (file-browser → side-agent, via its "Send to side agent" no-pause flow) and
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
      <FileBrowserProvider flowName={flowName} startedAt={startedAt} enabled={capabilities.fileBrowser} flowPauseState={flowPauseState} onOpenChange={setFilesOpen} attentionShortcut={attentionShortcut}>
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
                selectedStageId={workspaceStageId}
                onSelect={handleSelectStage}
                onEditPreNote={setPreNoteModalStageId}
                onPause={handlePause}
                onButton={handleButton}
                progressDone={stages.filter((s) => s.status === 'done').length}
                progressTotal={stages.length}
                accounting={accounting}
                version={version}
                assistants={assistantsByStage}
                selectedTaskId={selectedTaskId}
                onSelectTask={handleSelectAssistantTask}
                collaborationEnabled={capabilities.collaboration}
                onAttachHelper={setAttachHelperStageId}
                canAddStage={capabilities.addStage}
                onAddStage={() => setAddStageOpen(true)}
              />
            }
            tabs={<WorkspaceTabs tabs={tabs} activeId={activeTabId} onSelect={onSelectTab} />}
            workspace={
              <>
                {/* Контекст стадии воркспейса (имя + статус) — общая шапка и для
                    Feed, и для деталей (мокап показывает контекст стадии над
                    лентой). Cost/Team — глобальные виды, не привязанные к стадии:
                    оставлять над ними имя/статус последней выбранной стадии
                    подразумевало бы несуществующий пер-стейдж фильтр. */}
                {workspaceStage !== null && wsState.view !== 'cost' && wsState.view !== 'full-feed' && wsState.view !== 'team' && (
                  <WorkspaceHeader
                    stage={workspaceStage}
                    connected={connected}
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
                          onClick={() => setPeopleOpen((v) => !v)}
                        >
                          <span aria-hidden="true">👥</span>
                        </button>
                      ) : undefined
                    }
                  />
                )}
                {wsState.view === 'cost' ? (
                  <CostPanel
                    stages={stages}
                    runCost={runCost}
                    runOverheadCost={runOverheadCost}
                    coverageIssues={coverageIssues}
                    accounting={accounting}
                  />
                ) : wsState.view === 'full-feed' ? (
                  <FeedWorkspace
                    events={events}
                    stageId={null}
                    scopeToken="scope:full"
                    showStageBadges
                    onOpenDialog={handleOpenDialogFromFeed}
                    emptyHint="No events yet"
                  />
                ) : wsState.view === 'team' ? (
                  // Team (T7.3) — global, like Cost/Full feed above: must be checked BEFORE the
                  // `workspaceStage === null` Feed fallback below, since Team has no selected-stage
                  // dependency at all (a never-selected stage must still show Team, not Feed).
                  <TeamView roster={meshRoster} stages={stages} events={events} profiles={assistantProfiles} />
                ) : wsState.view === 'feed' || workspaceStage === null ? (
                  <div className="stage-feed-layout">
                    <FeedWorkspace
                      events={stageEvents}
                      stageId={workspaceStage?.id ?? null}
                      // scopeToken не нужен: дефолт FeedWorkspace из stageId даёт ровно `scope:stage:<id>` / `scope:none`.
                      // Явный токен нужен только Full-feed-сайту (scope:full), который переопределяет дефолт.
                      emptyHint={workspaceStage === null ? 'Select a stage to see its feed' : 'No events for this stage yet'}
                      onOpenDialog={handleOpenDialogFromFeed}
                      noteTarget={noteTarget}
                      composerDrafts={composerDrafts.current}
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
                      mesh={stageMesh ? { roster: { ...meshRoster, participants: meshParticipants }, profiles: assistantProfiles, stages, pick: peoplePick, deliveries: meshDeliveryReceipts, sideFailures: meshSideFailures } : undefined}
                      reviseAvailable={reviseAvailable}
                    />
                    {showPeople && workspaceStage !== null && (
                      <StagePeoplePanel
                        stageId={workspaceStage.id}
                        participants={meshParticipants}
                        stages={stages}
                        profiles={assistantProfiles}
                        onAgentAdded={addParticipant}
                        onAttachHelper={canAttachHelper(workspaceStage, capabilities.collaboration) ? setAttachHelperStageId : undefined}
                        open={peopleOpen}
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
                  <AssistantTaskView task={selectedTask} allTasks={selectedTaskStageTasks} maxDepth={assistantMaxDepth} />
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
              </>
            }
          />
        </MaximizeProvider>
      </main>

      {preNoteModalStageId !== null && (
        <AgentNoteModal
          stageId={preNoteModalStageId}
          variant="prenote"
          initialNote={stages.find((s) => s.id === preNoteModalStageId)?.preNote ?? ''}
          onCancel={() => setPreNoteModalStageId(null)}
          onSubmit={handleSubmitPreNote}
          attentionShortcut={attentionShortcut}
        />
      )}

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
    </SideAgentProvider>
    </MessageTimeZoneProvider>
  )
}

function buildWebSocketUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

  return `${protocol}//${window.location.host}/ws`
}
