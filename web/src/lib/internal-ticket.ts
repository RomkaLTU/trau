import type { InternalIssue, InternalIssueDraft } from './issues'
import type { EnqueueRequest, QueueResponse } from './queue'

// InternalTicketPlan is the Internal ticket form resolved to what will be filed:
// the trimmed title, the sub-item titles that carry one, and whether those
// sub-items make it an epic.
export interface InternalTicketPlan {
  title: string
  subs: string[]
  isEpic: boolean
}

// planInternalTicket reads the form's rows as typed. The form has no kind
// control: any sub-item row with a title makes the ticket an epic, and a blank
// row is the list's resting state rather than input.
export function planInternalTicket(
  title: string,
  subs: string[],
): InternalTicketPlan {
  const filled = subs.map((s) => s.trim()).filter((s) => s !== '')
  return { title: title.trim(), subs: filled, isEpic: filled.length > 0 }
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

// createAndQueue files the plan into the hub store and lines the result up: the
// parent, then each sub-item under it in row order, then the parent onto the
// queue. A stage that fails names what the earlier ones already filed — the
// issues exist by then, so retrying the form from the top would file them twice.
export async function createAndQueue(
  plan: InternalTicketPlan,
  createIssue: (draft: InternalIssueDraft) => Promise<InternalIssue>,
  enqueueOne: (req: EnqueueRequest) => Promise<QueueResponse>,
): Promise<QueueResponse> {
  const parent = await createIssue({ title: plan.title })

  for (const [i, title] of plan.subs.entries()) {
    try {
      await createIssue({ title, parent: parent.id })
    } catch (err) {
      throw new Error(
        `${parent.id} was created, but sub-item ${i + 1} failed: ${errorText(err)}\n` +
          'Nothing was queued — the epic is on the backlog, incomplete.',
      )
    }
  }

  try {
    return await enqueueOne({
      id: parent.id,
      kind: plan.isEpic ? 'epic' : 'ticket',
    })
  } catch (err) {
    throw new Error(
      `${parent.id} was created, but not queued: ${errorText(err)}\n` +
        'It is on the backlog — queue it by id.',
    )
  }
}
