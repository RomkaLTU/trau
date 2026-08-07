import { describe, expect, it } from 'vitest'

import {
  UNMAPPED,
  boardNameError,
  deriveGroupingRows,
  mappingSpec,
  parseBoardStates,
  serializeBoardStates,
  serializeGrouping,
  type BoardStatePair,
  type StatusColumn,
} from '@/lib/statusmap'

// The spec is what tells the two helpers which semantics they are under, so the
// tests name the real provider specs rather than a hand-built stand-in.
const azure = mappingSpec('azure')!
const linear = mappingSpec('linear')!

const columns: StatusColumn[] = [
  { name: 'New', suggestedGroup: 'backlog' },
  { name: 'Ready to Develop', suggestedGroup: 'unstarted' },
  { name: 'Ready to test', suggestedGroup: 'started' },
  { name: 'Done', suggestedGroup: 'done' },
]

describe('parseBoardStates', () => {
  it('reads the pairs the hub writes, whitespace and case included', () => {
    expect(parseBoardStates('New=backlog, Ready to Develop=UNSTARTED ,Done=done')).toEqual([
      { name: 'New', group: 'backlog' },
      { name: 'Ready to Develop', group: 'unstarted' },
      { name: 'Done', group: 'done' },
    ])
  })

  it('drops a pair naming a group trau does not have, keeping the rest', () => {
    expect(parseBoardStates('New=nowhere,Done=done')).toEqual([
      { name: 'Done', group: 'done' },
    ])
  })

  it('splits on the first = and ignores a pair without one', () => {
    expect(parseBoardStates('a=b=done,orphan,,Done=done')).toEqual([
      { name: 'Done', group: 'done' },
    ])
  })
})

describe('serializeBoardStates', () => {
  it('writes only the mapped rows and round-trips through the parser', () => {
    const rows: BoardStatePair[] = [
      { name: 'New', group: 'backlog' },
      { name: 'Parked', group: UNMAPPED },
      { name: 'Done', group: 'done' },
    ]
    expect(serializeBoardStates(rows)).toBe('New=backlog,Done=done')
    expect(parseBoardStates(serializeBoardStates(rows))).toEqual([
      { name: 'New', group: 'backlog' },
      { name: 'Done', group: 'done' },
    ])
  })

  it('serializes an all-unmapped editor to the empty value', () => {
    expect(serializeBoardStates([{ name: 'New', group: UNMAPPED }])).toBe('')
  })
})

describe('boardNameError', () => {
  it('rejects the two characters the grammar spends', () => {
    expect(boardNameError('Ready, set')).toContain(',')
    expect(boardNameError('a=b')).toContain('=')
    expect(boardNameError('  ')).toBe('Enter a column name.')
  })

  it('accepts an ordinary column name', () => {
    expect(boardNameError(' Ready to Develop ')).toBeNull()
  })
})

describe('deriveGroupingRows', () => {
  it('prefills from the suggestions when the repo has written nothing', () => {
    const rows = deriveGroupingRows(columns, '', azure)
    expect(rows.map((r) => r.group)).toEqual([
      'backlog',
      'unstarted',
      'started',
      'done',
    ])
    expect(rows.every((r) => r.onBoard)).toBe(true)
  })

  it('treats a written mapping as exhaustive, so an omitted column reads unmapped', () => {
    const rows = deriveGroupingRows(columns, 'New=backlog,done=canceled', azure)
    expect(rows.map((r) => [r.name, r.group])).toEqual([
      ['New', 'backlog'],
      ['Ready to Develop', UNMAPPED],
      ['Ready to test', UNMAPPED],
      ['Done', 'canceled'],
    ])
    expect(rows[3].suggested).toBe('done')
  })

  it('keeps a mapped name the board no longer carries so saving never drops it', () => {
    const rows = deriveGroupingRows(columns, 'Triage=backlog', azure)
    const extra = rows.find((r) => r.name === 'Triage')
    expect(extra).toEqual({
      name: 'Triage',
      group: 'backlog',
      suggested: UNMAPPED,
      onBoard: false,
    })
  })

  it('falls back to the config value alone when the board could not be read', () => {
    const rows = deriveGroupingRows([], 'New=backlog,Done=done', azure)
    expect(rows.map((r) => r.name)).toEqual(['New', 'Done'])
    expect(rows.every((r) => !r.onBoard)).toBe(true)
  })
})

// The Linear key overlays rather than replaces: a written pair moves that state
// alone and every other row keeps the section its own state type derives, so the
// editor never shows Unknown and saving never writes a row it did not change.
describe('deriveGroupingRows on an overlay key', () => {
  const states: StatusColumn[] = [
    { name: 'Triage', suggestedGroup: 'backlog' },
    { name: 'Todo', suggestedGroup: 'unstarted' },
    { name: 'Ready for QA', suggestedGroup: 'started' },
    { name: 'Done', suggestedGroup: 'done' },
  ]

  it('keeps every unlisted state on its derived section, overriding only what is written', () => {
    const rows = deriveGroupingRows(states, 'Ready for QA=done', linear)
    expect(rows.map((r) => [r.name, r.group])).toEqual([
      ['Triage', 'backlog'],
      ['Todo', 'unstarted'],
      ['Ready for QA', 'done'],
      ['Done', 'done'],
    ])
    expect(rows.some((r) => r.group === UNMAPPED)).toBe(false)
  })

  it('serializes only the rows that differ from their derived section', () => {
    const rows = deriveGroupingRows(states, 'Ready for QA=done', linear)
    expect(serializeGrouping(rows, linear)).toBe('Ready for QA=done')

    const edited = rows.map((r) =>
      r.name === 'Triage' ? { ...r, group: 'unstarted' as const } : r,
    )
    expect(serializeGrouping(edited, linear)).toBe(
      'Triage=unstarted,Ready for QA=done',
    )
  })

  it('writes nothing once every row is put back on its derived section', () => {
    const rows = deriveGroupingRows(states, 'Ready for QA=done', linear)
    const reset = rows.map((r) => ({ ...r, group: r.suggested }))
    expect(serializeGrouping(reset, linear)).toBe('')
  })

  it('still writes every row of an exhaustive key', () => {
    const rows = deriveGroupingRows(states, '', azure)
    expect(serializeGrouping(rows, azure)).toBe(
      'Triage=backlog,Todo=unstarted,Ready for QA=started,Done=done',
    )
  })
})

describe('mappingSpec', () => {
  it('gives each provider with a mapping key its own key and semantics', () => {
    expect(mappingSpec('azure')).toMatchObject({
      key: 'AZURE_BOARD_STATES',
      overlay: false,
    })
    expect(mappingSpec('linear')).toMatchObject({
      key: 'LINEAR_BOARD_STATES',
      overlay: true,
    })
    expect(mappingSpec('jira')).toBeNull()
    expect(mappingSpec('internal')).toBeNull()
  })
})
