import { describe, expect, it } from 'vitest'

import {
  UNMAPPED,
  boardNameError,
  deriveGroupingRows,
  parseBoardStates,
  serializeBoardStates,
  type BoardStatePair,
  type StatusColumn,
} from '@/lib/statusmap'

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
    const rows = deriveGroupingRows(columns, '')
    expect(rows.map((r) => r.group)).toEqual([
      'backlog',
      'unstarted',
      'started',
      'done',
    ])
    expect(rows.every((r) => r.onBoard)).toBe(true)
  })

  it('treats a written mapping as exhaustive, so an omitted column reads unmapped', () => {
    const rows = deriveGroupingRows(columns, 'New=backlog,done=canceled')
    expect(rows.map((r) => [r.name, r.group])).toEqual([
      ['New', 'backlog'],
      ['Ready to Develop', UNMAPPED],
      ['Ready to test', UNMAPPED],
      ['Done', 'canceled'],
    ])
    expect(rows[3].suggested).toBe('done')
  })

  it('keeps a mapped name the board no longer carries so saving never drops it', () => {
    const rows = deriveGroupingRows(columns, 'Triage=backlog')
    const extra = rows.find((r) => r.name === 'Triage')
    expect(extra).toEqual({
      name: 'Triage',
      group: 'backlog',
      suggested: UNMAPPED,
      onBoard: false,
    })
  })

  it('falls back to the config value alone when the board could not be read', () => {
    const rows = deriveGroupingRows([], 'New=backlog,Done=done')
    expect(rows.map((r) => r.name)).toEqual(['New', 'Done'])
    expect(rows.every((r) => !r.onBoard)).toBe(true)
  })
})
