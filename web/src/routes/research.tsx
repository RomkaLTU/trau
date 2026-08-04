import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import { FlaskConical, Loader2 } from "lucide-react";

import { ActivitySwitch } from "@/components/grill/activity";
import { ErrorNote } from "@/components/grill/banners";
import { Composer } from "@/components/grill/composer";
import {
  GrillConversation,
  type GrillStatus,
} from "@/components/grill/conversation";
import {
  GrillModelSelect,
  GrillProviderSelect,
} from "@/components/grill/model-select";
import { Button } from "@/components/ui/button";
import {
  PageHeader,
  ProjectScopeGate,
  RepoHealthGate,
  StatusPill,
  useActiveRepo,
} from "@/components/trau";
import {
  isAwaitingAnswer,
  researchGrillSessionsQueryOptions,
  startGrillSession,
  startModelOptions,
  type GrillDefaults,
  type GrillListResponse,
  type GrillSession,
} from "@/lib/grill";
import { inboxPill, type InboxPillTone } from "@/lib/inbox";
import { repoScopeSwitch } from "@/lib/notification-center";
import { standardTitle, usePageTitle } from "@/lib/page-title";
import { useProjectRepo } from "@/lib/projects";
import { cn } from "@/lib/utils";

interface ResearchSearch {
  session?: string;
  repo?: string;
}

export const Route = createFileRoute("/research")({
  component: ResearchPage,
  // session selects a report to read (or new to open the composer) and repo names the
  // project it belongs to — both read at runtime through nuqs so the dock and a pushed
  // notification can point straight at a waiting session.
  validateSearch: (search: Record<string, unknown>): ResearchSearch => {
    const out: ResearchSearch = {};
    if (typeof search.session === "string" && search.session !== "")
      out.session = search.session;
    if (typeof search.repo === "string" && search.repo !== "")
      out.repo = search.repo;
    return out;
  },
});

const NEW_SESSION = "new";

const PILL_TEXT_TONE: Record<InboxPillTone, string> = {
  warn: "text-warn",
  active: "text-teal",
  verify: "text-info",
  success: "text-done",
  todo: "text-faint",
};

// ResearchStart is what a not-yet-started research session will run on. The choice
// belongs to the page rather than the composer, so it survives reading a report and
// coming back; until the user picks, it trails the repo default the hub reports for
// research. Switching repo drops it, so the new repo's own default wins.
interface ResearchStart {
  defaults?: GrillDefaults;
  provider: string;
  model: string;
  onProviderChange: (provider: string) => void;
  onModelChange: (model: string) => void;
}

// researchTitle names a session in the rail: the title the agent gave its report,
// once it has written one. Until then — and for a report finished before titles were
// required — the hub's join falls back to the seed, the question the session opened
// on, which is the only title an issue-less research session ever has.
function researchTitle(session: GrillSession): string {
  return (
    session.report_title?.trim() ||
    session.issue_title?.trim() ||
    "Untitled research"
  );
}

// A report is read long after the day it was written, so a row carries its date.
function researchDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function ResearchPage() {
  usePageTitle(standardTitle("Research"));
  const { repo: activeRepo, repos, setRepo } = useActiveRepo();
  const repo = useProjectRepo(activeRepo ?? "", repos);
  const queryClient = useQueryClient();
  const list = useQuery(researchGrillSessionsQueryOptions(repo));

  // A link into a waiting session carries the project it belongs to, because the page
  // is built from the active scope and not from the link. The scope adopts it once the
  // repo list is in, then the param is dropped so the switcher stays in charge.
  const [linkedRepo, setLinkedRepo] = useQueryState("repo", parseAsString);
  useEffect(() => {
    if (!linkedRepo || repos.length === 0) return;
    const adopt = repoScopeSwitch(linkedRepo, activeRepo, repos);
    if (adopt) setRepo(adopt);
    void setLinkedRepo(null);
  }, [linkedRepo, activeRepo, repos, setRepo, setLinkedRepo]);

  // An abandoned session was ended with nothing to show, so it leaves the page
  // entirely; everything else — still running, waiting on an answer, or applied —
  // has a report to reach.
  const sessions = (list.data?.sessions ?? []).filter(
    (s) => s.state !== "abandoned",
  );

  const [peek, setPeek] = useQueryState(
    "session",
    parseAsString.withOptions({ history: "push" }),
  );
  const selected =
    peek === NEW_SESSION
      ? null
      : (sessions.find((s) => s.id === peek) ?? sessions[0] ?? null);

  const defaults = list.data?.defaults;
  const [pickedProvider, setPickedProvider] = useState<string | null>(null);
  const [pickedModel, setPickedModel] = useState<string | null>(null);
  const pickAllowed = !defaults?.providers?.some(
    (p) => p.name === pickedProvider && p.disabled,
  );
  const starter: ResearchStart = {
    defaults,
    provider:
      (pickAllowed ? pickedProvider : null) ?? defaults?.provider ?? "claude",
    model: (pickAllowed ? pickedModel : null) ?? defaults?.model ?? "",
    onProviderChange: (next) => {
      setPickedProvider(next);
      setPickedModel("");
    },
    onModelChange: setPickedModel,
  };

  const [status, setStatus] = useState<GrillStatus | null>(null);
  const live =
    status !== null && status.session.id === selected?.id ? status : null;

  // Approving a report writes nothing to the tracker and leaves nowhere to advance
  // to: the session stays selected and its review flips to the applied report in
  // place, so only the rail needs telling that the row now reads applied.
  function refreshList() {
    void queryClient.invalidateQueries({ queryKey: ["grill", repo, "research"] });
  }

  // Discarding abandons the session, which drops it off the page — the selection
  // falls back to the newest report rather than a row that is no longer there.
  function onDiscarded() {
    void setPeek(null);
    refreshList();
  }

  return (
    <ProjectScopeGate className="min-h-0 flex-1" action="run research sessions">
      <div className="absolute inset-0 flex flex-col">
        <PageHeader
          className="shrink-0"
          eyebrow={repo || "research"}
          title="Research"
          description="Ask a question, get a findings report — every report stays readable here."
          actions={
            <Button
              variant="outline"
              size="sm"
              onClick={() => void setPeek(NEW_SESSION)}
            >
              <FlaskConical />
              New research
            </Button>
          }
        />

        <RepoHealthGate repo={repo} className="min-h-0 flex-1">
          <div className="absolute inset-0 flex flex-col px-8 pb-4 md:grid md:grid-cols-[260px_minmax(0,1fr)]">
            <SessionSelect
              sessions={sessions}
              selectedId={selected?.id ?? null}
              onSelect={(id) => void setPeek(id)}
            />
            <SessionRail
              sessions={sessions}
              live={live?.session ?? null}
              selectedId={selected?.id ?? null}
              onSelect={(id) => void setPeek(id)}
            />

            <section
              aria-label="Research"
              className="flex min-h-0 min-w-0 flex-col"
            >
              {list.error ? (
                <div className="flex min-h-0 flex-1 items-center justify-center p-8">
                  <ErrorNote message={(list.error as Error).message} />
                </div>
              ) : list.isLoading ? (
                <div className="flex min-h-0 flex-1 items-center justify-center p-8">
                  <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
                    <Loader2 className="size-4 animate-spin" />
                    Loading research…
                  </p>
                </div>
              ) : selected ? (
                <SessionColumn
                  key={selected.id}
                  repo={repo}
                  session={selected}
                  status={live}
                  onStatus={setStatus}
                  onApplied={refreshList}
                  onDiscarded={onDiscarded}
                />
              ) : (
                <StartColumn
                  repo={repo}
                  starter={starter}
                  onStarted={(session) => void setPeek(session.id)}
                />
              )}
            </section>
          </div>
        </RepoHealthGate>
      </div>
    </ProjectScopeGate>
  );
}

// SessionColumn is the report zone: the session bar over the conversation, which runs
// the question-and-answer turns while the session is live and turns into the report
// document once it finishes. An applied session opens on that same document, its
// transcript tucked behind the disclosure.
function SessionColumn({
  repo,
  session,
  status,
  onStatus,
  onApplied,
  onDiscarded,
}: {
  repo: string;
  session: GrillSession;
  status: GrillStatus | null;
  onStatus: (status: GrillStatus) => void;
  onApplied: () => void;
  onDiscarded: () => void;
}) {
  const live = status?.session ?? session;
  const pill = inboxPill(live.state);

  return (
    <>
      <div className="shrink-0 border-b border-border">
        <div className="flex flex-wrap items-center justify-between gap-2 py-3 pl-5 pr-1">
          <span className="flex min-w-0 items-center gap-2 text-sm font-medium text-foreground">
            {live.issue_id && (
              <span className="shrink-0 font-mono text-muted-foreground">
                {live.issue_id}
              </span>
            )}
            <span className="truncate">{researchTitle(live)}</span>
          </span>
          <div className="flex shrink-0 items-center gap-2">
            <ActivitySwitch />
            <span className="font-mono text-xs text-muted-foreground">
              {researchDate(live.created_at)}
            </span>
            {status?.stream === "error" && (
              <span className="inline-flex items-center gap-1 text-xs text-warn">
                <span aria-hidden="true">⚠</span>
                reconnecting…
              </span>
            )}
            <StatusPill state={pill.tone} label={pill.label} />
          </div>
        </div>
      </div>

      <div className="relative flex min-h-0 flex-1 flex-col">
        <GrillConversation
          key={session.id}
          repo={repo}
          initial={session}
          report
          autoFocus={isAwaitingAnswer(session.state)}
          onStatus={onStatus}
          onApplied={onApplied}
          onDiscarded={onDiscarded}
        />
      </div>
    </>
  );
}

// StartColumn opens a research session: the first message is the question, and the
// session runs from there. Nothing exists server-side until it is sent.
function StartColumn({
  repo,
  starter,
  onStarted,
}: {
  repo: string;
  starter: ResearchStart;
  onStarted: (session: GrillSession) => void;
}) {
  const queryClient = useQueryClient();
  const start = useMutation({
    mutationFn: (seed: string) =>
      startGrillSession(repo, "", {
        seed,
        model: starter.model,
        provider: starter.provider,
        mode: "research",
      }),
    onSuccess: (session) => {
      queryClient.setQueryData<GrillListResponse>(
        ["grill", repo, "research"],
        (prev) =>
          prev
            ? {
                ...prev,
                sessions: [
                  session,
                  ...prev.sessions.filter((s) => s.id !== session.id),
                ],
              }
            : { repo, sessions: [session] },
      );
      onStarted(session);
    },
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex min-h-0 flex-1 items-center justify-center px-6">
        <p className="max-w-sm text-balance text-center text-sm leading-relaxed text-muted-foreground">
          Ask the question you want researched. Your first message starts an
          investigation against primary sources — you get a findings report, and
          nothing is filed unless you ask for it.
        </p>
      </div>
      <div className="flex flex-col gap-3 border-t border-border p-4">
        <div className="flex justify-end">
          <StartModelSelect starter={starter} />
        </div>
        <Composer
          repo={repo}
          placeholder="Ask your question…"
          disabled={start.isPending}
          submitting={start.isPending}
          onSend={(text) => start.mutate(text)}
          autoFocus
        />
        {start.error && <ErrorNote message={(start.error as Error).message} />}
      </div>
    </div>
  );
}

// StartModelSelect settles the provider and model before the session exists, since
// both lock at the first turn. Provider is its own picker — choosing one swaps the
// model list to that provider's catalog.
function StartModelSelect({ starter }: { starter: ResearchStart }) {
  const providers = starter.defaults?.providers ?? [{ name: starter.provider }];
  return (
    <div className="flex items-center gap-1">
      <span className="text-xs text-muted-foreground">Runs on</span>
      <GrillProviderSelect
        provider={starter.provider}
        providers={providers}
        onChange={starter.onProviderChange}
      />
      <GrillModelSelect
        provider={starter.provider}
        model={starter.model}
        options={startModelOptions(starter.defaults, starter.provider)}
        label="Research model"
        hideProvider
        onChange={starter.onModelChange}
      />
    </div>
  );
}

function SessionRail({
  sessions,
  live,
  selectedId,
  onSelect,
}: {
  sessions: GrillSession[];
  live: GrillSession | null;
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <nav
      aria-label="Research sessions"
      className="hidden min-h-0 flex-col gap-1.5 overflow-y-auto border-r border-border py-4 pr-3 md:flex"
    >
      <div className="flex items-center justify-between px-2.5">
        <p className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
          Reports
        </p>
        <span className="font-mono text-[0.65rem] tabular-nums text-info">
          {sessions.length}
        </span>
      </div>
      <ul className="flex flex-col gap-0.5">
        {sessions.map((session) => (
          <SessionRow
            key={session.id}
            session={live?.id === session.id ? live : session}
            selected={selectedId === session.id}
            onSelect={() => onSelect(session.id)}
          />
        ))}
        {sessions.length === 0 && (
          <li className="px-2.5 py-1 font-mono text-xs text-faint">none yet</li>
        )}
      </ul>
    </nav>
  );
}

function SessionRow({
  session,
  selected,
  onSelect,
}: {
  session: GrillSession;
  selected: boolean;
  onSelect: () => void;
}) {
  const pill = inboxPill(session.state);
  return (
    <li className="relative">
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        aria-label={`Open ${researchTitle(session)}`}
        className={cn(
          "flex w-full flex-col gap-0.5 rounded-md px-2.5 py-2 text-left transition-colors",
          selected ? "bg-primary/10" : "hover:bg-secondary",
        )}
      >
        {selected && (
          <span
            aria-hidden="true"
            className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-primary"
          />
        )}
        <span className="flex items-center gap-2">
          <span
            className={cn(
              "font-mono text-xs",
              selected ? "text-primary" : "text-muted-foreground",
            )}
          >
            {researchDate(session.created_at)}
          </span>
          {session.issue_id && (
            <span className="truncate font-mono text-xs text-faint">
              {session.issue_id}
            </span>
          )}
          <span
            className={cn(
              "ml-auto shrink-0 font-mono text-[0.65rem]",
              PILL_TEXT_TONE[pill.tone],
            )}
          >
            {pill.label}
          </span>
        </span>
        <span
          className={cn(
            "line-clamp-2 text-xs leading-relaxed",
            selected ? "text-foreground" : "text-muted-foreground",
          )}
        >
          {researchTitle(session)}
        </span>
      </button>
    </li>
  );
}

// SessionSelect is the rail's fallback under md, where 260px of chrome would crowd
// out the conversation.
function SessionSelect({
  sessions,
  selectedId,
  onSelect,
}: {
  sessions: GrillSession[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  if (sessions.length === 0) return null;
  return (
    <label className="flex shrink-0 flex-col gap-1 py-3 md:hidden">
      <span className="sr-only">Research sessions</span>
      <select
        value={selectedId ?? ""}
        onChange={(e) => onSelect(e.target.value)}
        className="h-9 w-full rounded-md border bg-card px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50 dark:bg-input"
      >
        {sessions.map((session) => (
          <option key={session.id} value={session.id}>
            {researchDate(session.created_at)} — {researchTitle(session)}
          </option>
        ))}
      </select>
    </label>
  );
}
