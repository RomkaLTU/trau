// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import {
  decideStale,
  entryInProgress,
  loadReloadedFor,
  recordReloadedFor,
  type StaleInput,
} from './reload-on-update'

function input(over: Partial<StaleInput> = {}): StaleInput {
  return {
    pageBuild: 'gaaa111',
    serverBuild: 'gbbb222',
    reloadedFor: '',
    busy: false,
    ...over,
  }
}

describe('decideStale', () => {
  it('reloads when the served stamp differs from the page', () => {
    expect(decideStale(input())).toBe('reload')
  })

  it('does nothing when the stamps agree', () => {
    expect(decideStale(input({ serverBuild: 'gaaa111' }))).toBe('none')
  })

  it('does nothing when the hub answers no stamp at all', () => {
    expect(decideStale(input({ serverBuild: '' }))).toBe('none')
  })

  it('does nothing when the page carries no stamp', () => {
    expect(decideStale(input({ pageBuild: '' }))).toBe('none')
  })

  it('shows the notice when the mismatch survived this window reload', () => {
    expect(decideStale(input({ reloadedFor: 'gbbb222' }))).toBe('notice')
  })

  it('reloads again for a stamp newer than the one already reloaded for', () => {
    expect(
      decideStale(input({ serverBuild: 'gccc333', reloadedFor: 'gbbb222' })),
    ).toBe('reload')
  })

  it('waits while an entry is in progress', () => {
    expect(decideStale(input({ busy: true }))).toBe('none')
  })

  it('still shows the notice while an entry is in progress', () => {
    expect(decideStale(input({ busy: true, reloadedFor: 'gbbb222' }))).toBe(
      'notice',
    )
  })
})

describe('entryInProgress', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  function focus(html: string): HTMLElement {
    document.body.innerHTML = html
    const el = document.body.firstElementChild as HTMLElement
    el.focus()
    return el
  }

  it('is false with nothing focused', () => {
    document.body.innerHTML = '<textarea>an answer</textarea>'
    expect(entryInProgress(document)).toBe(false)
  })

  it('is true for a focused textarea holding text', () => {
    const el = focus('<textarea></textarea>') as HTMLTextAreaElement
    el.value = 'a grill answer half written'
    expect(entryInProgress(document)).toBe(true)
  })

  it('is false for a focused but empty textarea', () => {
    const el = focus('<textarea></textarea>') as HTMLTextAreaElement
    el.value = '   '
    expect(entryInProgress(document)).toBe(false)
  })

  it('is true for a focused text input holding text', () => {
    const el = focus('<input type="text" />') as HTMLInputElement
    el.value = 'a half-typed name'
    expect(entryInProgress(document)).toBe(true)
  })

  it('ignores a focused checkbox, whose value is not an entry', () => {
    focus('<input type="checkbox" />')
    expect(entryInProgress(document)).toBe(false)
  })

  it('is true for a focused contenteditable holding text', () => {
    focus('<div contenteditable="true">a draft</div>')
    expect(entryInProgress(document)).toBe(true)
  })

  it('is false for an empty contenteditable', () => {
    focus('<div contenteditable="true"></div>')
    expect(entryInProgress(document)).toBe(false)
  })

  it('is false without a document', () => {
    expect(entryInProgress(undefined)).toBe(false)
  })
})

describe('the reloaded mark', () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  it('starts empty and remembers the stamp it was spent on', () => {
    expect(loadReloadedFor()).toBe('')
    recordReloadedFor('gbbb222')
    expect(loadReloadedFor()).toBe('gbbb222')
  })
})
