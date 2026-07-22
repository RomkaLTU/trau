import { useQueries, useQuery } from "@tanstack/react-query";

import { type Assignee } from "./assignee";
import { backlogQueryOptions, type BacklogEntry } from "./backlog";
import { DEFAULT_STATE_GROUPS } from "./backlog-filters";
import {
  activeSessionForIssue,
  appliedGrillSessionsQueryOptions,
  GRILLABLE_LABELS,
  grillSessionsQueryOptions,
  isAwaitingAnswer,
  isSettled,
  type GrillSession,
  type GrillState,
  type PregrillResponse,
} from "./grill";

// InboxAttention is why an unclear issue is in the inbox, driven by its active
// grilling session: answer = a question is waiting on the user (waiting/parked/
// stalled), thinking = the agent is mid-turn, open = untouched (no session yet),
// review = a finished proposal awaiting apply, done = applied today. It doubles as
// the queue's sort tier.
export type InboxAttention = "answer" | "thinking" | "open" | "review" | "done";

const ATTENTION_ORDER: Record<InboxAttention, number> = {
  answer: 0,
  thinking: 1,
  open: 2,
  review: 3,
  done: 4,
};

// InboxGroup is a queue rail section. Waiting collects everything still owed a turn
// — questions, untouched issues, and running sessions alike — because they are all
// work the user has yet to finish, in the order they need attention.
export type InboxGroup = "waiting" | "review" | "done";

const GROUP_OF: Record<InboxAttention, InboxGroup> = {
  answer: "waiting",
  thinking: "waiting",
  open: "waiting",
  review: "review",
  done: "done",
};

const GROUP_LABELS: Record<InboxGroup, string> = {
  waiting: "Waiting for you",
  review: "Ready to review",
  done: "Done today",
};

// InboxItem is one queue row. entry is the board issue behind it, absent on a Done
// today row — applying drops the triage labels the board queries key on, so a
// settled session's id and title are all that survive. draft marks an issue-less
// authoring row: a live from-scratch session, or the fresh Draft opened by New issue
// before its first message starts one. A draft's id is a client sentinel, never a
// tracker identifier.
export interface InboxItem {
  id: string;
  title: string;
  entry?: BacklogEntry;
  assignee?: Assignee | null;
  session?: GrillSession;
  attention: InboxAttention;
  draft?: boolean;
}

// NEW_DRAFT_ID is the sentinel id of the fresh, not-yet-started Draft — the row New
// issue opens with an empty thread and a focused composer. It exists only in the
// client until the first message starts an authoring session; the colon keeps it
// clear of any tracker identifier.
export const NEW_DRAFT_ID = "draft:new";

// draftItemId names a live authoring session's row by its session id, keeping it
// distinct from both tracker ids and the fresh Draft's sentinel.
export function draftItemId(sessionId: string): string {
  return `draft:${sessionId}`;
}

// newDraftItem is the fresh Draft row: untouched, awaiting a first message, sorted
// among the day's other open work.
export function newDraftItem(): InboxItem {
  return { id: NEW_DRAFT_ID, title: "", attention: "open", draft: true };
}

export interface InboxGroupView {
  group: InboxGroup;
  label: string;
  items: InboxItem[];
}

export interface InboxCounts {
  total: number;
  // awaiting is the parked-awaiting-answer count — the "a question is waiting for
  // you" figure the nav badge emphasises.
  awaiting: number;
}

// inboxAttention classifies an issue by its active (unsettled) session. No session
// is an untouched issue; the awaiting-answer states surface first.
export function inboxAttention(session?: GrillSession): InboxAttention {
  if (!session) return "open";
  if (isAwaitingAnswer(session.state)) return "answer";
  if (session.state === "finished") return "review";
  return "thinking";
}

// mergeGrillableEntries flattens the per-label backlog pages into one board, keyed
// by id so an issue carrying two triage labels appears once, ordered by workflow
// group then numeric-aware id to match the hub's board ordering.
export function mergeGrillableEntries(
  lists: readonly (readonly BacklogEntry[] | undefined)[],
): BacklogEntry[] {
  const byId = new Map<string, BacklogEntry>();
  for (const list of lists) {
    for (const entry of list ?? []) {
      if (!byId.has(entry.id)) byId.set(entry.id, entry);
    }
  }
  return [...byId.values()].sort(compareEntries);
}

// buildInbox attaches each issue's active session and folds in the repo's live
// authoring drafts, then sorts the board into attention tiers — answer, thinking,
// open, review — newest activity first within a tier, so the freshest work sits
// where the walk-through starts. Timestamp ties keep the canonical board order
// (a stable sort over already-ordered rows).
export function buildInbox(
  entries: readonly BacklogEntry[],
  sessions: GrillSession[] = [],
): InboxItem[] {
  const items = entries.map((entry) => {
    const session = activeSessionForIssue(sessions, entry.id);
    return {
      id: entry.id,
      title: entry.title,
      entry,
      assignee: entry.assignee ?? null,
      session,
      attention: inboxAttention(session),
    };
  });
  return [...items, ...authoringItems(sessions)].sort(
    (a, b) =>
      ATTENTION_ORDER[a.attention] - ATTENTION_ORDER[b.attention] ||
      itemActivity(b) - itemActivity(a),
  );
}

// itemActivity is an item's latest movement as a sortable instant: the
// conversation's last turn when a session exists, otherwise the issue's tracker
// update (falling back to creation). An item with no readable timestamp sorts
// after everything dated.
export function itemActivity(item: InboxItem): number {
  const ts =
    item.session?.updated_at ??
    item.entry?.updated_at ??
    item.entry?.created_at;
  const at = ts ? Date.parse(ts) : Number.NaN;
  return Number.isNaN(at) ? 0 : at;
}

// authoringItems lifts the repo's live, issue-less authoring sessions into draft
// rows — titled by their seed, grouped by the same attention logic as any other
// item. A settled session has filed or discarded its issue and drops out.
export function authoringItems(sessions: readonly GrillSession[]): InboxItem[] {
  return sessions
    .filter((s) => !s.issue_id && !isSettled(s.state))
    .map((s) => ({
      id: draftItemId(s.id),
      title: s.issue_title ?? "",
      session: s,
      attention: inboxAttention(s),
      draft: true,
    }));
}

// isToday reports whether an RFC3339 timestamp falls on now's local calendar day —
// "today" as the person triaging sees it, not as UTC sees it.
export function isToday(ts: string, now: Date): boolean {
  const at = new Date(ts);
  return (
    at.getFullYear() === now.getFullYear() &&
    at.getMonth() === now.getMonth() &&
    at.getDate() === now.getDate()
  );
}

// doneTodayItems is the day's finished triage, newest first: sessions applied today,
// one row per issue. An authoring session has no issue to show, and a re-grilled
// issue keeps only its latest apply.
export function doneTodayItems(
  sessions: readonly GrillSession[],
  now: Date,
): InboxItem[] {
  const seen = new Set<string>();
  const out: InboxItem[] = [];
  for (const session of sessions) {
    const id = session.issue_id;
    if (!id || session.state !== "applied") continue;
    if (!isToday(session.updated_at, now) || seen.has(id)) continue;
    seen.add(id);
    out.push({
      id,
      title: session.issue_title ?? "",
      session,
      attention: "done",
    });
  }
  return out;
}

// inboxGroups lays the rail out in its three fixed sections. Every group renders even
// when empty, so the rail keeps its shape as sessions move between them.
export function inboxGroups(
  items: readonly InboxItem[],
  done: readonly InboxItem[] = [],
): InboxGroupView[] {
  const all = [...items, ...done];
  const groups: InboxGroup[] = ["waiting", "review", "done"];
  return groups.map((group) => ({
    group,
    label: GROUP_LABELS[group],
    items: all.filter((item) => GROUP_OF[item.attention] === group),
  }));
}

export function inboxCounts(items: readonly InboxItem[]): InboxCounts {
  let awaiting = 0;
  for (const item of items) {
    if (item.attention === "answer") awaiting++;
  }
  return { total: items.length, awaiting };
}

// selectedItem resolves the ?issue= selection: a queue row first, then a Done today
// row — an applied session stays openable for reference — falling back to the queue
// head when the id is in neither list.
export function selectedItem(
  items: readonly InboxItem[],
  done: readonly InboxItem[],
  peek: string | null,
): InboxItem | null {
  return (
    items.find((item) => item.id === peek) ??
    done.find((item) => item.id === peek) ??
    items[0] ??
    null
  );
}

// inboxPosition is the zero-based index of an issue in the walk-through, or -1 when
// it has left the queue (e.g. its outcome was just applied).
export function inboxPosition(items: readonly InboxItem[], id: string): number {
  return items.findIndex((item) => item.id === id);
}

// nextIssueId / prevIssueId step the walk-through. next past the last item and prev
// before the first both return null, which the caller reads as "nowhere to go".
export function nextIssueId(
  items: readonly InboxItem[],
  id: string,
): string | null {
  const at = inboxPosition(items, id);
  if (at === -1) return null;
  return items[at + 1]?.id ?? null;
}

export function prevIssueId(
  items: readonly InboxItem[],
  id: string,
): string | null {
  const at = inboxPosition(items, id);
  if (at <= 0) return null;
  return items[at - 1].id;
}

// skipTarget is where Skip lands: the item after id, wrapping to the top so a skipped
// item comes round again rather than being lost, and starting at the first item when
// id has left the queue.
export function skipTarget(
  items: readonly InboxItem[],
  id: string | null,
): string | null {
  if (items.length === 0) return null;
  const at = items.findIndex((item) => item.id === id);
  return items[(at + 1) % items.length].id;
}

// postDeleteTarget advances the same way Skip does, but over every identifier the
// purge took — an epic's children leave the rail with it, so landing on one would
// select a row that is already gone. Null when nothing survived.
export function postDeleteTarget(
  items: readonly InboxItem[],
  deleted: readonly string[],
): string | null {
  const gone = new Set(deleted);
  const at = items.findIndex((item) => gone.has(item.id));
  const ahead = [...items.slice(at + 1), ...items.slice(0, at + 1)];
  return ahead.find((item) => !gone.has(item.id))?.id ?? null;
}

// InboxPillTone mirrors the design system's RunState names, so the session bar can
// style a pill without the model reaching into the component layer.
export type InboxPillTone = "warn" | "active" | "verify" | "success" | "todo";

export interface InboxPill {
  tone: InboxPillTone;
  label: string;
}

// rowSession is the session a queue row reports: the open thread's streamed copy
// when the row is the one on screen — the rail must never trail the session bar it
// sits beside — and the polled list's copy otherwise.
export function rowSession(
  item: InboxItem,
  live: GrillSession | null,
): GrillSession | undefined {
  return live && item.session?.id === live.id ? live : item.session;
}

// inboxPill reads a session from the triager's seat: waiting and parked both mean
// "your turn", and a finished proposal means "review". statePill says what the
// session is doing; this says what the person has to do about it.
export function inboxPill(state: GrillState): InboxPill {
  switch (state) {
    case "running":
      return { tone: "active", label: "thinking" };
    case "waiting":
    case "parked":
      return { tone: "warn", label: "your turn" };
    case "stalled":
      return { tone: "warn", label: "stalled" };
    case "finished":
      return { tone: "verify", label: "review" };
    case "applied":
      return { tone: "success", label: "applied" };
    case "abandoned":
      return { tone: "todo", label: "ended" };
  }
}

const GROUP_ORDER: Record<string, number> = {
  started: 0,
  unstarted: 1,
  backlog: 2,
  unknown: 3,
  done: 4,
  canceled: 5,
};

function compareEntries(a: BacklogEntry, b: BacklogEntry): number {
  const g = (GROUP_ORDER[a.group] ?? 9) - (GROUP_ORDER[b.group] ?? 9);
  return g !== 0 ? g : compareIssueIds(a.id, b.id);
}

// compareIssueIds orders identifiers numerically within a prefix so COD-9 precedes
// COD-100; a non-numeric or mismatched suffix falls back to a plain string compare.
export function compareIssueIds(a: string, b: string): number {
  const [pa, na] = splitId(a);
  const [pb, nb] = splitId(b);
  if (pa !== pb) return pa < pb ? -1 : 1;
  if (Number.isNaN(na) || Number.isNaN(nb)) return a < b ? -1 : a > b ? 1 : 0;
  return na - nb;
}

function splitId(id: string): [string, number] {
  const dash = id.lastIndexOf("-");
  if (dash === -1) return [id, Number.NaN];
  const num = Number(id.slice(dash + 1));
  return Number.isNaN(num) ? [id, Number.NaN] : [id.slice(0, dash + 1), num];
}

// grillableBacklogQueries reuses the backlog endpoint's label filter — one open-
// state query per triage label — instead of duplicating a "grillable" server
// filter. The union is merged client-side by mergeGrillableEntries.
function grillableBacklogQueries(repo: string) {
  const state = [...DEFAULT_STATE_GROUPS].join(",");
  return GRILLABLE_LABELS.map((label) =>
    backlogQueryOptions(repo, { label, state }),
  );
}

export interface InboxData {
  items: InboxItem[];
  isLoading: boolean;
  error: Error | null;
}

// useInbox assembles the triage inbox for a repo: the union of open, triage-labelled
// issues joined to their active grilling sessions. Shared by the page and the nav
// badge; react-query dedupes the underlying fetches.
export function useInbox(repo: string): InboxData {
  const backlogs = useQueries({ queries: grillableBacklogQueries(repo) });
  const sessions = useQuery(grillSessionsQueryOptions(repo));
  const entries = mergeGrillableEntries(backlogs.map((q) => q.data?.items));
  const items = buildInbox(entries, sessions.data?.sessions ?? []);
  const failed = backlogs.find((q) => q.error)?.error ?? sessions.error;
  return {
    items,
    isLoading: backlogs.some((q) => q.isLoading) || sessions.isLoading,
    error: (failed as Error) ?? null,
  };
}

export function useInboxCounts(repo: string): InboxCounts {
  return inboxCounts(useInbox(repo).items);
}

export interface InboxQueue extends InboxData {
  // done is the day's applied sessions. It sits outside items so the nav badge and
  // the walk-through keep counting only what still needs triage.
  done: InboxItem[];
  groups: InboxGroupView[];
}

// useInboxQueue is the workspace's read: the triage queue plus the day's applied
// sessions, grouped for the rail. A failed applied-sessions fetch leaves Done today
// empty rather than failing the page — the queue itself is what the user came for.
export function useInboxQueue(repo: string): InboxQueue {
  const inbox = useInbox(repo);
  const applied = useQuery(appliedGrillSessionsQueryOptions(repo));
  const done = doneTodayItems(applied.data?.sessions ?? [], new Date());
  return { ...inbox, done, groups: inboxGroups(inbox.items, done) };
}

// summarisePregrill turns a pre-grill pass response into a one-line recap for the
// inbox toolbar, naming only the outcomes that occurred.
export function summarisePregrill(res: PregrillResponse): string {
  const counts = {
    question_parked: 0,
    rewrite_drafted: 0,
    clear: 0,
    error: 0,
    skipped: 0,
  };
  for (const r of res.results) counts[r.outcome]++;
  const parts: string[] = [];
  if (counts.question_parked)
    parts.push(
      `${counts.question_parked} question${plural(counts.question_parked)} parked`,
    );
  if (counts.rewrite_drafted)
    parts.push(
      `${counts.rewrite_drafted} rewrite${plural(counts.rewrite_drafted)} drafted`,
    );
  if (counts.clear) parts.push(`${counts.clear} already clear`);
  if (counts.error) parts.push(`${counts.error} error${plural(counts.error)}`);
  if (counts.skipped) parts.push(`${counts.skipped} skipped`);
  return parts.length > 0
    ? `Ask ahead: ${parts.join(" · ")}`
    : "Ask ahead: nothing to do.";
}

function plural(n: number): string {
  return n === 1 ? "" : "s";
}
