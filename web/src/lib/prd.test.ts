import { describe, expect, it } from 'vitest'

import {
  clearDraft,
  draftIsEmpty,
  loadDraft,
  saveDraft,
  type PRDDraft,
} from './prd'

function memStore() {
  const m = new Map<string, string>()
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
    size: () => m.size,
  }
}

describe('PRD draft persistence', () => {
  it('round-trips a saved draft', () => {
    const store = memStore()
    const draft: PRDDraft = { title: 'Payments', markdown: '# Payments\n\nBody.' }
    saveDraft('acme', draft, store)
    expect(loadDraft('acme', store)).toEqual(draft)
  })

  it('preserves markdown byte-for-byte through a round-trip', () => {
    const store = memStore()
    const markdown = '# H1\n\n- a\n- b\n\n```go\nfmt.Println("x")\n```\n'
    saveDraft('acme', { title: 't', markdown }, store)
    expect(loadDraft('acme', store)?.markdown).toBe(markdown)
  })

  it('scopes drafts per repo', () => {
    const store = memStore()
    saveDraft('a', { title: 'A', markdown: 'a' }, store)
    saveDraft('b', { title: 'B', markdown: 'b' }, store)
    expect(loadDraft('a', store)?.title).toBe('A')
    expect(loadDraft('b', store)?.title).toBe('B')
  })

  it('clears a published draft', () => {
    const store = memStore()
    saveDraft('acme', { title: 't', markdown: 'm' }, store)
    clearDraft('acme', store)
    expect(loadDraft('acme', store)).toBeNull()
    expect(store.size()).toBe(0)
  })

  it('returns null for a missing or malformed draft', () => {
    const store = memStore()
    expect(loadDraft('nope', store)).toBeNull()
    store.setItem('trau.prd.draft.acme', '{not json')
    expect(loadDraft('acme', store)).toBeNull()
  })

  it('degrades to a no-op without a store', () => {
    expect(loadDraft('acme', null)).toBeNull()
    expect(() => saveDraft('acme', { title: 't', markdown: 'm' }, null)).not.toThrow()
    expect(() => clearDraft('acme', null)).not.toThrow()
  })

  it('treats a whitespace-only draft as empty', () => {
    expect(draftIsEmpty({ title: '  ', markdown: '\n\t' })).toBe(true)
    expect(draftIsEmpty({ title: 'x', markdown: '' })).toBe(false)
  })
})
