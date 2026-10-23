import { describe, expect, it } from 'vitest'
import {
  acknowledgeSent,
  commentKey,
  discardComment,
  editComment,
  emptyCommentState,
  foldComment,
  foldFileComments,
  isDraft,
  listComments,
  openComment,
  reanchorComment,
  rememberRecipient,
  saveAndFoldComment,
  saveComment,
  setCommentRecipient,
  type CommentAnchor,
  type CommentState,
} from './comment-state'

const plan = (line: number, version = 'v1'): Extract<CommentAnchor, { kind: 'plan' }> => ({ kind: 'plan', stageId: 's1', document: 'plan', version, line })
const question = (questionId: string, line: number | null = null): CommentAnchor => ({
  kind: 'question', stageId: 's1', phase: 'plan', questionId, line,
})
const file = (line: number): CommentAnchor => ({
  kind: 'file', root: 'project', path: 'a.go', displayPath: 'project/a.go', reference: '[AFM file: "/w/a.go"]', etag: 'e1', line,
})

function typed(state: CommentState, key: string, anchor: CommentAnchor, text: string): CommentState {
  return editComment(openComment(state, key, { anchor, quote: 'src' }), key, text)
}

describe('comment-state', () => {
  it('keeps plan, question and two file-line drafts at the same time', () => {
    let s = emptyCommentState(0)
    s = typed(s, commentKey(plan(3)), plan(3), 'plan note')
    s = typed(s, commentKey(question('q1')), question('q1'), 'free answer')
    s = typed(s, 'f1', file(10), 'line ten')
    s = typed(s, 'f2', file(11), 'line eleven')

    expect(listComments(s).map((c) => c.text)).toEqual(['plan note', 'free answer', 'line ten', 'line eleven'])
  })

  it('tuple keys separate plan versions, stages and question ids', () => {
    expect(commentKey(plan(3, 'v1'))).not.toBe(commentKey(plan(3, 'v2')))
    expect(commentKey(plan(3))).not.toBe(commentKey({ ...plan(3), stageId: 's2' }))
    expect(commentKey(question('q1'))).not.toBe(commentKey(question('q2')))
    expect(commentKey(question('q1', 2))).not.toBe(commentKey(question('q1')))
  })

  it('stores every keystroke and marks it draft until Save', () => {
    let s = typed(emptyCommentState(0), 'f1', file(1), 'a')
    expect(isDraft(listComments(s)[0]!)).toBe(true)

    s = saveComment(s, 'f1')
    expect(isDraft(listComments(s)[0]!)).toBe(false)

    s = editComment(s, 'f1', 'ab')
    const [entry] = listComments(s)
    expect(entry).toMatchObject({ text: 'ab', saved: 'a' })
    expect(isDraft(entry!)).toBe(true)
  })

  it('changing the recipient keeps the text and becomes the default for new comments', () => {
    let s = typed(emptyCommentState(0), 'f1', file(1), 'text')
    s = setCommentRecipient(s, 'f1', 'agent:x')

    expect(listComments(s)[0]).toMatchObject({ text: 'text', recipientId: 'agent:x' })
    s = openComment(s, 'f2', { anchor: file(2), quote: '' })
    expect(s.entries.get('f2')?.recipientId).toBe('agent:x')
  })

  it('rememberRecipient changes only the default for new comments', () => {
    let s = typed(emptyCommentState(0), 'f1', file(1), 'text')
    const before = s.entries.get('f1')

    s = rememberRecipient(s, 'agent:y')

    expect(s.lastRecipientId).toBe('agent:y')
    expect(s.entries.get('f1')).toBe(before)
    expect(s.revisionClock).toBe(before?.revision)
    expect(openComment(s, 'f2', { anchor: file(2), quote: '' }).entries.get('f2')?.recipientId).toBe('agent:y')
  })

  it('fold keeps non-empty text but drops an empty form; Discard removes explicitly', () => {
    let s = typed(emptyCommentState(0), 'f1', file(1), 'keep me')
    s = openComment(s, 'f2', { anchor: file(2), quote: '' })

    s = foldComment(foldComment(s, 'f1'), 'f2')
    expect(s.entries.get('f1')).toMatchObject({ text: 'keep me', editorOpen: false })
    expect(s.entries.has('f2')).toBe(false)

    s = discardComment(s, 'f1')
    expect(listComments(s)).toEqual([])
  })

  it('fold of a blank text restores the saved comment instead of deleting it', () => {
    let s = typed(saveComment(typed(emptyCommentState(0), 'f1', file(1), 'old'), 'f1'), 'f1', file(1), '')
    s = typed(s, 'f2', file(2), 'two')
    s = typed(saveComment(s, 'f2'), 'f2', file(2), '  \n ')

    s = foldComment(foldComment(s, 'f1'), 'f2')

    expect(s.entries.get('f1')).toMatchObject({ text: 'old', saved: 'old', editorOpen: false })
    expect(s.entries.get('f2')).toMatchObject({ text: 'two', saved: 'two', editorOpen: false })
    expect(isDraft(s.entries.get('f1')!)).toBe(false)
  })

  it('fold drops a whitespace-only text when nothing was saved', () => {
    const s = foldComment(typed(emptyCommentState(0), 'f1', file(1), '   '), 'f1')

    expect(listComments(s)).toEqual([])
  })

  it('saveAndFold saves non-blank text and closes the editor; a blank text on a new comment is discarded', () => {
    let s = typed(emptyCommentState(0), 'f1', file(1), 'note')
    s = typed(s, 'f2', file(2), ' \n ')

    s = saveAndFoldComment(saveAndFoldComment(s, 'f1'), 'f2')

    expect(s.entries.get('f1')).toMatchObject({ text: 'note', saved: 'note', editorOpen: false })
    expect(s.entries.has('f2')).toBe(false)
  })

  it('an empty Save on a saved comment is a no-op that keeps the saved text; deleting stays explicit', () => {
    let s = typed(saveComment(typed(emptyCommentState(0), 'f1', file(1), 'old'), 'f1'), 'f1', file(1), '')

    s = saveAndFoldComment(s, 'f1')

    expect(s.entries.get('f1')).toMatchObject({ text: 'old', saved: 'old', editorOpen: false })
  })

  it('reopening a folded comment restores the editor with the same text', () => {
    let s = foldComment(typed(emptyCommentState(0), 'f1', file(1), 'text'), 'f1')
    s = openComment(s, 'f1', { anchor: file(1), quote: 'ignored' })
    expect(s.entries.get('f1')).toMatchObject({ text: 'text', editorOpen: true, quote: 'src' })
  })

  it('does not mutate the previous state', () => {
    const before = typed(emptyCommentState(0), 'f1', file(1), 'a')
    const snapshot = JSON.stringify(listComments(before))
    editComment(before, 'f1', 'changed')
    discardComment(before, 'f1')
    expect(JSON.stringify(listComments(before))).toBe(snapshot)
  })

  it('acknowledges only captured revisions and keeps text edited after capture', () => {
    let s = typed(emptyCommentState(0), 'f1', file(1), 'one')
    s = typed(s, 'f2', file(2), 'two')
    const sent = listComments(s).map((c) => ({ key: c.key, revision: c.revision }))

    s = editComment(s, 'f2', 'two plus more')
    s = acknowledgeSent(s, 0, sent)

    expect(listComments(s).map((c) => [c.key, c.text])).toEqual([['f2', 'two plus more']])
  })

  it('ignores an acknowledgement that belongs to another run epoch', () => {
    const s = typed(emptyCommentState(1), 'f1', file(1), 'new run text')
    const sent = [{ key: 'f1', revision: listComments(s)[0]!.revision }]

    expect(acknowledgeSent(s, 0, sent)).toBe(s)
  })
  it('a comment deleted and recreated on the same key does not match the old captured revision', () => {
    let s = typed(emptyCommentState(0), 'f1', file(1), 'first')
    const sent = listComments(s).map((c) => ({ key: c.key, revision: c.revision }))

    s = discardComment(s, 'f1')
    s = typed(s, 'f1', file(1), 'recreated')
    s = acknowledgeSent(s, 0, sent)

    expect(listComments(s).map((c) => c.text)).toEqual(['recreated'])
  })

  it('the same holds for a plan/question key recreated after Discard', () => {
    const key = commentKey(question('q1'))
    let s = typed(emptyCommentState(0), key, question('q1'), 'answer')
    const sent = [{ key, revision: s.entries.get(key)!.revision }]

    s = typed(discardComment(s, key), key, question('q1'), 'new answer')

    expect(listComments(acknowledgeSent(s, 0, sent)).map((c) => c.text)).toEqual(['new answer'])
  })

  it('foldFileComments folds only open editors of that file and drops empty ones', () => {
    const other: CommentAnchor = { ...(file(1) as Extract<CommentAnchor, { kind: 'file' }>), path: 'b.go' }
    let s = typed(emptyCommentState(0), 'f1', file(1), 'unsaved')
    s = openComment(s, 'f2', { anchor: file(2), quote: '' })
    s = typed(s, 'f3', other, 'on b')

    s = foldFileComments(s, 'project', 'a.go')

    expect(s.entries.get('f1')).toMatchObject({ editorOpen: false, text: 'unsaved', saved: '' })
    expect(s.entries.has('f2')).toBe(false)
    expect(s.entries.get('f3')?.editorOpen).toBe(true)
  })

  describe('reanchorComment', () => {
    it('moves a file comment to another line keeping its id, text and recipient', () => {
      let s = typed(emptyCommentState(0), 'f1', file(1), 'note')
      s = setCommentRecipient(s, 'f1', 'agent:a')

      s = reanchorComment(s, 'f1', { line: 7, quote: 'moved src', etag: 'e2' })

      const entry = s.entries.get('f1')!
      expect(entry.anchor).toMatchObject({ kind: 'file', line: 7, etag: 'e2', path: 'a.go' })
      expect([entry.key, entry.text, entry.quote, entry.recipientId]).toEqual(['f1', 'note', 'moved src', 'agent:a'])
    })

    it('bumps the revision so an acknowledgement of the pre-move text does not delete it', () => {
      let s = typed(emptyCommentState(0), 'f1', file(1), 'note')
      const sent = [{ key: 'f1', revision: s.entries.get('f1')!.revision }]

      s = reanchorComment(s, 'f1', { line: 2, quote: 'q', etag: 'e2' })

      expect(listComments(acknowledgeSent(s, 0, sent))).toHaveLength(1)
    })

    it('refuses to land on a line that already has a comment in the same file', () => {
      let s = typed(emptyCommentState(0), 'f1', file(1), 'one')
      s = typed(s, 'f2', file(2), 'two')

      expect(reanchorComment(s, 'f1', { line: 2, quote: 'q', etag: 'e2' })).toBe(s)
    })

    it('ignores plan/question comments and unknown keys', () => {
      const key = commentKey(plan(3))
      const s = typed(emptyCommentState(0), key, plan(3), 'plan note')

      expect(reanchorComment(s, key, { line: 4, quote: 'q', etag: 'e' })).toBe(s)
      expect(reanchorComment(s, 'missing', { line: 4, quote: 'q', etag: 'e' })).toBe(s)
    })
  })
})
