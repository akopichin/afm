import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sendMeshMessageTo, type MeshParticipant } from '../../api/mesh-client'
import { FilesApiMock } from '../file-browser/test-support'
import { SideAgentContext, type SideAgentContextValue } from '../side-agent/side-agent-context'
import { CommentProvider, useComments, type CommentsApi } from './CommentProvider'
import { CommentRecipientsContext } from './comment-recipients'
import { FileCommentSendDialog, FileCommentSendStatus } from './FileCommentSendDialog'

vi.mock('../../api/mesh-client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/mesh-client')>()),
  sendMeshMessageTo: vi.fn(),
}))
const meshSend = vi.mocked(sendMeshMessageTo)
const qa: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true }

type Seed = { key: string; root?: string; path: string; line: number; quote: string; text: string; etag?: string; recipientId?: string; draft?: boolean }

const side = (over: Partial<SideAgentContextValue> = {}): SideAgentContextValue => ({
  enabled: true, isOpen: false, open: () => {}, openWith: () => {}, sendExactMessage: () => Promise.resolve(), unseenResult: false, active: false, ...over,
})

type Handlers = { onClose: () => void; onOpenSource: (t: { root: string; path: string; line: number }) => void }

function Probe({ out }: { out: { api: CommentsApi | null } }): null {
  out.api = useComments()
  return null
}

function tree(out: { api: CommentsApi | null }, s: SideAgentContextValue, only: string | null | undefined, h: Handlers, participants: MeshParticipant[]) {
  return (
    <SideAgentContext.Provider value={s}>
      <CommentRecipientsContext.Provider value={{ runId: 'r1', participants, stages: [] }}>
        <CommentProvider runId="r1">
          <Probe out={out} />
          {only !== undefined && <FileCommentSendDialog only={only} onClose={h.onClose} onOpenSource={h.onOpenSource} />}
          <FileCommentSendStatus />
        </CommentProvider>
      </CommentRecipientsContext.Provider>
    </SideAgentContext.Provider>
  )
}

function setup(seeds: Seed[], opts: { side?: Partial<SideAgentContextValue>; only?: string | null | undefined; participants?: MeshParticipant[] } = {}) {
  const out: { api: CommentsApi | null } = { api: null }
  const handlers: Handlers = { onClose: vi.fn(), onOpenSource: vi.fn() }
  const current = side(opts.side)
  const participants = opts.participants ?? []
  const view = render(tree(out, current, undefined, handlers, participants))
  const api = (): CommentsApi => {
    if (out.api === null) throw new Error('no api')
    return out.api
  }
  for (const s of seeds) {
    act(() => {
      api().open(s.key, {
        anchor: { kind: 'file', root: s.root ?? 'project', path: s.path, displayPath: `afm/${s.path}`, reference: `[AFM file: "/w/${s.root ?? 'project'}/${s.path}"]`, etag: s.etag ?? 'W/"1"', line: s.line },
        quote: s.quote,
      })
    })
    act(() => api().edit(s.key, s.text))
    if (s.draft !== true) act(() => api().saveAndFold(s.key))
    if (s.recipientId !== undefined) act(() => api().setRecipient(s.key, s.recipientId ?? ''))
  }
  const open = (only: string | null | undefined) => view.rerender(tree(out, current, only, handlers, participants))
  if (opts.only !== undefined) open(opts.only)
  return { api, handlers, open, close: () => open(undefined) }
}

function installFiles(files: Record<string, { content: string; etag?: string }>): FilesApiMock {
  const mock = new FilesApiMock()
  mock.setRoots([{ id: 'project', label: 'afm' }])
  for (const [path, f] of Object.entries(files)) mock.setContent('project', path, { language: 'go', content: f.content, etag: f.etag })
  mock.install()
  return mock
}

const A: Seed = { key: 'f1', path: 'a.go', line: 2, quote: 'alpha two', text: 'first remark' }
const B: Seed = { key: 'f2', path: 'b.go', line: 1, quote: 'beta one', text: 'second remark', draft: true }

const sendButton = () => screen.getByRole('button', { name: /^Send \d+ comments?/ })

async function ready(): Promise<void> {
  await waitFor(() => expect(sendButton()).toBeEnabled())
}

beforeEach(() => {
  installFiles({ 'a.go': { content: 'alpha one\nalpha two' }, 'b.go': { content: 'beta one\nbeta two' } })
})
beforeEach(() => {
  meshSend.mockReset()
  meshSend.mockResolvedValue({} as never)
})
afterEach(() => vi.restoreAllMocks())

describe('FileCommentSendDialog preview', () => {
  it('Send all previews every comment with file, line, full text and Draft', async () => {
    setup([A, B], { only: null })
    const dialog = screen.getByRole('dialog', { name: 'Send file comments' })
    expect(within(dialog).getByText('2 comments in 2 files')).toBeInTheDocument()
    expect(within(dialog).getByText('afm/a.go')).toBeInTheDocument()
    expect(within(dialog).getByText('afm/b.go')).toBeInTheDocument()
    expect(within(dialog).getByText('Line 2')).toBeInTheDocument()
    expect(within(dialog).getByText('first remark')).toBeInTheDocument()
    expect(within(dialog).getByText('second remark')).toBeInTheDocument()
    expect(within(dialog).getAllByText('Draft')).toHaveLength(1)
    await ready()
  })

  it('files with the same label and path in different roots get separate preview sections', async () => {
    const mock = installFiles({ 'a.go': { content: 'alpha one\nalpha two' } })
    mock.setRoots([{ id: 'project', label: 'afm' }, { id: 'other', label: 'afm' }])
    mock.setContent('other', 'a.go', { language: 'go', content: 'alpha one\nalpha two' })
    setup([A, { ...A, key: 'f9', root: 'other', text: 'other remark' }], { only: null })
    const dialog = screen.getByRole('dialog', { name: 'Send file comments' })
    expect(within(dialog).getAllByRole('region', { name: 'afm/a.go' })).toHaveLength(2)
    expect(within(dialog).getByText('2 comments in 2 files')).toBeInTheDocument()
    await ready()
  })

  it('a single comment dialog previews only that comment', async () => {
    setup([A, B], { only: 'f2' })
    expect(screen.getByText('1 comment in 1 file')).toBeInTheDocument()
    expect(screen.getByText('second remark')).toBeInTheDocument()
    expect(screen.queryByText('first remark')).toBeNull()
    await ready()
  })

  it('Cancel closes without touching text or individual recipients', async () => {
    const { api, handlers } = setup([{ ...A, recipientId: 'side:r1' }], { only: null })
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(handlers.onClose).toHaveBeenCalledTimes(1)
    expect(api().comments.map((c) => [c.text, c.recipientId])).toEqual([['first remark', 'side:r1']])
    expect(api().send.operation).toBeNull()
  })
})

describe('FileCommentSendDialog source refresh', () => {
  it('a 304 confirms the anchor using the stored ETag', async () => {
    const files = installFiles({ 'a.go': { content: 'alpha one\nalpha two', etag: 'W/"1"' } })
    setup([A], { only: null })
    await ready()
    const call = files.fetchMock?.mock.calls.find(([url]) => String(url).includes('/api/files/content'))
    expect((call?.[1]?.headers as Record<string, string>)['If-None-Match']).toBe('W/"1"')
  })

  it('a new ETag with the same line text confirms automatically', async () => {
    installFiles({ 'a.go': { content: 'alpha one\nalpha two', etag: 'W/"2"' } })
    setup([A], { only: null })
    await ready()
    expect(screen.queryByText('Source changed')).toBeNull()
  })

  it('a changed line needs confirmation: Send stays disabled until Use current line', async () => {
    installFiles({ 'a.go': { content: 'alpha one\nALPHA TWO', etag: 'W/"2"' } })
    const { api } = setup([A], { only: null })
    const button = await screen.findByRole('button', { name: 'Send 1 comment (1 need confirmation)' })
    expect(button).toBeDisabled()
    expect(screen.getByText('Source changed')).toBeInTheDocument()
    expect(screen.getByText(/ALPHA TWO/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Use current line 2' }))
    await waitFor(() => expect(sendButton()).toBeEnabled())
    expect(sendButton()).toHaveAccessibleName('Send 1 comment')
    const [entry] = api().comments
    expect(entry?.quote).toBe('ALPHA TWO')
    expect(entry?.text).toBe('first remark')
  })

  it('a changed line can be sent with the old quote after explicit confirmation', async () => {
    installFiles({ 'a.go': { content: 'alpha one\nALPHA TWO', etag: 'W/"2"' } })
    setup([A], { only: null })
    await screen.findByRole('button', { name: 'Send 1 comment (1 need confirmation)' })
    fireEvent.click(screen.getByRole('checkbox', { name: 'Send with the old quote' }))
    await waitFor(() => expect(sendButton()).toBeEnabled())
  })

  it('Open source hands the line to the owner', async () => {
    installFiles({ 'a.go': { content: 'alpha one\nALPHA TWO', etag: 'W/"2"' } })
    const { handlers } = setup([A], { only: null })
    fireEvent.click(await screen.findByRole('button', { name: 'Open source' }))
    expect(handlers.onOpenSource).toHaveBeenCalledWith({ root: 'project', path: 'a.go', line: 2 })
  })

  it('a vanished file offers Delete and a send without a current quote', async () => {
    installFiles({}) // 404 для a.go
    const { api } = setup([A], { only: null })
    await screen.findByRole('button', { name: 'Send 1 comment (1 need confirmation)' })
    expect(screen.getByText(/no longer exists/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Send without a current quote' }))
    await waitFor(() => expect(sendButton()).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(api().comments).toEqual([])
  })

  it('a read error blocks sending and keeps the text', async () => {
    const files = installFiles({ 'a.go': { content: 'x' } })
    files.setContentError('project', 'a.go', 'read_failed', 500)
    const { api } = setup([A], { only: null })
    expect(await screen.findByText(/could not read the source/i)).toBeInTheDocument()
    expect(sendButton()).toBeDisabled()
    expect(api().comments[0]?.text).toBe('first remark')
  })

  it('refreshes the source again immediately before sending and blocks if the line changed meanwhile', async () => {
    const files = installFiles({ 'a.go': { content: 'alpha one\nalpha two', etag: 'W/"1"' } })
    const sendExactMessage = vi.fn().mockResolvedValue(undefined)
    setup([A], { only: null, side: { sendExactMessage } })
    await ready()
    files.setContent('project', 'a.go', { language: 'go', content: 'alpha one\nchanged now', etag: 'W/"3"' })

    fireEvent.click(sendButton())
    await screen.findByRole('button', { name: 'Send 1 comment (1 need confirmation)' })
    expect(sendExactMessage).not.toHaveBeenCalled()
  })
})

describe('FileCommentSendDialog sending', () => {
  it('sends one message with the common message and both comments; a double click makes one request', async () => {
    const sendExactMessage = vi.fn().mockResolvedValue(undefined)
    const { api } = setup([A, B], { only: null, side: { sendExactMessage } })
    await ready()
    fireEvent.change(screen.getByRole('textbox', { name: 'Message' }), { target: { value: 'Please acknowledge.' } })

    fireEvent.click(sendButton())
    fireEvent.click(sendButton())

    await waitFor(() => expect(sendExactMessage).toHaveBeenCalledTimes(1))
    const [requestId, body] = sendExactMessage.mock.calls[0] as [string, string]
    expect(requestId).not.toBe('')
    expect(body).toMatch(/^Please acknowledge\.\n\n## File comments/)
    expect(body).toContain('first remark')
    expect(body).toContain('second remark')
    await waitFor(() => expect(api().comments).toEqual([]))
  })

  it('a successful send closes the dialog and reports Accepted with the recipient', async () => {
    const { handlers } = setup([A, B], { only: null })
    await ready()
    fireEvent.click(sendButton())
    await waitFor(() => expect(handlers.onClose).toHaveBeenCalled())
    expect(screen.getByText('Accepted: 2 comments → Assistant (not read yet)')).toBeInTheDocument()
  })

  it('sending one comment records its recipient and leaves the others', async () => {
    const { api } = setup([A, B], { only: 'f1' })
    await ready()
    fireEvent.click(sendButton())
    await waitFor(() => expect(api().comments.map((c) => c.key)).toEqual(['f2']))
  })

  it('text edited while the request is in flight survives the acknowledgement', async () => {
    let resolve!: () => void
    const sendExactMessage = vi.fn(() => new Promise<void>((r) => { resolve = r }))
    const { api } = setup([A], { only: null, side: { sendExactMessage } })
    await ready()
    fireEvent.click(sendButton())
    await waitFor(() => expect(sendExactMessage).toHaveBeenCalled())

    act(() => api().edit('f1', 'edited later'))
    await act(async () => resolve())
    await waitFor(() => expect(api().send.accepted).not.toBeNull())
    expect(api().comments.map((c) => c.text)).toEqual(['edited later'])
  })

  it('closing the dialog while pending does not cancel; success after unmount still acknowledges only captured revisions', async () => {
    let resolve!: () => void
    const sendExactMessage = vi.fn(() => new Promise<void>((r) => { resolve = r }))
    const { api, close } = setup([A, B], { only: null, side: { sendExactMessage } })
    await ready()
    fireEvent.click(sendButton())
    await waitFor(() => expect(sendExactMessage).toHaveBeenCalled())
    close()
    act(() => api().edit('f2', 'newer second'))

    await act(async () => resolve())
    await waitFor(() => expect(api().send.accepted).not.toBeNull())
    expect(api().comments.map((c) => c.key)).toEqual(['f2'])
  })

  it('after a failure, reopening the dialog retries the same frozen packet and request id', async () => {
    const sendExactMessage = vi.fn().mockRejectedValueOnce(new Error('connection lost')).mockResolvedValue(undefined)
    const { api, close, open } = setup([A], { only: null, side: { sendExactMessage } })
    await ready()
    fireEvent.click(sendButton())
    expect(await screen.findByRole('alert')).toHaveTextContent('connection lost')

    close()
    act(() => api().edit('f1', 'edited after failure'))
    open(null)
    expect(screen.getByText('connection lost')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    await waitFor(() => expect(sendExactMessage).toHaveBeenCalledTimes(2))
    const [first, second] = sendExactMessage.mock.calls as [string, string][]
    expect(second).toEqual(first)
    expect(second?.[1]).toContain('first remark')
    expect(second?.[1]).not.toContain('edited after failure')
    await waitFor(() => expect(api().send.accepted).not.toBeNull())
    expect(api().comments.map((c) => c.text)).toEqual(['edited after failure'])
  })

  it('Discard attempt drops the failed packet and returns to composing', async () => {
    const sendExactMessage = vi.fn().mockRejectedValue(new Error('nope'))
    const { api } = setup([A], { only: null, side: { sendExactMessage } })
    await ready()
    fireEvent.click(sendButton())
    await screen.findByRole('alert')
    fireEvent.click(screen.getByRole('button', { name: 'Discard attempt' }))
    expect(api().send.operation).toBeNull()
    await ready()
  })
})

describe('FileCommentSendDialog in flight and defaults', () => {
  describe('source refresh in flight', () => {
    // Ответ getContent удерживается, пока тест меняет форму; release отпускает все отложенные запросы содержимого.
    function holdContent(files: FilesApiMock): () => void {
      const original = files.fetchMock?.getMockImplementation()
      if (original === undefined) throw new Error('files mock is not installed')
      let release!: () => void
      const gate = new Promise<void>((r) => { release = r })
      files.fetchMock?.mockImplementation(async (input, init) => {
        if (String(input).includes('/api/files/content')) await gate
        return original(input, init)
      })
      return release
    }

    it('sends exactly what was confirmed by the click, and a later edit survives', async () => {
      const files = installFiles({ 'a.go': { content: 'alpha one\nalpha two' } })
      const sendExactMessage = vi.fn().mockResolvedValue(undefined)
      const { api } = setup([A], { only: null, side: { sendExactMessage }, participants: [qa] })
      fireEvent.change(screen.getByRole('textbox', { name: 'Message' }), { target: { value: 'confirmed note' } })
      fireEvent.change(screen.getByRole('combobox', { name: 'Recipient' }), { target: { value: 'side:r1' } })
      await ready()
      const release = holdContent(files)

      fireEvent.click(sendButton())
      fireEvent.change(screen.getByRole('textbox', { name: 'Message' }), { target: { value: 'changed during refresh' } })
      fireEvent.change(screen.getByRole('combobox', { name: 'Recipient' }), { target: { value: 'run:t-1' } })
      act(() => api().edit('f1', 'edited during refresh'))
      await act(async () => release())

      await waitFor(() => expect(sendExactMessage).toHaveBeenCalledTimes(1))
      expect(meshSend).not.toHaveBeenCalled()
      const [, body] = sendExactMessage.mock.calls[0] as [string, string]
      expect(body).toContain('confirmed note')
      expect(body).not.toContain('changed during refresh')
      expect(body).toContain('first remark')
      expect(body).not.toContain('edited during refresh')
      await waitFor(() => expect(api().send.accepted).not.toBeNull())
      expect(api().comments.map((c) => c.text)).toEqual(['edited during refresh'])
    })

    it('locks the form while the source is being checked', async () => {
      const files = installFiles({ 'a.go': { content: 'alpha one\nalpha two' } })
      setup([A], { only: null, participants: [qa] })
      fireEvent.change(screen.getByRole('combobox', { name: 'Recipient' }), { target: { value: 'side:r1' } })
      await ready()
      const release = holdContent(files)

      fireEvent.click(sendButton())

      expect(screen.getByRole('textbox', { name: 'Message' })).toBeDisabled()
      expect(screen.getByRole('combobox', { name: 'Recipient' })).toBeDisabled()
      await act(async () => release())
    })
  })

  describe('remembered recipient', () => {
    it('Send all remembers the chosen recipient as the default for new comments', async () => {
      const { api, close, open } = setup([A], { only: null, participants: [qa] })
      fireEvent.change(screen.getByRole('combobox', { name: 'Recipient' }), { target: { value: 'run:t-1' } })
      await ready()
      fireEvent.click(sendButton())
      await waitFor(() => expect(api().send.accepted).not.toBeNull())
      expect(api().lastRecipientId).toBe('run:t-1')
      close()

      act(() => api().open('f9', { anchor: { kind: 'file', root: 'project', path: 'b.go', displayPath: 'afm/b.go', reference: '[AFM file: "/w/b.go"]', etag: 'W/"1"', line: 1 }, quote: 'beta one' }))
      act(() => api().edit('f9', 'next remark'))
      expect(api().comments[0]?.recipientId).toBe('run:t-1')
      open(null)
      expect(screen.getByRole('combobox', { name: 'Recipient' })).toHaveValue('run:t-1')
      await ready()
    })

    it('a card Send also remembers it, without rewriting other comments', async () => {
      const { api } = setup([A, B], { only: 'f1', participants: [qa] })
      fireEvent.change(screen.getByRole('combobox', { name: 'Recipient' }), { target: { value: 'run:t-1' } })
      await ready()
      fireEvent.click(sendButton())
      await waitFor(() => expect(api().send.accepted).not.toBeNull())
      expect(api().lastRecipientId).toBe('run:t-1')
      expect(api().comments.map((c) => [c.key, c.recipientId])).toEqual([['f2', '']])
    })
  })
})

describe('FileCommentSendDialog gating', () => {
  it('a busy Assistant disables Send with the reason', async () => {
    setup([A], { only: null, side: { active: true } })
    expect(await screen.findByText('The Assistant is still answering — send when it finishes', { selector: '.comment-recipient-note' })).toBeInTheDocument()
    expect(sendButton()).toBeDisabled()
  })

  it('a recipient that left the roster blocks Send with the reason and keeps the choice', async () => {
    const { api } = setup([{ ...A, recipientId: 'run:gone' }], { only: 'f1' })
    expect(await screen.findByText('The chosen agent is no longer available — choose another one', { selector: '.comment-recipient-note' })).toBeInTheDocument()
    expect(sendButton()).toBeDisabled()
    expect(api().comments[0]?.recipientId).toBe('run:gone')
  })

  it('without any recipient Send is disabled with the reason', async () => {
    setup([A], { only: null, side: { enabled: false } })
    expect(await screen.findByText('No agent can receive comments right now', { selector: '.comment-recipient-note' })).toBeInTheDocument()
    expect(sendButton()).toBeDisabled()
  })

  it('an oversized packet disables Send and says why', async () => {
    setup([{ ...A, text: 'x'.repeat(70 * 1024) }], { only: null })
    expect(await screen.findByText(/over the 64 KiB limit/)).toBeInTheDocument()
    expect(sendButton()).toBeDisabled()
  })

  it('a dialog with nothing left to send has a disabled Send', async () => {
    setup([], { only: null })
    expect(screen.getByText('No file comments to send')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send 0 comments' })).toBeDisabled()
  })
})

describe('FileCommentSendDialog focus', () => {
  it('Escape closes the dialog and Tab stays inside it', async () => {
    const { handlers } = setup([A], { only: null })
    await ready()
    const dialog = screen.getByRole('dialog', { name: 'Send file comments' })
    const last = screen.getByRole('button', { name: /^Send 1 comment/ })
    last.focus()
    fireEvent.keyDown(document, { key: 'Tab' })
    expect(dialog.contains(document.activeElement)).toBe(true)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(handlers.onClose).toHaveBeenCalledTimes(1)
  })
})
