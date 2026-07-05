import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { ChevronDown, Search } from "lucide-react";

import {
  EmptyState,
  Eyebrow,
  RepoPicker,
  TerminalCard,
} from "@/components/trau";
import { cn } from "@/lib/utils";
import { reposQueryOptions } from "@/lib/runs";
import { lessonsQueryOptions, type Lesson } from "@/lib/lessons";

export const Route = createFileRoute("/lessons")({
  component: Lessons,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
});

function Lessons() {
  const { data, error, isPending } = useQuery(reposQueryOptions);
  const [selected, setSelected] = useState<string | null>(null);

  const repos = data?.repos ?? [];
  const active =
    selected && repos.some((r) => r.name === selected)
      ? selected
      : (repos.find((r) => r.live)?.name ?? repos[0]?.name ?? null);

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="done" className="text-done">
          LESSONS
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Lessons
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Browse and search the lessons ledger the agent accumulates across
          runs.
        </p>
      </header>

      {error && (
        <p className="font-mono text-sm text-destructive">{String(error)}</p>
      )}
      {isPending && !error && (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      )}

      {data && repos.length === 0 && (
        <EmptyState
          className="min-h-[300px]"
          message="No lessons learned yet — the herd is still young. They appear here once a trau loop records what it learned while repairing a run."
        />
      )}

      {active && (
        <LessonList
          repo={active}
          repos={repos.map((r) => r.name)}
          onRepoChange={setSelected}
        />
      )}
    </div>
  );
}

function LessonList({
  repo,
  repos,
  onRepoChange,
}: {
  repo: string;
  repos: string[];
  onRepoChange: (repo: string) => void;
}) {
  const { data, error, isPending } = useQuery(lessonsQueryOptions(repo));
  const [query, setQuery] = useState("");

  const lessons = data?.lessons ?? [];
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return lessons;
    return lessons.filter((l) => haystack(l).includes(q));
  }, [lessons, query]);

  return (
    <>
      <div className="flex flex-wrap items-end gap-6">
        <label className="flex flex-col gap-1.5">
          <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            search
          </span>
          <div className="relative w-72 max-w-full">
            <Search
              className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
              aria-hidden="true"
            />
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="search lessons…"
              aria-label="Search lessons"
              autoComplete="off"
              spellCheck={false}
              className="w-full rounded-md border border-border bg-input py-1.5 pl-8 pr-2.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
            />
          </div>
        </label>
        <RepoPicker
          repos={repos}
          value={repo}
          onChange={onRepoChange}
          label="repo"
        />
        {lessons.length > 0 && (
          <span className="ml-auto font-mono text-xs tabular-nums text-muted-foreground">
            {filtered.length} / {lessons.length}
          </span>
        )}
      </div>

      {error && (
        <p className="font-mono text-sm text-destructive">{String(error)}</p>
      )}
      {isPending && !error && (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      )}

      {!isPending && !error && lessons.length === 0 && (
        <EmptyState
          className="min-h-[300px]"
          message={`No lessons recorded for ${repo} yet — the herd is still young.`}
        />
      )}

      {lessons.length > 0 && filtered.length === 0 && (
        <p className="py-8 text-center font-mono text-sm text-muted-foreground">
          No lessons match “{query}”.
        </p>
      )}

      {filtered.length > 0 && (
        <div className="flex flex-col gap-4">
          {filtered.map((lesson, i) => (
            <LessonCard
              key={`${lesson.recorded_at ?? ""}-${lesson.ticket ?? ""}-${i}`}
              lesson={lesson}
            />
          ))}
        </div>
      )}
    </>
  );
}

const RESULT_CHIP: Record<string, string> = {
  repaired: "border-done/40 bg-done/10 text-done",
  quarantined: "border-fail/40 bg-fail/10 text-fail",
};

function LessonCard({ lesson }: { lesson: Lesson }) {
  const [open, setOpen] = useState(false);
  const hasDetail = Boolean(
    lesson.phase || lesson.attempted_fix || (lesson.evidence?.length ?? 0) > 0,
  );
  const title =
    [formatRecordedAt(lesson.recorded_at), lesson.ticket]
      .filter(Boolean)
      .join(" · ") || "lesson";

  return (
    <TerminalCard title={title}>
      <div className="flex flex-col gap-3">
        <p className="text-pretty font-sans text-sm leading-relaxed text-foreground">
          {lesson.lesson}
        </p>

        <div className="flex flex-wrap items-center gap-2">
          {lesson.result && (
            <span
              className={cn(
                "inline-flex items-center rounded-md border px-2 py-0.5 font-mono text-[0.7rem]",
                RESULT_CHIP[lesson.result] ??
                  "border-border bg-secondary/50 text-muted-foreground",
              )}
            >
              {lesson.result}
            </span>
          )}
          {lesson.failure_type && <Chip>{lesson.failure_type}</Chip>}
          {lesson.tags?.map((tag) => (
            <Chip key={tag}>{tag}</Chip>
          ))}

          {hasDetail && (
            <button
              type="button"
              onClick={() => setOpen((o) => !o)}
              aria-expanded={open}
              className="ml-auto inline-flex items-center gap-1 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              {open ? "less" : "details"}
              <ChevronDown
                className={cn(
                  "size-3.5 transition-transform",
                  open && "rotate-180",
                )}
                aria-hidden="true"
              />
            </button>
          )}
        </div>

        {open && hasDetail && (
          <div className="flex flex-col gap-3 border-t border-border/60 pt-3">
            {lesson.phase && <Detail label="phase">{lesson.phase}</Detail>}
            {lesson.attempted_fix && (
              <Detail label="attempted fix">{lesson.attempted_fix}</Detail>
            )}
            {lesson.evidence && lesson.evidence.length > 0 && (
              <div className="flex flex-col gap-1.5">
                <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
                  evidence
                </span>
                <ul className="flex flex-col gap-1 border-l-2 border-border pl-3">
                  {lesson.evidence.map((line, i) => (
                    <li
                      key={i}
                      className="font-mono text-xs text-muted-foreground"
                    >
                      {line}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </div>
    </TerminalCard>
  );
}

function Chip({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center rounded-md border border-border bg-secondary/50 px-2 py-0.5 font-mono text-[0.7rem] text-muted-foreground">
      {children}
    </span>
  );
}

function Detail({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      <p className="text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
        {children}
      </p>
    </div>
  );
}

function haystack(l: Lesson): string {
  return [
    l.lesson,
    l.ticket,
    l.phase,
    l.failure_type,
    l.result,
    l.attempted_fix,
    ...(l.tags ?? []),
    ...(l.evidence ?? []),
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

function formatRecordedAt(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
