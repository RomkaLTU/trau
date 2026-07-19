import { apiFetch } from './api'

export interface UploadedAttachment {
  id: number
  url: string
  filename: string
  mime_type: string
}

// uploadAttachment posts an image the user pasted, dropped, or picked to the hub,
// which stores it and returns the canonical serve URL the editor embeds. It is
// the app's one multipart call: FormData carries its own multipart Content-Type,
// so it deliberately sets none, while apiFetch still adds the bearer credential
// and raises the unauthorized signal on a 401.
export async function uploadAttachment(
  repo: string,
  file: File,
): Promise<UploadedAttachment> {
  const body = new FormData()
  body.append('file', file)
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/attachments`,
    { method: 'POST', body },
  )
  if (!res.ok) {
    throw new Error(await uploadError(res))
  }
  return (await res.json()) as UploadedAttachment
}

export interface AttachmentUploads {
  uploaded: UploadedAttachment[]
  errors: string[]
}

// uploadAttachments uploads a batch concurrently but reports the accepted files in
// the order they were given, so a multi-file paste, drop or pick keeps that order no
// matter which upload resolves first. Rejections are collected rather than thrown:
// one bad file in a batch must not discard the images that did upload.
export async function uploadAttachments(
  repo: string,
  files: File[],
): Promise<AttachmentUploads> {
  const results = await Promise.allSettled(
    files.map((file) => uploadAttachment(repo, file)),
  )
  const uploads: AttachmentUploads = { uploaded: [], errors: [] }
  for (const result of results) {
    if (result.status === 'fulfilled') {
      uploads.uploaded.push(result.value)
      continue
    }
    const reason: unknown = result.reason
    uploads.errors.push(
      reason instanceof Error ? reason.message : 'Image upload failed',
    )
  }
  return uploads
}

async function uploadError(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string }
    if (body.error) return body.error
  } catch {
    // fall through to a status-only message
  }
  return `upload failed (${res.status})`
}
