import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { FeedComposer, buildReplyBody } from './FeedComposer'

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

  describe('buildReplyBody', () => {
    it('без цитаты (null) — просто триммированный комментарий', () => {
      expect(buildReplyBody(null, '  привет  ')).toBe('привет')
    })

    it('однострочная цитата — строка с `> ` + пустая строка + комментарий', () => {
      expect(buildReplyBody('one line', 'comment')).toBe('> one line\n\ncomment')
    })

    it('многострочная цитата — каждая строка с префиксом `> `', () => {
      expect(buildReplyBody('a\nb', 'c')).toBe('> a\n> b\n\nc')
    })

    it('CRLF нормализуется в LF (без `\\r` в результате)', () => {
      const body = buildReplyBody('a\r\nb', 'c')
      expect(body).toBe('> a\n> b\n\nc')
      expect(body).not.toContain('\r')
    })

    it('вложенная цитата `> x` становится `> > x`', () => {
      expect(buildReplyBody('> x', 'c')).toBe('> > x\n\nc')
    })

    it('fenced code block сохраняется (каждая строка префиксится)', () => {
      expect(buildReplyBody('```\ncode\n```', 'c')).toBe('> ```\n> code\n> ```\n\nc')
    })
  })

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
})
