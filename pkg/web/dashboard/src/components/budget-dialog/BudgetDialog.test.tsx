import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { BudgetDialog } from './BudgetDialog'
import { BudgetUiContext, NO_BUDGET_UI, type BudgetUi } from './budget-context'

type Json = Record<string, unknown>

// Минимальная замена user-event (пакета в проекте нет): клик и ввод поверх fireEvent.
const userEvent = {
  click: async (el: HTMLElement) => {
    fireEvent.click(el)
  },
  clear: async (el: HTMLElement) => {
    fireEvent.change(el, { target: { value: '' } })
  },
  type: async (el: HTMLElement, text: string) => {
    fireEvent.change(el, { target: { value: (el as HTMLInputElement).value + text } })
  },
}

const view = (over: Json = {}): Json => ({
  scope: 'stage:impl',
  kind: 'stage',
  revision: 3,
  base: { tokens: 100000, stop_policy: 'hard' },
  base_source: 'declaration',
  override: { kind: 'none' },
  effective: { tokens: 100000, stop_policy: 'hard' },
  spent: { tokens: 82000, money_nano_usd: 0 },
  evaluation: { tokens: 'warning' },
  actions: ['update', 'resume', 'save_to_yaml'],
  ...over,
})

const yaml = (over: Json = {}): Json => ({ available: true, file: 'flow.yaml', declared: { tokens: 100000, stop_policy: 'hard' }, ...over })

type Handler = (url: string, init?: RequestInit) => { status?: number; body: unknown }

// api — мок fetch: ответы по маршруту; calls хранит разобранные тела POST.
function mockApi(handler: Handler) {
  const posts: { url: string; body: Json }[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(init.body as string) as Json })
    const { status = 200, body } = handler(url, init)
    return { ok: status >= 200 && status < 300, status, json: async () => body } as Response
  })
  return posts
}

function renderDialog(ui: Partial<BudgetUi> = {}, props: Partial<Parameters<typeof BudgetDialog>[0]> = {}) {
  const onClose = vi.fn()
  const onView = vi.fn()
  const onChanged = vi.fn()
  const value: BudgetUi = { ...NO_BUDGET_UI, enabled: true, ...ui }
  const tree = (el: ReactElement) => <BudgetUiContext.Provider value={value}>{el}</BudgetUiContext.Provider>
  const element = <BudgetDialog scope="stage:impl" opener={null} onClose={onClose} onView={onView} onChanged={onChanged} {...props} />
  const result = render(tree(element))
  return { ...result, onClose, onView, onChanged, rerender: () => result.rerender(tree(element)) }
}

async function ready() {
  await screen.findByRole('group', { name: /Limit for this/ })
}

describe('BudgetDialog', () => {
  afterEach(() => vi.restoreAllMocks())

  test('opens on the current view of the scope and shows the summary', async () => {
    mockApi((url) => (url.startsWith('/api/budgets/view') ? { body: { view: view(), yaml: yaml() } } : { body: {} }))
    renderDialog()
    expect(screen.getByText('Loading budget…')).toBeInTheDocument()
    await ready()
    expect(fetch).toHaveBeenCalledWith('/api/budgets/view?scope=stage%3Aimpl', undefined)
    expect(screen.getByRole('dialog', { name: 'Budget · Stage impl' })).toBeInTheDocument()
    expect(screen.getByText('82k tokens · $0.00')).toBeInTheDocument()
    expect(screen.getByText('100k tokens · Hard stop')).toBeInTheDocument()
    expect(screen.getByText('18k tokens left')).toBeInTheDocument()
    expect(screen.getByText('Near the limit')).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Use declared limit — 100k tokens' })).toBeChecked()
    expect(screen.getByRole('radio', { name: /No limit for this stage/ })).toBeInTheDocument()
    expect(screen.getByText('Parent limits still apply.')).toBeInTheDocument()
    expect(screen.getByText(/Declared limit: 100k tokens \(declared in the flow\)/)).toBeInTheDocument()
  })

  test('a failed load says so and still closes', async () => {
    mockApi(() => ({ status: 404, body: { error: 'budget_unknown_scope', message: 'unknown budget scope' } }))
    const { onClose } = renderDialog()
    expect(await screen.findByRole('alert')).toHaveTextContent('unknown budget scope')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalled()
  })

  test('custom fields validate input and keep Save disabled until it is valid', async () => {
    mockApi(() => ({ body: { view: view(), yaml: yaml() } }))
    renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    const tokens = screen.getByLabelText('Tokens')
    const usd = screen.getByLabelText(/^USD/)
    await userEvent.clear(tokens)
    await userEvent.clear(usd)
    expect(screen.getByText('Set Tokens or USD (at least one)')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Save and continue' })).toBeDisabled()

    await userEvent.type(tokens, 'lots')
    expect(tokens).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByText(/Use a number like 500k/)).toBeInTheDocument()
    await userEvent.clear(tokens)
    await userEvent.type(tokens, '500k')
    await userEvent.type(usd, '$12')
    expect(tokens).toHaveAttribute('aria-invalid', 'false')
    expect(screen.getByText('Needs explicit rates in config.yaml')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  test('policy choices carry their descriptions', async () => {
    mockApi(() => ({ body: { view: view(), yaml: yaml() } }))
    renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    const policies = screen.getByRole('group', { name: 'When the limit is reached' })
    expect(within(policies).getByText('Interrupts the running work of this scope when the limit is reached.')).toBeInTheDocument()
    expect(within(policies).getByText('Lets work that already started finish; nothing new starts.')).toBeInTheDocument()
    expect(within(policies).getByText('Only warns; work continues past the limit.')).toBeInTheDocument()
  })

  test('Save sends the strict request without resume and closes on success', async () => {
    const posts = mockApi((url) => (url === '/api/budgets/update' ? { body: { view: view({ revision: 4 }) } } : { body: { view: view(), yaml: yaml() } }))
    const { onClose, onView, onChanged } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '500k')
    await userEvent.click(screen.getByRole('radio', { name: /Warn only/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(posts).toEqual([
      { url: '/api/budgets/update', body: { scope: 'stage:impl', expected_revision: 3, override: { kind: 'limit', spec: { tokens: 500000, stop_policy: 'info' } } } },
    ])
    expect(onView).toHaveBeenLastCalledWith(expect.objectContaining({ revision: 4 }))
    expect(onChanged).toHaveBeenCalled()
  })

  test('Save and continue asks the server to resume and closes when the work continues', async () => {
    const posts = mockApi((url) => (url === '/api/budgets/update' ? { body: { view: view({ revision: 4 }) } } : { body: { view: view(), yaml: yaml() } }))
    const { onClose } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: /No limit for this stage/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(posts[0]?.body).toEqual({ scope: 'stage:impl', expected_revision: 3, override: { kind: 'unlimited' }, resume: true })
  })

  test('without the resume action there is no continue button at all', async () => {
    mockApi(() => ({ body: { view: view({ actions: ['update'] }), yaml: null } }))
    renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: /No limit for this stage/ }))
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'Save and continue' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull()
  })

  test('a clean draft offers Continue, which resumes under the current revision', async () => {
    const posts = mockApi((url) => (url === '/api/budgets/resume' ? { body: { view: view({ hold: null }) } } : { body: { view: view({ hold: { reason: 'limit_reached', scope: 'stage:impl' } }), yaml: yaml() } }))
    const { onClose } = renderDialog()
    await ready()
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(posts).toEqual([{ url: '/api/budgets/resume', body: { scope: 'stage:impl', expected_revision: 3 } }])
  })

  test('still limited: the limit is saved, the dialog stays open and the next request uses the new revision', async () => {
    const posts = mockApi((url) =>
      url === '/api/budgets/update'
        ? { body: { view: view({ revision: 4, override: { kind: 'limit', spec: { tokens: 90000, stop_policy: 'hard' } }, hold: { reason: 'limit_reached', scope: 'stage:impl' } }), resume_error: { code: 'budget_still_limited', message: 'spent 90k of 90k tokens' } } }
        : { body: { view: view({ hold: { reason: 'limit_reached', scope: 'stage:impl' } }), yaml: yaml() } },
    )
    const { onClose } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '90k')
    await userEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Limit saved. Work is still stopped: spent 90k of 90k tokens')
    expect(onClose).not.toHaveBeenCalled()
    // The draft is rebased on the saved view: it is clean now, so the form offers Continue; raising the limit again uses revision 4.
    expect(screen.getByLabelText('Tokens')).toHaveValue('90k')
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '200k')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(posts).toHaveLength(2))
    expect(posts[1]?.body).toMatchObject({ expected_revision: 4, override: { kind: 'limit', spec: { tokens: 200000 } } })
    expect(posts[1]?.body.resume).toBeUndefined()
  })

  test('a revision conflict keeps the draft, shows the current values and needs another Save', async () => {
    let viewReads = 0
    const posts = mockApi((url) => {
      if (url === '/api/budgets/update') {
        return posts.length === 1 ? { status: 409, body: { error: 'budget_revision_conflict', message: 'revision moved' } } : { body: { view: view({ revision: 8 }) } }
      }
      viewReads++
      return { body: { view: viewReads === 1 ? view() : view({ revision: 7, spent: { tokens: 99000, money_nano_usd: 0 } }), yaml: yaml() } }
    })
    const { onClose } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '300k')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/This budget changed while you were editing/)
    await screen.findByText('99k tokens · $0.00')
    expect(screen.getByLabelText('Tokens')).toHaveValue('300k')
    expect(onClose).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(posts[1]?.body.expected_revision).toBe(7)
  })

  test('a failed YAML export keeps the run limit, offers a retry and a separate Continue', async () => {
    const saved = view({ revision: 4, override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } }, hold: { reason: 'limit_reached', scope: 'stage:impl' } })
    const posts = mockApi((url) => {
      if (url === '/api/budgets/update') {
        return posts.length === 1
          ? { body: { view: saved, export: { status: 'failed', path: 'flow.yaml', error: 'file changed on disk' } } }
          : { body: { view: view({ revision: 5, override: saved.override as Json }), export: { status: 'saved', path: 'flow.yaml' } } }
      }
      return { body: { view: posts.length > 0 ? saved : view({ hold: { reason: 'limit_reached', scope: 'stage:impl' } }), yaml: yaml() } }
    })
    const { onClose } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '300k')
    await userEvent.click(screen.getByRole('checkbox', { name: 'Also save to flow.yaml' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Run limit saved, flow.yaml was not changed: file changed on disk')
    expect(posts[0]?.body).toMatchObject({ save_to_yaml: true, resume: true })
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled()

    await userEvent.click(screen.getByRole('button', { name: 'Retry saving to flow.yaml' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(posts[1]?.body).toEqual({ scope: 'stage:impl', expected_revision: 4, override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } }, save_to_yaml: true })
  })

  test('an unavailable YAML export is reported, keeps the dialog open, offers Continue and refreshes availability', async () => {
    let views = 0
    const saved = view({ revision: 4, override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } }, hold: { reason: 'limit_reached', scope: 'stage:impl' } })
    const posts = mockApi((url) => {
      if (url === '/api/budgets/update') return { body: { view: saved, export: { status: 'unavailable', path: '', error: 'declared by an alias' } } }
      views += 1
      return { body: { view: views === 1 ? view() : saved, yaml: yaml() } }
    })
    const { onClose } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '300k')
    await userEvent.click(screen.getByRole('checkbox', { name: 'Also save to flow.yaml' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Run limit saved, flow.yaml was not changed: declared by an alias')
    expect(posts).toHaveLength(1)
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled()
    await waitFor(() => expect(views).toBe(2))
  })

  test('an already saved custom limit can be exported without changing it again', async () => {
    const saved = view({ override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } } })
    const posts = mockApi((url) => (url === '/api/budgets/update' ? { body: { view: view({ revision: 4, override: saved.override as Json }), export: { status: 'saved', path: 'flow.yaml', error: '' } } } : { body: { view: saved, yaml: yaml() } }))
    const { onClose } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('checkbox', { name: 'Also save to flow.yaml' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save to flow.yaml' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(posts[0]?.body).toEqual({ scope: 'stage:impl', expected_revision: 3, override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } }, save_to_yaml: true })
  })

  test('Continue with the export option checked sends the export with the resume', async () => {
    const saved = view({ override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } }, hold: { reason: 'limit_reached', scope: 'stage:impl' } })
    const posts = mockApi((url) => (url === '/api/budgets/update' ? { body: { view: view({ revision: 4 }), export: { status: 'saved', path: 'flow.yaml', error: '' } } } : { body: { view: saved, yaml: yaml() } }))
    renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('checkbox', { name: 'Also save to flow.yaml' }))
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]?.url).toBe('/api/budgets/update')
    expect(posts[0]?.body).toMatchObject({ save_to_yaml: true, resume: true })
  })

  test('a late answer of a cancelled dialog does not close the next one', async () => {
    let release: (() => void) | undefined
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/budgets/update') {
        await gate
        return { ok: true, status: 200, json: async () => ({ view: view({ revision: 4 }) }) } as Response
      }
      return { ok: true, status: 200, json: async () => ({ view: view(), yaml: yaml() }) } as Response
    })
    const first = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: /No limit for this stage/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    first.unmount()
    await act(async () => {
      release?.()
      await gate
    })
    expect(first.onClose).not.toHaveBeenCalled()
    expect(first.onChanged).toHaveBeenCalled()
  })

  test.each([
    ['still limited', { view: view({ revision: 4, override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } } }), resume_error: { code: 'budget_still_limited', message: 'still over' } }],
    ['export failed', { view: view({ revision: 4, override: { kind: 'limit', spec: { tokens: 300000, stop_policy: 'hard' } } }), export: { status: 'failed', path: 'flow.yaml', error: 'busy' } }],
  ])('keeps keyboard focus inside the dialog after a partial save: %s', async (_name, body) => {
    mockApi((url) => (url === '/api/budgets/update' ? { body } : { body: { view: view(), yaml: yaml() } }))
    renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '300k')
    screen.getByRole('button', { name: 'Save and continue' }).focus()
    await userEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    const alert = await screen.findByRole('alert')
    await waitFor(() => expect(document.activeElement).toBe(alert))
    expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)
  })

  test('a held child stays blocked by the parent hold although the parent evaluation is ok now', async () => {
    mockApi(() => ({
      body: {
        view: view({
          hold: { reason: 'parent_limit_reached', scope: 'run' },
          parent_limits: [{ scope: 'run', limit: { tokens: 1000000, stop_policy: 'hard' }, spent: { tokens: 10, money_nano_usd: 0 }, evaluation: { tokens: 'ok' } }],
        }),
        yaml: yaml(),
      },
    }))
    renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: /No limit for this stage/ }))
    expect(screen.getByRole('button', { name: 'Save and continue' })).toBeDisabled()
    expect(screen.getByText(/Blocked by the budget of Run/)).toBeInTheDocument()
  })

  describe('Also save to the file', () => {
    test('is available only for a custom limit; otherwise disabled with a visible reason', async () => {
      mockApi(() => ({ body: { view: view(), yaml: yaml() } }))
      renderDialog()
      await ready()
      const box = screen.getByRole('checkbox', { name: 'Also save to flow.yaml' })
      expect(box).toBeDisabled()
      const reason = screen.getByText('Only a custom limit can be saved to flow.yaml.')
      expect(box.getAttribute('aria-describedby')).toBe(reason.id)
      await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
      expect(screen.getByRole('checkbox', { name: 'Also save to flow.yaml' })).toBeEnabled()
    })

    test('an unavailable declaration says why', async () => {
      mockApi(() => ({ body: { view: view(), yaml: yaml({ available: false, reason: 'the stage is declared through a YAML alias' }) } }))
      renderDialog()
      await ready()
      await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
      expect(screen.getByRole('checkbox', { name: /Also save to/ })).toBeDisabled()
      expect(screen.getByText('the stage is declared through a YAML alias')).toBeInTheDocument()
    })

    test('without a yaml block the option is off with a reason and nothing is exported', async () => {
      const posts = mockApi((url) => (url === '/api/budgets/update' ? { body: { view: view({ revision: 4 }) } } : { body: { view: view({ actions: ['update'] }) } }))
      const { onClose } = renderDialog()
      await ready()
      await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
      expect(screen.getByRole('checkbox', { name: /Also save to/ })).toBeDisabled()
      await userEvent.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(onClose).toHaveBeenCalled())
      expect(posts[0]?.body.save_to_yaml).toBeUndefined()
    })

    test('hints when the file declares another value than this run started with', async () => {
      mockApi(() => ({ body: { view: view(), yaml: yaml({ declared: { tokens: 500000, stop_policy: 'hard' } }) } }))
      renderDialog()
      await ready()
      expect(screen.getByRole('note')).toHaveTextContent('flow.yaml declares 500k tokens, but this run started with 100k tokens.')
    })
  })

  test('a blocking parent limit is explained and disables Save and continue', async () => {
    mockApi(() => ({
      body: {
        view: view({
          hold: { reason: 'parent_limit_reached', scope: 'run' },
          parent_limits: [{ scope: 'run', limit: { money_usd: 10, stop_policy: 'hard' }, spent: { tokens: 1, money_nano_usd: 10_000_000_000 }, evaluation: { money: 'reached' } }],
        }),
        yaml: yaml(),
      },
    }))
    renderDialog()
    await ready()
    expect(screen.getByRole('region', { name: 'Parent limits' })).toHaveTextContent('Run: $10.00 / $10.00 · Hard stop')
    await userEvent.click(screen.getByRole('radio', { name: /No limit for this stage/ }))
    const button = screen.getByRole('button', { name: 'Save and continue' })
    expect(button).toBeDisabled()
    const reason = screen.getByText(/Blocked by the budget of Run\. Changing the limit here does not unblock it/)
    expect(button.getAttribute('aria-describedby')).toBe(reason.id)
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  test('a closed run disables every action', async () => {
    mockApi((url) => (url === '/api/budgets/update' ? { status: 409, body: { error: 'budget_closed', message: 'closed' } } : { body: { view: view(), yaml: yaml() } }))
    renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: /No limit for this stage/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/run is closed or unavailable/)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Save and continue' })).toBeDisabled()
    expect(screen.getByRole('radio', { name: /No limit for this stage/ })).toBeDisabled()
  })

  test('polling and attention re-renders do not reset the draft or close the dialog', async () => {
    mockApi(() => ({ body: { view: view(), yaml: yaml() } }))
    const { rerender, onClose } = renderDialog()
    await ready()
    await userEvent.click(screen.getByRole('radio', { name: 'Custom limit' }))
    await userEvent.clear(screen.getByLabelText('Tokens'))
    await userEvent.type(screen.getByLabelText('Tokens'), '777k')
    rerender()
    rerender()
    expect(screen.getByLabelText('Tokens')).toHaveValue('777k')
    expect(onClose).not.toHaveBeenCalled()
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  test('Escape closes; focus enters the dialog and returns to the opener', async () => {
    mockApi(() => ({ body: { view: view(), yaml: yaml() } }))
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const { onClose, unmount } = renderDialog({}, { opener })
    await ready()
    await waitFor(() => expect(screen.getByRole('radio', { name: /Use declared limit/ })).toHaveFocus())
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
    unmount()
    expect(opener).toHaveFocus()
    opener.remove()
  })

  test('focus falls back when the opener unmounts right after the dialog closes', async () => {
    mockApi(() => ({ body: { view: view(), yaml: yaml() } }))
    const main = document.createElement('main')
    main.id = 'main'
    main.tabIndex = -1
    const opener = document.createElement('button')
    document.body.append(main, opener)
    opener.focus()
    const { unmount } = renderDialog({}, { opener })
    await ready()
    unmount()
    expect(opener).toHaveFocus()
    opener.remove()
    await waitFor(() => expect(main).toHaveFocus())
    main.remove()
  })

  test('a helper default shows the profile id from the declaration', async () => {
    mockApi(() => ({
      body: {
        view: view({ scope: 'task:t1', kind: 'helper', base_source: 'profile', declaration: { kind: 'stage_assistant', stage_id: 'impl', profile: 'hprof' } }),
        yaml: null,
      },
    }))
    renderDialog({}, { scope: 'task:t1' })
    await ready()
    expect(screen.getByText(/Declared limit: 100k tokens \(Default of profile hprof\)/)).toBeInTheDocument()
  })

  test('uses the helper label from the scope names', async () => {
    mockApi(() => ({ body: { view: view({ scope: 'task:t1', kind: 'helper' }), yaml: null } }))
    renderDialog({ names: new Map([['task:t1', 'Helper Reviewer · impl']]) }, { scope: 'task:t1' })
    await ready()
    expect(screen.getByRole('dialog', { name: 'Budget · Helper Reviewer · impl' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /No limit for this helper/ })).toBeInTheDocument()
  })
})
