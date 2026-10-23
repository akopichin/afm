// comment-recipients — кому можно отправить файловые комментарии: Assistant текущего рана и достижимые агенты
// из ростера, который App уже опрашивает (второго опроса нет). Чистые функции + контекст ростера.

import { createContext, useContext, useMemo } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import { participantLabel, participantRole } from '../../lib/mesh-roster'
import { recipientProblem } from '../feed-workspace/composer-recipients'
import { useSideAgentContext } from '../side-agent/side-agent-context'
import { useComments } from './CommentProvider'

export const CHOOSE_AGENT = 'Choose an agent'
export const NO_RECIPIENTS = 'No agent can receive comments right now'
export const ASSISTANT_BUSY = 'The Assistant is still answering — send when it finishes'
export const RECIPIENT_GONE = 'The chosen agent is no longer available — choose another one'

export type CommentRecipient = {
  // id — стабильный MeshParticipant.id; у Assistant — `side:<runId>` независимо от Mesh.
  id: string
  label: string
  role: string
  route: 'mesh' | 'assistant'
  // originStage — стадия, в чьей ленте и треде показывается разговор; достижимость сервер проверяет для любого адресата.
  originStage: string
  busy: boolean
}

export type CommentRecipientsInput = {
  runId: string
  // participants — ростер Mesh или пусто, если Mesh/collaboration выключены.
  participants: MeshParticipant[]
  stages: { id: string; status: string }[]
  assistant: { available: boolean; busy: boolean }
}

const FINISHED_STAGE_STATUSES = ['done', 'failed']

// A stage agent that finished reads no message; the roster's reachable flag does not know the stage status yet.
function canReceive(p: MeshParticipant, stages: CommentRecipientsInput['stages']): boolean {
  if (!['stage_agent', 'helper', 'run_agent'].includes(p.kind)) return false
  if (recipientProblem(p, participantLabel(p), stages) !== null) return false
  return p.kind !== 'stage_agent' || !FINISHED_STAGE_STATUSES.includes(stages.find((s) => s.id === p.stage_id)?.status ?? '')
}

export function commentRecipients({ runId, participants, stages, assistant }: CommentRecipientsInput): CommentRecipient[] {
  const list: CommentRecipient[] = []
  if (assistant.available && runId !== '') {
    list.push({ id: `side:${runId}`, label: 'Assistant', role: 'Assistant', route: 'assistant', originStage: '', busy: assistant.busy })
  }
  for (const p of participants) {
    if (!canReceive(p, stages)) continue
    list.push({
      id: p.id,
      label: participantLabel(p),
      role: participantRole(p.kind),
      route: 'mesh',
      originStage: p.kind === 'stage_agent' ? p.stage_id ?? '' : '',
      busy: false,
    })
  }
  return list
}

export type RecipientChoice = { recipient: CommentRecipient | null; problem: string | null }

// resolveCommentRecipient — адресат комментария: свой выбор, иначе последний в ране, иначе единственный доступный.
// Выбранный, но пропавший из ростера ID не стирается: Send выключен с причиной, а при возвращении агента всё работает.
export function resolveCommentRecipient(options: CommentRecipient[], recipientId: string, lastRecipientId: string): RecipientChoice {
  const find = (id: string) => options.find((o) => o.id === id)
  const chosen = find(recipientId)
  if (recipientId !== '' && chosen === undefined) return { recipient: null, problem: RECIPIENT_GONE }
  const recipient = chosen ?? find(lastRecipientId) ?? (options.length === 1 ? options[0] : undefined)
  if (recipient === undefined) return { recipient: null, problem: options.length === 0 ? NO_RECIPIENTS : CHOOSE_AGENT }
  return { recipient, problem: recipient.busy ? ASSISTANT_BUSY : null }
}

export type CommentRecipientsRoster = { runId: string; participants: MeshParticipant[]; stages: { id: string; status: string }[] }

// Вне App (тесты других компонентов) ростера нет: остаётся только Assistant, если он включён.
export const CommentRecipientsContext = createContext<CommentRecipientsRoster>({ runId: '', participants: [], stages: [] })

export function useCommentRecipients(): CommentRecipient[] {
  const { runId, participants, stages } = useContext(CommentRecipientsContext)
  const { enabled, active } = useSideAgentContext()
  return useMemo(
    () => commentRecipients({ runId, participants, stages, assistant: { available: enabled, busy: active } }),
    [runId, participants, stages, enabled, active],
  )
}

// useCommentRecipientChoice — тот же выбор, что показывает селектор: по нему диалог отправки решает, включать ли Send.
export function useCommentRecipientChoice(recipientId: string): RecipientChoice & { options: CommentRecipient[] } {
  const options = useCommentRecipients()
  const { lastRecipientId } = useComments()
  return { options, ...resolveCommentRecipient(options, recipientId, lastRecipientId) }
}
