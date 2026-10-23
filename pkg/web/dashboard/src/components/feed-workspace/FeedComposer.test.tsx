import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FeedComposer, type FeedComposerMeshProps } from './FeedComposer'
import * as meshClient from '../../api/mesh-client'
import * as runClient from '../../api/run-client'
import type { MeshParticipant } from '../../api/mesh-client'
import type { MeshRoster } from '../../hooks/use-mesh'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { Stage } from '../../types'
import { meshDeliveries, sideAgentFailures } from './mesh-receipt'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'

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

  it('submits on Enter', () => {
    const onSend = vi.fn().mockResolvedValue(undefined)
    render(<FeedComposer stageId="s1" onSend={onSend} />)

    const textarea = screen.getByRole('textbox')
    fireEvent.change(textarea, { target: { value: 'via keyboard' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })

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

    it('focuses the textarea when a reply target appears (Enter works right after ↩)', () => {
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
    function renderComposer(mesh: FeedComposerMeshProps = meshProps(), extra: Extra = {}, wrap: (children: ReactElement) => ReactElement = (children) => children) {
      const onSend = vi.fn().mockResolvedValue(undefined)
      const utils = render(wrap(<FeedComposer stageId="s1" onSend={onSend} mesh={mesh} {...extra} />))
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
      fireEvent.keyDown(input(), { key: 'Enter' })
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

    it('Enter sends to two picked people; Shift+Enter adds a line, as the hint says', async () => {
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm1', seq: 1, thread_id: 'mt-1' })
      const { rerender } = renderComposer()
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-1', seq: 1 } })} />)
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={meshProps({ pick: { participantId: 'run:t-2', seq: 2 } })} />)
      type('please both review')
      expect(screen.getByText('Enter to send · Shift+Enter adds a line')).toBeInTheDocument()
      expect(fireEvent.keyDown(input(), { key: 'Enter', shiftKey: true })).toBe(true)
      expect(send).not.toHaveBeenCalled()
      fireEvent.keyDown(input(), { key: 'Enter' })
      await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['run:t-1', 'run:t-2'], body: 'please both review' })))
    })

    it("a stage post refused for a mention that can no longer read it shows the server's reason and keeps the draft", async () => {
      const reason = '@qa cannot read this post (task t-1 has reached a terminal status) — remove the mention and send again'
      vi.spyOn(runClient, 'postStageDiscussionMessage').mockRejectedValue(new runClient.FlowApiError('/api/stages/s1/discussion', 409, 'invalid_status', reason))
      renderComposer()
      type('@s1 @qa status?')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(reason))
      expect(input().value).toBe('@s1 @qa status?')
      expect(within(recipients()).getByText('@qa')).toBeInTheDocument()
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
      fireEvent.keyDown(input(), { key: 'Enter' })
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
      fireEvent.keyDown(input(), { key: 'Enter' })
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

    it('shows a terminal side failure from the run notice even before the thread reply is in the stage poll', async () => {
      const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'claude', reachable: true }
      vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm-side' } as Awaited<ReturnType<typeof meshClient.sendMeshMessageTo>>)
      const mesh = meshProps({ roster: roster({ participants: [...roster().participants, side] }), sideFailures: new Set(['m-side']) })
      renderComposer(mesh)
      type('@assistant help')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('claude · Could not answer'))
    })

    it('sends to the bound @assistant from a stage whose own handle is @assistant-2, keeping the side:r1 recipient', async () => {
      const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'claude', reachable: true }
      const assistantStage: MeshParticipant = { id: 'stage:assistant', kind: 'stage_agent', scope: 'stage', stage_id: 'assistant', alias: 'assistant-2', display_name: 'Smoke', reachable: true }
      const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm-side' } as Awaited<ReturnType<typeof meshClient.sendMeshMessageTo>>)
      const post = vi.spyOn(runClient, 'postStageDiscussionMessage')
      const mesh = meshProps({ roster: roster({ participants: [side, assistantStage] }), stages: [makeStage({ id: 'assistant' })] })
      render(<FeedComposer stageId="assistant" onSend={vi.fn()} mesh={mesh} />)
      expect(within(recipients()).getByText('@assistant-2')).toBeInTheDocument()

      type('@ass')
      fireEvent.click(screen.getByText('@assistant'))
      expect(within(recipients()).getByText('@assistant')).toBeInTheDocument()
      expect(within(recipients()).queryByText('@assistant-2')).toBeNull()
      type(`${input().value}help`)
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

      await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['side:r1'] })))
      expect(post).not.toHaveBeenCalled()
      await waitFor(() => expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('claude · Accepted · waiting for an inbox check'))
    })

    it('shows an undeliverable side receipt before thread refresh without changing another recipient', async () => {
      const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'claude', reachable: true }
      vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm-side' } as Awaited<ReturnType<typeof meshClient.sendMeshMessageTo>>)
      const mesh = meshProps({ roster: roster({ participants: [...roster().participants, side] }) })
      const { rerender } = renderComposer(mesh)
      type('@assistant @qa help')
      fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
      await waitFor(() => expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('claude · Accepted · waiting for an inbox check'))

      const events = [
        { type: 'mesh_receipt', payload: { message_id: 'm-side', participant_id: 'side:r1', status: 'undeliverable' }, stageId: '', timestamp: '' },
        { type: 'mesh_receipt', payload: { message_id: 'm-side', participant_id: 'run:t-1', status: 'delivered' }, stageId: '', timestamp: '' },
      ]
      rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={{
        ...mesh, deliveries: meshDeliveries(events), sideFailures: sideAgentFailures(events),
      }} conversationMessages={[]} />)
      const delivery = screen.getByRole('status', { name: 'Delivery' })
      expect(delivery).toHaveTextContent('claude · Could not answer')
      expect(delivery).toHaveTextContent('QA reviewer · Delivered')
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
        const dialog = screen.getByRole('dialog', { name: /attach helper/i })
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
        await waitFor(() => expect(screen.queryByRole('dialog', { name: /attach helper/i })).toBeNull())
        expect(screen.getByRole('status', { name: 'Delivery' })).toHaveTextContent('Accepted · waiting for an inbox check')
        expect(within(recipients()).queryByText(/Attach helper/)).toBeNull()
        expect(input().value).toBe('')
      })

      it('Cancel issues no request and leaves the draft untouched', () => {
        const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
        openPreview()
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
        expect(screen.queryByRole('dialog', { name: /attach helper/i })).toBeNull()
        expect(attachSpy).not.toHaveBeenCalled()
        expect(input().value).toBe('please take a look')
      })

      it('an attached helper gets the text alone: with other recipients Send says so', () => {
        renderComposer()
        selectMention('@frontendDeveloper')
        type('@qa please')
        fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
        expect(screen.getByRole('alert')).toHaveTextContent('An attached helper gets the text alone')
        expect(screen.queryByRole('dialog', { name: /attach helper/i })).toBeNull()
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

    // --- /loop: a slash command is an operation, never a message ---
    describe('/loop command mode', () => {
      const helper: MeshParticipant = { id: 'helper:t-5', kind: 'helper', scope: 'stage', stage_id: 's1', alias: 'rev', display_name: 'Reviewer', reachable: true }
      const otherHelper: MeshParticipant = { id: 'helper:t-6', kind: 'helper', scope: 'stage', stage_id: 's2', alias: 'other', display_name: 'Other helper', reachable: true }
      const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'claude', reachable: true }
      const loopRoster = () => roster({ participants: [runAgent, docsAgent, stageAgent, helper, otherHelper, side, finished] })
      const loopMesh = (overrides: Partial<FeedComposerMeshProps> = {}) => meshProps({ roster: loopRoster(), ...overrides })
      const sendButton = (name: string) => screen.getByRole('button', { name })

      function spyOnMessageRoutes() {
        return [
          vi.spyOn(meshClient, 'sendMeshMessageTo'),
          vi.spyOn(runClient, 'postStageDiscussionMessage'),
          vi.spyOn(meshClient, 'attachAndSendMesh'),
        ]
      }
      const expectNothingSent = (spies: ReturnType<typeof spyOnMessageRoutes>) => spies.forEach((spy) => expect(spy).not.toHaveBeenCalled())

      const created: meshClient.MeshLoop = {
        id: 'L1', creator_id: 'user', target_id: 'run:t-1', every: '5m', origin_stage: 's1', status: 'active', next_tick_at: '2026-10-23T10:05:07Z',
      }
      const inTokyo = (children: ReactElement) => (
        <MessageTimeZoneProvider timeZone="Asia/Tokyo" utcOffsetMinutes={540}>{children}</MessageTimeZoneProvider>
      )
      const run = () => fireEvent.keyDown(input(), { key: 'Enter' })

      it.each([{ key: 'Enter' }, { key: 'Enter', ctrlKey: true }, { key: 'Enter', metaKey: true }])('submits loop commands with %j', async (key) => {
        const add = vi.spyOn(meshClient, 'addMeshLoop').mockResolvedValue(created)
        renderComposer(loopMesh())
        type('/loop 5m @qa check')
        fireEvent.keyDown(input(), key)
        await waitFor(() => expect(add).toHaveBeenCalledTimes(1))
      })

      it('keeps Shift+Enter as a newline and ignores a repeated Enter during loop submission', async () => {
        let finish: (loop: meshClient.MeshLoop) => void = () => {}
        const add = vi.spyOn(meshClient, 'addMeshLoop').mockReturnValue(new Promise((resolve) => { finish = resolve }))
        renderComposer(loopMesh())
        type('/loop 5m @qa check')
        expect(fireEvent.keyDown(input(), { key: 'Enter', shiftKey: true })).toBe(true)
        expect(add).not.toHaveBeenCalled()
        run()
        fireEvent.keyDown(input(), { key: 'Enter', repeat: true })
        expect(add).toHaveBeenCalledTimes(1)
        await act(async () => finish(created))
        expect(await screen.findByRole('status')).toHaveTextContent('Loop L1 started')
      })

      it('a partial /loop shows the muted syntax linked to the textarea and selectable intervals, not the To row or Interrupt', () => {
        renderComposer(loopMesh())
        type('/loop')
        expect(input()).toHaveAccessibleDescription(/\/loop <interval> \[--until-stage-end\] @agent <prompt> · \/loop list · \/loop stop <id>/)
        const examples = screen.getByRole('group', { name: 'Interval examples' })
        expect(within(examples).getAllByRole('button').map((b) => b.textContent)).toEqual(['5m', '15m', '1h'])
        for (const b of within(examples).getAllByRole('button')) expect(b).toHaveAccessibleDescription(/\/loop <interval>/)
        expect(screen.queryByRole('group', { name: 'Recipients' })).toBeNull()
        expect(screen.queryByLabelText('Interrupt agent')).toBeNull()
        // Send stays clickable even here: clicking it on incomplete syntax must still show why (mouse users
        // get no other way to see the parser's error).
        expect(sendButton('Start loop')).toBeEnabled()
      })

      it('an ordinary draft, /loops and a mid-text /loop keep the ordinary composer', () => {
        renderComposer(loopMesh())
        for (const text of ['hello', '/loops 5m', 'please /loop 5m @qa x']) {
          type(text)
          expect(recipients()).toBeInTheDocument()
          expect(screen.queryByRole('group', { name: 'Interval examples' })).toBeNull()
          expect(input()).not.toHaveAccessibleDescription(/\/loop <interval>/)
        }
      })

      it('/loops and a mid-text /loop actually reach the ordinary route, never the loop endpoints', async () => {
        const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'dm-1' })
        const add = vi.spyOn(meshClient, 'addMeshLoop')
        const list = vi.spyOn(meshClient, 'listMeshLoops')
        renderComposer(loopMesh())
        type('/loops 5m')
        run()
        await waitFor(() => expect(post).toHaveBeenCalledWith('s1', expect.any(String), { body: '/loops 5m', mentions: [], interrupt: false }))
        expect(add).not.toHaveBeenCalled()
        expect(list).not.toHaveBeenCalled()
      })

      it('clicking 5m completes the interval, keeps the draft and returns the focus to the textarea', () => {
        renderComposer(loopMesh())
        type('/loop')
        fireEvent.click(screen.getByRole('button', { name: '5m' }))
        expect(input().value).toBe('/loop 5m ')
        expect(input()).toHaveFocus()
        expect(screen.queryByRole('group', { name: 'Interval examples' })).toBeNull()
      })

      it('the examples are native buttons in the tab order, and an example replaces the interval typed so far', () => {
        renderComposer(loopMesh())
        type('/loop 5')
        const hour = screen.getByRole('button', { name: '1h' })
        expect(hour.tagName).toBe('BUTTON')
        expect(hour).toHaveAttribute('type', 'button')
        expect(hour).not.toHaveAttribute('tabindex', '-1')
        fireEvent.click(hour)
        expect(input().value).toBe('/loop 1h ')
      })

      it('@ after the interval offers only run agents and this stage\'s helpers — no stage agent, Side agent, other stage helper or attach profile', () => {
        renderComposer(loopMesh())
        type('/loop 5m @')
        const options = screen.getAllByRole('option').map((o) => o.textContent ?? '')
        expect(options).toHaveLength(3)
        expect(options.join('|')).toMatch(/QA reviewer/)
        expect(options.join('|')).toMatch(/Docs writer/)
        expect(options.join('|')).toMatch(/Reviewer/)
        expect(options.join('|')).not.toMatch(/Backend agent|claude|Other helper|Frontend reviewer|Old/)
      })

      it('shows the resolved target in its own row and a valid command can be started', () => {
        const spies = spyOnMessageRoutes()
        renderComposer(loopMesh())
        type('/loop 5m @qa проверь логи @docs')
        const target = screen.getByRole('group', { name: 'Loop target' })
        expect(within(target).getByText('@qa')).toBeInTheDocument()
        expect(within(target).queryByText('@docs')).toBeNull()
        expect(screen.queryByRole('group', { name: 'Recipients' })).toBeNull()
        expect(sendButton('Start loop')).toBeEnabled()
        expectNothingSent(spies)
      })

      it('keeps showing the resolved target while the prompt is still being typed', () => {
        renderComposer(loopMesh())
        type('/loop 5m @qa')
        const target = screen.getByRole('group', { name: 'Loop target' })
        expect(within(target).getByText('@qa')).toBeInTheDocument()
        type('/loop 5m @qa ')
        expect(within(screen.getByRole('group', { name: 'Loop target' })).getByText('@qa')).toBeInTheDocument()
      })

      it('Send already reads Run for an in-progress /loop stop or /loop list, before the rest parses', () => {
        renderComposer(loopMesh())
        type('/loop stop')
        // Still clickable (malformed syntax is rejected visibly on submit, like every other case), but its
        // label must already say Run, not Start loop, once the subcommand itself is unambiguous.
        expect(sendButton('Run')).toBeEnabled()
        type('/loop list x')
        expect(sendButton('Run')).toBeEnabled()
        type('/loop')
        expect(sendButton('Start loop')).toBeEnabled()
      })

      it('an unknown, ineligible or unreachable target is a visible problem and keeps Send off', () => {
        renderComposer(loopMesh())
        for (const [text, problem] of [
          ['/loop 5m @nobody check', /No one is called @nobody/],
          ['/loop 5m @s1 check', /run agent or a helper of this stage/],
          ['/loop 5m @assistant check', /run agent or a helper of this stage/],
          ['/loop 5m @other check', /run agent or a helper of this stage/],
          ['/loop 5m @old check', /can't receive messages/],
        ] as const) {
          type(text)
          expect(screen.getByRole('alert')).toHaveTextContent(problem)
          expect(sendButton('Start loop')).toBeDisabled()
        }
      })

      it('Cmd/Ctrl+Enter on an unresolvable target is a no-op too: the disabled Send button has no keyboard escape hatch', () => {
        const spies = spyOnMessageRoutes()
        const add = vi.spyOn(meshClient, 'addMeshLoop')
        renderComposer(loopMesh())
        type('/loop 5m @s1 check')
        run()
        expect(input().value).toBe('/loop 5m @s1 check')
        expect(add).not.toHaveBeenCalled()
        expectNothingSent(spies)
      })

      it('malformed syntax is rejected visibly on submit, the draft stays and nothing is sent', () => {
        const spies = spyOnMessageRoutes()
        renderComposer(loopMesh())
        for (const [text, error] of [
          ['/loop 5m @qa ', /prompt/i],
          ['/loop 5m check the logs', /target/i],
          ['/loop soon @qa x', /duration/i],
        ] as const) {
          type(text)
          expect(sendButton('Start loop')).toBeEnabled()
          fireEvent.keyDown(input(), { key: 'Enter', metaKey: true })
          expect(screen.getByRole('alert')).toHaveTextContent(error)
          expect(input().value).toBe(text)
        }
        expectNothingSent(spies)
      })

      it('malformed syntax is rejected visibly on a pointer click too, the draft stays and nothing is sent', () => {
        const spies = spyOnMessageRoutes()
        renderComposer(loopMesh())
        for (const [text, error] of [
          ['/loop 5m @qa ', /prompt/i],
          ['/loop 5m check the logs', /target/i],
          ['/loop soon @qa x', /duration/i],
        ] as const) {
          type(text)
          const button = sendButton('Start loop')
          expect(button).toBeEnabled()
          fireEvent.click(button)
          expect(screen.getByRole('alert')).toHaveTextContent(error)
          expect(input().value).toBe(text)
        }
        expectNothingSent(spies)
      })

      it('typing clears a submit error', () => {
        renderComposer(loopMesh())
        type('/loop 5m @qa ')
        fireEvent.keyDown(input(), { key: 'Enter', metaKey: true })
        expect(screen.getByRole('alert')).toBeInTheDocument()
        type('/loop 5m @qa x')
        expect(screen.queryByRole('alert')).toBeNull()
      })

      it('a valid command never reaches the message, stage-post or attach endpoints', async () => {
        const spies = spyOnMessageRoutes()
        const add = vi.spyOn(meshClient, 'addMeshLoop').mockResolvedValue(created)
        renderComposer(loopMesh())
        type('/loop 5m @qa проверь логи')
        run()
        await waitFor(() => expect(add).toHaveBeenCalledTimes(1))
        expectNothingSent(spies)
      })

      it('/loop 5m @qa <prompt> schedules exactly that prompt for the resolved target from this Feed', async () => {
        const add = vi.spyOn(meshClient, 'addMeshLoop').mockResolvedValue(created)
        renderComposer(loopMesh())
        type('/loop 5m @qa проверь логи @docs')
        fireEvent.click(sendButton('Start loop'))
        await waitFor(() => expect(add).toHaveBeenCalledWith({ requestId: expect.any(String), target: 'run:t-1', every: '5m', prompt: 'проверь логи @docs', originStage: 's1' }))
      })

      it('--until-stage-end bounds the loop to the stage whose Feed it is created in', async () => {
        const add = vi.spyOn(meshClient, 'addMeshLoop').mockResolvedValue({ ...created, until_stage: 's1' })
        renderComposer(loopMesh())
        type('/loop 1h --until-stage-end @qa check')
        run()
        await waitFor(() => expect(add).toHaveBeenCalledWith(expect.objectContaining({ every: '1h', prompt: 'check', untilStage: 's1', originStage: 's1' })))
        expect(await screen.findByRole('status')).toHaveTextContent(/ends with Backend/)
      })

      it('a created loop shows its id, next tick in host time with seconds, stop command and origin Feed, and clears the draft', async () => {
        vi.spyOn(meshClient, 'addMeshLoop').mockResolvedValue(created)
        renderComposer(loopMesh(), {}, inTokyo)
        type('/loop 5m @qa проверь логи')
        run()
        const receipt = await screen.findByRole('status')
        expect(receipt).toHaveTextContent('Loop L1 started')
        expect(receipt).toHaveTextContent('every 5m')
        expect(receipt).toHaveTextContent('@qa')
        expect(receipt).toHaveTextContent(/Next tick .*19:05:07/)
        expect(receipt).toHaveTextContent('/loop stop L1')
        expect(receipt).toHaveTextContent('Feed: Backend')
        expect(input().value).toBe('')
      })

      it.each(['create', 'list'])('uses the stage id for unnamed origin and bound stages in a %s result', async (action) => {
        const loop = { ...created, until_stage: 's1' }
        vi.spyOn(meshClient, 'addMeshLoop').mockResolvedValue(loop)
        vi.spyOn(meshClient, 'listMeshLoops').mockResolvedValue({ loops: [loop] })
        renderComposer(loopMesh({ stages: [makeStage({ name: '' })] }))
        type(action === 'create' ? '/loop 5m --until-stage-end @qa check' : '/loop list')
        run()
        const receipt = await screen.findByRole('status')
        expect(receipt).toHaveTextContent('Feed: s1')
        expect(receipt).toHaveTextContent('ends with s1')
      })

      it('clears only the submitted command: text typed while it was in flight stays', async () => {
        let finish: (loop: meshClient.MeshLoop) => void = () => {}
        vi.spyOn(meshClient, 'addMeshLoop').mockReturnValue(new Promise((resolve) => { finish = resolve }))
        renderComposer(loopMesh())
        type('/loop 5m @qa check')
        run()
        expect(sendButton('Sending…')).toBeDisabled()
        type('/loop 5m @qa check\nand more')
        await act(async () => finish(created))
        expect(input().value).toBe('and more')
      })

      it('the receipt stays until the next edit, and Escape dismisses it', async () => {
        vi.spyOn(meshClient, 'addMeshLoop').mockResolvedValue(created)
        renderComposer(loopMesh())
        type('/loop 5m @qa check')
        run()
        expect(await screen.findByRole('status')).toHaveTextContent('Loop L1 started')
        fireEvent.keyDown(input(), { key: 'Escape' })
        expect(screen.queryByRole('status')).toBeNull()

        type('/loop 5m @qa again')
        run()
        expect(await screen.findByRole('status')).toBeInTheDocument()
        type('hello')
        expect(screen.queryByRole('status')).toBeNull()
      })

      it('a failed create keeps the draft, shows the server reason as an alert and retries under the same request id', async () => {
        const add = vi.spyOn(meshClient, 'addMeshLoop')
          .mockRejectedValueOnce(new meshClient.MeshApiError('/api/mesh/loop', 409, 'invalid_status', 'at most 16 loops can run at once'))
          .mockResolvedValueOnce(created)
        renderComposer(loopMesh())
        type('/loop 5m @qa check')
        run()
        expect(await screen.findByRole('alert')).toHaveTextContent('at most 16 loops can run at once')
        expect(input().value).toBe('/loop 5m @qa check')
        expect(screen.queryByRole('status')).toBeNull()
        expect(sendButton('Start loop')).toBeEnabled()

        fireEvent.click(sendButton('Start loop'))
        await waitFor(() => expect(add).toHaveBeenCalledTimes(2))
        expect(add.mock.calls[0]?.[0].requestId).toBe(add.mock.calls[1]?.[0].requestId)
        expect(await screen.findByRole('status')).toHaveTextContent('Loop L1 started')
        expect(screen.queryByRole('alert')).toBeNull()
      })

      it('the error of a failed command goes away when the draft is edited', async () => {
        vi.spyOn(meshClient, 'stopMeshLoop').mockRejectedValue(new Error('unknown loop "L9"'))
        renderComposer(loopMesh())
        type('/loop stop L9')
        run()
        expect(await screen.findByRole('alert')).toHaveTextContent('unknown loop "L9"')
        type('/loop stop L1')
        expect(screen.queryByRole('alert')).toBeNull()
      })

      const doneFeed = () => {
        const done = { ...stageAgent, reachable: false, unreachable_reason: 'stage s1 has reached a terminal status' }
        return loopMesh({ roster: roster({ participants: [runAgent, done] }), stages: [makeStage({ status: 'done' })] })
      }

      it('/loop list and /loop stop L1 are offered in a completed stage\'s Feed with no recipient and no Interrupt', () => {
        const spies = spyOnMessageRoutes()
        renderComposer(doneFeed(), { reviseAvailable: false })
        for (const text of ['/loop list', '/loop stop L1']) {
          type(text)
          expect(screen.queryByRole('group', { name: 'Recipients' })).toBeNull()
          expect(screen.queryByLabelText('Interrupt agent')).toBeNull()
          expect(within(screen.getByRole('group', { name: 'Loop target' })).getByText(/whole run/i)).toBeInTheDocument()
          expect(sendButton('Run')).toBeEnabled()
        }
        expectNothingSent(spies)
      })

      it('/loop list shows every loop of the run: id, creator, target, interval, origin Feed, stage bound and state', async () => {
        const spies = spyOnMessageRoutes()
        const list = vi.spyOn(meshClient, 'listMeshLoops').mockResolvedValue({
          loops: [
            created,
            { id: 'L2', creator_id: 'run:t-2', target_id: 'helper:t-5', every: '1m30s', until_stage: 's2', status: 'ended', end_reason: 'the conversation reached its message limit' },
            { id: 'L3', creator_id: 'user', target_id: 'run:t-2', every: '1h', origin_stage: 's2', status: 'stopped', end_reason: 'stopped by You' },
          ],
        })
        renderComposer(loopMesh({ stages: [makeStage(), makeStage({ id: 's2', name: 'Docs stage' })] }), {}, inTokyo)
        type('/loop list')
        run()
        const result = await screen.findByRole('status')
        expect(list).toHaveBeenCalledTimes(1)
        const rows = within(result).getAllByRole('listitem').map((li) => li.textContent ?? '')
        expect(rows).toHaveLength(3)
        expect(rows[0]).toMatch(/L1.*active.*every 5m.*@qa.*You.*Feed: Backend.*next tick .*19:05:07/)
        expect(rows[1]).toMatch(/L2.*ended.*the conversation reached its message limit.*every 1m30s.*@rev.*Docs writer.*run conversations.*ends with Docs stage/)
        expect(rows[2]).toMatch(/L3.*stopped.*stopped by You.*every 1h.*@docs.*Feed: Docs stage/)
        expect(input().value).toBe('')
        expectNothingSent(spies)
      })

      it('/loop list says when the run has no loops', async () => {
        vi.spyOn(meshClient, 'listMeshLoops').mockResolvedValue({ loops: [] })
        renderComposer(loopMesh())
        type('/loop list')
        run()
        expect(await screen.findByRole('status')).toHaveTextContent('No loops in this run')
      })

      it('/loop stop L1 works from a completed stage\'s Feed and reports the stopped loop', async () => {
        const spies = spyOnMessageRoutes()
        const stop = vi.spyOn(meshClient, 'stopMeshLoop').mockResolvedValue({ ...created, status: 'stopped', end_reason: 'stopped by You', next_tick_at: undefined })
        renderComposer(doneFeed(), { reviseAvailable: false })
        type('/loop stop l1')
        fireEvent.click(sendButton('Run'))
        expect(await screen.findByRole('status')).toHaveTextContent('Loop L1 stopped')
        expect(stop).toHaveBeenCalledWith({ requestId: expect.any(String), loopId: 'L1' })
        expect(input().value).toBe('')
        expectNothingSent(spies)
      })

      it('a failed /loop list keeps the draft and shows the reason', async () => {
        vi.spyOn(meshClient, 'listMeshLoops').mockRejectedValue(new meshClient.MeshApiError('/api/mesh/loops', 404, undefined))
        renderComposer(loopMesh())
        type('/loop list')
        run()
        expect(await screen.findByRole('alert')).toHaveTextContent('/api/mesh/loops -> 404')
        expect(input().value).toBe('/loop list')
      })

      it('/loop list ignores a reply draft, People-panel chips and an attaching profile', async () => {
        const spies = spyOnMessageRoutes()
        const list = vi.spyOn(meshClient, 'listMeshLoops').mockResolvedValue({ loops: [] })
        const reply = { messageId: 'm5', threadId: 'mt-docs', senderId: 'run:t-2', preview: 'Docs question', members: ['user', 'run:t-2'] }
        const { rerender } = renderComposer(loopMesh({ pick: { participantId: 'run:t-1', seq: 1 } }), { meshReply: reply })
        type('/loop list')
        expect(screen.queryByRole('group', { name: 'Recipients' })).toBeNull()
        expect(sendButton('Run')).toBeEnabled()
        run()
        await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
        expectNothingSent(spies)
        rerender(<FeedComposer stageId="s1" onSend={vi.fn()} mesh={loopMesh()} meshReply={null} />)
        type('')
        selectMention('@frontendDeveloper')
        type('/loop list')
        fireEvent.click(sendButton('Run'))
        await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
        expect(screen.queryByRole('dialog', { name: /attach helper/i })).toBeNull()
        expectNothingSent(spies)
        // Leaving loop mode brings the attach chip and its ordinary Send back.
        type('please look')
        expect(within(recipients()).getByText(/Attach helper/)).toBeInTheDocument()
      })

      it('a /loop note in Revise says slash commands run in Message and cannot be applied', () => {
        const { onSend } = renderComposer(loopMesh())
        fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
        const note = screen.getByPlaceholderText('Note to agent…')
        fireEvent.change(note, { target: { value: '/loop 5m @qa check' } })
        expect(screen.getByText('Slash commands run in Message')).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Apply Revise' })).toBeDisabled()
        fireEvent.keyDown(note, { key: 'Enter', metaKey: true })
        expect(onSend).not.toHaveBeenCalled()
        expect(note).toHaveValue('/loop 5m @qa check')
        fireEvent.change(note, { target: { value: '/loops are fine here' } })
        expect(screen.queryByText('Slash commands run in Message')).toBeNull()
        expect(screen.getByRole('button', { name: 'Apply Revise' })).toBeEnabled()
      })
    })
  })
})
