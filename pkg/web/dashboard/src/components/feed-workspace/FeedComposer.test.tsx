import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FeedComposer, type FeedComposerMeshProps } from './FeedComposer'
import * as meshClient from '../../api/mesh-client'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import type { MeshRoster } from '../../hooks/use-mesh'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { Stage } from '../../types'

describe('FeedComposer', () => {
  it('sends the trimmed text and clears on success', async () => {
    const onSend = vi.fn().mockResolvedValue(undefined)
    render(<FeedComposer stageId="s1" onSend={onSend} />)

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: '  учти edge case  ' } })
    fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))

    expect(onSend).toHaveBeenCalledWith('учти edge case')
    await waitFor(() => expect(textarea.value).toBe(''))
  })

  it('keeps the text when delivery fails (onSend rejects)', async () => {
    const onSend = vi.fn().mockRejectedValue(new Error('409'))
    render(<FeedComposer stageId="s1" onSend={onSend} />)

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'note that failed' } })
    fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))

    await waitFor(() => expect(onSend).toHaveBeenCalled())
    // Текст сохранён — пользователь может повторить.
    expect(textarea.value).toBe('note that failed')
  })

  it('does not erase text typed while a send is in flight', async () => {
    let resolveSend: () => void = () => {}
    const onSend = vi.fn().mockReturnValue(new Promise<void>((res) => { resolveSend = res }))
    render(<FeedComposer stageId="s1" onSend={onSend} />)

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'first' } })
    fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
    // Поле остаётся редактируемым — пользователь дописывает, пока идёт отправка.
    fireEvent.change(textarea, { target: { value: 'first and more' } })
    resolveSend()

    await waitFor(() => expect(onSend).toHaveBeenCalledWith('first'))
    // Свежий черновик НЕ затёрт очисткой после успешной отправки прежнего текста.
    expect(textarea.value).toBe('first and more')
  })

  it('disables send while empty and does not call onSend on empty submit', () => {
    const onSend = vi.fn().mockResolvedValue(undefined)
    render(<FeedComposer stageId="s1" onSend={onSend} />)

    const sendBtn = screen.getByRole('button', { name: /send note to agent/i })
    expect(sendBtn).toBeDisabled()

    // Пробел не считается — по-прежнему disabled и no-op.
    fireEvent.change(screen.getByRole('textbox'), { target: { value: '   ' } })
    expect(sendBtn).toBeDisabled()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('submits on Cmd/Ctrl+Enter', () => {
    const onSend = vi.fn().mockResolvedValue(undefined)
    render(<FeedComposer stageId="s1" onSend={onSend} />)

    const textarea = screen.getByRole('textbox')
    fireEvent.change(textarea, { target: { value: 'via keyboard' } })
    fireEvent.keyDown(textarea, { key: 'Enter', metaKey: true })

    expect(onSend).toHaveBeenCalledWith('via keyboard')
  })

  // --- Ответ на мысль агента (quote chip + доставка quote+comment) ---

  describe('reply chip', () => {
    it('рендерит чип, когда задан replyQuote', () => {
      render(<FeedComposer stageId="s1" onSend={vi.fn()} replyQuote="цитируемая мысль" />)
      expect(screen.getByText(/in reply to/i)).toBeInTheDocument()
      expect(screen.getByText('цитируемая мысль')).toBeInTheDocument()
    })

    it('не рендерит чип, когда replyQuote null/не задан', () => {
      const { container } = render(<FeedComposer stageId="s1" onSend={vi.fn()} replyQuote={null} />)
      expect(container.querySelector('.feed-reply-chip')).toBeNull()
    })

    it('✕ вызывает onCancelReply', () => {
      const onCancelReply = vi.fn()
      render(<FeedComposer stageId="s1" onSend={vi.fn()} replyQuote="мысль" onCancelReply={onCancelReply} />)
      fireEvent.click(screen.getByRole('button', { name: /cancel reply to thought/i }))
      expect(onCancelReply).toHaveBeenCalledTimes(1)
    })

    it('send остаётся disabled при пустом комментарии даже с цитатой', () => {
      const onSend = vi.fn().mockResolvedValue(undefined)
      render(<FeedComposer stageId="s1" onSend={onSend} replyQuote="мысль" />)
      const sendBtn = screen.getByRole('button', { name: /send note to agent/i })
      expect(sendBtn).toBeDisabled()
      fireEvent.click(sendBtn)
      expect(onSend).not.toHaveBeenCalled()
    })

    it('на отправке доставляет quote+comment через onSend', () => {
      const onSend = vi.fn().mockResolvedValue(undefined)
      render(<FeedComposer stageId="s1" onSend={onSend} replyQuote="мысль агента" replyKey="k1" />)
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'сделай X' } })
      fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
      expect(onSend).toHaveBeenCalledWith('> мысль агента\n\nсделай X')
    })

    it('на успехе очищает текст и вызывает onSent(replyKey)', async () => {
      const onSend = vi.fn().mockResolvedValue(undefined)
      const onSent = vi.fn()
      render(<FeedComposer stageId="s1" onSend={onSend} replyQuote="мысль" replyKey="k1" onSent={onSent} />)
      const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: 'ответ' } })
      fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
      await waitFor(() => expect(textarea.value).toBe(''))
      expect(onSent).toHaveBeenCalledWith('k1')
    })

    it('на отказе сохраняет текст и НЕ вызывает onSent', async () => {
      const onSend = vi.fn().mockRejectedValue(new Error('409'))
      const onSent = vi.fn()
      render(<FeedComposer stageId="s1" onSend={onSend} replyQuote="мысль" replyKey="k1" onSent={onSent} />)
      const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: 'ответ' } })
      fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
      await waitFor(() => expect(onSend).toHaveBeenCalled())
      expect(textarea.value).toBe('ответ')
      expect(onSent).not.toHaveBeenCalled()
    })

    it('focuses the textarea when a reply target appears (Cmd+Enter works right after ↩)', () => {
      const onSend = vi.fn().mockResolvedValue(undefined)
      const { rerender } = render(<FeedComposer stageId="s1" onSend={onSend} />)
      const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
      expect(document.activeElement).not.toBe(textarea)

      // Клик по ↩ у мысли = появление replyKey → фокус уезжает в поле ввода.
      rerender(<FeedComposer stageId="s1" onSend={onSend} replyQuote="мысль" replyKey="seq:5" />)
      expect(document.activeElement).toBe(textarea)
    })
  })

  // --- Pause on focus (toggle + banner + focus/blur проброс) ---
  describe('pause on focus', () => {
    it('renders the toggle and calls onTogglePauseOnFocus on change', () => {
      const onToggle = vi.fn()
      render(
        <FeedComposer
          stageId="s1"
          onSend={vi.fn()}
          pauseOnFocusEnabled={false}
          onTogglePauseOnFocus={onToggle}
        />,
      )
      const toggle = screen.getByRole('checkbox', { name: /hold the agent while you type/i })
      expect(toggle).not.toBeChecked()
      fireEvent.click(toggle)
      expect(onToggle).toHaveBeenCalledWith(true)
    })

    it('labels the toggle with a single phrase — no separate orphaned hint', () => {
      const { container } = render(
        <FeedComposer
          stageId="s1"
          onSend={vi.fn()}
          pauseOnFocusEnabled={false}
          onTogglePauseOnFocus={vi.fn()}
        />,
      )
      // Один текст, и он — подпись самого тумблера (внутри <label>), поэтому
      // связь с переключателем очевидна и глазу, и скринридеру.
      expect(container.querySelector('.composer-opts-hint')).toBeNull()
      expect(screen.queryByText(/pause on focus/i)).toBeNull()
      expect(
        screen.getByText(/hold the agent while you type/i).closest('label.switch'),
      ).not.toBeNull()
    })

    it('clicking the label text (not just the checkbox) toggles it — the whole phrase is the hit target', () => {
      const onToggle = vi.fn()
      render(
        <FeedComposer
          stageId="s1"
          onSend={vi.fn()}
          pauseOnFocusEnabled={false}
          onTogglePauseOnFocus={onToggle}
        />,
      )
      fireEvent.click(screen.getByText(/hold the agent while you type/i))
      expect(onToggle).toHaveBeenCalledWith(true)
    })

    it('shows the paused banner when paused', () => {
      render(<FeedComposer stageId="s1" onSend={vi.fn()} paused />)
      expect(screen.getByText(/agent is on hold/i)).toBeInTheDocument()
    })

    it('does not show the banner when not paused', () => {
      const { container } = render(<FeedComposer stageId="s1" onSend={vi.fn()} />)
      expect(container.querySelector('.pause-banner')).toBeNull()
    })

    it('Resume now calls onResumeNow', () => {
      const onResumeNow = vi.fn()
      render(<FeedComposer stageId="s1" onSend={vi.fn()} paused onResumeNow={onResumeNow} />)
      fireEvent.click(screen.getByRole('button', { name: /resume now/i }))
      expect(onResumeNow).toHaveBeenCalledTimes(1)
    })

    it('focusing the textarea calls onFocus', () => {
      const onFocus = vi.fn()
      render(<FeedComposer stageId="s1" onSend={vi.fn()} onFocus={onFocus} />)
      fireEvent.focus(screen.getByRole('textbox'))
      expect(onFocus).toHaveBeenCalledTimes(1)
    })

    it('blurring reports whether a draft is present', () => {
      const onBlur = vi.fn()
      render(<FeedComposer stageId="s1" onSend={vi.fn()} onBlur={onBlur} />)
      const textarea = screen.getByRole('textbox')

      fireEvent.blur(textarea)
      expect(onBlur).toHaveBeenLastCalledWith(false)

      fireEvent.change(textarea, { target: { value: '  hi  ' } })
      fireEvent.blur(textarea)
      expect(onBlur).toHaveBeenLastCalledWith(true)
    })
  })

  // --- Agent Mesh Phase 7, T7.4: Message mode (recipients, @mention, receipts, threads) ---
  describe('mesh mode (T7.4)', () => {
    afterEach(() => {
      vi.restoreAllMocks()
    })

    function makeStage(overrides: Partial<Stage> = {}): Stage {
      return {
        id: 's1', name: 'Backend', status: 'running', updatedAt: '', interactive: false,
        autonomous: false, autoApprove: false, hasDialog: false, showPlan: false, showDialog: false,
        isScript: false, pausedFrom: '', preNote: '', buttons: [],
        ...overrides,
      }
    }

    const runAgent: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true }
    const stageAgent: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', display_name: 'Backend agent', reachable: true }
    const profiles: AssistantProfile[] = [
      { id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '' },
    ]

    function roster(overrides: Partial<MeshRoster> = {}): MeshRoster {
      return { enabled: true, probed: true, participants: [runAgent, stageAgent], threads: [], ...overrides }
    }

    function meshProps(overrides: Partial<FeedComposerMeshProps> = {}): FeedComposerMeshProps {
      return { roster: roster(), profiles, stages: [makeStage()], ...overrides }
    }

    function openMessageMode(mesh: FeedComposerMeshProps = meshProps()) {
      const utils = render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh} />)
      fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
      return utils
    }

    function selectMention(label: string): void {
      const ta = screen.getByRole('textbox', { name: /message the mesh/i })
      fireEvent.change(ta, { target: { value: '@', selectionStart: 1 } })
      fireEvent.click(screen.getByText(label))
    }

    it('does not render mode tabs when mesh is not provided (legacy, unchanged behavior)', () => {
      render(<FeedComposer stageId="s1" onSend={vi.fn()} />)
      expect(screen.queryByRole('tablist', { name: 'Composer mode' })).toBeNull()
    })

    it('shows Message/Revise tabs when mesh is provided, defaulting to Revise with the "Apply Revise" label', () => {
      render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
      expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')
      expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'false')
      expect(screen.getByRole('button', { name: 'Apply Revise' })).toBeInTheDocument()
    })

    it('free text — including "fix this bug" — never triggers a Revise in Message mode', async () => {
      const onSend = vi.fn().mockResolvedValue(undefined)
      const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-new', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
      const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })

      render(<FeedComposer stageId="s1" onSend={onSend} mesh={meshProps()} />)
      fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
      selectMention('@run:qa')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa fix this bug' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalled())
      expect(onSend).not.toHaveBeenCalled()
      expect(openSpy).toHaveBeenCalled()
      expect(sendSpy).toHaveBeenCalledWith('mt-new', expect.objectContaining({ body: '@run:qa fix this bug' }))
    })

    it('@ opens a server-sourced list distinguishing This run / This stage / Attach to stage', () => {
      openMessageMode()
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@', selectionStart: 1 } })
      expect(screen.getByText('This run')).toBeInTheDocument()
      expect(screen.getByText('This stage')).toBeInTheDocument()
      expect(screen.getByText('Attach to stage')).toBeInTheDocument()
      expect(screen.getByText('@run:qa')).toBeInTheDocument()
      expect(screen.getByText('@stage:s1')).toBeInTheDocument()
      expect(screen.getByText('@frontendDeveloper')).toBeInTheDocument()
    })

    it('selecting a name and typing starts nothing — no request fires until Send', () => {
      const openSpy = vi.spyOn(meshClient, 'openMeshThread')
      const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage')
      const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')

      openMessageMode()
      selectMention('@run:qa')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa hello there' } })

      expect(openSpy).not.toHaveBeenCalled()
      expect(sendSpy).not.toHaveBeenCalled()
      expect(attachSpy).not.toHaveBeenCalled()
    })

    it('addressing an existing participant with an open thread uses its id and creates no new task', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      const openSpy = vi.spyOn(meshClient, 'openMeshThread')
      const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 2 })
      const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')

      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }) }))
      selectMention('@run:qa')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa please review' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalled())
      expect(sendSpy).toHaveBeenCalledWith('mt-1', expect.objectContaining({ recipientIds: ['run:qa'] }))
      expect(openSpy).not.toHaveBeenCalled()
      expect(attachSpy).not.toHaveBeenCalled()
    })

    it('choosing a new participant (no existing private thread) starts a NEW thread rather than widening one', async () => {
      // A 3-member thread that happens to include run:qa must NOT be reused (private-thread widening).
      const groupThread: MeshThread = { id: 'mt-group', scope: 'run', members: ['user', 'run:qa', 'run:dev'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-new', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
      const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })

      openMessageMode(meshProps({ roster: roster({ threads: [groupThread] }) }))
      selectMention('@run:qa')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa hi' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalled())
      expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ scope: 'run', memberIds: ['run:qa'] }))
      expect(sendSpy).toHaveBeenCalledWith('mt-new', expect.objectContaining({ recipientIds: ['run:qa'] }))
    })

    it('deleting the @mention from the draft un-addresses the recipient (Send is disabled again)', () => {
      openMessageMode(meshProps())
      selectMention('@run:qa')
      const ta = screen.getByRole('textbox', { name: /message the mesh/i })
      fireEvent.change(ta, { target: { value: '@run:qa hello' } })
      expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled()
      fireEvent.change(ta, { target: { value: 'hello' } })
      expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
    })

    it('a thread opened in Message mode is remembered across a Revise round-trip (no duplicate open)', async () => {
      const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-new', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
      vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      openMessageMode(meshProps())
      selectMention('@run:qa')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa one' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1))

      fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
      fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
      selectMention('@run:qa')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa two' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenLastCalledWith('mt-new', expect.objectContaining({ body: '@run:qa two' })))
      expect(openSpy).toHaveBeenCalledTimes(1)
    })

    it('Reply preserves the thread and in_reply_to', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
        messages: [{ id: 'm0', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: 'original question', seq: 1, created_at: '2026-10-18T00:00:00Z' }],
        next_cursor: 1,
      })
      const openSpy = vi.spyOn(meshClient, 'openMeshThread')
      const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 2 })

      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }) }))
      selectMention('@run:qa')
      await waitFor(() => expect(screen.getByText('original question')).toBeInTheDocument())
      fireEvent.click(screen.getByRole('button', { name: 'Reply' }))
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa here is the answer' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalled())
      expect(sendSpy).toHaveBeenCalledWith('mt-1', expect.objectContaining({ inReplyTo: 'm0', body: '@run:qa here is the answer' }))
      expect(openSpy).not.toHaveBeenCalled()
    })

    // --- Agent Mesh Phase 7, T7.5: attach preview for a profile with no instance ---
    describe('"Attach to stage" opens a preview before anything is created (T7.5)', () => {
      function openPreview(): void {
        openMessageMode()
        selectMention('@frontendDeveloper')
        fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@frontendDeveloper please take a look' } })
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      }

      it('Send opens a preview showing the assignment, Configured executor, stage binding and mode — issues no request', () => {
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
        openPreview()

        const dialog = screen.getByRole('dialog', { name: /attach assistant/i })
        expect(within(dialog).getByText('@frontendDeveloper please take a look')).toBeInTheDocument()
        expect(within(dialog).getByText('Configured executor')).toBeInTheDocument()
        expect(within(dialog).getByText('claude')).toBeInTheDocument()
        expect(within(dialog).getByText('Backend')).toBeInTheDocument()
        expect(within(dialog).getByText(/advisory/i)).toBeInTheDocument()
        expect(attachSpy).not.toHaveBeenCalled()
      })

      it('"Attach and send" issues exactly one POST carrying the assignment text and the stage id, and renders the resulting thread', async () => {
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
          messages: [{ id: 'm1', thread_id: 'mt-attach', sender_id: 'user', recipient_ids: ['helper:t1'], kind: 'message', body: '@frontendDeveloper please take a look', seq: 1, created_at: '2026-10-18T00:00:00Z' }],
          next_cursor: 1,
        })
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't1', participant_id: 'helper:t1', thread_id: 'mt-attach', message_id: 'm1' })
        const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage')

        openPreview()
        fireEvent.click(screen.getByRole('button', { name: /attach and send/i }))

        await waitFor(() => expect(attachSpy).toHaveBeenCalledTimes(1))
        expect(attachSpy).toHaveBeenCalledWith(expect.objectContaining({
          profileId: 'frontendDeveloper', scope: 'stage', stageId: 's1',
          instruction: '@frontendDeveloper please take a look', firstMessage: '@frontendDeveloper please take a look',
        }))
        expect(sendSpy).not.toHaveBeenCalled()
        await waitFor(() => expect(screen.getByText('@frontendDeveloper please take a look')).toBeInTheDocument())
        expect(screen.queryByRole('dialog', { name: /attach assistant/i })).toBeNull()
      })

      it('Cancel creates neither task nor message and issues no request at all, leaving the draft untouched', () => {
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
        const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage')
        openPreview()

        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

        expect(screen.queryByRole('dialog', { name: /attach assistant/i })).toBeNull()
        expect(attachSpy).not.toHaveBeenCalled()
        expect(sendSpy).not.toHaveBeenCalled()
        expect(screen.getByRole('textbox', { name: /message the mesh/i })).toHaveValue('@frontendDeveloper please take a look')
      })

      it('a server error (unavailable profile / exceeded quota) renders in-dialog with no partial attach', async () => {
        vi.spyOn(meshClient, 'attachAndSendMesh').mockRejectedValue(new Error('quota exceeded'))
        openPreview()

        fireEvent.click(screen.getByRole('button', { name: /attach and send/i }))

        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/quota exceeded/i))
        // No partial attach: the dialog stays open, and nothing optimistic reached the feed/roster.
        expect(screen.getByRole('dialog', { name: /attach assistant/i })).toBeInTheDocument()
        expect(screen.queryByRole('status')).toBeNull()
      })

      it('a double confirm (double click) reuses the same request_id and does not duplicate the helper', async () => {
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't1', participant_id: 'helper:t1', thread_id: 'mt-attach' })
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
        openPreview()

        const confirmBtn = screen.getByRole('button', { name: /attach and send/i })
        fireEvent.click(confirmBtn)
        fireEvent.click(confirmBtn)

        await waitFor(() => expect(attachSpy).toHaveBeenCalledTimes(1))
      })

      it('a re-submit after a slow response reuses the same request_id', async () => {
        const attachSpy = vi
          .spyOn(meshClient, 'attachAndSendMesh')
          .mockRejectedValueOnce(new Error('transient'))
          .mockResolvedValueOnce({ task_id: 't1', participant_id: 'helper:t1', thread_id: 'mt-attach' })
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
        openPreview()

        const confirmBtn = screen.getByRole('button', { name: /attach and send/i })
        fireEvent.click(confirmBtn)
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/transient/i))
        fireEvent.click(confirmBtn)

        await waitFor(() => expect(attachSpy).toHaveBeenCalledTimes(2))
        const [firstCall, secondCall] = attachSpy.mock.calls
        expect(firstCall?.[0].requestId).toBe(secondCall?.[0].requestId)
      })
    })

    it('receipts: shows "Awaiting delivery" while the send is in flight, then "Delivered" once resolved', async () => {
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-new', scope: 'stage', members: ['user', 'stage:s1'], status: 'open' })
      let resolveSend: (v: meshClient.MeshSendReceipt) => void = () => {}
      vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => { resolveSend = res }))

      openMessageMode()
      selectMention('@stage:s1')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@stage:s1 status?' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(screen.getByText('Awaiting delivery')).toBeInTheDocument())
      resolveSend({ message_id: 'm1', seq: 1 })
      await waitFor(() => expect(screen.getByText('Delivered')).toBeInTheDocument())
    })

    it('receipts: shows "Answered" once a reply to the sent message appears in the thread', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
        messages: [{ id: 'reply1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', in_reply_to: 'm-sent', body: 'done', seq: 2, created_at: '2026-10-18T00:00:00Z' }],
        next_cursor: 2,
      })
      vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm-sent', seq: 2 })

      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }) }))
      selectMention('@run:qa')
      await waitFor(() => expect(screen.getByText('done')).toBeInTheDocument())
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@run:qa ping' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(screen.getByText('Answered')).toBeInTheDocument())
    })

    it('receipts: shows "Paused · waiting for Continue" when the recipient\'s own stage is paused', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'stage:s1'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })

      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }), stages: [makeStage({ status: 'paused' })] }))
      selectMention('@stage:s1')
      fireEvent.change(screen.getByRole('textbox', { name: /message the mesh/i }), { target: { value: '@stage:s1 status?' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(screen.getByText('Paused · waiting for Continue')).toBeInTheDocument())
    })
  })
})
