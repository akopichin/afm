import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MeshApiError, sendMeshMessageTo } from '../../api/mesh-client'
import { SideAgentApiError } from '../../api/side-agent-client'
import type { DraftComment } from '../file-browser/draft-comments'
import type { FrozenSend } from './CommentProvider'
import {
  ASSISTANT_LIMIT_BYTES,
  MESH_LIMIT_BYTES,
  buildCommentMessage,
  deliverComments,
  messageBytes,
  messageLimitProblem,
} from './comment-delivery'

vi.mock('../../api/mesh-client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/mesh-client')>()),
  sendMeshMessageTo: vi.fn(),
}))

const meshSend = vi.mocked(sendMeshMessageTo)

function draft(over: Partial<DraftComment> = {}): DraftComment {
  return {
    root: 'root',
    path: 'a.go',
    displayPath: 'project/a.go',
    reference: '[AFM file: "/work/a.go"]',
    line: 3,
    origLineText: 'x := @helper',
    text: 'rename this',
    ...over,
  }
}

function frozen(over: Partial<FrozenSend> = {}): FrozenSend {
  return { recipientId: 'run:qa', route: 'mesh', originStage: '', body: 'body', revisions: [{ key: 'file:1', revision: 1 }], ...over }
}

describe('buildCommentMessage', () => {
  it('puts the common message first, then one File comments block for all files', () => {
    const out = buildCommentMessage(
      [draft(), draft({ path: 'b.go', displayPath: 'project/b.go', reference: '[AFM file: "/work/b.go"]', line: 9, text: 'and here' })],
      'Please look at these',
    )
    expect(out.startsWith('Please look at these\n\n## File comments\n')).toBe(true)
    expect(out.match(/^## File comments$/gm)).toHaveLength(1)
    expect(out).toContain('"project/a.go"')
    expect(out).toContain('"project/b.go"')
  })

  it('an empty or blank common message still sends the comments, with no leading blank lines', () => {
    for (const message of ['', '  \n ']) {
      const out = buildCommentMessage([draft()], message)
      expect(out.startsWith('## File comments\n')).toBe(true)
    }
  })

  it('has no trailing whitespace so the byte count is stable', () => {
    const out = buildCommentMessage([draft()], 'hi')
    expect(out).toBe(out.trimEnd())
  })

  it('without comments there is nothing to send', () => {
    expect(buildCommentMessage([], 'only a message')).toBe('')
  })
})

describe('message limits', () => {
  it('counts UTF-8 bytes, not characters', () => {
    expect(messageBytes('abc')).toBe(3)
    expect(messageBytes('я')).toBe(2)
    expect(messageBytes('😀')).toBe(4)
  })

  it('Mesh accepts exactly 16 KiB and rejects one byte more', () => {
    expect(MESH_LIMIT_BYTES).toBe(16 * 1024)
    expect(messageLimitProblem('mesh', 'a'.repeat(MESH_LIMIT_BYTES))).toBeNull()
    expect(messageLimitProblem('mesh', 'a'.repeat(MESH_LIMIT_BYTES + 1))).toMatch(/16 KiB.*fewer comments/i)
  })

  it('Assistant accepts exactly 64 KiB and rejects one byte more', () => {
    expect(ASSISTANT_LIMIT_BYTES).toBe(64 * 1024)
    expect(messageLimitProblem('assistant', 'a'.repeat(ASSISTANT_LIMIT_BYTES))).toBeNull()
    expect(messageLimitProblem('assistant', 'a'.repeat(ASSISTANT_LIMIT_BYTES + 1))).toMatch(/64 KiB.*fewer comments/i)
  })

  it('multibyte text hits the limit before the character count does', () => {
    expect(messageLimitProblem('mesh', 'я'.repeat(MESH_LIMIT_BYTES / 2 + 1))).not.toBeNull()
  })
})

describe('deliverComments', () => {
  beforeEach(() => {
    meshSend.mockReset()
  })

  it('Mesh route: exactly one explicit recipient id, the request id and the frozen body', async () => {
    meshSend.mockResolvedValue({} as never)
    await deliverComments(frozen({ recipientId: 'run:qa', body: 'hello @other' }), 'req-1', vi.fn())
    expect(meshSend).toHaveBeenCalledTimes(1)
    expect(meshSend).toHaveBeenCalledWith({ requestId: 'req-1', recipientIds: ['run:qa'], body: 'hello @other' })
  })

  it('stage agent: the frozen origin stage goes along so the server checks reachability', async () => {
    meshSend.mockResolvedValue({} as never)
    await deliverComments(frozen({ recipientId: 'stage:impl', originStage: 'impl' }), 'req-1', vi.fn())
    expect(meshSend).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['stage:impl'], originStage: 'impl' }))
  })

  it('a server rejection (agent just finished) surfaces its reason and keeps the request retryable', async () => {
    meshSend.mockRejectedValue(new MeshApiError('/api/mesh/messages', 409, 'recipient_unreachable', 'impl has finished'))
    await expect(deliverComments(frozen({ originStage: 'impl' }), 'req-1', vi.fn())).rejects.toThrow('impl has finished')
  })

  it('Mesh 413 becomes a clear too-large message', async () => {
    meshSend.mockRejectedValue(new MeshApiError('/api/mesh/messages', 413, undefined))
    await expect(deliverComments(frozen(), 'req-1', vi.fn())).rejects.toThrow(/too large.*16 KiB.*fewer comments/i)
  })

  it('Assistant route works without Mesh: goes through the Assistant sender with the same id and body', async () => {
    const assistant = vi.fn().mockResolvedValue(undefined)
    await deliverComments(frozen({ route: 'assistant', recipientId: 'side:r1', body: 'exact' }), 'req-2', assistant)
    expect(assistant).toHaveBeenCalledWith('req-2', 'exact')
    expect(meshSend).not.toHaveBeenCalled()
  })

  it('Assistant busy rejects with a readable reason', async () => {
    const assistant = vi.fn().mockRejectedValue(new SideAgentApiError(409, 'busy'))
    await expect(deliverComments(frozen({ route: 'assistant' }), 'req-2', assistant)).rejects.toThrow(/still answering/i)
  })

  it('Assistant 413 and context limit are explained', async () => {
    const tooLarge = vi.fn().mockRejectedValue(new SideAgentApiError(413, 'message_too_large'))
    await expect(deliverComments(frozen({ route: 'assistant' }), 'r', tooLarge)).rejects.toThrow(/too large.*64 KiB/i)
    const context = vi.fn().mockRejectedValue(new SideAgentApiError(422, 'context_limit'))
    await expect(deliverComments(frozen({ route: 'assistant' }), 'r', context)).rejects.toThrow(/conversation.*too long/i)
  })

  it('an unavailable Assistant (503) is not reported as a generic failure', async () => {
    const assistant = vi.fn().mockRejectedValue(new SideAgentApiError(503, 'storage_unavailable'))
    await expect(deliverComments(frozen({ route: 'assistant' }), 'r', assistant)).rejects.toThrow(/unavailable|try again/i)
  })

  it('an oversized frozen body is refused before any request', async () => {
    const assistant = vi.fn()
    await expect(deliverComments(frozen({ body: 'a'.repeat(MESH_LIMIT_BYTES + 1) }), 'r', assistant)).rejects.toThrow(/16 KiB/)
    await expect(deliverComments(frozen({ route: 'assistant', body: 'a'.repeat(ASSISTANT_LIMIT_BYTES + 1) }), 'r', assistant)).rejects.toThrow(/64 KiB/)
    expect(meshSend).not.toHaveBeenCalled()
    expect(assistant).not.toHaveBeenCalled()
  })

  it('network errors pass through unchanged', async () => {
    meshSend.mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(deliverComments(frozen(), 'r', vi.fn())).rejects.toThrow('Failed to fetch')
  })
})
