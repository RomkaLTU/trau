import { describe, expect, it } from 'vitest'

import {
  activityStep,
  activityText,
  checkpointStep,
  checkpointSteps,
  liveSteps,
  stepName,
  stepPill,
} from '@/lib/steps'

describe('activityStep', () => {
  it('groups every Activity into Build, Verify, or Ship', () => {
    expect(['build', 'lintfix', 'cleanup', 'handoff'].map(activityStep)).toEqual([0, 0, 0, 0])
    expect(['verify', 'repair', 'bugfix'].map(activityStep)).toEqual([1, 1, 1])
    expect(['commit', 'pr', 'ci-wait', 'merge'].map(activityStep)).toEqual([2, 2, 2, 2])
  })

  it('reads an unknown Activity as before-Build', () => {
    expect(activityStep('')).toBe(-1)
    expect(activityStep('picking')).toBe(-1)
  })
})

describe('checkpointStep', () => {
  it('reads a checkpoint as the Step the run has actually reached', () => {
    expect(checkpointStep('')).toBe(-1)
    expect(checkpointStep('building')).toBe(0)
    expect(checkpointStep('built')).toBe(0)
    expect(checkpointStep('handed_off')).toBe(1)
    expect(checkpointStep('verified')).toBe(2)
    expect(checkpointStep('pr_open')).toBe(2)
    expect(checkpointStep('merged')).toBe(3)
  })
})

describe('activityText', () => {
  it('splits a numbered detail and keeps a bare Activity intact', () => {
    expect(activityText('repair', 'repair2')).toBe('repair 2')
    expect(activityText('bugfix', 'bugfix3')).toBe('bugfix 3')
    expect(activityText('verify', '')).toBe('verify')
    expect(activityText('ci-wait')).toBe('ci-wait')
  })
})

describe('liveSteps', () => {
  const states = (activity: string | undefined, phase: string) =>
    Object.fromEntries(liveSteps(activity, undefined, phase).steps.map((s) => [s.label, s.state]))

  it('shows Verify active with a repair sub-label while the checkpoint still says handed_off', () => {
    const { steps, subLabel } = liveSteps('repair', 'repair2', 'handed_off')
    expect(Object.fromEntries(steps.map((s) => [s.label, s.state]))).toEqual({
      Build: 'done',
      Verify: 'active',
      Ship: 'todo',
    })
    expect(subLabel).toBe('Verify · repair 2')
  })

  it('shows Ship active with a ci-wait sub-label during CI polling', () => {
    const { steps, subLabel } = liveSteps('ci-wait', '', 'pr_open')
    expect(steps.map((s) => s.state)).toEqual(['done', 'done', 'active'])
    expect(subLabel).toBe('Ship · ci-wait')
  })

  it('derives the Step from the checkpoint when the heartbeat carries no Activity', () => {
    expect(states(undefined, 'handed_off')).toEqual({ Build: 'done', Verify: 'active', Ship: 'todo' })
    expect(liveSteps(undefined, undefined, 'handed_off').subLabel).toBeUndefined()
  })

  it('never renders a completion rank as the active step for an old-binary heartbeat', () => {
    expect(states(undefined, 'building')).toEqual({ Build: 'active', Verify: 'todo', Ship: 'todo' })
    expect(states(undefined, 'verified')).toEqual({ Build: 'done', Verify: 'done', Ship: 'active' })
  })
})

describe('checkpointSteps', () => {
  it('marks the Step a stopped run reached', () => {
    expect(checkpointSteps('handed_off').map((s) => s.state)).toEqual(['done', 'active', 'todo'])
  })

  it('completes every Step once merged', () => {
    expect(new Set(checkpointSteps('merged').map((s) => s.state))).toEqual(new Set(['done']))
  })

  it('marks the Step a failed run stopped in', () => {
    expect(checkpointSteps('verified', true).map((s) => s.state)).toEqual(['done', 'done', 'fail'])
  })
})

describe('stepName / stepPill', () => {
  it('names the active Step, Activity first then checkpoint', () => {
    expect(stepName('repair', 'handed_off')).toBe('Verify')
    expect(stepName(undefined, 'verified')).toBe('Ship')
    expect(stepName(undefined, '')).toBe('')
    expect(stepName(undefined, 'merged')).toBe('')
  })

  it('labels a live loop by its Step, staying on the active palette', () => {
    expect(stepPill('ci-wait', 'pr_open')).toEqual({ state: 'active', label: 'ship' })
    expect(stepPill(undefined, 'handed_off')).toEqual({ state: 'active', label: 'verify' })
    expect(stepPill(undefined, '')).toEqual({ state: 'active', label: 'running' })
  })
})
