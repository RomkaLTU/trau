import { describe, expect, it } from 'vitest'

import { degradedNotice } from './repo-health-gate'

describe('degradedNotice', () => {
  it('says the tracker is rate-limiting rather than blaming the setup', () => {
    const notice = degradedNotice('loop', 'rate-limit')

    expect(notice.headline).toContain('rate-limiting')
    expect(notice.hint).toContain('retries automatically')
    expect(notice.fixable).toBe(false)
  })

  it('offers no configuration fix for a tracker that is merely unreachable', () => {
    const notice = degradedNotice('loop', 'transient')

    expect(notice.hint).toContain('retries automatically')
    expect(notice.fixable).toBe(false)
  })

  it('points at the configuration when that is what is broken', () => {
    const notice = degradedNotice('loop', 'config')

    expect(notice.headline).toContain('loop')
    expect(notice.hint).toContain('credentials')
    expect(notice.fixable).toBe(true)
  })

  it('treats an unclassified failure as one a human has to look at', () => {
    expect(degradedNotice('loop', '').fixable).toBe(true)
  })
})
