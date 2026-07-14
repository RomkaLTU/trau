import { describe, expect, it } from 'vitest'

import { loopTitle, runTitle, standardTitle } from '@/lib/page-title'

describe('standardTitle', () => {
  it('puts the page first, then the app suffix', () => {
    expect(standardTitle('Overview')).toBe('Overview · trau')
    expect(standardTitle('Run once')).toBe('Run once · trau')
  })
})

describe('runTitle', () => {
  it('shows ticket and pill state with no glyph while a phase runs', () => {
    expect(runTitle('COD-786', 'build')).toBe('COD-786 · build · trau')
    expect(runTitle('COD-786', 'verify')).toBe('COD-786 · verify · trau')
    expect(runTitle('COD-786', 'queued')).toBe('COD-786 · queued · trau')
  })

  it('prefixes the settled and attention glyphs', () => {
    expect(runTitle('COD-786', 'merged')).toBe('✓ COD-786 · merged · trau')
    expect(runTitle('COD-786', 'paused')).toBe('⏸ COD-786 · paused · trau')
    expect(runTitle('COD-786', 'fault')).toBe('⚠ COD-786 · fault · trau')
    expect(runTitle('COD-786', 'quarantined')).toBe('⚠ COD-786 · quarantined · trau')
  })

  it('drops the state segment when there is no pill yet', () => {
    expect(runTitle('COD-786', '')).toBe('COD-786 · trau')
  })
})

describe('loopTitle', () => {
  it('is the bare page name when idle', () => {
    expect(loopTitle({ kind: 'idle' })).toBe('Loop · trau')
  })

  it('shows drain progress, running ticket, and phase', () => {
    expect(
      loopTitle({ kind: 'draining', done: 2, total: 5, ticket: 'COD-786', step: 'build' }),
    ).toBe('2/5 COD-786 · build · trau')
  })

  it('drops the ticket when nothing is running yet', () => {
    expect(
      loopTitle({ kind: 'draining', done: 0, total: 3, ticket: '', step: 'draining' }),
    ).toBe('0/3 · draining · trau')
  })

  it('marks a pause with ⏸ and other halts with ⚠', () => {
    expect(loopTitle({ kind: 'halted', halt: 'paused', ticket: 'COD-1' })).toBe(
      '⏸ COD-1 · paused · trau',
    )
    expect(loopTitle({ kind: 'halted', halt: 'fault', ticket: 'COD-2' })).toBe(
      '⚠ COD-2 · fault · trau',
    )
    expect(loopTitle({ kind: 'halted', halt: 'quarantined', ticket: 'COD-3' })).toBe(
      '⚠ COD-3 · quarantined · trau',
    )
    expect(loopTitle({ kind: 'halted', halt: 'budget', ticket: '' })).toBe(
      '⚠ queue · budget · trau',
    )
  })

  it('marks a clean drain with ✓', () => {
    expect(loopTitle({ kind: 'done', total: 5 })).toBe('✓ 5 done · trau')
  })
})
