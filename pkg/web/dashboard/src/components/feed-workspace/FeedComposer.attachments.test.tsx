import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { FeedComposer, type FeedComposerMeshProps, type MeshReplyTarget } from './FeedComposer'
import * as api from '../../api/run-client'
import * as meshClient from '../../api/mesh-client'
import type { Stage } from '../../types'

const mesh: FeedComposerMeshProps = {
  roster: { enabled: true, probed: true, threads: [], participants: [
    { id: 'user', kind: 'user', scope: 'run', reachable: true },
    { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', reachable: true },
  ] },
  profiles: [], stages: [{ id: 's1', status: 'running' } as Stage],
}
const meshWithHelper: FeedComposerMeshProps = {
  ...mesh,
  profiles: [{ id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '' }],
}
const meshWithRunAgent: FeedComposerMeshProps = {
  ...mesh,
  roster: { ...mesh.roster, participants: [...mesh.roster.participants, { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', reachable: true }] },
}
const replyTarget: MeshReplyTarget = { messageId: 'm5', threadId: 'mt-1', senderId: 'run:t-1', preview: 'Found a bug', members: ['user', 'run:t-1'] }
const GLUED = '[Screenshot: /attachments/glued.png] tool calls'
function paste(node: HTMLElement, name: string) {
  fireEvent.paste(node, { clipboardData: { items: [{ kind: 'file', type: 'image/png', getAsFile: () => new File(['x'], name, { type: 'image/png' }) }] } })
}
beforeEach(() => {
  let sequence = 0
  vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:image-${++sequence}`)
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  vi.spyOn(api, 'uploadAttachment').mockImplementation(async (_id, file) => ({ path: `/attachments/${file instanceof File ? file.name : 'image.png'}` }))
})
afterEach(() => vi.restoreAllMocks())

it.each(['Message', 'Revise'] as const)('%s toolbar uploads and removes an image through the shared Attach control', async (mode) => {
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={vi.fn()} />)
  fireEvent.click(screen.getByRole('tab', { name: mode }))
  const panel = screen.getByRole('tabpanel', { name: mode })
  const textarea = mode === 'Message' ? within(panel).getByRole('combobox') : within(panel).getByRole('textbox', { name: 'Revision instructions' })
  fireEvent.click(within(panel).getByRole('button', { name: 'Attach' }))
  fireEvent.click(within(panel).getByRole('menuitem', { name: /upload image/i }))
  const fileInputs = panel.querySelectorAll('input[type="file"]')
  expect(fileInputs).toHaveLength(1)
  fireEvent.change(fileInputs[0]!, { target: { files: [new File(['image'], 'toolbar.png', { type: 'image/png' })] } })
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/toolbar.png]\n'))
  expect(within(panel).getByAltText('Pasted screenshot')).toBeVisible()
  fireEvent.click(within(panel).getByRole('button', { name: 'Remove pasted image' }))
  expect(textarea).toHaveValue('')
  expect(within(panel).queryByAltText('Pasted screenshot')).toBeNull()
})

it.each(['Message', 'Revise'] as const)('%s removes only sent attachments from both the textarea and saved draft', async (mode) => {
  let finish!: () => void
  const sending = new Promise<void>((resolve) => { finish = resolve })
  vi.spyOn(api, 'postStageDiscussionMessage').mockImplementation(async () => { await sending; return { messageId: 'sent' } })
  const saved = vi.fn()
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={() => sending} onDraftChange={saved} />)
  if (mode === 'Revise') fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  const textarea = mode === 'Message' ? screen.getByRole('combobox') : screen.getByPlaceholderText('Describe what the agent should change…')
  paste(textarea, 'sent.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/sent.png]\n'))
  fireEvent.click(screen.getByRole('button', { name: mode === 'Message' ? 'Send message' : 'Send revision' }))
  fireEvent.change(textarea, { target: { value: `${(textarea as HTMLTextAreaElement).value}Keep this note\n` } })
  paste(textarea, 'later.png')
  await waitFor(() => expect(screen.getAllByAltText('Pasted screenshot')).toHaveLength(2))
  await waitFor(() => expect((textarea as HTMLTextAreaElement).value).toContain('/later.png'))
  await act(async () => { finish(); await sending })
  expect(textarea).toHaveValue('Keep this note\n[Screenshot: /attachments/later.png]\n')
  expect(screen.getAllByAltText('Pasted screenshot')).toHaveLength(1)
  expect(screen.getByAltText('Pasted screenshot')).toHaveAttribute('src', 'blob:image-2')
  expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:image-1')
  expect(URL.revokeObjectURL).not.toHaveBeenCalledWith('blob:image-2')
  const key = mode === 'Message' ? 'messageAttachments' : 'reviseAttachments'
  const last = saved.mock.calls.map(([patch]) => patch).filter((patch) => key in patch).at(-1)
  expect(last[key]).toHaveLength(1)
  expect(last[key][0].previewUrl).toBe('blob:image-2')
})

// The user glued the marker's line to their text: the line break after it is gone, the image is still sent.
it.each(['Message', 'Revise'] as const)('%s drops a sent image from the composer and draft although the line break after its marker is gone', async (mode) => {
  vi.spyOn(api, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'sent' })
  const saved = vi.fn()
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={vi.fn().mockResolvedValue(undefined)} onDraftChange={saved} />)
  if (mode === 'Revise') fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  const textarea = mode === 'Message' ? screen.getByRole('combobox') : screen.getByPlaceholderText('Describe what the agent should change…')
  paste(textarea, 'glued.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/glued.png]\n'))
  fireEvent.change(textarea, { target: { value: '[Screenshot: /attachments/glued.png] tool calls' } })
  fireEvent.click(screen.getByRole('button', { name: mode === 'Message' ? 'Send message' : 'Send revision' }))
  await waitFor(() => expect(screen.queryByAltText('Pasted screenshot')).toBeNull())
  expect(textarea).toHaveValue('')
  const key = mode === 'Message' ? 'messageAttachments' : 'reviseAttachments'
  const last = saved.mock.calls.map(([patch]) => patch).filter((patch) => key in patch).at(-1)
  expect(last[key]).toHaveLength(0)
  expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:image-1')
})

it.each(['Message', 'Revise'] as const)('%s keeps the image chip and text when sending fails', async (mode) => {
  const failing = vi.fn().mockRejectedValue(new Error('offline'))
  vi.spyOn(api, 'postStageDiscussionMessage').mockImplementation(failing)
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={failing} />)
  if (mode === 'Revise') fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  const textarea = mode === 'Message' ? screen.getByRole('combobox') : screen.getByPlaceholderText('Describe what the agent should change…')
  paste(textarea, 'kept.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/kept.png]\n'))
  fireEvent.change(textarea, { target: { value: '[Screenshot: /attachments/kept.png] still here' } })
  fireEvent.click(screen.getByRole('button', { name: mode === 'Message' ? 'Send message' : 'Send revision' }))
  await waitFor(() => expect(failing).toHaveBeenCalled())
  await act(async () => { await Promise.resolve() })
  expect(textarea).toHaveValue('[Screenshot: /attachments/kept.png] still here')
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
})

it('keeps an image chip whose marker is not in the sent text', async () => {
  vi.spyOn(api, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'sent' })
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={vi.fn()} />)
  const textarea = screen.getByRole('combobox')
  paste(textarea, 'unsent.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/unsent.png]\n'))
  fireEvent.change(textarea, { target: { value: 'plain note' } })
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
  await waitFor(() => expect(api.postStageDiscussionMessage).toHaveBeenCalled())
  await waitFor(() => expect(textarea).toHaveValue(''))
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
})

it('pastes a clipboard image into Revise, uploads it and inserts its marker and chip', async () => {
  const send = vi.fn().mockResolvedValue(undefined)
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={send} />)
  fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  const textarea = screen.getByPlaceholderText('Describe what the agent should change…')
  paste(textarea, 'clipboard.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/clipboard.png]\n'))
  expect(api.uploadAttachment).toHaveBeenCalledWith('s1', expect.objectContaining({ name: 'clipboard.png', type: 'image/png' }))
  expect(screen.getByAltText('Pasted screenshot')).toHaveAttribute('src', 'blob:image-1')
  fireEvent.click(screen.getByRole('button', { name: 'Send revision' }))
  await waitFor(() => expect(send).toHaveBeenCalledWith('[Screenshot: /attachments/clipboard.png]'))
})

// The glued marker also has to be recognised on the two other send paths: a reply in a conversation and a new helper.
function renderReply(saved: (patch: Record<string, unknown>) => void) {
  render(<FeedComposer stageId="s1" mesh={meshWithRunAgent} meshReply={replyTarget} onSend={vi.fn()} onDraftChange={saved} />)
  return screen.getByRole('combobox')
}
function lastAttachments(saved: ReturnType<typeof vi.fn>, key: string) {
  return saved.mock.calls.map(([patch]) => patch).filter((patch) => key in patch).at(-1)?.[key]
}

it('Reply in conversation drops a sent image although the line break after its marker is gone', async () => {
  vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm9', seq: 9, thread_id: 'mt-1' })
  const saved = vi.fn()
  const textarea = renderReply(saved)
  paste(textarea, 'glued.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/glued.png]\n'))
  fireEvent.change(textarea, { target: { value: GLUED } })
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
  await waitFor(() => expect(screen.queryByAltText('Pasted screenshot')).toBeNull())
  expect(textarea).toHaveValue('')
  expect(lastAttachments(saved, 'replyAttachments')).toHaveLength(0)
})

it('Reply in conversation keeps the image chip and text when sending fails', async () => {
  const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockRejectedValue(new Error('offline'))
  const saved = vi.fn()
  const textarea = renderReply(saved)
  paste(textarea, 'glued.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/glued.png]\n'))
  fireEvent.change(textarea, { target: { value: GLUED } })
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
  await waitFor(() => expect(send).toHaveBeenCalled())
  await act(async () => { await Promise.resolve() })
  expect(textarea).toHaveValue(GLUED)
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  expect(lastAttachments(saved, 'replyAttachments')).toHaveLength(1)
})

async function pasteForNewHelper(saved: (patch: Record<string, unknown>) => void) {
  render(<FeedComposer stageId="s1" mesh={meshWithHelper} onSend={vi.fn()} onDraftChange={saved} />)
  const textarea = screen.getByRole('combobox')
  fireEvent.change(textarea, { target: { value: '@' } })
  fireEvent.click(screen.getByText('@frontendDeveloper'))
  paste(textarea, 'glued.png')
  await waitFor(() => expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument())
  fireEvent.change(textarea, { target: { value: GLUED } })
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
  fireEvent.click(await screen.findByRole('button', { name: /attach and send/i }))
  return textarea
}

it('Attach helper drops a sent image after confirmation although the line break after its marker is gone', async () => {
  vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't1', participant_id: 'helper:t1', thread_id: 'mt-attach', message_id: 'm1' })
  const saved = vi.fn()
  const textarea = await pasteForNewHelper(saved)
  await waitFor(() => expect(screen.queryByAltText('Pasted screenshot')).toBeNull())
  expect(textarea).toHaveValue('')
  expect(lastAttachments(saved, 'messageAttachments')).toHaveLength(0)
})

it('Attach helper keeps the image chip and text when the attach fails', async () => {
  const attach = vi.spyOn(meshClient, 'attachAndSendMesh').mockRejectedValue(new Error('offline'))
  const saved = vi.fn()
  const textarea = await pasteForNewHelper(saved)
  await waitFor(() => expect(attach).toHaveBeenCalled())
  await screen.findByRole('alert')
  expect(textarea).toHaveValue(GLUED)
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  expect(lastAttachments(saved, 'messageAttachments')).toHaveLength(1)
})
