import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { MeshParticipant } from '../../api/mesh-client'
import { SideAgentContext, type SideAgentContextValue } from '../side-agent/side-agent-context'
import { CommentProvider } from './CommentProvider'
import { CommentRecipientSelect } from './CommentRecipientSelect'
import { CommentRecipientsContext, type CommentRecipientsRoster } from './comment-recipients'

const impl: MeshParticipant = { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', stage_id: 'impl', alias: 'impl', display_name: 'Backend', reachable: true }
const qa: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true }

const sideAgent = (over: Partial<SideAgentContextValue> = {}): SideAgentContextValue => ({
  enabled: true, isOpen: false, open: () => {}, openWith: () => {}, sendExactMessage: () => Promise.resolve(), unseenResult: false, active: false, ...over,
})

function renderSelect({
  roster = { runId: 'r1', participants: [impl, qa], stages: [{ id: 'impl', status: 'running' }] },
  side = sideAgent(),
  value = '',
  onChange = vi.fn(),
}: { roster?: CommentRecipientsRoster; side?: SideAgentContextValue; value?: string; onChange?: (id: string) => void } = {}) {
  const tree = (r: CommentRecipientsRoster, s: SideAgentContextValue, v: string) => (
    <SideAgentContext.Provider value={s}>
      <CommentRecipientsContext.Provider value={r}>
        <CommentProvider runId="r1">
          <CommentRecipientSelect recipientId={v} onChange={onChange} />
        </CommentProvider>
      </CommentRecipientsContext.Provider>
    </SideAgentContext.Provider>
  )
  const view = render(tree(roster, side, value))
  return { onChange, update: (r: CommentRecipientsRoster, s = side, v = value) => view.rerender(tree(r, s, v)) }
}

const select = () => screen.getByRole('combobox', { name: 'Recipient' }) as HTMLSelectElement

afterEach(() => vi.restoreAllMocks())

describe('CommentRecipientSelect', () => {
  it('lists every reachable recipient with its role', () => {
    renderSelect()
    expect(Array.from(select().options).map((o) => o.textContent)).toEqual([
      'Choose an agent',
      'Assistant · Assistant',
      'Backend · Stage executor',
      'QA · Run agent',
    ])
  })

  it('with several recipients and no earlier choice shows the prompt', () => {
    renderSelect()
    expect(select().value).toBe('')
    expect(screen.getByText('Choose an agent', { selector: '.comment-recipient-note' })).toBeTruthy()
  })

  it('selects the Assistant when it is the only recipient, with no roster at all', () => {
    renderSelect({ roster: { runId: 'r1', participants: [], stages: [] } })
    expect(select().value).toBe('side:r1')
    expect(screen.queryByText('Choose an agent', { selector: '.comment-recipient-note' })).toBeNull()
  })

  it('reports a pick by participant id', () => {
    const { onChange } = renderSelect()
    fireEvent.change(select(), { target: { value: 'run:t-1' } })
    expect(onChange).toHaveBeenCalledWith('run:t-1')
  })

  it('keeps the chosen recipient across a roster refresh', () => {
    const { update } = renderSelect({ value: 'run:t-1' })
    expect(select().value).toBe('run:t-1')
    update({ runId: 'r1', participants: [{ ...qa, display_name: 'QA bot' }, impl], stages: [{ id: 'impl', status: 'running' }] }, sideAgent(), 'run:t-1')
    expect(select().value).toBe('run:t-1')
    expect(screen.getByRole('option', { name: 'QA bot · Run agent' })).toBeTruthy()
  })

  it('explains a chosen recipient that disappeared and returns to it when it is back', () => {
    const { update } = renderSelect({ value: 'run:t-1' })
    update({ runId: 'r1', participants: [impl], stages: [{ id: 'impl', status: 'running' }] }, sideAgent(), 'run:t-1')
    expect(select().value).toBe('')
    expect(screen.getByText(/no longer available/i)).toBeTruthy()
    update({ runId: 'r1', participants: [impl, qa], stages: [{ id: 'impl', status: 'running' }] }, sideAgent(), 'run:t-1')
    expect(select().value).toBe('run:t-1')
    expect(screen.queryByText(/no longer available/i)).toBeNull()
  })

  it('shows that the Assistant is busy', () => {
    renderSelect({ roster: { runId: 'r1', participants: [], stages: [] }, side: sideAgent({ active: true }) })
    expect(screen.getByRole('option', { name: 'Assistant · Assistant (busy)' })).toBeTruthy()
    expect(screen.getByText(/still answering/i)).toBeTruthy()
  })

  it('explains an empty list when nobody can receive comments', () => {
    renderSelect({ roster: { runId: 'r1', participants: [], stages: [] }, side: sideAgent({ enabled: false }) })
    expect(screen.getByText(/no agent/i)).toBeTruthy()
    expect(select().disabled).toBe(true)
  })

  it('does not call the network: it reads the roster the app already polls', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    renderSelect()
    expect(fetchSpy).not.toHaveBeenCalled()
  })
})
