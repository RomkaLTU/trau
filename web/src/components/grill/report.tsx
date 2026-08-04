import { noReport, WarningList } from "@/components/grill/outcome-review";
import { Markdown } from "@/components/markdown";
import {
  type GrillSession,
  type OutcomePayload,
  type ReportSource,
} from "@/lib/grill";

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
  const sources = outcome.sources ?? [];
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
