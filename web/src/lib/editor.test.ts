import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { editorUrl, loadEditor, storeEditor, type EditorID } from './editor'

const ROOT = '/Users/rd/Projects/loop'

describe('editorUrl', () => {
  it('builds a folder link for every editor', () => {
    const urls: Record<EditorID, string> = {
      vscode: 'vscode://file/Users/rd/Projects/loop',
      cursor: 'cursor://file/Users/rd/Projects/loop',
      windsurf: 'windsurf://file/Users/rd/Projects/loop',
      zed: 'zed://file/Users/rd/Projects/loop',
      jetbrains: 'idea://open?file=%2FUsers%2Frd%2FProjects%2Floop',
    }
    for (const [editor, url] of Object.entries(urls)) {
      expect(editorUrl(editor as EditorID, ROOT)).toBe(url)
    }
  })

  it('appends the line for every editor', () => {
    const urls: Record<EditorID, string> = {
      vscode: 'vscode://file/Users/rd/Projects/loop/main.go:42',
      cursor: 'cursor://file/Users/rd/Projects/loop/main.go:42',
      windsurf: 'windsurf://file/Users/rd/Projects/loop/main.go:42',
      zed: 'zed://file/Users/rd/Projects/loop/main.go:42',
      jetbrains:
        'idea://open?file=%2FUsers%2Frd%2FProjects%2Floop%2Fmain.go&line=42',
    }
    for (const [editor, url] of Object.entries(urls)) {
      expect(editorUrl(editor as EditorID, `${ROOT}/main.go`, 42)).toBe(url)
    }
  })

  it('encodes spaces without touching the separators', () => {
    expect(editorUrl('vscode', '/Users/rd/My Projects/a file.go', 7)).toBe(
      'vscode://file/Users/rd/My%20Projects/a%20file.go:7',
    )
    expect(editorUrl('jetbrains', '/Users/rd/My Projects/a file.go')).toBe(
      'idea://open?file=%2FUsers%2Frd%2FMy%20Projects%2Fa%20file.go',
    )
  })

  it('encodes unicode and reserved characters', () => {
    expect(editorUrl('zed', '/tmp/naïve/rün#1.go')).toBe(
      'zed://file/tmp/na%C3%AFve/r%C3%BCn%231.go',
    )
  })
})

describe('loadEditor', () => {
  beforeEach(() => {
    const items = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => items.get(key) ?? null,
      setItem: (key: string, value: string) => void items.set(key, value),
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('defaults to VS Code and reads back what was stored', () => {
    expect(loadEditor()).toBe('vscode')
    storeEditor('zed')
    expect(loadEditor()).toBe('zed')
  })

  it('falls back to the default when storage holds an unknown editor', () => {
    localStorage.setItem('trau.editor', 'emacs')
    expect(loadEditor()).toBe('vscode')
  })
})
