import { describe, expect, it } from 'vitest'

import { matchesTeamSync } from './teamsync'

describe('matchesTeamSync', () => {
  it('matches the panel name and its own controls', () => {
    expect(matchesTeamSync('team sync')).toBe(true)
    expect(matchesTeamSync('Sync Now')).toBe(true)
    expect(matchesTeamSync('teammates')).toBe(true)
    expect(matchesTeamSync('verify')).toBe(false)
  })

  it('matches everything on an empty query', () => {
    expect(matchesTeamSync('')).toBe(true)
  })
})
