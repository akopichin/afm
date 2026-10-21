import { useEffect, useState } from 'react'
import { MeshApiError, listMeshParticipants, listMeshThreads, type MeshParticipant, type MeshThread, type MeshThreadSummaries } from '../../api/mesh-client'
import { startSerialPoll } from '../../lib/serial-poll'

// Poll interval — independent of /api/status's own poll, same posture as useStageAssistants/
// useAssistantProfiles (AGENTS.md: a capability's own data is its own request, never bolted onto the status
// payload).
const POLL_INTERVAL_MS = 3000

export type MeshRoster = {
  // The run whose poll produced this snapshot. A run switch cannot reuse an old run's summaries.
  runId?: string
  // enabled — whether the mesh capability is actually on. There is no status.capabilities.mesh field today
  // (unlike collaboration/addStage/sideAgent, whose status field gates the equivalent hook elsewhere): the
  // Phase 7 HTTP surface (pkg/server/mesh.go) nils out and 404s when the capability is off, with no separate
  // advertisement. This hook treats the FIRST participants fetch as the capability probe — a 404 there is
  // "mesh is off", exactly mirroring the server's own nil-Service convention, just read from the client side
  // instead of a status flag. Once that probe succeeds, normal polling begins; once it 404s, polling stops
  // for good (a capability does not flip on mid-run).
  enabled: boolean
  // probed — whether the first fetch has resolved at all. Lets a caller tell "still finding out" (render
  // nothing yet) apart from "found out: off" (enabled=false, probed=true).
  probed: boolean
  participants: MeshParticipant[]
  threads: MeshThread[]
  // Older callers can construct a roster without summaries; this hook always supplies a map.
  summaries?: MeshThreadSummaries
}

const EMPTY: MeshRoster = { enabled: false, probed: false, participants: [], threads: [], summaries: {} }

// useMeshRoster is the Team view's one read hook for "who is in the mesh and what conversations exist" —
// GET /api/mesh/participants + GET /api/mesh/threads, polled together. A transient error on a LATER poll
// keeps the last known roster (same posture as useStageAssistants); a 404 on the FIRST poll is treated as
// "capability off" and stops polling.
export function useMeshRoster(runId = ''): MeshRoster {
  const [state, setState] = useState<MeshRoster>(EMPTY)

  useEffect(() => {
    let first = true
    // Serial: two slow polls never overlap, so an older roster never replaces a newer one.
    const poll = startSerialPoll(
      (isStopped) =>
        Promise.all([listMeshParticipants(), listMeshThreads()]).then(
          ([p, t]) => {
            first = false
            if (!isStopped()) setState({ runId, enabled: true, probed: true, participants: p.participants, threads: t.threads, summaries: t.summaries })
          },
          (err: unknown) => {
            const wasFirst = first
            first = false
            if (isStopped() || !wasFirst) return // later-poll transient error: keep whatever roster we already have
            // A 404 on the first poll: the capability is off for good (it does not flip on mid-run).
            if (err instanceof MeshApiError && err.status === 404) poll.stop()
            setState({ ...EMPTY_PROBED, runId })
          },
        ),
      POLL_INTERVAL_MS,
    )
    return poll.stop
  }, [runId])

  return state.runId === runId ? state : { ...EMPTY, runId }
}

const EMPTY_PROBED: MeshRoster = { enabled: false, probed: true, participants: [], threads: [], summaries: {} }
