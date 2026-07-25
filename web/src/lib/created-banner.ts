// CreatedIssue is one filed issue's receipt: the repo it landed in, its identifier
// and title, how many sub-issues were submitted alongside it, and the labels of the
// apply steps that did not land.
export interface CreatedIssue {
  repo: string
  id: string
  title: string
  subCount: number
  failedSteps: string[]
}

// describeCreated is the banner's headline. A create carrying sub-issues filed an
// epic, so it names the count that was submitted — the response reports only the
// parent identifier.
export function describeCreated(created: CreatedIssue): string {
  if (created.subCount === 0) return `Created ${created.id} "${created.title}"`
  const noun = created.subCount === 1 ? 'sub-issue' : 'sub-issues'
  return `Created epic ${created.id} "${created.title}" with ${created.subCount} ${noun}`
}

// describeCreatedFailures names the steps that errored inside an apply whose parent
// still landed, or '' when every step went through.
export function describeCreatedFailures(failedSteps: readonly string[]): string {
  if (failedSteps.length === 0) return ''
  const noun = failedSteps.length === 1 ? 'step' : 'steps'
  return `${failedSteps.length} ${noun} failed: ${failedSteps.join(', ')}`
}
