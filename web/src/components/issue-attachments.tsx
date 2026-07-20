import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Download, FileText, RotateCw, TriangleAlert } from "lucide-react";

import { markdownImageSources } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { apiFetch } from "@/lib/api";
import {
  formatAttachmentSize,
  issueAttachmentsQueryOptions,
  type IssueAttachment,
} from "@/lib/attachments";

// IssueAttachments lists the files an issue carries that its markdown does not
// already display: every non-image file as a downloadable row, and any image the
// bodies never embed as a thumbnail. A pending row is shown like any other —
// following its link is what makes the hub fetch the bytes.
export function IssueAttachments({
  repo,
  id,
  attachments,
  bodies,
}: {
  repo: string;
  id: string;
  attachments: IssueAttachment[];
  bodies: string[];
}) {
  const queryClient = useQueryClient();
  const retry = useMutation({
    mutationFn: (url: string) => apiFetch(url),
    onSettled: () =>
      void queryClient.invalidateQueries({
        queryKey: issueAttachmentsQueryOptions(repo, id).queryKey,
      }),
  });

  const embedded = new Set(bodies.flatMap(markdownImageSources));
  const listed = attachments.filter((att) => !isEmbedded(att, embedded));
  if (listed.length === 0) return null;

  return (
    <div className="mt-6 flex flex-col gap-2 border-t pt-4">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Attachments · {listed.length}
      </h3>
      {listed.map((att) => (
        <AttachmentRow
          key={att.id}
          attachment={att}
          onRetry={() => retry.mutate(att.url)}
          retrying={retry.isPending}
        />
      ))}
    </div>
  );
}

function isEmbedded(att: IssueAttachment, sources: Set<string>): boolean {
  return (
    sources.has(att.url) || (!!att.source_url && sources.has(att.source_url))
  );
}

function AttachmentRow({
  attachment,
  onRetry,
  retrying,
}: {
  attachment: IssueAttachment;
  onRetry: () => void;
  retrying: boolean;
}) {
  const name = attachment.filename || `attachment-${attachment.id}`;

  if (attachment.state === "failed") {
    return (
      <div className="flex items-start gap-2.5 rounded-md border border-fail/40 bg-fail/5 px-3 py-2">
        <TriangleAlert
          className="mt-0.5 size-3.5 shrink-0 text-fail"
          aria-hidden
        />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate font-mono text-xs text-foreground">
            {name}
          </span>
          <span className="text-xs leading-relaxed text-muted-foreground">
            {attachment.error || "Could not download this file."}
          </span>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={onRetry}
          disabled={retrying}
        >
          <RotateCw />
          Retry
        </Button>
      </div>
    );
  }

  if (attachment.is_image) {
    return (
      <a
        href={attachment.url}
        target="_blank"
        rel="noreferrer"
        className="flex items-center gap-3 rounded-md border bg-card px-3 py-2 transition-colors hover:bg-muted"
      >
        <img
          src={attachment.url}
          alt={name}
          loading="lazy"
          className="size-12 shrink-0 rounded border object-cover"
        />
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
          {name}
        </span>
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
          {formatAttachmentSize(attachment.size_bytes)}
        </span>
      </a>
    );
  }

  return (
    <a
      href={attachment.url}
      download={name}
      className="flex items-center gap-3 rounded-md border bg-card px-3 py-2 transition-colors hover:bg-muted"
    >
      <FileText className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
        {name}
      </span>
      <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
        {formatAttachmentSize(attachment.size_bytes)}
      </span>
      <Download
        className="size-3.5 shrink-0 text-muted-foreground"
        aria-hidden
      />
    </a>
  );
}
