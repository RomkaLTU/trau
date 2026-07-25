import { describe, expect, it } from 'vitest'

import {
  describeCreated,
  describeCreatedFailures,
  type CreatedIssue,
} from './created-banner'

function created(over: Partial<CreatedIssue> = {}): CreatedIssue {
  return {
    repo: 'loop',
    id: 'COD-1234',
    title: 'Team sync',
    subCount: 0,
    failedSteps: [],
    ...over,
  }
}

describe('describeCreated', () => {
  it('names a single filed issue', () => {
    expect(describeCreated(created())).toBe('Created COD-1234 "Team sync"')
  })

  it('names an epic with its submitted sub-issue count', () => {
    expect(describeCreated(created({ subCount: 3 }))).toBe(
      'Created epic COD-1234 "Team sync" with 3 sub-issues',
    )
  })

  it('keeps the count singular for a lone sub-issue', () => {
    expect(describeCreated(created({ subCount: 1 }))).toBe(
      'Created epic COD-1234 "Team sync" with 1 sub-issue',
    )
  })
})

describe('describeCreatedFailures', () => {
  it('says nothing when every step landed', () => {
    expect(describeCreatedFailures([])).toBe('')
  })

  it('lists the steps that did not land', () => {
    expect(
      describeCreatedFailures(['sub-issue: Wire the picker', 'assign: COD-1235']),
    ).toBe('2 steps failed: sub-issue: Wire the picker, assign: COD-1235')
  })

  it('keeps a lone failure singular', () => {
    expect(describeCreatedFailures(['assign: COD-1235'])).toBe(
      '1 step failed: assign: COD-1235',
    )
  })
})
