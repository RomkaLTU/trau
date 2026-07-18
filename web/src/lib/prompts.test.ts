import { describe, expect, it } from 'vitest'

import {
  globalSeed,
  matchesPrompt,
  repoResetFallback,
  repoSeed,
} from './prompts'
import type { Prompt, RepoPrompt } from './prompts'

function prompt(over: Partial<Prompt> = {}): Prompt {
  return {
    name: 'build_instruction',
    title: 'Build instruction',
    description: 'The main build-phase prompt.',
    placeholders: [],
    default: 'default body',
    override: null,
    ...over,
  }
}

function repoPrompt(over: Partial<RepoPrompt> = {}): RepoPrompt {
  return {
    ...prompt(),
    repo_override: null,
    effective: 'default',
    effective_body: '',
    ...over,
  }
}

describe('globalSeed', () => {
  it('seeds from the built-in default when uncustomized', () => {
    expect(globalSeed(prompt())).toBe('default body')
  })

  it('seeds from the override when one exists', () => {
    expect(globalSeed(prompt({ override: 'custom' }))).toBe('custom')
  })
})

describe('repoSeed', () => {
  it('prefers the repo override', () => {
    const p = repoPrompt({ repo_override: 'repo', override: 'global' })
    expect(repoSeed(p)).toBe('repo')
  })

  it('falls back to the global override', () => {
    expect(repoSeed(repoPrompt({ override: 'global' }))).toBe('global')
  })

  it('falls back to the built-in default', () => {
    expect(repoSeed(repoPrompt())).toBe('default body')
  })
})

describe('repoResetFallback', () => {
  it('reverts to global when a global override exists', () => {
    const p = repoPrompt({ repo_override: 'repo', override: 'global' })
    expect(repoResetFallback(p)).toBe('global')
  })

  it('reverts to the built-in default otherwise', () => {
    expect(repoResetFallback(repoPrompt({ repo_override: 'repo' }))).toBe(
      'default',
    )
  })
})

describe('matchesPrompt', () => {
  it('matches on name, title, and description', () => {
    const p = prompt()
    expect(matchesPrompt(p, 'build_inst')).toBe(true)
    expect(matchesPrompt(p, 'Build Inst')).toBe(true)
    expect(matchesPrompt(p, 'build-phase')).toBe(true)
    expect(matchesPrompt(p, 'verify')).toBe(false)
  })

  it('matches everything on an empty query', () => {
    expect(matchesPrompt(prompt(), '')).toBe(true)
  })
})
