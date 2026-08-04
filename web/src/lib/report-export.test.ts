import { describe, expect, it } from 'vitest'

import type { GrillSession, OutcomePayload } from '@/lib/grill'
import {
  composeReportMarkdown,
  reportFileName,
} from '@/lib/report-export'

const session: GrillSession = {
  id: '7',
  repo: 'loop',
  issue_title: 'Which retry policy does the SDK use?',
  state: 'finished',
  mode: 'research',
  provider: 'claude',
  model: 'opus-5',
  created_at: '2026-07-17T10:00:00Z',
  updated_at: '2026-07-19T14:30:00Z',
}

const outcome: OutcomePayload = {
  disposition: 'research',
  title: 'How the SDK retries',
  findings: '# Question\n\nWhich policy?\n\n## Conclusion\n\nExponential backoff.',
  summary: 'The SDK backs off exponentially.',
}

describe('composeReportMarkdown', () => {
  it('leads with the title and a metadata line, then the findings', () => {
    const md = composeReportMarkdown(session, outcome)
    expect(md.split('\n\n')[0]).toBe('# How the SDK retries')
    expect(md).toContain('_2026-07-19 · claude · opus-5 · loop_')
    expect(md).toContain('## Conclusion\n\nExponential backoff.')
    expect(md).toContain('The SDK backs off exponentially.')
    expect(md.endsWith('\n')).toBe(true)
  })

  it('names a titleless report by the question it opened on', () => {
    const md = composeReportMarkdown(session, { ...outcome, title: '' })
    expect(md.startsWith('# Which retry policy does the SDK use?\n')).toBe(true)
  })

  it('falls back to a placeholder title when nothing named the report', () => {
    const md = composeReportMarkdown(
      { ...session, issue_title: '' },
      { ...outcome, title: '' },
    )
    expect(md.startsWith('# Untitled research\n')).toBe(true)
  })

  it('lists the sources it cited as links, with their notes', () => {
    const md = composeReportMarkdown(session, {
      ...outcome,
      sources: [
        {
          title: 'Retry docs',
          url: 'https://sdk.example/retries',
          note: 'the backoff table',
        },
        { title: '', url: 'https://blog.other.example/v3' },
      ],
    })
    expect(md).toContain(
      '## Sources\n1. [Retry docs](https://sdk.example/retries) — the backoff table\n2. [https://blog.other.example/v3](https://blog.other.example/v3)',
    )
  })

  it('leaves out the Sources section when the research cited none', () => {
    expect(composeReportMarkdown(session, outcome)).not.toContain('## Sources')
    expect(
      composeReportMarkdown(session, { ...outcome, sources: [] }),
    ).not.toContain('## Sources')
  })

  it('still writes a document when the session recorded no report', () => {
    const md = composeReportMarkdown(session, {
      ...outcome,
      findings: '   ',
      summary: '',
    })
    expect(md).toBe('# How the SDK retries\n\n_2026-07-19 · claude · opus-5 · loop_\n')
  })
})

describe('reportFileName', () => {
  it('dates the file and slugs the title', () => {
    expect(reportFileName(session, outcome)).toBe(
      '2026-07-19-how-the-sdk-retries.md',
    )
  })

  it('slugs away punctuation and collapses the runs it leaves', () => {
    expect(
      reportFileName(session, { ...outcome, title: '  Retries: v3 — & beyond! ' }),
    ).toBe('2026-07-19-retries-v3-beyond.md')
  })

  it('names a report the slug empties out', () => {
    expect(reportFileName(session, { ...outcome, title: '???' })).toBe(
      '2026-07-19-report.md',
    )
  })

  it('drops the date when the session carries no readable one', () => {
    expect(
      reportFileName({ ...session, updated_at: 'never' }, outcome),
    ).toBe('how-the-sdk-retries.md')
  })
})
