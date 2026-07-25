// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'

import { copyText } from './clipboard'

type ClipboardStub = { writeText: (text: string) => Promise<void> }

function setClipboard(clipboard: ClipboardStub | undefined) {
  Object.defineProperty(navigator, 'clipboard', {
    value: clipboard,
    configurable: true,
  })
}

function captureExecCommand(result: boolean): string[] {
  const copied: string[] = []
  document.execCommand = vi.fn(() => {
    const area = document.querySelector('textarea')
    if (area) copied.push(area.value)
    return result
  })
  return copied
}

afterEach(() => {
  setClipboard(undefined)
  document.body.innerHTML = ''
})

describe('copyText', () => {
  it('writes through the async clipboard API when it is available', async () => {
    const writeText = vi.fn(async () => {})
    setClipboard({ writeText })
    const copied = captureExecCommand(true)

    await expect(copyText('http://127.0.0.1:8728/api/v1/mcp')).resolves.toBe(
      true,
    )
    expect(writeText).toHaveBeenCalledWith('http://127.0.0.1:8728/api/v1/mcp')
    expect(copied).toEqual([])
  })

  it('falls back to a selection copy outside a secure context', async () => {
    setClipboard(undefined)
    const copied = captureExecCommand(true)

    await expect(copyText('claude mcp add --transport http trau')).resolves.toBe(
      true,
    )
    expect(copied).toEqual(['claude mcp add --transport http trau'])
  })

  it('falls back when the clipboard write is refused', async () => {
    setClipboard({
      writeText: vi.fn(async () => {
        throw new Error('permission denied')
      }),
    })
    const copied = captureExecCommand(true)

    await expect(copyText('[mcp_servers.trau]')).resolves.toBe(true)
    expect(copied).toEqual(['[mcp_servers.trau]'])
  })

  it('reports failure rather than rejecting when the fallback cannot copy', async () => {
    setClipboard(undefined)
    document.execCommand = vi.fn(() => {
      throw new Error('unsupported')
    })

    await expect(copyText('snippet')).resolves.toBe(false)
  })

  it('leaves no stray textarea behind and restores focus', async () => {
    setClipboard(undefined)
    captureExecCommand(true)
    const button = document.createElement('button')
    document.body.append(button)
    button.focus()

    await copyText('snippet')

    expect(document.querySelectorAll('textarea')).toHaveLength(0)
    expect(document.activeElement).toBe(button)
  })
})
