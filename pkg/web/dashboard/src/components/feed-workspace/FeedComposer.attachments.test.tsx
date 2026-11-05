import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { FeedComposer, type FeedComposerMeshProps, type MeshReplyTarget } from './FeedComposer'
import { createComposerDraftStore, type ComposerDraftStore } from './composer-draft-store'
import { selectConversation, setMode } from './composer-draft'
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
// textarea — the one field, in whichever mode; chooseMode — the "Send as" switch.
const textareaOf = () => document.querySelector<HTMLTextAreaElement>('textarea.chat-composer-input')!
const chooseMode = (name: 'Chat' | 'Restart with note') => fireEvent.click(within(screen.getByRole('group', { name: 'Send as' })).getByRole('button', { name }))
const sendButton = (mode: 'Chat' | 'Restart with note') => within(textareaOf().closest('.pasteable-textarea-wrap')!.querySelector<HTMLElement>('.pasteable-toolbar')!).getByRole('button', { name: mode === 'Chat' ? 'Send message' : 'Restart with note' })
function storeIn(mode: 'Chat' | 'Restart with note'): ComposerDraftStore {
  const store = createComposerDraftStore()
  if (mode === 'Restart with note') store.update('s1', (d) => setMode(d, 'restart'))
  return store
}
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

it.each(['Chat', 'Restart with note'] as const)('%s toolbar uploads and removes an image through the shared Attach control', async (mode) => {
  const { container } = render(<FeedComposer stageId="s1" mesh={mesh} onSend={vi.fn()} />)
  chooseMode(mode)
  const textarea = mode === 'Chat' ? screen.getByRole('combobox') : screen.getByRole('textbox', { name: 'Restart instructions' })
  fireEvent.click(screen.getByRole('button', { name: 'Attach' }))
  fireEvent.click(screen.getByRole('menuitem', { name: /upload image/i }))
  const fileInputs = container.querySelectorAll('input[type="file"]')
  expect(fileInputs).toHaveLength(1)
  fireEvent.change(fileInputs[0]!, { target: { files: [new File(['image'], 'toolbar.png', { type: 'image/png' })] } })
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/toolbar.png]\n'))
  expect(screen.getByAltText('Pasted screenshot')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Remove pasted image' }))
  expect(textarea).toHaveValue('')
  expect(screen.queryByAltText('Pasted screenshot')).toBeNull()
})

it('switching Chat ↔ Restart keeps the image chip and an upload in progress, without uploading again', async () => {
  let finish!: (value: { path: string }) => void
  vi.mocked(api.uploadAttachment).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  const store = createComposerDraftStore()
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={vi.fn()} draftStore={store} />)
  const textarea = textareaOf()
  fireEvent.change(textarea, { target: { value: 'look ' } })
  paste(textarea, 'slow.png')
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  chooseMode('Restart with note')
  chooseMode('Chat')
  chooseMode('Restart with note')
  expect(textareaOf()).toBe(textarea)
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  await act(async () => finish({ path: '/attachments/slow.png' }))
  expect(textarea).toHaveValue('look [Screenshot: /attachments/slow.png]\n')
  expect(api.uploadAttachment).toHaveBeenCalledTimes(1)
  expect(store.get('s1').attachments).toHaveLength(1)
})

it.each(['Chat', 'Restart with note'] as const)('%s removes only sent attachments from both the textarea and saved draft', async (mode) => {
  let finish!: () => void
  const sending = new Promise<void>((resolve) => { finish = resolve })
  vi.spyOn(api, 'postStageDiscussionMessage').mockImplementation(async () => { await sending; return { messageId: 'sent' } })
  const store = storeIn(mode)
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={() => sending} draftStore={store} />)
  const textarea = textareaOf()
  paste(textarea, 'sent.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/sent.png]\n'))
  fireEvent.click(sendButton(mode))
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
  expect(store.get('s1').attachments.map((a) => a.previewUrl)).toEqual(['blob:image-2'])
})

// The user glued the marker's line to their text: the line break after it is gone, the image is still sent.
it.each(['Chat', 'Restart with note'] as const)('%s drops a sent image from the composer and draft although the line break after its marker is gone', async (mode) => {
  vi.spyOn(api, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'sent' })
  const store = storeIn(mode)
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={vi.fn().mockResolvedValue(undefined)} draftStore={store} />)
  const textarea = textareaOf()
  paste(textarea, 'glued.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/glued.png]\n'))
  fireEvent.change(textarea, { target: { value: '[Screenshot: /attachments/glued.png] tool calls' } })
  fireEvent.click(sendButton(mode))
  await waitFor(() => expect(screen.queryByAltText('Pasted screenshot')).toBeNull())
  expect(textarea).toHaveValue('')
  expect(store.get('s1').attachments).toHaveLength(0)
  expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:image-1')
})

it('a success after the composer unmounted drops the sent image from the stored draft and revokes its preview', async () => {
  let finish!: () => void
  const sending = new Promise<void>((resolve) => { finish = resolve })
  const store = storeIn('Restart with note')
  const { unmount } = render(<FeedComposer stageId="s1" mesh={mesh} onSend={() => sending} draftStore={store} />)
  paste(textareaOf(), 'sent.png')
  await waitFor(() => expect(textareaOf()).toHaveValue('[Screenshot: /attachments/sent.png]\n'))
  fireEvent.click(sendButton('Restart with note'))
  unmount()
  await act(async () => { finish(); await sending })
  expect(store.get('s1')).toMatchObject({ text: '', attachments: [], inFlight: null })
  expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:image-1')
})

it('a success after the reply changed keeps the text and its image', async () => {
  let finish!: () => void
  const sending = new Promise<void>((resolve) => { finish = resolve })
  const store = storeIn('Restart with note')
  render(<FeedComposer stageId="s1" mesh={meshWithRunAgent} onSend={() => sending} draftStore={store} />)
  paste(textareaOf(), 'kept.png')
  await waitFor(() => expect(textareaOf()).toHaveValue('[Screenshot: /attachments/kept.png]\n'))
  fireEvent.click(sendButton('Restart with note'))
  act(() => {
    store.update('s1', (d) => selectConversation(d, replyTarget))
  })
  await act(async () => { finish(); await sending })
  expect(textareaOf()).toHaveValue('[Screenshot: /attachments/kept.png]\n')
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  expect(URL.revokeObjectURL).not.toHaveBeenCalled()
})

it.each(['Chat', 'Restart with note'] as const)('%s keeps the image chip and text when sending fails', async (mode) => {
  const failing = vi.fn().mockRejectedValue(new Error('offline'))
  vi.spyOn(api, 'postStageDiscussionMessage').mockImplementation(failing)
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={failing} draftStore={storeIn(mode)} />)
  const textarea = textareaOf()
  paste(textarea, 'kept.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/kept.png]\n'))
  fireEvent.change(textarea, { target: { value: '[Screenshot: /attachments/kept.png] still here' } })
  fireEvent.click(sendButton(mode))
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

it('pastes a clipboard image into Restart, uploads it and inserts its marker and chip', async () => {
  const send = vi.fn().mockResolvedValue(undefined)
  render(<FeedComposer stageId="s1" mesh={mesh} onSend={send} />)
  chooseMode('Restart with note')
  const textarea = screen.getByPlaceholderText('New instructions — the agent restarts this phase with them…')
  paste(textarea, 'clipboard.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/clipboard.png]\n'))
  expect(api.uploadAttachment).toHaveBeenCalledWith('s1', expect.objectContaining({ name: 'clipboard.png', type: 'image/png' }))
  expect(screen.getByAltText('Pasted screenshot')).toHaveAttribute('src', 'blob:image-1')
  fireEvent.click(sendButton('Restart with note'))
  await waitFor(() => expect(send).toHaveBeenCalledWith('[Screenshot: /attachments/clipboard.png]'))
})

// The glued marker also has to be recognised on the two other send paths: a reply in a conversation and a new helper.
function renderReply(store: ComposerDraftStore) {
  store.update('s1', (d) => selectConversation(d, replyTarget))
  render(<FeedComposer stageId="s1" mesh={meshWithRunAgent} onSend={vi.fn()} draftStore={store} />)
  return screen.getByRole('combobox')
}

it('Reply in conversation drops a sent image although the line break after its marker is gone', async () => {
  vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm9', seq: 9, thread_id: 'mt-1' })
  const store = createComposerDraftStore()
  const textarea = renderReply(store)
  paste(textarea, 'glued.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/glued.png]\n'))
  fireEvent.change(textarea, { target: { value: GLUED } })
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
  await waitFor(() => expect(screen.queryByAltText('Pasted screenshot')).toBeNull())
  expect(textarea).toHaveValue('')
  expect(store.get('s1').attachments).toHaveLength(0)
})

it('Reply in conversation keeps the image chip and text when sending fails', async () => {
  const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockRejectedValue(new Error('offline'))
  const store = createComposerDraftStore()
  const textarea = renderReply(store)
  paste(textarea, 'glued.png')
  await waitFor(() => expect(textarea).toHaveValue('[Screenshot: /attachments/glued.png]\n'))
  fireEvent.change(textarea, { target: { value: GLUED } })
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
  await waitFor(() => expect(send).toHaveBeenCalled())
  await act(async () => { await Promise.resolve() })
  expect(textarea).toHaveValue(GLUED)
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  expect(store.get('s1').attachments).toHaveLength(1)
})

async function pasteForNewHelper(store: ComposerDraftStore) {
  render(<FeedComposer stageId="s1" mesh={meshWithHelper} onSend={vi.fn()} draftStore={store} />)
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
  const store = createComposerDraftStore()
  const textarea = await pasteForNewHelper(store)
  await waitFor(() => expect(screen.queryByAltText('Pasted screenshot')).toBeNull())
  expect(textarea).toHaveValue('')
  expect(store.get('s1').attachments).toHaveLength(0)
})

it('Attach helper keeps the image chip and text when the attach fails', async () => {
  const attach = vi.spyOn(meshClient, 'attachAndSendMesh').mockRejectedValue(new Error('offline'))
  const store = createComposerDraftStore()
  const textarea = await pasteForNewHelper(store)
  await waitFor(() => expect(attach).toHaveBeenCalled())
  await screen.findByRole('alert')
  expect(textarea).toHaveValue(GLUED)
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  expect(store.get('s1')).toMatchObject({ inFlight: null, text: GLUED })
  expect(store.get('s1').attachments).toHaveLength(1)
})

it('a late Attach helper success after Cancel keeps the text and the image added meanwhile', async () => {
  let finish: (v: meshClient.MeshAttachAndSendResult) => void = () => {}
  const attach = vi.spyOn(meshClient, 'attachAndSendMesh').mockReturnValue(new Promise((res) => { finish = res }))
  const store = createComposerDraftStore()
  render(<FeedComposer stageId="s1" mesh={meshWithHelper} onSend={vi.fn()} draftStore={store} />)
  const textarea = textareaOf()
  fireEvent.change(textarea, { target: { value: '@' } })
  fireEvent.click(screen.getByText('@frontendDeveloper'))
  fireEvent.change(textarea, { target: { value: 'review the parser' } })
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
  fireEvent.click(screen.getByRole('button', { name: /attach and send/i }))
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  fireEvent.change(textarea, { target: { value: 'review the parser and the lexer ' } })
  paste(textarea, 'late.png')
  await waitFor(() => expect(textarea.value).toContain('[Screenshot: /attachments/late.png]'))
  const edited = textarea.value
  await act(async () => finish({ task_id: 't1', participant_id: 'helper:t1', thread_id: 'mt-attach', message_id: 'm1' }))
  expect(attach).toHaveBeenCalledWith(expect.objectContaining({ instruction: 'review the parser' }))
  expect(textarea).toHaveValue(edited.slice('review the parser '.length))
  expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  expect(store.get('s1').attachments).toHaveLength(1)
})
