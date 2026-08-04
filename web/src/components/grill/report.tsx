import { noReport, WarningList } from "@/components/grill/outcome-review";
import { Markdown } from "@/components/markdown";
import { type GrillSession, type OutcomePayload } from "@/lib/grill";

// ReportDocument reads a finished research session as what it produced: a titled
// document with the report as its body, rather than a proposal card with the findings
// folded away. It is the primary surface both before approval and after — approving
// writes a comment, it does not change what the report says.
export function ReportDocument({
  session,
  outcome,
  warnings,
}: {
  session: GrillSession;
  outcome: OutcomePayload;
  warnings: string[];
}) {
  const findings = outcome.findings?.trim() ?? "";
  const summary = outcome.summary.trim();
  return (
    <article className="mx-auto flex w-full max-w-[72ch] flex-col gap-5 px-6 py-8">
      <header className="flex flex-col gap-2">
        <h1 className="text-balance text-2xl font-semibold leading-tight tracking-tight text-foreground">
          {reportTitle(session, outcome)}
        </h1>
        <ReportMeta session={session} />
      </header>
      {findings === "" ? (
        <p className="text-sm text-muted-foreground">{noReport}</p>
      ) : (
        <Markdown document className="text-foreground">
          {findings}
        </Markdown>
      )}
      {summary !== "" && (
        <p className="border-t border-border pt-4 text-sm leading-relaxed text-muted-foreground">
          {summary}
        </p>
      )}
      {warnings.length > 0 && <WarningList warnings={warnings} />}
    </article>
  );
}

// reportTitle is the title the agent gave the report; a session finished before
// titles were required has none and is still named by its seed question.
function reportTitle(
  session: GrillSession,
  outcome: OutcomePayload,
): string {
  return (
    outcome.title?.trim() || session.issue_title?.trim() || "Untitled research"
  );
}

// A report is read long after the day it was written, so the document dates itself —
// off updated_at, the moment the session finished — and names what wrote it.
function ReportMeta({ session }: { session: GrillSession }) {
  const parts = [
    reportDate(session.updated_at),
    [session.provider, session.model].filter(Boolean).join(" · "),
  ].filter((part) => part !== "");
  return (
    <p className="font-mono text-xs text-muted-foreground">
      {parts.join("  ·  ")}
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
