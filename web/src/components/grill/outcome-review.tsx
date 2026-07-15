import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  CheckCircle2,
  Eye,
  Loader2,
  MessageCirclePlus,
  Pencil,
  Plus,
  Trash2,
  X,
  XCircle,
} from "lucide-react";

import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  abandonGrill,
  applyGrill,
  diffHasChanges,
  diffLines,
  type DiffLine,
  type GrillApplyResponse,
  type GrillApplyStep,
  type GrillSession,
  type OutcomePayload,
  type SubIssueProposal,
} from "@/lib/grill";
import { issueQueryOptions } from "@/lib/issues";
import { cn } from "@/lib/utils";

export function OutcomeProposal({ outcome }: { outcome: OutcomePayload }) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-info/40 bg-info/5 p-3">
      <div className="flex items-center gap-2">
        <Badge variant="outline">{dispositionLabel(outcome.disposition)}</Badge>
        <span className="text-xs text-muted-foreground">Proposed outcome</span>
      </div>
      {outcome.summary && (
        <p className="whitespace-pre-wrap text-sm text-foreground">
          {outcome.summary}
        </p>
      )}
      {outcome.proposed_description && (
        <details className="text-sm">
          <summary className="cursor-pointer text-xs text-muted-foreground">
            Proposed description
          </summary>
          <div className="mt-2 rounded-md border bg-card px-3 py-2">
            <Markdown>{outcome.proposed_description}</Markdown>
          </div>
        </details>
      )}
    </div>
  );
}

// OutcomeReview is the approve-then-apply gate for a finished session: the proposal
// is shown for review — a rewrite as an old→new diff the user can edit, a
// needs_split or no_change as a plain confirmation — and nothing reaches the tracker
// until Apply. A partial apply keeps the session finished and shows each step so the
// user can retry; a full apply flips the session to applied and refreshes the
// drawer's issue so it leaves the unclear set. A proposal the user is not sold on
// takes a follow-up instead, which reopens the session for another turn.
export function OutcomeReview({
  repo,
  issueId,
  session,
  outcome,
  onSession,
  onApplied,
  onDiscarded,
  onAskFollowUp,
}: {
  repo: string;
  issueId: string;
  session: GrillSession;
  outcome: OutcomePayload;
  onSession: (session: GrillSession) => void;
  onApplied?: () => void;
  onDiscarded?: () => void;
  // onAskFollowUp is what the follow-up button offers; hosts withhold it once the
  // composer it reopens is already showing.
  onAskFollowUp?: () => void;
}) {
  const queryClient = useQueryClient();
  const issue = useQuery(issueQueryOptions(repo, issueId));
  const isRewrite = outcome.disposition === "rewrite";
  const isSplit = outcome.disposition === "split";
  const isCreate = outcome.disposition === "create";
  // A create outcome files an epic when it carries a breakdown, a single issue
  // otherwise.
  const isCreateEpic = isCreate && (outcome.sub_issues?.length ?? 0) > 0;
  const carriesDescription = isRewrite || isSplit || isCreate;
  const carriesSubs = isSplit || isCreateEpic;
  const [title, setTitle] = useState(outcome.title ?? "");
  const [draft, setDraft] = useState(outcome.proposed_description ?? "");
  const [editing, setEditing] = useState(false);
  const [subs, setSubs] = useState<SubIssueDraft[]>(() =>
    toSubDrafts(outcome.sub_issues ?? []),
  );

  // The session's new state rides onSession (and the hub's SSE state frame), so the
  // grill list is left to go stale on its own — invalidating it here would drop the
  // panel's now-settled active session and retrigger GrillPanel's auto-start. Only
  // the issue and board are refreshed, which is what makes the issue leave the
  // unclear set once its triage labels are gone.
  const apply = useMutation({
    mutationFn: () =>
      applyGrill(
        session.id,
        carriesDescription ? draft : "",
        carriesSubs ? toSubIssues(subs) : undefined,
        isCreate ? title.trim() : undefined,
      ),
    onSuccess: (res) => {
      onSession(res.session);
      if (res.applied) {
        void queryClient.invalidateQueries({
          queryKey: ["issue", repo, issueId],
        });
        void queryClient.invalidateQueries({ queryKey: ["backlog", repo] });
        onApplied?.();
      }
    },
  });

  const discard = useMutation({
    mutationFn: () => abandonGrill(session.id),
    onSuccess: (settled) => {
      onSession(settled);
      onDiscarded?.();
    },
  });

  if (session.state === "applied") {
    return <AppliedCard outcome={outcome} steps={apply.data?.steps ?? []} />;
  }

  const failedSteps = apply.data && !apply.data.applied ? apply.data.steps : [];
  const busy = apply.isPending || discard.isPending;
  const splitReady = subsAreComplete(subs);
  const createReady =
    title.trim() !== "" &&
    draft.trim() !== "" &&
    (!isCreateEpic || subsAreComplete(subs));
  const blockApply =
    busy || (isSplit && !splitReady) || (isCreate && !createReady);

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-info/40 bg-info/5 p-3">
      <div className="flex items-center gap-2">
        <Badge variant="outline">{dispositionLabel(outcome.disposition)}</Badge>
        <span className="text-xs text-muted-foreground">
          Review before applying
        </span>
      </div>

      {isRewrite ? (
        <RewriteBody
          current={issue.data?.description ?? ""}
          draft={draft}
          editing={editing}
          loading={issue.isLoading}
          onChange={setDraft}
          onEdit={() => setEditing(true)}
          onPreview={() => setEditing(false)}
        />
      ) : isSplit ? (
        <SplitBody
          current={issue.data?.description ?? ""}
          draft={draft}
          editing={editing}
          loading={issue.isLoading}
          onDraftChange={setDraft}
          onEdit={() => setEditing(true)}
          onPreview={() => setEditing(false)}
          subs={subs}
          onSubsChange={setSubs}
        />
      ) : isCreate ? (
        <CreateBody
          title={title}
          draft={draft}
          editing={editing}
          isEpic={isCreateEpic}
          labels={outcome.labels ?? []}
          subs={subs}
          onTitleChange={setTitle}
          onDraftChange={setDraft}
          onEdit={() => setEditing(true)}
          onPreview={() => setEditing(false)}
          onSubsChange={setSubs}
        />
      ) : (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {outcome.disposition === "no_change"
            ? "No changes are needed. Close this session out — nothing is written to the tracker."
            : "Marks the issue needs-split and posts the summary comment. The description is left unchanged."}
        </p>
      )}

      <SummaryPreview summary={outcome.summary} />

      {failedSteps.length > 0 && <StepList steps={failedSteps} />}

      {apply.error && (
        <p className="text-xs text-destructive">
          {(apply.error as Error).message}
        </p>
      )}
      {discard.error && (
        <p className="text-xs text-destructive">
          {(discard.error as Error).message}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={() => apply.mutate()} disabled={blockApply}>
          {apply.isPending ? <Loader2 className="animate-spin" /> : <Check />}
          {applyLabel(outcome.disposition, apply.data)}
        </Button>
        {onAskFollowUp && (
          <Button
            variant="outline"
            size="sm"
            onClick={onAskFollowUp}
            disabled={busy}
          >
            <MessageCirclePlus />
            Ask a follow-up
          </Button>
        )}
        {outcome.disposition !== "no_change" && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => discard.mutate()}
            disabled={busy}
          >
            {discard.isPending ? (
              <Loader2 className="animate-spin" />
            ) : (
              <Trash2 />
            )}
            Discard
          </Button>
        )}
      </div>
    </div>
  );
}

// SubIssueDraft is the review UI's editable form of a proposed slice. blockedBy
// holds the keys of blocking siblings, not their indices, so adding or removing a
// card never silently rewires a dependency.
interface SubIssueDraft {
  key: string;
  title: string;
  description: string;
  labels: string[];
  blockedBy: string[];
}

let subKeySeq = 0;

function newSubKey(): string {
  subKeySeq += 1;
  return `sub-new-${subKeySeq}`;
}

// toSubDrafts turns the agent's index-referenced proposal into editable cards keyed
// by a stable key, resolving each blocked_by index to the sibling's key and dropping
// any out-of-range or self reference.
function toSubDrafts(proposals: SubIssueProposal[]): SubIssueDraft[] {
  const keys = proposals.map((_, i) => `sub-${i}`);
  return proposals.map((p, i) => ({
    key: keys[i],
    title: p.title,
    description: p.description,
    labels: p.labels ?? [],
    blockedBy: (p.blocked_by ?? [])
      .filter((idx) => idx >= 0 && idx < keys.length && idx !== i)
      .map((idx) => keys[idx]),
  }));
}

// toSubIssues converts the cards back to the wire proposal, resolving each blocking
// key to its current index and trimming the text the hub will validate again.
function toSubIssues(drafts: SubIssueDraft[]): SubIssueProposal[] {
  const indexByKey = new Map(drafts.map((d, i) => [d.key, i]));
  return drafts.map((d, i) => {
    const blocked_by = d.blockedBy
      .map((k) => indexByKey.get(k))
      .filter((idx): idx is number => idx !== undefined && idx !== i);
    const sub: SubIssueProposal = {
      title: d.title.trim(),
      description: d.description.trim(),
    };
    if (d.labels.length > 0) sub.labels = d.labels;
    if (blocked_by.length > 0) sub.blocked_by = blocked_by;
    return sub;
  });
}

function subsAreComplete(subs: SubIssueDraft[]): boolean {
  return (
    subs.length > 0 &&
    subs.every((s) => s.title.trim() !== "" && s.description.trim() !== "")
  );
}

const subInputClass =
  "w-full rounded-md border bg-card px-2 py-1 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50";

// SplitBody is the split review: the parent's epic-framing description shown as an
// editable old→new diff, then the proposed slices as cards the user can edit, add,
// remove, and re-wire before Apply files them.
function SplitBody({
  current,
  draft,
  editing,
  loading,
  onDraftChange,
  onEdit,
  onPreview,
  subs,
  onSubsChange,
}: {
  current: string;
  draft: string;
  editing: boolean;
  loading: boolean;
  onDraftChange: (text: string) => void;
  onEdit: () => void;
  onPreview: () => void;
  subs: SubIssueDraft[];
  onSubsChange: (subs: SubIssueDraft[]) => void;
}) {
  return (
    <div className="flex flex-col gap-3">
      <RewriteBody
        current={current}
        draft={draft}
        editing={editing}
        loading={loading}
        onChange={onDraftChange}
        onEdit={onEdit}
        onPreview={onPreview}
      />
      <SubIssueList subs={subs} onSubsChange={onSubsChange} />
    </div>
  );
}

// CreateBody is the create review: an editable title, the new issue's description
// (edited or previewed as markdown — no diff, since nothing exists to compare
// against), and for an epic the proposed slices as editable cards. A single issue
// shows its proposed labels instead.
function CreateBody({
  title,
  draft,
  editing,
  isEpic,
  labels,
  subs,
  onTitleChange,
  onDraftChange,
  onEdit,
  onPreview,
  onSubsChange,
}: {
  title: string;
  draft: string;
  editing: boolean;
  isEpic: boolean;
  labels: string[];
  subs: SubIssueDraft[];
  onTitleChange: (text: string) => void;
  onDraftChange: (text: string) => void;
  onEdit: () => void;
  onPreview: () => void;
  onSubsChange: (subs: SubIssueDraft[]) => void;
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <span className="text-xs font-medium text-muted-foreground">Title</span>
        <input
          value={title}
          onChange={(e) => onTitleChange(e.target.value)}
          placeholder="Issue title"
          className={subInputClass}
        />
      </div>
      <NewBody
        draft={draft}
        editing={editing}
        onChange={onDraftChange}
        onEdit={onEdit}
        onPreview={onPreview}
      />
      {isEpic ? (
        <SubIssueList subs={subs} onSubsChange={onSubsChange} />
      ) : (
        labels.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-[11px] text-muted-foreground">Labels</span>
            {labels.map((l) => (
              <Badge key={l} variant="secondary">
                {l}
              </Badge>
            ))}
          </div>
        )
      )}
    </div>
  );
}

// SubIssueList is the shared editable list of proposed slices — the split parent's
// children and the create-epic parent's children both use it: add, remove, edit, and
// re-wire the sibling blocking relations before Apply files them.
function SubIssueList({
  subs,
  onSubsChange,
}: {
  subs: SubIssueDraft[];
  onSubsChange: (subs: SubIssueDraft[]) => void;
}) {
  const update = (key: string, patch: Partial<SubIssueDraft>) =>
    onSubsChange(subs.map((s) => (s.key === key ? { ...s, ...patch } : s)));
  const add = () =>
    onSubsChange([
      ...subs,
      {
        key: newSubKey(),
        title: "",
        description: "",
        labels: [],
        blockedBy: [],
      },
    ]);
  const remove = (key: string) =>
    onSubsChange(
      subs
        .filter((s) => s.key !== key)
        .map((s) => ({
          ...s,
          blockedBy: s.blockedBy.filter((k) => k !== key),
        })),
    );
  const toggleDep = (key: string, depKey: string) => {
    const sub = subs.find((s) => s.key === key);
    if (!sub) return;
    const blockedBy = sub.blockedBy.includes(depKey)
      ? sub.blockedBy.filter((k) => k !== depKey)
      : [...sub.blockedBy, depKey];
    update(key, { blockedBy });
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">
          Sub-issues ({subs.length})
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2 text-xs"
          onClick={add}
        >
          <Plus />
          Add
        </Button>
      </div>
      {subs.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          Add at least one sub-issue before applying.
        </p>
      ) : (
        subs.map((sub, i) => (
          <SubIssueCard
            key={sub.key}
            index={i}
            sub={sub}
            siblings={subs}
            onChange={update}
            onRemove={remove}
            onToggleDep={toggleDep}
          />
        ))
      )}
    </div>
  );
}

function SubIssueCard({
  index,
  sub,
  siblings,
  onChange,
  onRemove,
  onToggleDep,
}: {
  index: number;
  sub: SubIssueDraft;
  siblings: SubIssueDraft[];
  onChange: (key: string, patch: Partial<SubIssueDraft>) => void;
  onRemove: (key: string) => void;
  onToggleDep: (key: string, depKey: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-card px-3 py-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">
          Slice {index + 1}
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2 text-xs text-muted-foreground"
          onClick={() => onRemove(sub.key)}
        >
          <X />
          Remove
        </Button>
      </div>
      <input
        value={sub.title}
        onChange={(e) => onChange(sub.key, { title: e.target.value })}
        placeholder="Title"
        className={subInputClass}
      />
      <textarea
        value={sub.description}
        onChange={(e) => onChange(sub.key, { description: e.target.value })}
        rows={3}
        placeholder="Description an agent can implement without guessing"
        className={cn(subInputClass, "min-h-20 resize-y font-mono text-xs")}
      />
      {siblings.length > 1 && (
        <div className="flex flex-col gap-1">
          <span className="text-[11px] text-muted-foreground">Blocked by</span>
          <div className="flex flex-wrap gap-1">
            {siblings.map((other, oi) => {
              if (other.key === sub.key) return null;
              const on = sub.blockedBy.includes(other.key);
              return (
                <button
                  key={other.key}
                  type="button"
                  onClick={() => onToggleDep(sub.key, other.key)}
                  className={cn(
                    "rounded border px-2 py-0.5 text-[11px]",
                    on
                      ? "border-info/50 bg-info/10 text-foreground"
                      : "border-border text-muted-foreground",
                  )}
                >
                  #{oi + 1} {other.title.trim() || "untitled"}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

function RewriteBody({
  current,
  draft,
  editing,
  loading,
  onChange,
  onEdit,
  onPreview,
}: {
  current: string;
  draft: string;
  editing: boolean;
  loading: boolean;
  onChange: (text: string) => void;
  onEdit: () => void;
  onPreview: () => void;
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">
          Description
        </span>
        {editing ? (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={onPreview}
          >
            <Eye />
            Preview diff
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={onEdit}
          >
            <Pencil />
            Edit
          </Button>
        )}
      </div>
      {editing ? (
        <textarea
          value={draft}
          onChange={(e) => onChange(e.target.value)}
          rows={10}
          className="min-h-40 w-full resize-y rounded-md border bg-card px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        />
      ) : loading ? (
        <p className="text-xs text-muted-foreground">
          Loading the current description…
        </p>
      ) : (
        <DiffView current={current} next={draft} />
      )}
    </div>
  );
}

// NewBody shows a created issue's description with an edit/preview toggle. There is
// nothing on the tracker to diff against, so preview renders the draft as markdown
// rather than an old→new diff.
function NewBody({
  draft,
  editing,
  onChange,
  onEdit,
  onPreview,
}: {
  draft: string;
  editing: boolean;
  onChange: (text: string) => void;
  onEdit: () => void;
  onPreview: () => void;
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">
          Description
        </span>
        {editing ? (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={onPreview}
          >
            <Eye />
            Preview
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={onEdit}
          >
            <Pencil />
            Edit
          </Button>
        )}
      </div>
      {editing ? (
        <textarea
          value={draft}
          onChange={(e) => onChange(e.target.value)}
          rows={10}
          className="min-h-40 w-full resize-y rounded-md border bg-card px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        />
      ) : draft.trim() === "" ? (
        <p className="rounded-md border bg-card px-3 py-2 text-xs text-muted-foreground">
          No description yet — add one before applying.
        </p>
      ) : (
        <div className="max-h-72 overflow-auto rounded-md border bg-card px-3 py-2 text-sm">
          <Markdown>{draft}</Markdown>
        </div>
      )}
    </div>
  );
}

export function DiffView({ current, next }: { current: string; next: string }) {
  const lines = diffLines(current, next);
  if (!diffHasChanges(lines)) {
    return (
      <p className="rounded-md border bg-card px-3 py-2 text-xs text-muted-foreground">
        No change from the current description.
      </p>
    );
  }
  return (
    <div className="max-h-72 overflow-auto rounded-md border bg-card py-1 font-mono text-xs leading-relaxed">
      {lines.map((line, i) => (
        <DiffRow key={i} line={line} />
      ))}
    </div>
  );
}

function DiffRow({ line }: { line: DiffLine }) {
  const style =
    line.op === "insert"
      ? "bg-done/10 text-done"
      : line.op === "delete"
        ? "bg-fail/10 text-fail"
        : "text-muted-foreground";
  const sign = line.op === "insert" ? "+" : line.op === "delete" ? "-" : " ";
  return (
    <div className={cn("flex gap-2 px-3 whitespace-pre-wrap", style)}>
      <span aria-hidden="true" className="select-none">
        {sign}
      </span>
      <span className="flex-1 break-words">{line.text || " "}</span>
    </div>
  );
}

function SummaryPreview({ summary }: { summary: string }) {
  const text = summary.trim();
  if (text === "") return null;
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs font-medium text-muted-foreground">
        Summary comment
      </span>
      <div className="rounded-md border bg-card px-3 py-2 text-sm">
        <Markdown>{text}</Markdown>
      </div>
    </div>
  );
}

const STEP_LABELS: Record<string, string> = {
  description: "Description",
  comment: "Summary comment",
  labels: "Labels",
  relations: "Blocking relations",
};

function StepList({ steps }: { steps: GrillApplyStep[] }) {
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-card px-3 py-2">
      {steps.map((step) => {
        const ok = step.status === "ok";
        return (
          <div key={step.step} className="flex items-start gap-2 text-xs">
            {ok ? (
              <Check
                className="mt-0.5 size-3.5 shrink-0 text-done"
                aria-hidden="true"
              />
            ) : (
              <XCircle
                className="mt-0.5 size-3.5 shrink-0 text-fail"
                aria-hidden="true"
              />
            )}
            <div className="flex flex-col gap-0.5">
              <span className={ok ? "text-foreground" : "text-fail"}>
                {STEP_LABELS[step.step] ?? step.step}
              </span>
              {step.error && (
                <span className="text-muted-foreground">{step.error}</span>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function AppliedCard({
  outcome,
  steps,
}: {
  outcome: OutcomePayload;
  steps: GrillApplyStep[];
}) {
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-done/40 bg-done/5 p-3">
      <div className="flex items-center gap-2">
        <CheckCircle2
          className="size-4 shrink-0 text-done"
          aria-hidden="true"
        />
        <p className="text-sm font-medium">Applied</p>
        <Badge variant="outline">{dispositionLabel(outcome.disposition)}</Badge>
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">
        {outcome.disposition === "no_change"
          ? "Session closed out — nothing was written to the tracker."
          : outcome.disposition === "create"
            ? "The new issue was filed on the tracker."
            : "The outcome was written to the tracker. This issue is cleared."}
      </p>
      {steps.length > 0 && <StepList steps={steps} />}
    </div>
  );
}

function applyLabel(disposition: string, result?: GrillApplyResponse): string {
  if (result && !result.applied) return "Retry";
  if (disposition === "no_change") return "Close out";
  if (disposition === "create") return "Create";
  return "Apply";
}

function dispositionLabel(disposition: string): string {
  switch (disposition) {
    case "rewrite":
      return "Rewrite";
    case "split":
      return "Split into epic";
    case "needs_split":
      return "Needs split";
    case "create":
      return "Create";
    case "no_change":
      return "No change";
    default:
      return disposition || "Outcome";
  }
}
