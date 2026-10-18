import { useEffect, useRef, useState } from 'react'
import { MeshApiError, listMeshParticipants, listMeshThreads, type MeshParticipant, type MeshThread } from '../../api/mesh-client'

// Poll interval — independent of /api/status's own poll, same posture as useStageAssistants/
// useAssistantProfiles (AGENTS.md: a capability's own data is its own request, never bolted onto the status
// payload).
const POLL_INTERVAL_MS = 3000

export type MeshRoster = {
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
}

const EMPTY: MeshRoster = { enabled: false, probed: false, participants: [], threads: [] }

// useMeshRoster is the Team view's one read hook for "who is in the mesh and what conversations exist" —
// GET /api/mesh/participants + GET /api/mesh/threads, polled together. A transient error on a LATER poll
// keeps the last known roster (same posture as useStageAssistants); a 404 on the FIRST poll is treated as
// "capability off" and stops polling.
export function useMeshRoster(): MeshRoster {
  const [state, setState] = useState<MeshRoster>(EMPTY)
  const offRef = useRef(false)

  useEffect(() => {
    let cancelled = false
    offRef.current = false
    let timer: ReturnType<typeof setInterval> | undefined

    const load = (first: boolean): void => {
      if (offRef.current) return
      Promise.all([listMeshParticipants(), listMeshThreads()])
        .then(([p, t]) => {
          if (cancelled) return
          setState({ enabled: true, probed: true, participants: p.participants, threads: t.threads })
        })
        .catch((err: unknown) => {
          if (cancelled) return
          if (first && err instanceof MeshApiError && err.status === 404) {
            offRef.current = true
            if (timer !== undefined) clearInterval(timer)
            setState(EMPTY_PROBED)
            return
          }
          if (first) setState(EMPTY_PROBED)
          // Later-poll transient error: keep whatever roster we already have.
        })
    }

    load(true)
    timer = setInterval(() => load(false), POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      if (timer !== undefined) clearInterval(timer)
    }
  }, [])

  return state
}

const EMPTY_PROBED: MeshRoster = { enabled: false, probed: true, participants: [], threads: [] }
