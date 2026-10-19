import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
      const ta = screen.getByRole('combobox', { name: /message the mesh/i })
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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa fix this bug' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalled())
      expect(onSend).not.toHaveBeenCalled()
      // The new conversation belongs to the feed it was started from.
      expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ originStage: 's1' }))
      expect(sendSpy).toHaveBeenCalledWith('mt-new', expect.objectContaining({ body: '@run:qa fix this bug' }))
    })

    it('@ opens a server-sourced list distinguishing This run / This stage / Attach to stage', () => {
      openMessageMode()
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@', selectionStart: 1 } })
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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa hello there' } })

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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa please review' } })
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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa hi' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalled())
      expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ scope: 'run', memberIds: ['run:qa'] }))
      expect(sendSpy).toHaveBeenCalledWith('mt-new', expect.objectContaining({ recipientIds: ['run:qa'] }))
    })

    it('deleting the @mention from the draft un-addresses the recipient (Send is disabled again)', () => {
      openMessageMode(meshProps())
      selectMention('@run:qa')
      const ta = screen.getByRole('combobox', { name: /message the mesh/i })
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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa one' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1))

      fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
      fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
      selectMention('@run:qa')
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa two' } })
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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa here is the answer' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalled())
      expect(sendSpy).toHaveBeenCalledWith('mt-1', expect.objectContaining({ inReplyTo: 'm0', body: '@run:qa here is the answer' }))
      expect(openSpy).not.toHaveBeenCalled()
    })

    // The stage people panel: a click puts the person's @handle into the composer and addresses them.
    describe('a people panel pick', () => {
      function combobox(): HTMLTextAreaElement {
        return screen.getByRole('combobox', { name: /message the mesh/i }) as HTMLTextAreaElement
      }

      it('switches to Message, inserts the @handle and sets the target, like an @ menu pick', async () => {
        vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-new', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
        const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
        const onSend = vi.fn()
        const { rerender } = render(<FeedComposer stageId="s1" onSend={onSend} mesh={meshProps()} />)
        expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')

        rerender(<FeedComposer stageId="s1" onSend={onSend} mesh={meshProps({ pick: { participantId: 'run:qa', seq: 1 } })} />)

        expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
        expect(combobox().value).toBe('@run:qa ')
        expect(combobox()).toHaveFocus()
        expect(screen.queryByText('Pick someone with @ or press Reply')).toBeNull()

        fireEvent.change(combobox(), { target: { value: '@run:qa please re-check' } })
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
        await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-new', expect.objectContaining({ recipientIds: ['run:qa'] })))
        expect(onSend).not.toHaveBeenCalled()
      })

      it('a pick is applied once; picking the same person again does not repeat the mention', () => {
        const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:qa', seq: 1 } })} />)
        fireEvent.change(combobox(), { target: { value: '@run:qa hi' } })

        // Revise and back remounts Message mode: the old pick is not inserted again.
        fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
        fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
        expect(combobox().value).toBe('')

        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'stage:s1', seq: 2 } })} />)
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'stage:s1', seq: 3 } })} />)
        expect(combobox().value).toBe('@stage:s1 ')
      })

      it('a Reply in conversation from the feed answers that message in its thread', async () => {
        const openSpy = vi.spyOn(meshClient, 'openMeshThread')
        const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm2', seq: 2 })
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
        const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
        const reply = { messageId: 'm1', threadId: 'mt-group', preview: 'Found a bug' }
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:qa', seq: 1, reply } })} />)

        expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
        expect(combobox().value).toBe('@run:qa ')
        expect(screen.getByText('Found a bug')).toBeInTheDocument()

        fireEvent.change(combobox(), { target: { value: '@run:qa fixed' } })
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
        await waitFor(() =>
          expect(sendSpy).toHaveBeenCalledWith('mt-group', expect.objectContaining({ recipientIds: ['run:qa'], inReplyTo: 'm1' })),
        )
        expect(openSpy).not.toHaveBeenCalled()
      })

      it('a second pick replaces the previous @handle, keeps the text and puts the caret at the end', () => {
        const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:qa', seq: 1 } })} />)
        fireEvent.change(combobox(), { target: { value: '@run:qa please check the login page' } })

        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'stage:s1', seq: 2 } })} />)
        expect(combobox().value).toBe('@stage:s1 please check the login page')
        expect(combobox().selectionStart).toBe(combobox().value.length)
        expect(combobox().selectionEnd).toBe(combobox().value.length)
      })

      it('a pick replaces a handle typed at the start of the draft', () => {
        const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
        fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
        fireEvent.change(combobox(), { target: { value: '@somebody hi' } })
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:qa', seq: 1 } })} />)
        expect(combobox().value).toBe('@run:qa hi')
      })

      it('a pick keeps the reply only when the new person is in that conversation', async () => {
        const group: MeshThread = { id: 'mt-group', scope: 'run', members: ['user', 'run:qa', 'stage:s1'], status: 'open' }
        const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm2', seq: 2 })
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
        const writer: MeshParticipant = { id: 'run:t-w', kind: 'run_agent', scope: 'run', alias: 'writer', display_name: 'Writer', reachable: true }
        const mesh = (pick: FeedComposerMeshProps['pick']) =>
          meshProps({ roster: roster({ participants: [runAgent, stageAgent, writer], threads: [group] }), pick })
        const reply = { messageId: 'm1', threadId: 'mt-group', preview: 'Found a bug' }
        const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh(null)} />)
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh({ participantId: 'run:qa', seq: 1, reply })} />)

        // stage:s1 is in the conversation: the reply stays, now addressed to it.
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh({ participantId: 'stage:s1', seq: 2 })} />)
        expect(screen.getByText('Found a bug')).toBeInTheDocument()
        fireEvent.change(combobox(), { target: { value: '@stage:s1 fixed' } })
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
        await waitFor(() =>
          expect(sendSpy).toHaveBeenCalledWith('mt-group', expect.objectContaining({ recipientIds: ['stage:s1'], inReplyTo: 'm1' })),
        )

        // The writer is not: picking them drops the reply.
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh({ participantId: 'run:qa', seq: 3, reply })} />)
        expect(screen.getByText('Found a bug')).toBeInTheDocument()
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh({ participantId: 'run:t-w', seq: 4 })} />)
        expect(screen.queryByText('Found a bug')).toBeNull()
        expect(combobox().value.startsWith('@writer')).toBe(true)
        expect(combobox().value).not.toContain('@run:qa')
      })

      it('ignores a pick made before the composer mounted, and a finished participant', () => {
        const finished: MeshParticipant = { ...runAgent, reachable: false, unreachable_reason: 'done' }
        const mesh = (pick: FeedComposerMeshProps['pick']) => meshProps({ roster: roster({ participants: [finished, stageAgent] }), pick })
        const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh({ participantId: 'stage:s1', seq: 4 })} />)
        expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')

        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={mesh({ participantId: 'run:qa', seq: 5 })} />)
        expect(combobox().value).toBe('')
        expect(screen.getByText('Pick someone with @ or press Reply')).toBeInTheDocument()
      })
    })

    describe('the conversation preview is sticky', () => {
      const writer: MeshParticipant = { id: 'run:t-w', kind: 'run_agent', scope: 'run', alias: 'writer', display_name: 'Writer', reachable: true }
      const writerThread: MeshThread = { id: 'mt-w', scope: 'run', members: ['user', 'run:t-w'], status: 'open' }

      function openWithWritersReply(): HTMLTextAreaElement {
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
          messages: [{ id: 'mw1', thread_id: 'mt-w', sender_id: 'run:t-w', recipient_ids: ['user'], kind: 'reply', body: 'draft is ready', seq: 1, created_at: '2026-10-19T00:00:00Z' }],
          next_cursor: 1,
        })
        openMessageMode(meshProps({ roster: roster({ participants: [runAgent, stageAgent, writer], threads: [writerThread] }) }))
        selectMention('@writer')
        return screen.getByRole('combobox', { name: /message the mesh/i }) as HTMLTextAreaElement
      }

      it("erasing the @mention keeps the conversation in view; Send stays off until someone is picked", async () => {
        const ta = openWithWritersReply()
        await waitFor(() => expect(screen.getByText('draft is ready')).toBeInTheDocument())

        fireEvent.change(ta, { target: { value: 'thanks' } })
        expect(screen.getByText('draft is ready')).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
        expect(screen.getByText('Pick someone with @ or press Reply')).toBeInTheDocument()
      })

      it('× closes the conversation', async () => {
        openWithWritersReply()
        await waitFor(() => expect(screen.getByText('draft is ready')).toBeInTheDocument())
        fireEvent.click(screen.getByRole('button', { name: 'Close conversation' }))
        expect(screen.queryByText('draft is ready')).toBeNull()
      })

      it('your own messages offer no Reply, and Close sits in its own header row', async () => {
        vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
          messages: [
            { id: 'mw0', thread_id: 'mt-w', sender_id: 'user', recipient_ids: ['run:t-w'], kind: 'message', body: 'please draft it', seq: 1, created_at: '2026-10-19T00:00:00Z' },
            { id: 'mw1', thread_id: 'mt-w', sender_id: 'run:t-w', recipient_ids: ['user'], kind: 'reply', body: 'draft is ready', seq: 2, created_at: '2026-10-19T00:00:01Z' },
          ],
          next_cursor: 2,
        })
        openMessageMode(meshProps({ roster: roster({ participants: [runAgent, stageAgent, writer], threads: [writerThread] }) }))
        selectMention('@writer')
        await waitFor(() => expect(screen.getByText('draft is ready')).toBeInTheDocument())
        expect(screen.getAllByRole('button', { name: 'Reply' })).toHaveLength(1)
        expect(screen.getByText('please draft it').closest('li')).not.toHaveTextContent('Reply')
        expect(screen.getByRole('button', { name: 'Close conversation' }).closest('.mesh-message-thread-header')).not.toBeNull()
      })

      it("Reply re-addresses the message's sender with its @handle and answers in that conversation", async () => {
        const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'mw2', seq: 2 })
        const ta = openWithWritersReply()
        await waitFor(() => expect(screen.getByText('draft is ready')).toBeInTheDocument())
        fireEvent.change(ta, { target: { value: 'looks good' } })

        fireEvent.click(screen.getByRole('button', { name: 'Reply' }))
        expect(ta.value).toBe('@writer looks good')
        expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled()
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

        await waitFor(() =>
          expect(sendSpy).toHaveBeenCalledWith('mt-w', expect.objectContaining({ recipientIds: ['run:t-w'], inReplyTo: 'mw1', body: '@writer looks good' })),
        )
      })
    })

    it('a reply target picked while a send is in flight survives that send finishing', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
        messages: [
          { id: 'm0', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: 'first question', seq: 1, created_at: '2026-10-18T00:00:00Z' },
          { id: 'm1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: 'second question', seq: 2, created_at: '2026-10-18T00:00:01Z' },
        ],
        next_cursor: 2,
      })
      let resolveSend!: (v: { message_id: string; seq: number }) => void
      vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))

      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }) }))
      selectMention('@run:qa')
      await waitFor(() => expect(screen.getByText('second question')).toBeInTheDocument())
      const [replyToFirst, replyToSecond] = screen.getAllByRole('button', { name: 'Reply' })
      fireEvent.click(replyToFirst as HTMLElement)
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa answer one' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalledWith('mt-1', expect.objectContaining({ inReplyTo: 'm0' })))

      fireEvent.click(replyToSecond as HTMLElement)
      await act(async () => resolveSend({ message_id: 'm2', seq: 3 }))

      expect(document.querySelector('.feed-reply-chip-src')).toHaveTextContent('second question')
    })

    it('a successful send clears the reply target it was sent with', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
        messages: [{ id: 'm0', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: 'original question', seq: 1, created_at: '2026-10-18T00:00:00Z' }],
        next_cursor: 1,
      })
      vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 2 })

      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }) }))
      selectMention('@run:qa')
      await waitFor(() => expect(screen.getByText('original question')).toBeInTheDocument())
      fireEvent.click(screen.getByRole('button', { name: 'Reply' }))
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa answer' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(document.querySelector('.feed-reply-chip')).toBeNull())
    })

    // --- Agent Mesh Phase 7, T7.5: attach preview for a profile with no instance ---
    describe('"Attach to stage" opens a preview before anything is created (T7.5)', () => {
      function openPreview(): void {
        openMessageMode()
        selectMention('@frontendDeveloper')
        fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@frontendDeveloper please take a look' } })
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
        expect(screen.getByRole('combobox', { name: /message the mesh/i })).toHaveValue('@frontendDeveloper please take a look')
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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@stage:s1 status?' } })
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
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@run:qa ping' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(screen.getByText('Answered')).toBeInTheDocument())
    })

    it('receipts: shows "Paused · waiting for Continue" when the recipient\'s own stage is paused', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'stage:s1'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })

      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }), stages: [makeStage({ status: 'paused' })] }))
      selectMention('@stage:s1')
      fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: '@stage:s1 status?' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(screen.getByText('Paused · waiting for Continue')).toBeInTheDocument())
    })

    it('Message/Revise follow the tab pattern: arrow keys switch modes and each tab controls its panel', () => {
      render(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
      const revise = screen.getByRole('tab', { name: 'Revise' })
      expect(revise).toHaveAttribute('tabindex', '0')
      expect(revise).toHaveAttribute('aria-controls', screen.getByRole('tabpanel').id)
      revise.focus()
      fireEvent.keyDown(revise, { key: 'ArrowLeft' })
      const message = screen.getByRole('tab', { name: 'Message' })
      expect(message).toHaveFocus()
      expect(message).toHaveAttribute('aria-selected', 'true')
      expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', message.id)
      expect(screen.getByRole('combobox', { name: /message the mesh/i })).toBeInTheDocument()
    })

    it('a retry of the same message after a lost response reuses its request id; an edited message gets a new one', async () => {
      const existingThread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
      vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockRejectedValue(new Error('network'))
      openMessageMode(meshProps({ roster: roster({ threads: [existingThread] }) }))
      selectMention('@run:qa')
      const input = screen.getByRole('combobox', { name: /message the mesh/i })
      fireEvent.change(input, { target: { value: '@run:qa ping' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(2))
      expect(sendSpy.mock.calls[1]?.[1].requestId).toBe(sendSpy.mock.calls[0]?.[1].requestId)

      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
      fireEvent.change(input, { target: { value: '@run:qa ping again' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(3))
      expect(sendSpy.mock.calls[2]?.[1].requestId).not.toBe(sendSpy.mock.calls[0]?.[1].requestId)
    })

    it('a send that finishes after another recipient was picked neither clears the new draft nor opens the old thread', async () => {
      const threadSpy = vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
      vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-new', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
      let resolveSend!: (v: meshClient.MeshSendReceipt) => void
      vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))
      openMessageMode()
      selectMention('@run:qa')
      const input = screen.getByRole('combobox', { name: /message the mesh/i }) as HTMLTextAreaElement
      fireEvent.change(input, { target: { value: '@run:qa one' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalled())

      selectMention('@stage:s1')
      fireEvent.change(input, { target: { value: '@stage:s1 two' } })
      await act(async () => resolveSend({ message_id: 'm1', seq: 1 }))

      expect(input.value).toBe('@stage:s1 two')
      expect(threadSpy).not.toHaveBeenCalledWith('mt-new', expect.anything(), expect.anything())
      expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled()
    })
  })
})
