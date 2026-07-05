import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { Play } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  EmptyState,
  Eyebrow,
  RepoPicker,
  SegmentedControl,
  TerminalCard,
  type SegmentOption,
} from "@/components/trau";
import { Terminal } from "@/components/terminal";
import { instancesQueryOptions, type RepoView } from "@/lib/instances";
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
  const { data, error, isPending } = useQuery(instancesQueryOptions);
  const repos = useMemo(() => sortRepos(data?.repos ?? []), [data]);

  const [repo, setRepo] = useState("");
  const [id, setID] = useState(FOLLOW_NEWEST);

  useEffect(() => {
    if (repos.length === 0) return;
    if (!repos.some((r) => r.name === repo)) {
      setRepo(repos[0].name);
      setID(FOLLOW_NEWEST);
    }
  }, [repos, repo]);

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

      {repo && (
        <Panel
          repos={repos}
          repo={repo}
          id={id}
          onRepoChange={(name) => {
            setRepo(name);
            setID(FOLLOW_NEWEST);
          }}
          onPhaseChange={setID}
        />
      )}
    </div>
  );
}

function Panel({
  repos,
  repo,
  id,
  onRepoChange,
  onPhaseChange,
}: {
  repos: RepoView[];
  repo: string;
  id: string;
  onRepoChange: (repo: string) => void;
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
        <RepoPicker
          repos={repos.map((r) => r.name)}
          value={repo}
          onChange={onRepoChange}
          label="repo"
        />
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
              <Link to="/instances">
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

// sortRepos floats live repos to the top so the default selection is one that is
// actively producing a transcript.
function sortRepos(repos: RepoView[]): RepoView[] {
  return [...repos].sort((a, b) => {
    if (a.live !== b.live) return a.live ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}
