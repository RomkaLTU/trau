import {
  Fragment,
  useCallback,
  useEffect,
  useState,
  type ReactNode,
  type Ref,
} from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { parseAsString, useQueryState, useQueryStates } from "nuqs";
import {
  Archive,
  ArchiveRestore,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronsUpDown,
  CornerDownRight,
  FilePlus,
  ListFilter,
  ListPlus,
  Pencil,
  Search,
  Sparkles,
  Tag,
  Users,
} from "lucide-react";

import {
  AssigneeAvatar,
  PageHeader,
  ProjectScopeGate,
  RepoHealthGate,
  useActiveRepo,
} from "@/components/trau";
import {
  SegmentedControl,
  type SegmentOption,
} from "@/components/trau/segmented-control";
import { ArchiveToast } from "@/components/archive-toast";
import { CreatedToast } from "@/components/created-toast";
import { InternalIssueForm } from "@/components/internal-issue-form";
import { IssueDrawer } from "@/components/issue-drawer";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { assigneeLabel } from "@/lib/assignee";
import { assigneesQueryOptions, type AssigneeFacet } from "@/lib/assignees";
import {
  backlogQueryOptions,
  backlogSections,
  hiddenStateGroups,
  nestBacklogRows,
  STATE_GROUPS,
  type BacklogEntry,
} from "@/lib/backlog";
import { loadExpandedEpics, storeExpandedEpics } from "@/lib/backlog-expanded";
import {
  backlogFilterParsers,
  backlogParamsFromFilters,
  effectiveStateGroups,
  hasActiveFilters,
  toggleStateGroup,
} from "@/lib/backlog-filters";
import { archiveToastMessage, useArchiveIssue } from "@/lib/archive";
import { NEW_DRAFT_ID } from "@/lib/inbox";
import { internalIssueQueryOptions, type InternalIssue } from "@/lib/issues";
import { labelsQueryOptions } from "@/lib/labels";
import { standardTitle, usePageTitle } from "@/lib/page-title";
import {
  enqueueFresh,
  publishQueue,
  queueActiveIds,
  queueQueryOptions,
} from "@/lib/queue";
import { cn } from "@/lib/utils";

interface BacklogSearch {
  issue?: string;
  created?: string;
}

export const Route = createFileRoute("/backlog")({
  component: BacklogPage,
  // issue opens the drawer over the list; created lands a freshly filed issue with its
  // row highlighted instead. Both are read at runtime through nuqs, typed here so the
  // inbox can link into them.
  validateSearch: (search: Record<string, unknown>): BacklogSearch => {
    const out: BacklogSearch = {};
    if (typeof search.issue === "string" && search.issue !== "")
      out.issue = search.issue;
    if (typeof search.created === "string" && search.created !== "")
      out.created = search.created;
    return out;
  },
});

const PAGE_SIZE = 50;

type SourceFilter = "all" | "internal" | "synced";

const SOURCE_OPTIONS: readonly SegmentOption<SourceFilter>[] = [
  { value: "all", label: "All" },
  { value: "internal", label: "Internal" },
  { value: "synced", label: "Synced" },
];

function useExpandedEpics(repo: string) {
  const [expanded, setExpanded] = useState<Set<string>>(() =>
    loadExpandedEpics(repo),
  );

  useEffect(() => setExpanded(loadExpandedEpics(repo)), [repo]);

  const toggle = useCallback(
    (id: string) => {
      setExpanded((cur) => {
        const next = new Set(cur);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        storeExpandedEpics(repo, next);
        return next;
      });
    },
    [repo],
  );

  return { expanded, toggle };
}

function BacklogPage() {
  usePageTitle(standardTitle("Backlog"));
  const { repo: activeRepo } = useActiveRepo();
  const repo = activeRepo ?? "";
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [created, setCreated] = useState<InternalIssue | null>(null);
  // A create applied in the inbox lands here as ?created=<id>: the highlighted row is
  // the whole confirmation, no drawer and no toast. The param is consumed into state
  // and dropped, so a refresh or a Back never re-highlights.
  const [landed, setLanded] = useState<string | null>(null);
  const [archiveNote, setArchiveNote] = useState<string | null>(null);
  const { expanded, toggle } = useExpandedEpics(repo);

  const [filters, setFilters] = useQueryStates(backlogFilterParsers, {
    history: "push",
  });
  const { q, state, label, assignee, source, archived, page } = filters;

  // The peeked issue is its own history entry so browser Back closes the drawer
  // without unwinding a filter change; a cold ?issue= link opens it over the list.
  const [peek, setPeek] = useQueryState(
    "issue",
    parseAsString.withOptions({ history: "push" }),
  );

  const [arrival, setArrival] = useQueryState("created", parseAsString);

  const [text, setText] = useState(q);

  useEffect(() => setText(q), [q]);

  useEffect(() => {
    const id = setTimeout(() => {
      const next = text.trim();
      if (next !== q)
        setFilters({ q: next, page: null }, { history: "replace" });
    }, 150);
    return () => clearTimeout(id);
  }, [text, q, setFilters]);

  useEffect(() => {
    if (!created) return;
    const id = setTimeout(() => setCreated(null), 8000);
    return () => clearTimeout(id);
  }, [created]);

  useEffect(() => {
    if (!arrival) return;
    setLanded(arrival);
    void setArrival(null, { history: "replace" });
  }, [arrival, setArrival]);

  useEffect(() => {
    if (!landed) return;
    const id = setTimeout(() => setLanded(null), 8000);
    return () => clearTimeout(id);
  }, [landed]);

  useEffect(() => {
    if (!archiveNote) return;
    const id = setTimeout(() => setArchiveNote(null), 6000);
    return () => clearTimeout(id);
  }, [archiveNote]);

  const backlog = useQuery(
    backlogQueryOptions(repo, backlogParamsFromFilters(filters, PAGE_SIZE)),
  );
  const queue = useQuery(queueQueryOptions(repo));
  const queued = queueActiveIds(queue.data?.items ?? []);
  const items = backlog.data?.items ?? [];
  const counts = backlog.data?.counts ?? {};
  const total = backlog.data?.total ?? 0;
  const archivedCount = backlog.data?.archived_count ?? 0;
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const hasFilters = hasActiveFilters(filters);
  const sections = backlogSections(
    items,
    counts,
    effectiveStateGroups(state),
    (page - 1) * PAGE_SIZE,
  );
  const hidden = hiddenStateGroups(counts, effectiveStateGroups(state));

  // The board refetches on mount, so the landed row appears only once that read
  // resolves; a ref fires on mount and no-ops when the id never shows up at all.
  const revealLanded = useCallback((node: HTMLLIElement | null) => {
    node?.scrollIntoView({ block: "center" });
  }, []);

  const highlighted = created?.id ?? landed;

  const renderRow = (
    entry: BacklogEntry,
    extra?: { nested?: boolean; expanded?: boolean; onToggle?: () => void },
  ) => (
    <BacklogRow
      key={entry.id}
      repo={repo}
      entry={entry}
      editing={editing === entry.id}
      inQueue={queued.has(entry.id)}
      highlight={highlighted === entry.id}
      rowRef={landed === entry.id ? revealLanded : undefined}
      archivedView={archived}
      nested={extra?.nested}
      expanded={extra?.expanded}
      onToggle={extra?.onToggle}
      onArchived={(id, done, removed) =>
        setArchiveNote(archiveToastMessage(id, done, removed))
      }
      onOpen={() => void setPeek(entry.id)}
      onOpenParent={(id) => void setPeek(id)}
      onToggleEdit={() =>
        setEditing((cur) => (cur === entry.id ? null : entry.id))
      }
      onEditDone={() => setEditing(null)}
    />
  );

  return (
    <ProjectScopeGate action="manage the backlog">
      <PageHeader
        eyebrow={repo || "backlog"}
        title="Backlog"
        description={
          archived
            ? "Archived issues — unarchive any to return it to the board."
            : "In-progress, todo and backlog work — done and canceled are hidden until you filter for them."
        }
        actions={
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => {
                setEditing(null);
                setCreating((v) => !v);
              }}
              className="inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-sm text-foreground transition-colors hover:bg-muted"
            >
              <FilePlus className="size-4" />
              New internal
            </button>
            <Link
              to="/inbox"
              search={{ issue: NEW_DRAFT_ID }}
              className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground transition-opacity hover:opacity-90"
            >
              <Sparkles className="size-4" />
              New issue
            </Link>
          </div>
        }
      />

      <RepoHealthGate>
        <div className="flex flex-col gap-4 px-8 py-6">
          {creating && (
            <InternalIssueForm
              repo={repo}
              onDone={(saved) => {
                setCreating(false);
                setCreated(saved);
              }}
              onCancel={() => setCreating(false)}
            />
          )}

          <div className="flex flex-wrap items-center gap-3">
            <div className="flex min-w-56 flex-1 items-center gap-2 rounded-md border border-border bg-input px-2.5 py-1.5">
              <Search className="size-4 shrink-0 text-muted-foreground" />
              <input
                type="text"
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder="Search id or title…"
                className="w-full bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
              />
            </div>
            <StateFilter
              value={state}
              onChange={(next) =>
                setFilters({ state: next.length ? next : null, page: null })
              }
            />
            <LabelFilter
              repo={repo}
              value={label}
              onChange={(next) =>
                setFilters({ label: next || null, page: null })
              }
            />
            <AssigneeFilter
              repo={repo}
              value={assignee}
              onChange={(next) =>
                setFilters({ assignee: next || null, page: null })
              }
            />
            <SegmentedControl
              aria-label="Source"
              options={SOURCE_OPTIONS}
              value={source ?? "all"}
              onChange={(v) =>
                setFilters({ source: v === "all" ? null : v, page: null })
              }
            />
            {(archived || archivedCount > 0) && (
              <Button
                type="button"
                variant={archived ? "default" : "outline"}
                size="sm"
                className="h-9"
                aria-pressed={archived}
                onClick={() =>
                  setFilters({ archived: archived ? null : true, page: null })
                }
              >
                <Archive />
                Archived
                <Badge
                  variant={archived ? "outline" : "secondary"}
                  className="ml-0.5 tabular-nums"
                >
                  {archivedCount}
                </Badge>
              </Button>
            )}
          </div>

          {backlog.isLoading && (
            <p className="text-sm text-muted-foreground">Loading backlog…</p>
          )}
          {backlog.error && (
            <p className="text-sm text-destructive">
              {String((backlog.error as Error).message)}
            </p>
          )}

          {backlog.data && (
            <div className="flex flex-col gap-6">
              {sections.map((section) => (
                <section key={section.group} className="flex flex-col gap-2">
                  {!section.continuation && (
                    <div className="flex items-baseline gap-1.5 px-1">
                      <h2 className="text-sm font-semibold text-foreground">
                        {section.label}
                      </h2>
                      <span aria-hidden className="text-muted-foreground/50">
                        ·
                      </span>
                      <span className="text-xs tabular-nums text-muted-foreground">
                        {section.count}
                      </span>
                    </div>
                  )}
                  <ul className="flex flex-col gap-2">
                    {nestBacklogRows(section.items).map((node) =>
                      node.kind === "epic" ? (
                        <Fragment key={node.entry.id}>
                          {renderRow(node.entry, {
                            expanded: expanded.has(node.entry.id),
                            onToggle: () => toggle(node.entry.id),
                          })}
                          {expanded.has(node.entry.id) && (
                            <EpicChildren
                              repo={repo}
                              epicId={node.entry.id}
                              archived={archived}
                              fallback={node.children}
                              renderRow={renderRow}
                            />
                          )}
                        </Fragment>
                      ) : (
                        renderRow(node.entry)
                      ),
                    )}
                  </ul>
                </section>
              ))}

              {items.length === 0 && (
                <p className="rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
                  {hasFilters
                    ? "No issues match these filters."
                    : "No issues yet — create one to get started."}
                </p>
              )}

              {hidden.length > 0 && (
                <p className="px-1 text-xs text-muted-foreground">
                  {hidden.map((h, i) => (
                    <Fragment key={h.group}>
                      {i > 0 && (
                        <span className="px-1 text-muted-foreground/50">·</span>
                      )}
                      <button
                        type="button"
                        onClick={() =>
                          setFilters({ state: [h.group], page: null })
                        }
                        className="tabular-nums underline-offset-2 transition-colors hover:text-foreground hover:underline"
                      >
                        {h.count} {h.group}
                      </button>
                    </Fragment>
                  ))}
                  {" hidden"}
                </p>
              )}
            </div>
          )}

          {total > PAGE_SIZE && (
            <div className="flex items-center justify-between pt-1">
              <p className="text-xs text-muted-foreground">
                Showing {(page - 1) * PAGE_SIZE + 1}–
                {Math.min(page * PAGE_SIZE, total)} of {total}
              </p>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setFilters({ page: Math.max(1, page - 1) })}
                  disabled={page <= 1}
                >
                  Previous
                </Button>
                <span className="text-xs text-muted-foreground">
                  Page {page} of {pageCount}
                </span>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setFilters({ page: Math.min(pageCount, page + 1) })
                  }
                  disabled={page >= pageCount}
                >
                  Next
                </Button>
              </div>
            </div>
          )}
        </div>
      </RepoHealthGate>

      {created && (
        <CreatedToast
          id={created.id}
          title={created.title}
          onView={() => {
            void setPeek(created.id);
            setCreated(null);
          }}
          onDismiss={() => setCreated(null)}
        />
      )}

      {archiveNote && (
        <ArchiveToast
          message={archiveNote}
          onDismiss={() => setArchiveNote(null)}
        />
      )}

      <IssueDrawer
        repo={repo}
        issueId={peek}
        onOpenChange={(open) => {
          if (!open) void setPeek(null);
        }}
        onSelectIssue={(id) => void setPeek(id)}
      />
    </ProjectScopeGate>
  );
}

function StateFilter({
  value,
  onChange,
}: {
  value: string[];
  onChange: (next: string[]) => void;
}) {
  const [open, setOpen] = useState(false);
  const selected = new Set(value);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className="h-9" aria-label="State">
          <ListFilter className="text-muted-foreground" />
          State
          {value.length > 0 && (
            <Badge variant="secondary" className="ml-0.5 tabular-nums">
              {value.length}
            </Badge>
          )}
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-52 p-0">
        <Command>
          <CommandInput placeholder="Filter states…" />
          <CommandList>
            <CommandEmpty>No states.</CommandEmpty>
            <CommandGroup>
              {STATE_GROUPS.map((group) => {
                const active = selected.has(group);
                return (
                  <CommandItem
                    key={group}
                    value={group}
                    onSelect={() => onChange(toggleStateGroup(value, group))}
                  >
                    <span
                      className={cn(
                        "flex size-4 items-center justify-center rounded-[4px] border",
                        active
                          ? "border-primary bg-primary text-primary-foreground"
                          : "border-border",
                      )}
                    >
                      {active && (
                        <Check className="size-3 text-primary-foreground" />
                      )}
                    </span>
                    {group}
                  </CommandItem>
                );
              })}
            </CommandGroup>
            {value.length > 0 && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem
                    onSelect={() => onChange([])}
                    className="justify-center text-center text-muted-foreground"
                  >
                    Clear states
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function LabelFilter({
  repo,
  value,
  onChange,
}: {
  repo: string;
  value: string;
  onChange: (next: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const labels = useQuery(labelsQueryOptions(repo));
  const facets = labels.data?.labels ?? [];

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="h-9 w-48 justify-between"
          aria-label="Label"
        >
          <span className="flex min-w-0 items-center gap-1.5">
            <Tag className="text-muted-foreground" />
            <span className={cn("truncate", !value && "text-muted-foreground")}>
              {value || "Label"}
            </span>
          </span>
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-56 p-0">
        <Command>
          <CommandInput placeholder="Search labels…" />
          <CommandList>
            <CommandEmpty>
              {labels.isLoading ? "Loading labels…" : "No labels found."}
            </CommandEmpty>
            <CommandGroup>
              {facets.map((facet) => (
                <CommandItem
                  key={facet.name}
                  value={facet.name}
                  onSelect={() => {
                    onChange(facet.name === value ? "" : facet.name);
                    setOpen(false);
                  }}
                >
                  <Check
                    className={cn(
                      "size-4",
                      value === facet.name ? "opacity-100" : "opacity-0",
                    )}
                  />
                  <span className="flex-1 truncate">{facet.name}</span>
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {facet.count}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
            {value && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem
                    onSelect={() => {
                      onChange("");
                      setOpen(false);
                    }}
                    className="justify-center text-center text-muted-foreground"
                  >
                    Clear label
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function assigneeFilterLabel(value: string, facets: AssigneeFacet[]): string {
  if (value === 'me') return 'Me'
  if (value === 'unassigned') return 'Unassigned'
  return facets.find((f) => f.id === value)?.name ?? value
}

function AssigneeOption({
  facet,
  active,
  onSelect,
}: {
  facet: AssigneeFacet
  active: boolean
  onSelect: () => void
}) {
  const label = assigneeLabel(facet)
  return (
    <CommandItem value={label} onSelect={onSelect}>
      <Check className={cn('size-4', active ? 'opacity-100' : 'opacity-0')} />
      <AssigneeAvatar assignee={facet} className="size-5 text-[0.6rem]" />
      <span className="flex-1 truncate">{label}</span>
      <span className="text-xs text-muted-foreground tabular-nums">
        {facet.count}
      </span>
    </CommandItem>
  )
}

function AssigneeFilter({
  repo,
  value,
  onChange,
}: {
  repo: string
  value: string
  onChange: (next: string) => void
}) {
  const [open, setOpen] = useState(false)
  const assignees = useQuery(assigneesQueryOptions(repo))
  const facets = assignees.data?.assignees ?? []
  const unassigned = assignees.data?.unassigned ?? 0

  const select = (next: string) => {
    onChange(next === value ? '' : next)
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="h-9 w-48 justify-between"
          aria-label="Assignee"
        >
          <span className="flex min-w-0 items-center gap-1.5">
            <Users className="text-muted-foreground" />
            <span className={cn('truncate', !value && 'text-muted-foreground')}>
              {value ? `Assignee: ${assigneeFilterLabel(value, facets)}` : 'Assignee'}
            </span>
          </span>
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-60 p-0">
        <Command>
          <CommandInput placeholder="Search assignees…" />
          <CommandList>
            <CommandEmpty>
              {assignees.isLoading
                ? 'Loading assignees…'
                : 'No assignees found.'}
            </CommandEmpty>
            <CommandGroup>
              {facets
                .filter((facet) => facet.me)
                .map((facet) => (
                  <AssigneeOption
                    key="me"
                    facet={facet}
                    active={value === 'me'}
                    onSelect={() => select('me')}
                  />
                ))}
              {unassigned > 0 && (
                <CommandItem
                  value="Unassigned"
                  onSelect={() => select('unassigned')}
                >
                  <Check
                    className={cn(
                      'size-4',
                      value === 'unassigned' ? 'opacity-100' : 'opacity-0',
                    )}
                  />
                  <span className="flex-1 truncate">Unassigned</span>
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {unassigned}
                  </span>
                </CommandItem>
              )}
              {facets
                .filter((facet) => !facet.me)
                .map((facet) => (
                  <AssigneeOption
                    key={facet.id}
                    facet={facet}
                    active={value === facet.id}
                    onSelect={() => select(facet.id)}
                  />
                ))}
            </CommandGroup>
            {value && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem
                    onSelect={() => {
                      onChange('')
                      setOpen(false)
                    }}
                    className="justify-center text-center text-muted-foreground"
                  >
                    Clear assignee
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

function EpicChildren({
  repo,
  epicId,
  archived,
  fallback,
  renderRow,
}: {
  repo: string;
  epicId: string;
  archived: boolean;
  fallback: BacklogEntry[];
  renderRow: (entry: BacklogEntry, extra?: { nested?: boolean }) => ReactNode;
}) {
  const children = useQuery(
    backlogQueryOptions(repo, { parent: epicId, archived }),
  );
  const rows = children.data?.items ?? fallback;
  return <>{rows.map((child) => renderRow(child, { nested: true }))}</>;
}

function BacklogRow({
  repo,
  entry,
  editing,
  inQueue,
  highlight = false,
  rowRef,
  archivedView,
  nested = false,
  expanded,
  onArchived,
  onOpen,
  onOpenParent,
  onToggle,
  onToggleEdit,
  onEditDone,
}: {
  repo: string;
  entry: BacklogEntry;
  editing: boolean;
  inQueue: boolean;
  highlight?: boolean;
  rowRef?: Ref<HTMLLIElement>;
  archivedView: boolean;
  nested?: boolean;
  expanded?: boolean;
  onArchived: (id: string, archived: boolean, queueRemoved: number) => void;
  onOpen: () => void;
  onOpenParent: (id: string) => void;
  onToggle?: () => void;
  onToggleEdit: () => void;
  onEditDone: () => void;
}) {
  const queryClient = useQueryClient();
  const internal = entry.source === "internal";
  const isEpic = entry.has_children && !nested;
  const { children_settled: settled, children_total: total } = entry;
  const issueQuery = useQuery({
    ...internalIssueQueryOptions(repo, entry.id),
    enabled: editing && internal,
  });
  const addToQueue = useMutation({
    mutationFn: () => enqueueFresh(repo, { id: entry.id }),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
  });
  const archive = useArchiveIssue(repo, (result, vars) =>
    onArchived(vars.id, vars.archived, result.queue_removed),
  );
  // A family child carries no archive stamp of its own — only its epic can be
  // unarchived — so the Unarchive action stays off the nested rows in the
  // archived view.
  const showArchiveAction = archivedView ? !nested : true;

  return (
    <li
      ref={rowRef}
      className={cn(
        "group rounded-lg border bg-card transition-colors hover:border-ring/40",
        nested && "ml-6",
        highlight && "border-primary/60 bg-primary/5",
      )}
    >
      <div className="flex flex-wrap items-center gap-3 px-4 py-3">
        {isEpic && (total ?? 0) > 0 && (
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={
              expanded ? `Collapse ${entry.id}` : `Expand ${entry.id}`
            }
            className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            {expanded ? (
              <ChevronDown className="size-4" aria-hidden />
            ) : (
              <ChevronRight className="size-4" aria-hidden />
            )}
          </button>
        )}
        {!nested && entry.parent && (
          <button
            type="button"
            onClick={() => onOpenParent(entry.parent!)}
            aria-label={`Open epic ${entry.parent}`}
            className="inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-xs text-muted-foreground transition-colors hover:border-ring/40 hover:text-foreground"
          >
            <CornerDownRight className="size-3" aria-hidden />
            {entry.parent}
          </button>
        )}
        <button
          type="button"
          onClick={onOpen}
          aria-label={`Open ${entry.id}`}
          className="flex min-w-0 flex-1 items-center gap-3 text-left"
        >
          <span className="font-mono text-sm font-medium text-foreground">
            {entry.id}
          </span>
          <span className="min-w-0 flex-1 truncate text-sm text-foreground">
            {entry.title}
          </span>
          {isEpic && settled != null && total != null && (
            <span
              className="inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-xs text-muted-foreground"
              aria-label={`${settled} of ${total} settled`}
            >
              <span aria-hidden>◑</span>
              <span className="tabular-nums">
                {settled}/{total}
              </span>
            </span>
          )}
          {entry.ready && (
            <span className="rounded-full border border-emerald-500/40 bg-emerald-500/5 px-2 py-0.5 text-xs text-emerald-600 dark:text-emerald-400">
              ready
            </span>
          )}
          {inQueue && (
            <span className="rounded-full border border-sky-500/40 bg-sky-500/5 px-2 py-0.5 text-xs text-sky-600 dark:text-sky-400">
              queued
            </span>
          )}
          <span className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
            {entry.group}
          </span>
          <span
            className={cn(
              "rounded-full px-2 py-0.5 font-mono text-xs",
              internal
                ? "border border-primary/40 bg-primary/5 text-primary"
                : "border text-muted-foreground",
            )}
          >
            {entry.source}
          </span>
          {entry.assignee && <AssigneeAvatar assignee={entry.assignee} />}
        </button>
        {internal && (
          <button
            type="button"
            onClick={onToggleEdit}
            className="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <Pencil className="size-3.5" />
            Edit
          </button>
        )}
        {!archivedView && (
          <button
            type="button"
            onClick={() => addToQueue.mutate()}
            disabled={inQueue || addToQueue.isPending}
            className="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
          >
            <ListPlus className="size-3.5" />
            {inQueue ? "Queued" : "Add to queue"}
          </button>
        )}
        {showArchiveAction && (
          <button
            type="button"
            onClick={() =>
              archive.mutate({ id: entry.id, archived: !archivedView })
            }
            disabled={archive.isPending}
            aria-label={
              archivedView ? `Unarchive ${entry.id}` : `Archive ${entry.id}`
            }
            title={archivedView ? "Unarchive" : "Archive"}
            className="inline-flex size-7 shrink-0 items-center justify-center rounded-md border text-muted-foreground opacity-0 transition-colors hover:text-foreground focus-visible:opacity-100 disabled:opacity-50 group-hover:opacity-100"
          >
            {archivedView ? (
              <ArchiveRestore className="size-3.5" aria-hidden />
            ) : (
              <Archive className="size-3.5" aria-hidden />
            )}
          </button>
        )}
      </div>
      {addToQueue.error && (
        <p className="px-4 pb-2 text-xs text-destructive">
          {String((addToQueue.error as Error).message)}
        </p>
      )}
      {archive.error && (
        <p className="px-4 pb-2 text-xs text-destructive">
          {String((archive.error as Error).message)}
        </p>
      )}
      {editing && internal && issueQuery.data && (
        <div className="border-t px-4 py-3">
          <InternalIssueForm
            repo={repo}
            issue={issueQuery.data}
            onDone={onEditDone}
            onCancel={onEditDone}
          />
        </div>
      )}
    </li>
  );
}
