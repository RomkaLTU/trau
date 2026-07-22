import { describe, expect, it } from 'vitest'

import { deleteToastMessage, deleteWarning } from './delete-issue'

describe('deleteWarning', () => {
  it('warns about permanence on an internal leaf and nothing more', () => {
    expect(
      deleteWarning({ id: 'COD-936', source: 'internal', children: 0 }),
    ).toBe(
      'Deletes the conversation and every local record — messages, attachments, notifications, queue entries.',
    )
  })

  it('adds the never-resync warning for a synced ticket', () => {
    expect(
      deleteWarning({ id: 'COD-936', source: 'linear', children: 0 }),
    ).toContain(
      'It will never sync into this repo again. The issue on the tracker is not touched.',
    )
  })

  it('counts the children an epic takes with it, pluralized', () => {
    expect(
      deleteWarning({ id: 'COD-936', source: 'internal', children: 3 }),
    ).toContain('Also deletes its 3 children.')
    expect(
      deleteWarning({ id: 'COD-936', source: 'internal', children: 1 }),
    ).toContain('Also deletes its 1 child.')
  })

  it('never mentions children on a leaf', () => {
    expect(
      deleteWarning({ id: 'COD-936', source: 'linear', children: 0 }),
    ).not.toContain('children')
  })
})

describe('deleteToastMessage', () => {
  it('names the deleted ticket', () => {
    expect(deleteToastMessage('COD-936', ['COD-936'])).toBe('COD-936 deleted')
  })

  it('reports the children that went with an epic, pluralized', () => {
    expect(
      deleteToastMessage('COD-936', ['COD-936', 'COD-937', 'COD-938']),
    ).toBe('COD-936 and 2 children deleted')
    expect(deleteToastMessage('COD-936', ['COD-936', 'COD-937'])).toBe(
      'COD-936 and 1 child deleted',
    )
  })
})
