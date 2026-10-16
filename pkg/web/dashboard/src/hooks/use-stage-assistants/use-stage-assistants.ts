import { useEffect, useRef, useState } from 'react'
import { listStageAssistants, type AssistantTaskWire } from '../../api/run-client'
import {
  isAssistantTaskMode,
  isAssistantTaskStatus,
  type AssistantTask,
  type AssistantTaskNode,
} from '../../types/assistant-task'

// Poll interval — independent of /api/status's own 3s poll (AGENTS.md: this is its OWN request, not a field
// bolted onto the status payload). Same cadence as /api/status so the rail doesn't feel laggier than the
// rest of the dashboard.
const POLL_INTERVAL_MS = 3000

// StageAssistantsSummary — one stage's task tree PLUS the two numbers the rail actually needs for a
// collapsed branch: count and whether anything needs the user's attention. The rail never gets more than
// this out of /api/status (that stays un-fattened) — the full tree lives only here, behind its own poll.
export type StageAssistantsSummary = {
  tasks: AssistantTask[]
  roots: AssistantTaskNode[]
  count: number
  hasAttention: boolean
}

const EMPTY_SUMMARY: StageAssistantsSummary = { tasks: [], roots: [], count: 0, hasAttention: false }

function toAssistantTask(raw: AssistantTaskWire): AssistantTask {
  return {
    id: raw.id,
    parentTaskId: raw.parent_task_id ?? null,
    profileId: raw.profile_id,
    title: raw.title ?? '',
    mode: isAssistantTaskMode(raw.mode) ? raw.mode : 'advisory',
    trigger: raw.trigger,
    status: isAssistantTaskStatus(raw.status) ? raw.status : 'queued',
    statusReason: raw.status_reason ?? '',
    depth: typeof raw.depth === 'number' && raw.depth > 0 ? raw.depth : 1,
    watchPasses: typeof raw.watch_passes === 'number' ? raw.watch_passes : null,
    watchLastCheckpointSeq: typeof raw.watch_last_checkpoint_seq === 'number' ? raw.watch_last_checkpoint_seq : null,
    blocksRootCompletion: raw.blocks_root_completion === true,
    waived: raw.waived === true,
    waiverReason: raw.waiver_reason ?? '',
  }
}

// buildTree links a flat, parent-id-referencing task list into a forest of root-level nodes (no ParentTaskID)
// each carrying its own descendants, depth-first in server order. Этап 3 only ever admits depth-1 tasks
// (Attach never sets ParentTaskID — see assistants_read.go), so today every node here is a leaf; the walk is
// written generally so Этап 4a's recursion needs no second tree-builder.
export function buildTree(tasks: AssistantTask[]): AssistantTaskNode[] {
  const byParent = new Map<string, AssistantTask[]>()
  const roots: AssistantTask[] = []
  for (const t of tasks) {
    if (t.parentTaskId === null) {
      roots.push(t)
      continue
    }
    const siblings = byParent.get(t.parentTaskId)
    if (siblings) siblings.push(t)
    else byParent.set(t.parentTaskId, [t])
  }
  const toNode = (t: AssistantTask): AssistantTaskNode => ({
    ...t,
    children: (byParent.get(t.id) ?? []).map(toNode),
  })
  return roots.map(toNode)
}

function summarize(tasks: AssistantTask[]): StageAssistantsSummary {
  if (tasks.length === 0) return EMPTY_SUMMARY
  return {
    tasks,
    roots: buildTree(tasks),
    count: tasks.length,
    // paused — a task is held and needs a human decision (continue/cancel); the one task status today's
    // runtime can land a helper in that genuinely waits on the user, see pkg/assistants.TaskStatus.
    hasAttention: tasks.some((t) => t.status === 'paused'),
  }
}

// tasksSignature is a cheap, order-sensitive fingerprint of every field buildTree/the rail actually render —
// NOT a deep-equal: two polls whose tasks are byte-identical in every field this touches get the SAME
// signature, so useStageAssistants can skip rebuilding the tree (and, with it, every AssistantTaskRow in the
// branch) on a poll tick that changed nothing (dev-plan.md §19.4 — T4a.8's TestTree_NotRebuiltOnEveryPoll). A
// changed ORDER (same set, different sequence) is deliberately treated as a change too: buildTree's sibling
// order is server order, and a reordered poll response is a real content change for display purposes, not
// noise.
function tasksSignature(tasks: AssistantTask[]): string {
  return tasks
    .map(
      (t) =>
        `${t.id}|${t.parentTaskId ?? ''}|${t.status}|${t.statusReason}|${t.title}|${t.mode}|${t.depth}|${t.watchPasses ?? ''}|${t.watchLastCheckpointSeq ?? ''}|${t.blocksRootCompletion}|${t.waived}`,
    )
    .join('\n')
}

// useStageAssistants polls GET /api/stages/{id}/assistants for every id in stageIds, independently of
// /api/status. enabled gates the capability (capabilities.collaboration) — off, or no stage ids, means no
// network calls and an empty map. A transient per-stage error keeps that stage's last known summary rather
// than flashing it empty (same posture as useReviewNoteCount).
export function useStageAssistants(stageIds: string[], enabled: boolean): Record<string, StageAssistantsSummary> {
  const [data, setData] = useState<Record<string, StageAssistantsSummary>>({})
  // signatures — per-stage tasksSignature of whatever data[id] was last BUILT from, so a poll tick that
  // changed nothing can reuse the existing StageAssistantsSummary object (same reference) instead of calling
  // summarize()/buildTree() again — see tasksSignature's own doc comment (T4a.8's TestTree_NotRebuiltOnEveryPoll).
  const signatures = useRef<Record<string, string>>({})
  // Stage ids are stable in practice (a run's stage set doesn't change) — key on their join so the effect
  // doesn't tear down/rebuild its interval every time useStatus hands back a structurally-equal but
  // referentially-new stages array (every ~3s).
  const key = stageIds.join(' ')

  useEffect(() => {
    if (!enabled) {
      setData({})
      return
    }
    const ids = key.length === 0 ? [] : key.split(' ')
    if (ids.length === 0) {
      setData({})
      return
    }

    let cancelled = false
    // generation (T11.2, TestHistory_StaleResponsesInvalidatedOnStageTaskRunSwitch) — the same
    // useFilePreview-style monotonic-ref guard, but per POLL TICK rather than per effect run: `cancelled`
    // alone only protects against unmount/stageIds-set changes, not a slower EARLIER tick's response arriving
    // AFTER a faster LATER tick's response already landed (a real race under network jitter, since
    // setInterval fires load() again without waiting for the previous call's Promise.all to settle). Without
    // this, the stale earlier tick would silently overwrite the fresher one.
    const generation = { current: 0 }
    const load = (): void => {
      const gen = ++generation.current
      Promise.all(
        ids.map((id) =>
          listStageAssistants(id)
            .then((res): [string, AssistantTask[] | null] => [id, res.tasks.map(toAssistantTask)])
            .catch((): [string, AssistantTask[] | null] => [id, null]),
        ),
      ).then((results) => {
        if (cancelled || gen !== generation.current) return
        setData((prev) => {
          let changedAny = false
          const next = { ...prev }
          for (const [id, tasks] of results) {
            if (tasks === null) continue // transient error — keep whatever we had
            const sig = tasksSignature(tasks)
            if (signatures.current[id] === sig && prev[id] !== undefined) continue // unchanged — reuse prev's reference
            signatures.current[id] = sig
            next[id] = summarize(tasks)
            changedAny = true
          }
          return changedAny ? next : prev
        })
      })
    }

    load()
    const timer = setInterval(load, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [key, enabled])

  return data
}
