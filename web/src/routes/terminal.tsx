import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { Play } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  EmptyState,
  Eyebrow,
  ProjectScopeGate,
  SegmentedControl,
  TerminalCard,
  useActiveRepo,
  type SegmentOption,
} from "@/components/trau";
import { Terminal } from "@/components/terminal";
import { instancesQueryOptions, type RepoView } from "@/lib/instances";
import { standardTitle, usePageTitle } from "@/lib/page-title";
import {
  transcriptsQueryOptions,
  type TranscriptView,
} from "@/lib/transcripts";

export const Route = createFileRoute("/terminal")({
  component: TerminalPage,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(instancesQueryOptions),
});

const FOLLOW_NEWEST = "";

function TerminalPage() {
  usePageTitle(standardTitle("Terminal"));
  const { repo } = useActiveRepo();
  const { data, error, isPending } = useQuery(instancesQueryOptions);
  const repos = useMemo(() => data?.repos ?? [], [data]);

  const [id, setID] = useState(FOLLOW_NEWEST);

  useEffect(() => {
    setID(FOLLOW_NEWEST);
  }, [repo]);

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="active" className="text-teal">
          TERMINAL
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Terminal
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Watch live phase transcripts, like tailing the agent’s terminal.
        </p>
      </header>

      {error && (
        <p className="font-mono text-sm text-destructive">{String(error)}</p>
      )}
      {isPending && !error && (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      )}

      {data && repos.length === 0 && (
        <TerminalCard title="terminal">
          <p className="font-sans text-sm leading-relaxed text-muted-foreground">
            No repos have run a trau loop on this machine yet.
          </p>
        </TerminalCard>
      )}

      {data && repos.length > 0 && (
        <ProjectScopeGate action="watch a transcript" className="min-h-[300px]">
          {repo && (
            <Panel repos={repos} repo={repo} id={id} onPhaseChange={setID} />
          )}
        </ProjectScopeGate>
      )}
    </div>
  );
}

function Panel({
  repos,
  repo,
  id,
  onPhaseChange,
}: {
  repos: RepoView[];
  repo: string;
  id: string;
  onPhaseChange: (id: string) => void;
}) {
  const { data } = useQuery(transcriptsQueryOptions(repo));
  const transcripts = data?.transcripts ?? [];

  const repoLive = repos.find((r) => r.name === repo)?.live ?? false;
  const newestID = transcripts[0]?.id;
  const streaming = repoLive && (id === FOLLOW_NEWEST || id === newestID);
  const label =
    id === FOLLOW_NEWEST
      ? "newest"
      : (transcripts.find((t) => t.id === id)?.label ?? id);

  return (
    <>
      <div className="flex flex-wrap items-end gap-6">
        <div className="flex flex-col gap-1.5">
          <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            phase
          </span>
          <SegmentedControl
            aria-label="Phase"
            options={phaseOptions(transcripts)}
            value={id}
            onChange={onPhaseChange}
          />
        </div>
      </div>

      {transcripts.length === 0 ? (
        <EmptyState
          className="min-h-[360px]"
          message={`Nothing streaming for ${repo} yet.`}
          actions={
            <Button asChild size="sm" className="font-mono">
              <Link to="/run-once">
                <Play className="size-4" aria-hidden="true" />
                Run once
              </Link>
            </Button>
          }
        />
      ) : (
        <Terminal
          key={`${repo}:${id || "newest"}`}
          repo={repo}
          id={id || undefined}
          title={`${repo} · ${label}.log`}
          live={streaming}
          className="min-h-[420px]"
        />
      )}
    </>
  );
}

const MAX_PHASES = 6;

// phaseOptions collapses reruns of the same phase to their newest entry so the
// segmented control stays a phase switch, not a run archive.
function phaseOptions(transcripts: TranscriptView[]): SegmentOption<string>[] {
  const options: SegmentOption<string>[] = [
    { value: FOLLOW_NEWEST, label: "newest" },
  ];
  const seen = new Set<string>();
  for (const t of transcripts) {
    if (seen.has(t.label)) continue;
    seen.add(t.label);
    options.push({ value: t.id, label: t.label });
    if (options.length > MAX_PHASES) break;
  }
  return options;
}
