import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { FeedComposer, type FeedComposerMeshProps } from './FeedComposer'
import * as api from '../../api/run-client'
import type { Stage } from '../../types'

const mesh: FeedComposerMeshProps = {
  roster: { enabled: true, probed: true, threads: [], participants: [
    { id: 'user', kind: 'user', scope: 'run', reachable: true },
    { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', reachable: true },
  ] },
  profiles: [], stages: [{ id: 's1', status: 'running' } as Stage],
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
