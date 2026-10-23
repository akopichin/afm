import { useEffect } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { CommentProvider, useComments, type CommentsApi } from '../comments/CommentProvider'
import { CommentRecipientsContext } from '../comments/comment-recipients'
import { SideAgentContext } from '../side-agent/side-agent-context'
import { FileCommentsList } from './FileCommentsList'
import { FilesApiMock } from './test-support'

type Seed = { key: string; root: string; path: string; line: number; quote: string; text: string; saved?: boolean; etag?: string }

// Seed кладёт комментарии в run-scoped хранилище так же, как это делает FileViewer.
function Seeder({ seeds }: { seeds: Seed[] }): null {
  const { open, edit, save, fold } = useComments()
  useEffect(() => {
    for (const s of seeds) {
      open(s.key, {
        anchor: { kind: 'file', root: s.root, path: s.path, displayPath: `${s.root}/${s.path}`, reference: '', etag: s.etag ?? 'W/"1"', line: s.line },
        quote: s.quote,
      })
      edit(s.key, s.text)
      if (s.saved === true) save(s.key)
      fold(s.key)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  return null
}

function renderList(seeds: Seed[], props: Partial<React.ComponentProps<typeof FileCommentsList>> = {}) {
  const onOpen = vi.fn()
  render(
    <CommentProvider runId="r1">
      <Seeder seeds={seeds} />
      <FileCommentsList rootLabel={(id) => (id === 'project' ? 'afm' : id)} onOpen={onOpen} {...props} />
    </CommentProvider>,
  )
  return { onOpen }
}

const A3: Seed = { key: 'k1', root: 'project', path: 'a.go', line: 3, quote: 'three', text: 'first note', saved: true }
const A1: Seed = { key: 'k2', root: 'project', path: 'a.go', line: 1, quote: 'one', text: 'second note', saved: true }
const B2: Seed = { key: 'k3', root: 'docs', path: 'b.md', line: 2, quote: 'two', text: 'third note' }

let files: FilesApiMock

beforeEach(() => {
  files = new FilesApiMock()
  files.setRoots([{ id: 'project', label: 'afm' }, { id: 'docs', label: 'docs' }])
  files.setContent('project', 'a.go', { language: 'go', content: 'one\ntwo\nthree', etag: 'W/"1"' })
  files.setContent('docs', 'b.md', { language: 'md', content: 'x\ntwo', etag: 'W/"1"' })
  files.install()
})
afterEach(() => vi.restoreAllMocks())

const contentCalls = (): string[] =>
  (files.fetchMock?.mock.calls ?? []).map(([url]) => String(url)).filter((url) => url.includes('/api/files/content'))
const rowNamed = (name: RegExp): HTMLElement => screen.getByRole('button', { name })

describe('FileCommentsList', () => {
  test('shows a hint when there are no file comments', () => {
    renderList([])
    expect(screen.getByText(/no file comments/i)).toBeInTheDocument()
  })

  test('rows are buttons grouped by root and file, ordered by line', () => {
    renderList([A3, B2, A1])
    const groups = screen.getAllByRole('group')
    expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['afm / a.go', 'docs / b.md'])
    const rows = within(groups[0] as HTMLElement).getAllByRole('button')
    expect(rows.map((r) => r.textContent)).toEqual([expect.stringContaining('Line 1'), expect.stringContaining('Line 3')])
  })

  test('files with the same label and path in different roots stay separate groups', () => {
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    renderList([A1, { ...A3, root: 'other' }], { rootLabel: () => 'afm' })
    expect(screen.getAllByRole('group', { name: 'afm / a.go' })).toHaveLength(2)
    expect(errors).not.toHaveBeenCalled()
  })

  test('a row shows the full comment text, not a truncated preview', () => {
    const long = `${'word '.repeat(80)}tail`
    renderList([{ ...A1, text: long }])
    expect(screen.getByRole('button', { name: /Line 1/ }).textContent).toContain(long)
  })

  test('only unsaved text carries the Draft badge', () => {
    renderList([A1, B2])
    expect(within(screen.getByRole('button', { name: /second note/ })).queryByText('Draft')).toBeNull()
    expect(within(screen.getByRole('button', { name: /third note/ })).getByText('Draft')).toBeInTheDocument()
  })

  test('clicking a row reports its root, path and line', () => {
    const { onOpen } = renderList([A3, B2])
    fireEvent.click(screen.getByRole('button', { name: /third note/ }))
    expect(onOpen).toHaveBeenCalledWith({ root: 'docs', path: 'b.md', line: 2 })
  })

  test('a row names the agent the comment is addressed to', () => {
    const side = { enabled: true, isOpen: false, open: () => {}, openWith: () => {}, sendExactMessage: () => Promise.resolve(), unseenResult: false, active: false }
    render(
      <SideAgentContext.Provider value={side}>
        <CommentRecipientsContext.Provider value={{ runId: 'r1', participants: [], stages: [] }}>
          <CommentProvider runId="r1">
            <Seeder seeds={[A1]} />
            <FileCommentsList rootLabel={(id) => id} onOpen={vi.fn()} />
          </CommentProvider>
        </CommentRecipientsContext.Provider>
      </SideAgentContext.Provider>,
    )
    expect(within(screen.getByRole('button', { name: /second note/ })).getByText('→ Assistant')).toBeInTheDocument()
  })

  test('a row without any available agent says so', () => {
    renderList([A1])
    expect(within(screen.getByRole('button', { name: /second note/ })).getByText('No agent chosen')).toBeInTheDocument()
  })

  describe('source status', () => {
    test('a changed source shows Source changed for a file that is not open', async () => {
      files.setContent('project', 'a.go', { language: 'go', content: 'one\ntwo\nCHANGED', etag: 'W/"2"' })
      renderList([A3, A1])
      await waitFor(() => expect(within(rowNamed(/first note/)).getByText('Source changed')).toBeInTheDocument())
      expect(within(rowNamed(/second note/)).queryByText('Source changed')).toBeNull()
    })

    test('an unchanged source (304) shows no badge', async () => {
      renderList([A3])
      await waitFor(() => expect(contentCalls()).toHaveLength(1))
      await act(async () => {})
      expect(within(rowNamed(/first note/)).queryByText(/Source/)).toBeNull()
    })

    test('a new ETag with the same line shows no badge', async () => {
      files.setContent('project', 'a.go', { language: 'go', content: 'one\ntwo\nthree\nfour', etag: 'W/"2"' })
      renderList([A3])
      await waitFor(() => expect(contentCalls()).toHaveLength(1))
      await act(async () => {})
      expect(within(rowNamed(/first note/)).queryByText(/Source/)).toBeNull()
    })

    test('a deleted file shows Source missing', async () => {
      renderList([{ ...A3, path: 'gone.go' }])
      await waitFor(() => expect(within(rowNamed(/first note/)).getByText('Source missing')).toBeInTheDocument())
    })

    test('a read error shows no badge', async () => {
      files.setContentError('project', 'a.go', 'internal', 500)
      renderList([A3])
      await waitFor(() => expect(contentCalls()).toHaveLength(1))
      await act(async () => {})
      expect(within(rowNamed(/first note/)).queryByText(/Source/)).toBeNull()
    })

    test('loads each file once, however many comments it has', async () => {
      renderList([A3, A1, B2])
      await waitFor(() => expect(contentCalls()).toHaveLength(2))
      await act(async () => {})
      expect(contentCalls()).toHaveLength(2)
    })

    test('a late response of a previous check does not overwrite the newer one', async () => {
      files.setContent('project', 'a.go', { language: 'go', content: 'one\ntwo\nCHANGED', etag: 'W/"9"' })
      const original = files.fetchMock?.getMockImplementation()
      if (original === undefined) throw new Error('files mock is not installed')
      let release!: () => void
      const gate = new Promise<void>((r) => { release = r })
      let first = true
      files.fetchMock?.mockImplementation(async (input, init) => {
        const response = await original(input, init)
        if (String(input).includes('/api/files/content') && first) {
          first = false
          await gate
        }
        return response
      })
      const out: { api: CommentsApi | null } = { api: null }
      function Probe(): null {
        out.api = useComments()
        return null
      }
      render(
        <CommentProvider runId="r1">
          <Seeder seeds={[A3]} />
          <Probe />
          <FileCommentsList rootLabel={(id) => id} onOpen={vi.fn()} />
        </CommentProvider>,
      )
      await waitFor(() => expect(contentCalls()).toHaveLength(1))

      files.setContent('project', 'a.go', { language: 'go', content: 'one\ntwo\nthree', etag: 'W/"2"' })
      act(() => {
        out.api?.open('k9', { anchor: { kind: 'file', root: 'project', path: 'a.go', displayPath: 'project/a.go', reference: '', etag: 'W/"2"', line: 3 }, quote: 'three' })
        out.api?.edit('k9', 'newer note')
        out.api?.fold('k9')
      })
      await waitFor(() => expect(contentCalls()).toHaveLength(2))
      await act(async () => release())
      expect(screen.queryByText('Source changed')).toBeNull()
    })
  })
})
