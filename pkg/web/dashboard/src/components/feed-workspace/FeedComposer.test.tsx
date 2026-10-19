import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FeedComposer, type FeedComposerMeshProps } from './FeedComposer'
import * as meshClient from '../../api/mesh-client'
import * as runClient from '../../api/run-client'
import type { MeshParticipant } from '../../api/mesh-client'
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

  // --- Message mode: recipients ("To" chips, @, the stage agent by default), one send to all, receipts ---
  describe('mesh mode', () => {
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

    const runAgent: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true }
    const docsAgent: MeshParticipant = { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'docs', display_name: 'Docs writer', reachable: true }
    const stageAgent: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', display_name: 'Backend agent', reachable: true }
    const finished: MeshParticipant = {
      id: 'run:t-3', kind: 'run_agent', scope: 'run', alias: 'old', display_name: 'Old', reachable: false, unreachable_reason: 'task t-3 has reached a terminal status',
    }
    const profiles: AssistantProfile[] = [
      { id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '' },
    ]

    function roster(overrides: Partial<MeshRoster> = {}): MeshRoster {
      return { enabled: true, probed: true, participants: [runAgent, docsAgent, stageAgent, finished], threads: [], ...overrides }
    }

    function meshProps(overrides: Partial<FeedComposerMeshProps> = {}): FeedComposerMeshProps {
      return { roster: roster(), profiles, stages: [makeStage()], ...overrides }
    }

    type Extra = Partial<Parameters<typeof FeedComposer>[0]>
    function renderComposer(mesh: FeedComposerMeshProps = meshProps(), extra: Extra = {}) {
      const onSend = vi.fn().mockResolvedValue(undefined)
      const utils = render(<FeedComposer stageId="s1" onSend={onSend} mesh={mesh} {...extra} />)
      return { ...utils, onSend }
    }

    const input = () => screen.getByRole('combobox', { name: /message the mesh/i }) as HTMLTextAreaElement
    const recipients = () => screen.getByRole('group', { name: 'Recipients' })
    const type = (value: string) => fireEvent.change(input(), { target: { value, selectionStart: value.length } })

    function selectMention(label: string): void {
      type('@')
      fireEvent.click(screen.getByText(label))
    }

    it('does not render mode tabs when mesh is not provided (legacy, unchanged behavior)', () => {
      render(<FeedComposer stageId="s1" onSend={vi.fn()} />)
      expect(screen.queryByRole('tablist', { name: 'Composer mode' })).toBeNull()
    })

    it('starts in Message, addressed to the stage agent by default; Revise is a tab with "Apply Revise"', () => {
      renderComposer()
      expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
      expect(within(recipients()).getByText('@s1')).toBeInTheDocument()
      expect(within(recipients()).getByText('stage agent')).toBeInTheDocument()
      fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
      expect(screen.getByRole('button', { name: 'Apply Revise' })).toBeInTheDocument()
    })

    it('without Revise there is only the Message tab', () => {
      renderComposer(meshProps(), { reviseAvailable: false })
      expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Message'])
    })

    it('with nobody named the message is a post to the stage agent in its conversation, never a Revise', async () => {
      const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'dm-1' })
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo')
      const { onSend } = renderComposer()
      type('fix this bug')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(post).toHaveBeenCalledWith('s1', expect.any(String), { body: 'fix this bug', mentions: [], interrupt: false }))
      expect(send).not.toHaveBeenCalled()
      expect(onSend).not.toHaveBeenCalled()
      await waitFor(() => expect(input().value).toBe(''))
    })

    it('the stage agent and others together: ONE post in the stage conversation, the others joining it', async () => {
      const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'dm-1' })
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo')
      renderComposer()
      type('@s1 @qa please both check')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
      expect(post.mock.calls[0]?.[2]).toEqual({ body: '@s1 @qa please both check', mentions: ['run:t-1'], interrupt: false })
      expect(send).not.toHaveBeenCalled()
    })

    it('Interrupt agent: offered while the stage runs and its agent is a recipient of a new message; sent with the post', async () => {
      const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'dm-1' })
      const { rerender } = renderComposer(meshProps({ stages: [makeStage({ status: 'awaiting_approval' })] }))
      expect(screen.queryByLabelText('Interrupt agent')).toBeNull()

      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
      type('@qa only you')
      expect(screen.queryByLabelText('Interrupt agent')).toBeNull() // the stage agent is not addressed
      type('how is it going?')
      const toggle = screen.getByLabelText('Interrupt agent') as HTMLInputElement
      expect(toggle.checked).toBe(false)
      fireEvent.click(toggle)
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(post).toHaveBeenCalledWith('s1', expect.any(String), { body: 'how is it going?', mentions: [], interrupt: true }))

      // A reply stays in its conversation: no Interrupt there.
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} meshReply={{ messageId: 'm5', threadId: 'mt-1', senderId: 'stage:s1', preview: 'x', members: ['user', 'stage:s1'] }} />)
      expect(screen.queryByLabelText('Interrupt agent')).toBeNull()
    })

    it('Interrupt is reset after a successful post and kept after a failed one', async () => {
      const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({ messageId: 'dm-1' })
      renderComposer()
      type('status?')
      fireEvent.click(screen.getByLabelText('Interrupt agent'))
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
      expect((screen.getByLabelText('Interrupt agent') as HTMLInputElement).checked).toBe(true)
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
      expect(post.mock.calls[1]?.[2]).toMatchObject({ interrupt: true })
      await waitFor(() => expect(input().value).toBe(''))
      type('next')
      expect((screen.getByLabelText('Interrupt agent') as HTMLInputElement).checked).toBe(false)
    })

    it("a reply has its own draft: the ordinary draft's @mentions are not its recipients, and the draft comes back after it", async () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm9', seq: 9, thread_id: 'mt-docs' })
      const reply = { messageId: 'm5', threadId: 'mt-docs', senderId: 'run:t-2', preview: 'Docs question', members: ['user', 'run:t-2'] }
      const { rerender } = renderComposer()
      type('@qa half-written note')
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} meshReply={reply} />)
      expect(input().value).toBe('')
      expect(within(recipients()).queryByText('@qa')).toBeNull()
      type('the answer')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['run:t-2'], body: 'the answer', inReplyTo: 'm5' })))
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} meshReply={null} />)
      expect(input().value).toBe('@qa half-written note')
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()
    })

    it("the feed stage's finished agent can still get a post in its conversation, delivered to the others named", async () => {
      const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'dm-1' })
      const done = { ...stageAgent, reachable: false, unreachable_reason: 'stage s1 has reached a terminal status' }
      renderComposer(meshProps({ roster: roster({ participants: [runAgent, done] }), stages: [makeStage({ status: 'done' })] }), { reviseAvailable: false })
      type('@s1 @qa recap for the record')
      expect(within(recipients()).getByText('finished — post recorded, not read')).toBeInTheDocument()
      expect(screen.queryByLabelText('Interrupt agent')).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(post).toHaveBeenCalledWith('s1', expect.any(String), { body: '@s1 @qa recap for the record', mentions: ['run:t-1'], interrupt: false }))
    })

    it('a reply goes only to its conversation: someone outside it, or the finished stage agent, is an error and Send is off', () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo')
      const done = { ...stageAgent, reachable: false, unreachable_reason: 'stage s1 has reached a terminal status' }
      const reply = { messageId: 'm5', threadId: 'mt-docs', senderId: 'run:t-2', preview: 'Docs question', members: ['user', 'run:t-2', 'stage:s1'] }
      renderComposer(meshProps({ roster: roster({ participants: [runAgent, docsAgent, done] }), stages: [makeStage({ status: 'done' })] }), { reviseAvailable: false, meshReply: reply })
      type('@qa @s1 thanks')
      const alert = screen.getByRole('alert')
      expect(alert).toHaveTextContent('@qa is not in this conversation — a reply goes only to its members')
      expect(alert).toHaveTextContent("@s1 can't receive messages: stage s1 has reached a terminal status")
      expect(within(recipients()).queryByText('finished — post recorded, not read')).toBeNull()
      expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
      fireEvent.keyDown(input(), { key: 'Enter', metaKey: true })
      expect(send).not.toHaveBeenCalled()
    })

    it('the mesh arriving after mount opens Message — unless a Revise note is being written', () => {
      const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} />)
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
      expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
    })

    it('a late mesh keeps a Revise note being written in Revise', () => {
      const { rerender } = render(<FeedComposer stageId="s1" onSend={vi.fn()} />)
      fireEvent.change(screen.getByPlaceholderText('Note to agent…'), { target: { value: 'tighten tests' } })
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} />)
      expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')
      expect(screen.getByPlaceholderText('Note to agent…')).toHaveValue('tighten tests')
    })

    it('Cmd/Ctrl+Enter sends to two picked people; a plain Enter does not, and the hint says how', async () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm1', seq: 1, thread_id: 'mt-1' })
      const { rerender } = renderComposer()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-1', seq: 1 } })} />)
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 2 } })} />)
      type('please both review')
      expect(screen.getByText('Cmd/Ctrl+Enter to send · Enter adds a line')).not.toHaveClass('is-nudged')
      fireEvent.keyDown(input(), { key: 'Enter' })
      expect(send).not.toHaveBeenCalled()
      expect(screen.getByText('Cmd/Ctrl+Enter to send · Enter adds a line')).toHaveClass('is-nudged')
      fireEvent.keyDown(input(), { key: 'Enter', ctrlKey: true })
      await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['run:t-1', 'run:t-2'], body: 'please both review' })))
    })

    it('@ lists the agents of other stages too', () => {
      const other: MeshParticipant = { id: 'stage:docs', kind: 'stage_agent', scope: 'stage', stage_id: 'docs', alias: 'docs-stage', display_name: 'Docs stage', reachable: true }
      renderComposer(meshProps({ roster: roster({ participants: [runAgent, stageAgent, other] }) }))
      type('@')
      expect(screen.getByText('Other stages')).toBeInTheDocument()
      fireEvent.click(screen.getByText('@docs-stage'))
      expect(input().value).toBe('@docs-stage ')
      expect(within(recipients()).getByText('@docs-stage')).toBeInTheDocument()
    })

    it('one message to several people: typed @handles and picked chips together', async () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm1', seq: 1, thread_id: 'mt-1' })
      const { rerender } = renderComposer(meshProps({ pick: null }))
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 1 } })} />)
      selectMention('@qa')
      expect(input().value).toBe('@qa ')
      fireEvent.change(input(), { target: { value: '@qa please both review' } })
      const chips = within(recipients())
      expect(chips.getByText('@docs')).toBeInTheDocument()
      expect(chips.getByText('@qa')).toBeInTheDocument()
      expect(chips.queryByText('stage agent')).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
      expect(send.mock.calls[0]?.[0]).toMatchObject({ recipientIds: ['run:t-1', 'run:t-2'], body: '@qa please both review' })
    })

    it('a people-panel pick switches to Message and ADDS a chip without touching the text; a pick before mount is ignored', () => {
      const { rerender } = renderComposer(meshProps({ pick: { participantId: 'run:t-1', seq: 3 } }))
      expect(within(recipients()).queryByText('@qa')).toBeNull()
      fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-1', seq: 4 } })} />)
      expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()
      expect(input().value).toBe('')
      expect(input()).toHaveFocus()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 5 } })} />)
      expect(within(recipients()).getByText('@docs')).toBeInTheDocument()
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()
    })

    it('× on a chip removes the recipient: a picked one from the list, a typed one from the text', () => {
      const { rerender } = renderComposer()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 1 } })} />)
      type('@qa hello')
      fireEvent.click(screen.getByRole('button', { name: 'Remove @docs' }))
      fireEvent.click(screen.getByRole('button', { name: 'Remove @qa' }))
      expect(input().value).toBe('hello')
      expect(within(recipients()).getByText('stage agent')).toBeInTheDocument()
    })

    it('an unknown or unreachable recipient is an error with its reason and nothing is sent — no redirect', () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo')
      renderComposer()
      type('@nobody @old hi')
      expect(screen.getByRole('alert')).toHaveTextContent('No one is called @nobody in this run')
      expect(screen.getByRole('alert')).toHaveTextContent("@old can't receive messages: task t-3 has reached a terminal status")
      expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
      fireEvent.keyDown(input(), { key: 'Enter', metaKey: true })
      expect(send).not.toHaveBeenCalled()
    })

    it("the server's rejection reason is shown and the draft kept", async () => {
      vi.spyOn(meshClient, 'sendMeshMessageTo').mockRejectedValue(new meshClient.MeshApiError('/api/mesh/messages', 409, 'invalid_status', 'run:t-1 cannot receive messages (task finished)'))
      renderComposer()
      type('@qa hi')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('run:t-1 cannot receive messages (task finished)'))
      expect(input().value).toBe('@qa hi')
    })

    it('a stage agent waiting for its helpers reads only a Revise: no default recipient, and "Use Revise" switches', () => {
      renderComposer(meshProps({ stages: [makeStage({ status: 'awaiting_assistants' })] }))
      expect(within(recipients()).getByText(/reads a note only as a Revise/)).toBeInTheDocument()
      type('hi')
      expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
      fireEvent.click(screen.getByRole('button', { name: 'Use Revise' }))
      expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')
    })

    it('a finished stage agent is no default: a recipient is required', () => {
      const done = { ...stageAgent, reachable: false, unreachable_reason: 'stage s1 has reached a terminal status' }
      renderComposer(meshProps({ roster: roster({ participants: [runAgent, done] }), stages: [makeStage({ status: 'done' })] }), { reviseAvailable: false })
      expect(within(recipients()).getByText(/terminal status/)).toBeInTheDocument()
      type('hi')
      expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
      type('@qa hi')
      expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled()
    })

    it('"+" opens the @ list at the caret', () => {
      renderComposer()
      type('hello')
      fireEvent.click(screen.getByRole('button', { name: 'Add recipient' }))
      expect(input().value).toBe('hello @')
      expect(screen.getByText('This run')).toBeInTheDocument()
    })

    it('answering a conversation message adds its sender and sends in_reply_to; ✕ cancels', async () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm9', seq: 9, thread_id: 'mt-1' })
      const onCancel = vi.fn()
      const onReplySent = vi.fn()
      renderComposer(meshProps(), {
        meshReply: { messageId: 'm5', threadId: 'mt-1', senderId: 'run:t-1', preview: 'Found a bug', members: ['user', 'run:t-1'] },
        onCancelMeshReply: onCancel,
        onMeshReplySent: onReplySent,
      })
      expect(screen.getByText('↩ Reply in conversation')).toBeInTheDocument()
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()
      type('which one?')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['run:t-1'], inReplyTo: 'm5' })))
      await waitFor(() => expect(onReplySent).toHaveBeenCalledWith('m5'))
      fireEvent.click(screen.getByRole('button', { name: 'Cancel reply in conversation' }))
      expect(onCancel).toHaveBeenCalled()
    })

    it('a picked person who left the roster stays as a problem chip and Send stays off — the stage agent never takes over', () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo')
      const { rerender } = renderComposer()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 1 } })} />)
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 1 }, roster: roster({ participants: [runAgent, stageAgent] }) })} />)
      type('hi')
      expect(within(recipients()).queryByText('stage agent')).toBeNull()
      expect(screen.getByRole('alert')).toHaveTextContent('@run:t-2 is no longer available in this run')
      expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
      fireEvent.keyDown(input(), { key: 'Enter', metaKey: true })
      expect(send).not.toHaveBeenCalled()
    })

    it('× removes every source of a chip: a full id shown by its alias, and the same person also picked', () => {
      const { rerender } = renderComposer()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 1 } })} />)
      type('@run:t-2 and @docs hello')
      expect(within(recipients()).getAllByText('@docs')).toHaveLength(1)
      fireEvent.click(screen.getByRole('button', { name: 'Remove @docs' }))
      expect(input().value).toBe('and hello')
      expect(within(recipients()).queryByText('@docs')).toBeNull()
      expect(within(recipients()).getByText('stage agent')).toBeInTheDocument()
    })

    it('a reply is addressed to that conversation only: earlier picks wait, and picks made during it end with it', async () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm9', seq: 9, thread_id: 'mt-docs' })
      const reply = { messageId: 'm5', threadId: 'mt-docs', senderId: 'run:t-2', preview: 'Docs question', members: ['user', 'run:t-2'] }
      const { rerender } = renderComposer()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-1', seq: 1 } })} />)
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()

      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-1', seq: 1 } })} meshReply={reply} />)
      expect(within(recipients()).getByText('@docs')).toBeInTheDocument()
      expect(within(recipients()).queryByText('@qa')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Remove @docs' })).toBeNull()
      type('answer')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['run:t-2'], inReplyTo: 'm5' })))

      // The reply ended: its sender is no longer a recipient, the earlier pick is back.
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-1', seq: 1 } })} meshReply={null} />)
      expect(within(recipients()).queryByText('@docs')).toBeNull()
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()
    })

    it('a retry of the same message reuses its request id; another recipient set is a new message', async () => {
      const send = vi
        .spyOn(meshClient, 'sendMeshMessageTo')
        .mockRejectedValueOnce(new Error('network'))
        .mockResolvedValueOnce({ message_id: 'm1', seq: 1, thread_id: 'mt-1' })
        .mockRejectedValueOnce(new Error('network'))
      renderComposer()
      type('@qa hi')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
      expect(send.mock.calls[0]?.[0].requestId).toBe(send.mock.calls[1]?.[0].requestId)

      await waitFor(() => expect(input().value).toBe(''))
      type('@qa @docs hi')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(send).toHaveBeenCalledTimes(3))
      expect(send.mock.calls[2]?.[0].requestId).not.toBe(send.mock.calls[0]?.[0].requestId)
    })

    it('receipts per recipient: in flight, then each one\'s own state, "Answered" once that one replied', async () => {
      let resolveSend: (v: { messageId: string }) => void = () => {}
      vi.spyOn(runClient, 'postStageDiscussionMessage').mockReturnValue(new Promise((res) => { resolveSend = res }))
      const mesh = meshProps({ stages: [makeStage()] })
      const { rerender } = renderComposer(mesh)
      type('@qa @s1 status?')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      expect(screen.getByRole('status')).toHaveTextContent('Awaiting delivery')
      await act(async () => resolveSend({ messageId: 'm1' }))
      const delivery = screen.getByRole('status', { name: 'Delivery' })
      // Accepted by the API is not delivered: nobody is "Delivered" before its own delivery receipt.
      expect(delivery).toHaveTextContent('QA reviewer · Accepted · waiting for an inbox check')
      expect(delivery).toHaveTextContent('Backend agent · Accepted · waiting for an inbox check')

      const answer = { id: 'm2', thread_id: 'mt-1', sender_id: 'run:t-1', recipient_ids: ['user'], kind: 'message', in_reply_to: 'm1', body: 'ok', seq: 2, created_at: '' }
      const delivered = { ...mesh, deliveries: new Set(['m1|stage:s1']) }
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={delivered} conversationMessages={[answer]} />)
      expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('QA reviewer · Answered')
      expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('Backend agent · Delivered')
    })

    it('receipts: "Paused · waiting for Continue" when the recipient\'s own stage is paused', async () => {
      vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'm1' })
      renderComposer(meshProps({ stages: [makeStage({ status: 'paused' })] }))
      type('@s1 hi')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('Paused · waiting for Continue'))
    })

    it('Message/Revise follow the tab pattern: arrow keys switch modes and each tab controls its panel', () => {
      renderComposer()
      const message = screen.getByRole('tab', { name: 'Message' })
      message.focus()
      fireEvent.keyDown(message, { key: 'ArrowRight' })
      const revise = screen.getByRole('tab', { name: 'Revise' })
      expect(revise).toHaveAttribute('aria-selected', 'true')
      expect(revise).toHaveFocus()
      expect(screen.getByRole('tabpanel', { name: 'Revise' })).toBeVisible()
    })

    it('switching to Revise and back keeps the message draft and its recipients', () => {
      renderComposer()
      type('@qa draft')
      fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
      fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
      expect(input().value).toBe('@qa draft')
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()
    })

    it('a reply to a thought switches to Revise and focuses its field', () => {
      const { rerender } = renderComposer()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps()} replyQuote="a thought" replyKey="k1" />)
      expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')
      expect(screen.getByPlaceholderText('Note to agent…')).toHaveFocus()
    })

    describe('"Attach to stage" opens a preview before anything is created', () => {
      function openPreview(): void {
        renderComposer()
        selectMention('@frontendDeveloper')
        expect(within(recipients()).getByText('Attach helper: Frontend reviewer')).toBeInTheDocument()
        type('please take a look')
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      }

      it('Send opens a preview showing the assignment, Configured executor, stage binding and mode — issues no request', () => {
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
        const sendSpy = vi.spyOn(meshClient, 'sendMeshMessageTo')
        openPreview()
        const dialog = screen.getByRole('dialog', { name: /attach assistant/i })
        expect(within(dialog).getByText('please take a look')).toBeInTheDocument()
        expect(within(dialog).getByText('Configured executor')).toBeInTheDocument()
        expect(within(dialog).getByText('claude')).toBeInTheDocument()
        expect(within(dialog).getByText('Backend')).toBeInTheDocument()
        expect(within(dialog).getByText(/advisory/i)).toBeInTheDocument()
        expect(attachSpy).not.toHaveBeenCalled()
        expect(sendSpy).not.toHaveBeenCalled()
      })

      it('"Attach and send" issues exactly one POST with the assignment and the stage id, then shows its receipt', async () => {
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't1', participant_id: 'helper:t1', thread_id: 'mt-attach', message_id: 'm1' })
        openPreview()
        fireEvent.click(screen.getByRole('button', { name: /attach and send/i }))
        await waitFor(() => expect(attachSpy).toHaveBeenCalledTimes(1))
        expect(attachSpy).toHaveBeenCalledWith(expect.objectContaining({
          profileId: 'frontendDeveloper', scope: 'stage', stageId: 's1', instruction: 'please take a look', firstMessage: 'please take a look',
        }))
        await waitFor(() => expect(screen.queryByRole('dialog', { name: /attach assistant/i })).toBeNull())
        expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('Accepted · waiting for an inbox check')
        expect(within(recipients()).queryByText(/Attach helper/)).toBeNull()
        expect(input().value).toBe('')
      })

      it('Cancel issues no request and leaves the draft untouched', () => {
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
        openPreview()
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
        expect(screen.queryByRole('dialog', { name: /attach assistant/i })).toBeNull()
        expect(attachSpy).not.toHaveBeenCalled()
        expect(input().value).toBe('please take a look')
      })

      it('an attached helper gets the text alone: with other recipients Send says so', () => {
        renderComposer()
        selectMention('@frontendDeveloper')
        type('@qa please')
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
        expect(screen.getByRole('alert')).toHaveTextContent('An attached helper gets the text alone')
        expect(screen.queryByRole('dialog', { name: /attach assistant/i })).toBeNull()
      })

      it('a re-submit after a failed attach reuses the same request_id', async () => {
        const attachSpy = vi
          .spyOn(meshClient, 'attachAndSendMesh')
          .mockRejectedValueOnce(new Error('transient'))
          .mockResolvedValueOnce({ task_id: 't1', participant_id: 'helper:t1', thread_id: 'mt-attach' })
        openPreview()
        const confirmBtn = screen.getByRole('button', { name: /attach and send/i })
        fireEvent.click(confirmBtn)
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/transient/i))
        fireEvent.click(confirmBtn)
        await waitFor(() => expect(attachSpy).toHaveBeenCalledTimes(2))
        expect(attachSpy.mock.calls[0]?.[0].requestId).toBe(attachSpy.mock.calls[1]?.[0].requestId)
      })
    })
  })
})
