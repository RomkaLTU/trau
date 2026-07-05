import { apiFetch } from './api'

export interface IssueDraft {
  title: string
  description?: string
  labels?: string[]
}

export interface CreatedIssue {
  identifier: string
  url: string
  provider: string
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function createIssue(
  repo: string,
  draft: IssueDraft,
): Promise<CreatedIssue> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/issues`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(draft),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'create issue failed'))
  }
  return res.json()
}

export async function addComment(
  repo: string,
  ticket: string,
  body: string,
): Promise<void> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/comment`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'comment failed'))
  }
}
