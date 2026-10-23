import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyToClipboard } from './clipboard'

describe('copyToClipboard', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('reports success after the text was written', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    await expect(copyToClipboard('abc')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('abc')
  })

  it('reports failure when the Clipboard API is missing (insecure context)', async () => {
    vi.stubGlobal('navigator', {})

    await expect(copyToClipboard('abc')).resolves.toBe(false)
  })

  it('reports failure instead of throwing when the write is rejected', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })

    await expect(copyToClipboard('abc')).resolves.toBe(false)
  })
})
