import { describe, expect, it } from 'vitest'

import { draftFor, matchesQAAccount } from './qa'
import type { QAAccount } from './qa'

function account(over: Partial<QAAccount> = {}): QAAccount {
  return {
    id: 1,
    label: 'admin',
    username: 'qa-admin@example.test',
    description: 'covers the dashboard and user management flows',
    source: 'manual',
    secret_set: true,
    ...over,
  }
}

describe('draftFor', () => {
  it('seeds an empty draft for a new account', () => {
    expect(draftFor(null)).toEqual({
      label: '',
      username: '',
      secret: '',
      description: '',
    })
  })

  it('seeds from the account but never the secret', () => {
    const draft = draftFor(account())
    expect(draft.label).toBe('admin')
    expect(draft.username).toBe('qa-admin@example.test')
    expect(draft.description).toBe(
      'covers the dashboard and user management flows',
    )
    expect(draft.secret).toBe('')
  })

  it('leaves provenance out of the draft, so an edit cannot change it', () => {
    expect(draftFor(account({ source: 'agent' }))).toEqual({
      label: 'admin',
      username: 'qa-admin@example.test',
      secret: '',
      description: 'covers the dashboard and user management flows',
    })
  })
})

describe('matchesQAAccount', () => {
  it('matches everything on an empty query', () => {
    expect(matchesQAAccount(account(), '')).toBe(true)
  })

  it('matches label, username, and description case-insensitively', () => {
    expect(matchesQAAccount(account(), 'ADMIN')).toBe(true)
    expect(matchesQAAccount(account(), 'example.test')).toBe(true)
    expect(matchesQAAccount(account(), 'dashboard')).toBe(true)
  })

  it('rejects a query no field contains', () => {
    expect(matchesQAAccount(account(), 'billing')).toBe(false)
  })
})
