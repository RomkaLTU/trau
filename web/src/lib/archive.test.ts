import { describe, expect, it } from 'vitest'

import { archiveToastMessage } from './archive'

describe('archiveToastMessage', () => {
  it('names the archived issue', () => {
    expect(archiveToastMessage('COD-123', true, 0)).toBe('Archived COD-123')
  })

  it('reports pruned queued items, pluralized', () => {
    expect(archiveToastMessage('COD-123', true, 2)).toBe(
      'Archived COD-123 — removed 2 queued items',
    )
    expect(archiveToastMessage('COD-123', true, 1)).toBe(
      'Archived COD-123 — removed 1 queued item',
    )
  })

  it('names the restored issue on an unarchive and never mentions the queue', () => {
    expect(archiveToastMessage('COD-123', false, 0)).toBe('Unarchived COD-123')
  })
})
