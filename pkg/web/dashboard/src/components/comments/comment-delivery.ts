// comment-delivery — сборка и отправка одного сообщения с файловыми комментариями: текст, лимиты в байтах и
// маршрут (Mesh или Assistant). Не знает про компоненты: зависит только от замороженного пакета.

import { MeshApiError, sendMeshMessageTo } from '../../api/mesh-client'
import { SideAgentApiError } from '../../api/side-agent-client'
import { errorCodeToMessage } from '../../types/side-agent'
import { renderDraftComments, type DraftComment } from '../file-browser/draft-comments'
import { ASSISTANT_BUSY } from './comment-recipients'
import type { FrozenSend } from './CommentProvider'

// Пределы серверов: Mesh — agentcomm.MaxBodyBytes, Assistant — sideagent.MaxMessageBytes.
export const MESH_LIMIT_BYTES = 16 * 1024
export const ASSISTANT_LIMIT_BYTES = 64 * 1024

type Route = FrozenSend['route']

const LIMITS: Record<Route, { bytes: number; label: string }> = {
  mesh: { bytes: MESH_LIMIT_BYTES, label: '16 KiB' },
  assistant: { bytes: ASSISTANT_LIMIT_BYTES, label: '64 KiB' },
}

export function messageBytes(body: string): number {
  return new TextEncoder().encode(body).length
}

// messageLimitProblem — причина, по которой пакет нельзя отправить по маршруту; null — помещается.
export function messageLimitProblem(route: Route, body: string): string | null {
  const { bytes, label } = LIMITS[route]
  return messageBytes(body) > bytes ? `The message is over the ${label} limit — send fewer comments at once` : null
}

// buildCommentMessage — общее сообщение (если есть) и блок комментариев; без комментариев отправлять нечего.
export function buildCommentMessage(drafts: DraftComment[], message: string): string {
  const comments = renderDraftComments(drafts)
  if (comments === '') return ''
  const intro = message.trim()
  return (intro === '' ? comments : `${intro}\n\n${comments}`).trimEnd()
}

function meshFailure(err: unknown): unknown {
  if (err instanceof MeshApiError && err.status === 413) {
    return new Error(`The message is too large for one Mesh message (limit ${LIMITS.mesh.label}) — send fewer comments at once`)
  }
  return err
}

function assistantFailure(err: unknown): unknown {
  if (!(err instanceof SideAgentApiError)) return err
  if (err.code === 'busy') return new Error(ASSISTANT_BUSY)
  if (err.code === 'message_too_large') {
    return new Error(`The Assistant refused the message as too large (limit ${LIMITS.assistant.label}) — send fewer comments at once`)
  }
  return new Error(errorCodeToMessage(err.code ?? ''))
}

export type AssistantSender = (clientMessageId: string, body: string) => Promise<void>

// deliverComments отправляет замороженный пакет ровно одному адресату. Тот же requestId при повторе даёт серверу
// узнать операцию; Assistant идёт через sendAssistant, чтобы обновились running/история/индикатор.
export async function deliverComments(frozen: Readonly<FrozenSend>, requestId: string, sendAssistant: AssistantSender): Promise<void> {
  const problem = messageLimitProblem(frozen.route, frozen.body)
  if (problem !== null) throw new Error(problem)
  if (frozen.route === 'assistant') {
    try {
      await sendAssistant(requestId, frozen.body)
    } catch (err) {
      throw assistantFailure(err)
    }
    return
  }
  try {
    await sendMeshMessageTo({
      requestId,
      recipientIds: [frozen.recipientId],
      body: frozen.body,
      ...(frozen.originStage !== '' ? { originStage: frozen.originStage } : {}),
    })
  } catch (err) {
    throw meshFailure(err)
  }
}
