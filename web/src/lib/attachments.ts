import { useMemo } from "react";
import { queryOptions, useQuery } from "@tanstack/react-query";

import { apiFetch } from "./api";

export interface UploadedAttachment {
  id: number;
  url: string;
  filename: string;
  mime_type: string;
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
  const body = new FormData();
  body.append("file", file);
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/attachments`,
    { method: "POST", body },
  );
  if (!res.ok) {
    throw new Error(await uploadError(res));
  }
  return (await res.json()) as UploadedAttachment;
}

export interface AttachmentUploads {
  uploaded: UploadedAttachment[];
  errors: string[];
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
  );
  const uploads: AttachmentUploads = { uploaded: [], errors: [] };
  for (const result of results) {
    if (result.status === "fulfilled") {
      uploads.uploaded.push(result.value);
      continue;
    }
    const reason: unknown = result.reason;
    uploads.errors.push(
      reason instanceof Error ? reason.message : "Image upload failed",
    );
  }
  return uploads;
}

// IssueAttachment is one file the hub holds for an issue: the stored row plus the
// two things a client cannot derive — whether trau renders it inline, and the hub
// URL its bytes are served from. A tracker-hosted row stays `pending` until
// something requests those bytes, which caches or fails it; a failed row keeps its
// reason and retries on the next request.
export interface IssueAttachment {
  id: number;
  source: string;
  source_url?: string;
  filename: string;
  mime_type: string;
  size_bytes: number;
  state: "pending" | "cached" | "failed";
  error?: string;
  is_image: boolean;
  url: string;
}

async function fetchIssueAttachments(
  repo: string,
  id: string,
): Promise<IssueAttachment[]> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/${encodeURIComponent(
      id,
    )}/attachments`,
  );
  if (!res.ok) {
    throw new Error(`fetch attachments failed: ${res.status}`);
  }
  return res.json();
}

export const issueAttachmentsQueryOptions = (repo: string, id: string) =>
  queryOptions({
    queryKey: ["issue-attachments", repo, id],
    queryFn: () => fetchIssueAttachments(repo, id),
    enabled: repo !== "" && id !== "",
    staleTime: 15_000,
  });

// attachmentUrlMap points the tracker URL an issue body embeds at the hub
// attachment holding its bytes, which is the only form the browser can load.
export function attachmentUrlMap(
  attachments: IssueAttachment[],
): Record<string, string> {
  const map: Record<string, string> = {};
  for (const att of attachments) {
    if (att.source_url) map[att.source_url] = att.url;
  }
  return map;
}

const NO_ATTACHMENTS: IssueAttachment[] = [];

// useIssueAttachments reads an issue's attachments and the URL rewrites its
// markdown needs. Both surfaces that render issue bodies want the pair, and the
// shared query key means asking twice still costs one request.
export function useIssueAttachments(repo: string, id: string) {
  const { data } = useQuery(issueAttachmentsQueryOptions(repo, id));
  const attachments = data ?? NO_ATTACHMENTS;
  const urlMap = useMemo(() => attachmentUrlMap(attachments), [attachments]);
  return { attachments, urlMap };
}

// formatAttachmentSize renders a byte count for a file row. A pending row has no
// size yet, so it reads as unknown rather than as an empty file.
export function formatAttachmentSize(bytes: number): string {
  if (bytes <= 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`;
}

async function uploadError(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string };
    if (body.error) return body.error;
  } catch {
    // fall through to a status-only message
  }
  return `upload failed (${res.status})`;
}
