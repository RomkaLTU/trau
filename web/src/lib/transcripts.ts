import { queryOptions } from '@tanstack/react-query'

export interface TranscriptView {
  id: string
  label: string
  cols: number
  rows: number
  size: number
  modified: string
  live: boolean
}

export interface TranscriptsResponse {
  repo: string
  transcripts: TranscriptView[]
}

async function fetchTranscripts(repo: string): Promise<TranscriptsResponse> {
  const res = await fetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/transcripts`,
  )
  if (!res.ok) {
    throw new Error(`transcripts request failed: ${res.status}`)
  }
  return res.json()
}

export const transcriptsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['transcripts', repo],
    queryFn: () => fetchTranscripts(repo),
    enabled: repo !== '',
    refetchInterval: 3000,
  })

export interface TranscriptMeta {
  id: string
  cols: number
  rows: number
}

export type TranscriptStatus = 'connecting' | 'live' | 'error'

// decodeChunk turns one base64 SSE frame back into the raw PTY bytes the terminal
// emulator writes. The stream base64-encodes because raw PTY output carries
// control bytes and newlines that an SSE data line cannot hold verbatim.
export function decodeChunk(b64: string): Uint8Array {
  const bin = atob(b64)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) {
    bytes[i] = bin.charCodeAt(i)
  }
  return bytes
}

// transcriptStreamURL follows the newest transcript for a repo, or replays one
// finished phase when an id is given.
export function transcriptStreamURL(repo: string, id?: string): string {
  const base = `/api/v1/repos/${encodeURIComponent(repo)}/transcript/stream`
  return id ? `${base}?id=${encodeURIComponent(id)}` : base
}
