import { describe, expect, it } from 'vitest'

import { attachedEntry, draftFor, groupQAAccounts } from './qa'
import type { QAAccount } from './qa'
import type { AppURL } from './app-urls'

function account(over: Partial<QAAccount> = {}): QAAccount {
  return {
    id: 1,
    label: 'admin',
    username: 'qa-admin@example.test',
    description: 'covers the dashboard and user management flows',
    source: 'manual',
    secret_set: true,
    app_url_id: null,
    ...over,
  }
}

function entry(over: Partial<AppURL> = {}): AppURL {
  return {
    id: 10,
    label: 'storefront',
    url: 'http://localhost:3000',
    workspace: '',
    ...over,
  }
}

describe('draftFor', () => {
  it('seeds an empty unattached draft for a new account', () => {
    expect(draftFor(null)).toEqual({
      label: '',
      username: '',
      secret: '',
      description: '',
      app_url_id: null,
    })
  })

  it('seeds from the account but never the secret', () => {
    const draft = draftFor(account({ app_url_id: 10 }))
    expect(draft.label).toBe('admin')
    expect(draft.username).toBe('qa-admin@example.test')
    expect(draft.description).toBe(
      'covers the dashboard and user management flows',
    )
    expect(draft.app_url_id).toBe(10)
    expect(draft.secret).toBe('')
  })

  it('leaves provenance out of the draft, so an edit cannot change it', () => {
    expect(draftFor(account({ source: 'agent' }))).toEqual({
      label: 'admin',
      username: 'qa-admin@example.test',
      secret: '',
      description: 'covers the dashboard and user management flows',
      app_url_id: null,
    })
  })
})

describe('attachedEntry', () => {
  it('resolves a stored attachment, and reads a deleted one as unattached', () => {
    expect(attachedEntry([entry()], 10)).toEqual(entry())
    expect(attachedEntry([entry()], 99)).toBeNull()
    expect(attachedEntry([entry()], null)).toBeNull()
  })
})

describe('groupQAAccounts', () => {
  it('buckets accounts under their entry, unattached last', () => {
    const storefront = entry()
    const admin = entry({ id: 11, label: 'admin', workspace: 'admin' })
    const groups = groupQAAccounts(
      [
        account({ id: 1, label: 'shopper', app_url_id: 10 }),
        account({ id: 2, label: 'staff', app_url_id: 11 }),
        account({ id: 3, label: 'anyone' }),
      ],
      [storefront, admin],
    )

    expect(groups.map((g) => g.entry?.id ?? null)).toEqual([10, 11, null])
    expect(groups.map((g) => g.accounts.map((a) => a.label))).toEqual([
      ['shopper'],
      ['staff'],
      ['anyone'],
    ])
  })

  it('keeps an entry with no accounts', () => {
    const groups = groupQAAccounts([], [entry()])
    expect(groups).toHaveLength(2)
    expect(groups[0].accounts).toEqual([])
  })

  it('reads an attachment the repo no longer holds as unattached', () => {
    const groups = groupQAAccounts(
      [account({ label: 'orphan', app_url_id: 99 })],
      [entry()],
    )
    expect(groups[0].accounts).toEqual([])
    expect(groups[1].entry).toBeNull()
    expect(groups[1].accounts.map((a) => a.label)).toEqual(['orphan'])
  })

  it('puts every account in the any-URL group when the repo stores no entries', () => {
    const groups = groupQAAccounts([account({ label: 'admin' })], [])
    expect(groups).toHaveLength(1)
    expect(groups[0].entry).toBeNull()
    expect(groups[0].accounts.map((a) => a.label)).toEqual(['admin'])
  })
})
