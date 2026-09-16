import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { FeedComposer } from './FeedComposer'

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
})
