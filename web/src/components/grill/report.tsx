import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  Check,
  Copy,
  Download,
  FilePlus2,
  Loader2,
  Pencil,
  Printer,
  Trash2,
  TriangleAlert,
} from "lucide-react";
import { toast } from "sonner";

import { useDraftFromReport } from "@/components/grill/draft-from-report";
import { noReport, WarningList } from "@/components/grill/outcome-review";
import { Markdown } from "@/components/markdown";
import { ConfirmDialog } from "@/components/trau/confirm-dialog";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import {
  deleteGrillReport,
  renameGrillReport,
  type GrillSession,
  type OutcomePayload,
  type ReportSource,
} from "@/lib/grill";
import {
  composeReportMarkdown,
  downloadMarkdown,
  reportFileName,
  reportTitle,
} from "@/lib/report-export";
import { cn } from "@/lib/utils";

// ReportDocument reads a finished research session as what it produced: a titled
// document with the report as its body, rather than a proposal card with the findings
// folded away. It is the primary surface both before approval and after — approving
// writes a comment, it does not change what the report says.
export function ReportDocument({
  repo,
  session,
  outcome,
  warnings,
  onSession,
  onDeleted,
}: {
  repo: string;
  session: GrillSession;
  outcome: OutcomePayload;
  warnings: string[];
  onSession?: (session: GrillSession) => void;
  onDeleted?: () => void;
}) {
  const findings = outcome.findings?.trim() ?? "";
  const summary = outcome.summary.trim();
  const sources = outcome.sources ?? [];
  return (
    <article className="report-document mx-auto flex w-full max-w-[72ch] flex-col gap-5 px-6 py-8">
      <header className="flex flex-col gap-2">
        <ReportActions
          repo={repo}
          session={session}
          outcome={outcome}
          onDeleted={onDeleted}
        />
        <ReportTitle
          repo={repo}
          session={session}
          outcome={outcome}
          onSession={onSession}
        />
        <ReportMeta session={session} />
      </header>
      {findings === "" ? (
        <p className="text-sm text-muted-foreground">{noReport}</p>
      ) : (
        <Markdown document className="text-foreground">
          {findings}
        </Markdown>
      )}
      {sources.length > 0 && <SourceList sources={sources} />}
      {summary !== "" && (
        <p className="border-t border-border pt-4 text-sm leading-relaxed text-muted-foreground">
          {summary}
        </p>
      )}
      {warnings.length > 0 && <WarningList warnings={warnings} />}
    </article>
  );
}

const TITLE_CLASS =
  "text-balance text-2xl font-semibold leading-tight tracking-tight text-foreground";

// The title the agent chose is a first guess at what the report is about, so the
// heading is editable in place. The rename outranks every later outcome, which is why
// the hub is told rather than the document holding the new name locally.
function ReportTitle({
  repo,
  session,
  outcome,
  onSession,
}: {
  repo: string;
  session: GrillSession;
  outcome: OutcomePayload;
  onSession?: (session: GrillSession) => void;
}) {
  const queryClient = useQueryClient();
  const current = reportTitle(session, outcome);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(current);

  const rename = useMutation({
    mutationFn: (title: string) => renameGrillReport(session.id, title),
    onSuccess: (renamed) => {
      onSession?.(renamed);
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] });
    },
    onError: (err: Error) => toast.error(err.message),
  });

  function commit() {
    setEditing(false);
    const next = draft.trim();
    if (next === "" || next === current) {
      setDraft(current);
      return;
    }
    setDraft(next);
    rename.mutate(next);
  }

  if (editing) {
    return (
      <input
        autoFocus
        value={draft}
        aria-label="Report title"
        onFocus={(e) => e.currentTarget.select()}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit();
          if (e.key === "Escape") {
            setDraft(current);
            setEditing(false);
          }
        }}
        className={cn(
          TITLE_CLASS,
          "w-full rounded border border-border bg-input px-2 py-1 outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        )}
      />
    );
  }

  return (
    <h1 className={cn(TITLE_CLASS, "group flex items-start gap-2")}>
      <span className="min-w-0">{rename.isPending ? draft : current}</span>
      <button
        type="button"
        aria-label="Rename report"
        disabled={rename.isPending}
        onClick={() => {
          setDraft(current);
          setEditing(true);
        }}
        className="mt-1.5 shrink-0 text-muted-foreground opacity-0 transition-opacity hover:text-foreground group-hover:opacity-100 focus-visible:opacity-100 print:hidden"
      >
        <Pencil className="size-4" aria-hidden="true" />
      </button>
    </h1>
  );
}

// SourceList numbers the sources so the report's prose can point at one by position,
// and shows each link's host, since that is most of what says whether to trust it.
function SourceList({ sources }: { sources: ReportSource[] }) {
  return (
    <section className="flex flex-col gap-3 border-t border-border pt-4">
      <h2 className="text-sm font-semibold text-foreground">Sources</h2>
      <ol className="flex flex-col gap-2">
        {sources.map((source, i) => (
          <li key={`${i}-${source.url}`} className="flex gap-2 text-sm">
            <span className="font-mono text-xs leading-6 text-muted-foreground">
              {i + 1}.
            </span>
            <span className="flex min-w-0 flex-col">
              <a
                href={source.url}
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary underline underline-offset-2 hover:no-underline"
              >
                {source.title || source.url}
              </a>
              <span className="text-xs text-muted-foreground">
                {[sourceHost(source.url), source.note?.trim()]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}

function sourceHost(raw: string): string {
  try {
    return new URL(raw).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

const COPY_FEEDBACK = {
  idle: { icon: Copy, label: "Copy", tone: "" },
  copied: { icon: Check, label: "Copied", tone: "text-done" },
  failed: { icon: TriangleAlert, label: "Copy failed", tone: "text-destructive" },
} as const;

// Every action runs off what the client already holds; the PDF is the browser's own
// print pipeline, which styles.css narrows to the report document alone.
function ReportActions({
  repo,
  session,
  outcome,
  onDeleted,
}: {
  repo: string;
  session: GrillSession;
  outcome: OutcomePayload;
  onDeleted?: () => void;
}) {
  const [state, setState] = useState<keyof typeof COPY_FEEDBACK>("idle");
  const { icon: CopyIcon, label, tone } = COPY_FEEDBACK[state];

  useEffect(() => {
    if (state === "idle") return;
    const timer = setTimeout(() => setState("idle"), 1600);
    return () => clearTimeout(timer);
  }, [state]);

  return (
    <div className="flex flex-wrap items-center justify-end gap-1 print:hidden">
      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={() => {
          void copyText(composeReportMarkdown(session, outcome)).then((ok) =>
            setState(ok ? "copied" : "failed"),
          );
        }}
      >
        <CopyIcon className={cn("size-3.5", tone)} aria-hidden="true" />
        {label}
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={() =>
          downloadMarkdown(
            reportFileName(session, outcome),
            composeReportMarkdown(session, outcome),
          )
        }
      >
        <Download className="size-3.5" aria-hidden="true" />
        Download .md
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={() => window.print()}
      >
        <Printer className="size-3.5" aria-hidden="true" />
        Export PDF
      </Button>
      <DraftIssue repo={repo} session={session} />
      {session.state === "applied" && (
        <DeleteReport repo={repo} session={session} onDeleted={onDeleted} />
      )}
    </div>
  );
}

// A finished report is where the reading stops and the work starts: Draft an issue
// opens an Inbox interview seeded with it, in one click and with nothing to fill in.
// The report is left exactly as it is, so a second click drafts a second issue.
function DraftIssue({
  repo,
  session,
}: {
  repo: string;
  session: GrillSession;
}) {
  const { drafting, draft } = useDraftFromReport({
    repo,
    sessionId: session.id,
  });
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      disabled={drafting}
      onClick={draft}
    >
      {drafting ? (
        <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
      ) : (
        <FilePlus2 className="size-3.5" aria-hidden="true" />
      )}
      Draft an issue
    </Button>
  );
}

// A report is kept for good — the hub's retention sweep never prunes one — so deleting
// it is the only way it goes away, and the confirmation says as much.
function DeleteReport({
  repo,
  session,
  onDeleted,
}: {
  repo: string;
  session: GrillSession;
  onDeleted?: () => void;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);

  const remove = useMutation({
    mutationFn: () => deleteGrillReport(session.id),
    // The rail is refetched before the host is told, so the selection it falls back
    // to is a report that is still there.
    onSuccess: async () => {
      toast.success("Report deleted");
      await queryClient.invalidateQueries({ queryKey: ["grill", repo] });
      onDeleted?.();
    },
    onError: (err: Error) => toast.error(err.message),
  });

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        disabled={remove.isPending}
        onClick={() => setOpen(true)}
        className="text-destructive hover:bg-destructive/10 hover:text-destructive"
      >
        {remove.isPending ? (
          <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
        ) : (
          <Trash2 className="size-3.5" aria-hidden="true" />
        )}
        Delete
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        windowTitle="delete report"
        title="Delete this report?"
        description="The report and the transcript behind it leave the hub for good. It cannot be recovered."
        confirmLabel="Delete forever"
        destructive
        onConfirm={() => remove.mutate()}
      />
    </>
  );
}

// A report is read long after the day it was written, so the document dates itself —
// off updated_at, the moment the session finished — and names what wrote it. A report
// that answered a question about an issue reaches back to it.
function ReportMeta({ session }: { session: GrillSession }) {
  const parts = [
    reportDate(session.updated_at),
    [session.provider, session.model].filter(Boolean).join(" · "),
  ].filter((part) => part !== "");
  const issueId = session.issue_id ?? "";
  return (
    <p className="font-mono text-xs text-muted-foreground">
      {parts.join("  ·  ")}
      {issueId && (
        <>
          {"  ·  "}
          <Link
            to="/backlog"
            search={{ issue: issueId }}
            className="text-primary underline-offset-2 hover:underline"
          >
            {issueId}
          </Link>
        </>
      )}
    </p>
  );
}

function reportDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleDateString(undefined, {
        year: "numeric",
        month: "long",
        day: "numeric",
      });
}
