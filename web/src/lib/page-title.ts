import { useEffect } from 'react'

import type { LoopHaltKind } from '@/lib/loop'

const SUFFIX = 'trau'
const SEP = ' · '

function withSuffix(...segments: string[]): string {
  return [...segments.filter((s) => s !== ''), SUFFIX].join(SEP)
}

function withGlyph(glyph: string, text: string): string {
  return glyph ? `${glyph} ${text}` : text
}

export function standardTitle(page: string): string {
  return withSuffix(page)
}

// runGlyph marks only the settled or attention pill states — a running phase
// carries none, so the changing phase label is the signal on its own.
const RUN_GLYPH: Record<string, string> = {
  merged: '✓',
  paused: '⏸',
  fault: '⚠',
  quarantined: '⚠',
}

// runTitle is the live-run and run-detail tab title: it reuses the label the
// page's own status pill shows, so tab and page never disagree.
export function runTitle(ticket: string, pill: string): string {
  return withSuffix(withGlyph(RUN_GLYPH[pill] ?? '', ticket), pill)
}

const HALT_GLYPH: Record<LoopHaltKind, string> = {
  paused: '⏸',
  budget: '⚠',
  fault: '⚠',
  quarantined: '⚠',
}

export type LoopTitleState =
  | { kind: 'idle' }
  | { kind: 'draining'; done: number; total: number; ticket: string; step: string }
  | { kind: 'halted'; halt: LoopHaltKind; ticket: string }
  | { kind: 'done'; total: number }

export function loopTitle(state: LoopTitleState): string {
  switch (state.kind) {
    case 'draining':
      return withSuffix(`${state.done}/${state.total} ${state.ticket}`.trim(), state.step)
    case 'halted':
      return withSuffix(withGlyph(HALT_GLYPH[state.halt], state.ticket || 'queue'), state.halt)
    case 'done':
      return withSuffix(`✓ ${state.total} done`)
    case 'idle':
      return standardTitle('Loop')
  }
}

export function usePageTitle(title: string): void {
  useEffect(() => {
    document.title = title || SUFFIX
  }, [title])
}
