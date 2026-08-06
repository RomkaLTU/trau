import { describe, expect, it } from 'vitest'

import {
  PIPELINE_STEPS,
  SKIP_KEYS,
  canonicalSkips,
  skipLabel,
  toggleSkip,
} from '@/lib/skips'
import { checkpointSteps, withSkips } from '@/lib/steps'

describe('canonicalSkips', () => {
  it('orders and dedupes a named set', () => {
    expect(canonicalSkips(['merge', 'verify', 'merge'])).toEqual([
      'verify',
      'merge',
    ])
  })

  it('returns nothing for an empty set', () => {
    expect(canonicalSkips([])).toEqual([])
  })
})

describe('skipLabel', () => {
  it('names the set a queue row carries in canonical order', () => {
    expect(skipLabel(['ci', 'verify'])).toBe('skips: verify, ci')
  })

  it('is empty for an item that skips nothing', () => {
    expect(skipLabel([])).toBe('')
    expect(skipLabel(undefined)).toBe('')
  })
})

describe('toggleSkip', () => {
  it('adds the key when the Activity is unticked and drops it when re-ticked', () => {
    const off = toggleSkip([], 'verify', false)
    expect(off).toEqual(['verify'])
    expect(toggleSkip(off, 'verify', true)).toEqual([])
  })

  it('keeps the canonical order however the Activities are unticked', () => {
    const picked = toggleSkip(toggleSkip([], 'merge', false), 'lintfix', false)
    expect(picked).toEqual(['lintfix', 'merge'])
  })
})

describe('PIPELINE_STEPS', () => {
  it('offers exactly the five canonical keys as toggles', () => {
    const keys = PIPELINE_STEPS.flatMap((step) =>
      step.activities.map((a) => a.skip).filter((k): k is string => !!k),
    )
    expect(keys).toEqual(SKIP_KEYS)
  })

  it('locks build, commit and PR on', () => {
    const locked = PIPELINE_STEPS.flatMap((step) =>
      step.activities.filter((a) => !a.skip).map((a) => a.label),
    )
    expect(locked).toEqual(['Build', 'Commit', 'PR'])
  })
})

describe('withSkips', () => {
  it('marks the Verify Step skipped rather than complete', () => {
    const steps = withSkips(checkpointSteps('verified'), ['verify'])
    expect(steps.map((s) => s.state)).toEqual(['done', 'skipped', 'active'])
  })

  it('marks Verify skipped before the run reaches it', () => {
    const steps = withSkips(checkpointSteps('building'), ['verify'])
    expect(steps.map((s) => s.state)).toEqual(['active', 'skipped', 'todo'])
  })

  it('leaves Build and Ship alone — they always produce something', () => {
    const steps = withSkips(checkpointSteps('verified'), [
      'lintfix',
      'cleanup',
      'ci',
      'merge',
    ])
    expect(steps.map((s) => s.state)).toEqual(['done', 'done', 'active'])
  })

  it('returns the stepper untouched for a run that skipped nothing', () => {
    const base = checkpointSteps('verified')
    expect(withSkips(base, undefined)).toBe(base)
  })
})
