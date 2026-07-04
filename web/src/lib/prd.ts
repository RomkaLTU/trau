export interface PublishPRDRequest {
  title: string
  markdown: string
}

export interface PublishedPRD {
  url: string
  identifier?: string
  kind: 'document' | 'issue'
  provider: string
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function publishPRD(
  repo: string,
  req: PublishPRDRequest,
): Promise<PublishedPRD> {
  const res = await fetch(`/api/v1/repos/${encodeURIComponent(repo)}/prd`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'publish PRD failed'))
  }
  return res.json()
}

export interface PRDDraft {
  title: string
  markdown: string
}

type DraftStore = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

const DRAFT_PREFIX = 'trau.prd.draft.'

function draftKey(repo: string): string {
  return DRAFT_PREFIX + repo
}

// browserStore returns the persistence backend — localStorage in the browser, or
// null when it is unavailable (a non-DOM host) so callers degrade to a no-op
// rather than throwing.
function browserStore(): DraftStore | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function draftIsEmpty(draft: PRDDraft): boolean {
  return draft.title.trim() === '' && draft.markdown.trim() === ''
}

export function loadDraft(
  repo: string,
  store: DraftStore | null = browserStore(),
): PRDDraft | null {
  const raw = store?.getItem(draftKey(repo))
  if (!raw) return null
  try {
    const parsed = JSON.parse(raw) as Partial<PRDDraft>
    return { title: parsed.title ?? '', markdown: parsed.markdown ?? '' }
  } catch {
    return null
  }
}

export function saveDraft(
  repo: string,
  draft: PRDDraft,
  store: DraftStore | null = browserStore(),
): void {
  store?.setItem(draftKey(repo), JSON.stringify(draft))
}

export function clearDraft(
  repo: string,
  store: DraftStore | null = browserStore(),
): void {
  store?.removeItem(draftKey(repo))
}
