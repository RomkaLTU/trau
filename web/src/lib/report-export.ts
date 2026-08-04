import {
  type GrillSession,
  type OutcomePayload,
  type ReportSource,
} from '@/lib/grill'

// reportTitle is the title the agent gave the report; a session finished before
// titles were required has none and is still named by its seed question.
export function reportTitle(
  session: GrillSession,
  outcome: OutcomePayload,
): string {
  return (
    outcome.title?.trim() || session.issue_title?.trim() || 'Untitled research'
  )
}

// composeReportMarkdown writes the report as a document that stands on its own: it is
// read away from the app, so it dates itself, names what produced it, and inlines the
// sources the page only links.
export function composeReportMarkdown(
  session: GrillSession,
  outcome: OutcomePayload,
): string {
  const blocks = [`# ${reportTitle(session, outcome)}`]

  const meta = metaLine(session)
  if (meta !== '') blocks.push(`_${meta}_`)

  const findings = outcome.findings?.trim() ?? ''
  if (findings !== '') blocks.push(findings)

  const sources = outcome.sources ?? []
  if (sources.length > 0)
    blocks.push(['## Sources', ...sources.map(sourceLine)].join('\n'))

  const summary = outcome.summary.trim()
  if (summary !== '') blocks.push(summary)

  return blocks.join('\n\n') + '\n'
}

function metaLine(session: GrillSession): string {
  return [
    reportDay(session.updated_at),
    session.provider,
    session.model,
    session.repo,
  ]
    .filter((part) => part !== undefined && part !== '')
    .join(' · ')
}

function sourceLine(source: ReportSource, i: number): string {
  const label = source.title.trim() || source.url
  const note = source.note?.trim()
  return `${i + 1}. [${label}](${source.url})${note ? ` — ${note}` : ''}`
}

// reportFileName leads with the date so a folder of exported reports sorts by when
// they were written.
export function reportFileName(
  session: GrillSession,
  outcome: OutcomePayload,
): string {
  const parts = [reportDay(session.updated_at), slugify(reportTitle(session, outcome))]
  return parts.filter((part) => part !== '').join('-') + '.md'
}

function slugify(title: string): string {
  const slug = title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .slice(0, 60)
    .replace(/^-+|-+$/g, '')
  return slug || 'report'
}

// The exported document is read outside this machine, so its day is the timestamp's
// own UTC one rather than the reader's locale rendering of it.
function reportDay(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString().slice(0, 10)
}

export function downloadMarkdown(filename: string, markdown: string): void {
  const url = URL.createObjectURL(
    new Blob([markdown], { type: 'text/markdown;charset=utf-8' }),
  )
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.append(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}
