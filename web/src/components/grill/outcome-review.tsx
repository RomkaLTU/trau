import { useEffect, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  Check,
  CheckCircle2,
  ChevronDown,
  Eye,
  Inbox,
  ListPlus,
  Loader2,
  MessageCirclePlus,
  Pencil,
  Plus,
  RotateCcw,
  Trash2,
  TriangleAlert,
  X,
  XCircle,
} from "lucide-react";

import { Markdown } from "@/components/markdown";
import { AssigneePicker } from "@/components/trau/assignee-picker";
import { useCreatedBanner } from "@/components/trau/created-banner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { type Assignee } from "@/lib/assignee";
import { assignableUsersQueryOptions } from "@/lib/assignees";
import {
  azureCreateOptionsQueryOptions,
  type AzureCreateOptions,
} from "@/lib/azure";
import {
  abandonGrill,
  applyGrill,
  chooseGrillProposal,
  diffHasChanges,
  diffLines,
  grillAppliedOutcome,
  grillSessionsQueryOptions,
  isApplyInProgress,
  type DiffLine,
  type GrillApplyResponse,
  type GrillApplyStep,
  type GrillAppliedOutcome,
  type GrillDestination,
  type GrillDisagreement,
  type GrillMessage,
  type GrillMode,
  type GrillSession,
  type OutcomePayload,
  type SessionProposal,
  type SubIssueProposal,
} from "@/lib/grill";
import { issueQueryOptions } from "@/lib/issues";
import { enqueueOnce, publishQueue, type EnqueueRequest } from "@/lib/queue";
import { requeueRun } from "@/lib/rundetail";
import { cn } from "@/lib/utils";

export const noReport = "This session recorded no report.";

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
      {outcome.disposition === "research" && (
        <details className="text-sm">
          <summary className="cursor-pointer text-xs text-muted-foreground">
            Research report
          </summary>
          <div className="mt-2 rounded-md border bg-card px-3 py-2">
            {outcome.findings?.trim() ? (
              <Markdown>{outcome.findings}</Markdown>
            ) : (
              <p className="text-xs text-muted-foreground">{noReport}</p>
            )}
          </div>
        </details>
      )}
    </div>
  );
}

// ProposalChoice is the second-opinion review: every participant's draft outcome side
// by side, labeled by the provider that wrote it and each shown in its disposition's
// own shape. Nothing here is editable and nothing reaches the tracker — picking one
// promotes it to the session's outcome, and the ordinary editable review and Apply
// take over from there unchanged.
export function ProposalChoice({
  session,
  proposals,
  onChosen,
}: {
  session: GrillSession;
  proposals: SessionProposal[];
  onChosen: (message: GrillMessage) => void;
}) {
  const [picked, setPicked] = useState<string | null>(null);
  const choose = useMutation({
    mutationFn: (messageId: string) =>
      chooseGrillProposal(session.id, messageId),
    onSuccess: (res) => onChosen(res.message),
  });

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <Badge variant="outline">second opinion</Badge>
        <span className="text-xs text-muted-foreground">
          {proposals.length} proposals — pick the one to review and apply
        </span>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        {proposals.map((proposal) => (
          <div key={proposal.id} className="flex min-w-0 flex-col gap-2">
            <span className="font-mono text-xs text-muted-foreground">
              {proposal.provider || "unknown provider"}
            </span>
            <OutcomeProposal outcome={proposal.outcome} />
            {proposal.challengeNotes.length > 0 && (
              <div className="flex flex-col gap-1 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2">
                <span className="text-xs text-muted-foreground">
                  What it disputes
                </span>
                {proposal.challengeNotes.map((note, i) => (
                  <p key={i} className="whitespace-pre-wrap text-xs">
                    {note}
                  </p>
                ))}
              </div>
            )}
            <Button
              variant="outline"
              size="sm"
              disabled={choose.isPending}
              onClick={() => {
                setPicked(proposal.id);
                choose.mutate(proposal.id);
              }}
            >
              {choose.isPending && picked === proposal.id ? (
                <Loader2 className="animate-spin" />
              ) : (
                <Check />
              )}
              Use this proposal
            </Button>
          </div>
        ))}
      </div>
      {choose.error && (
        <p className="text-xs text-destructive">{choose.error.message}</p>
      )}
    </div>
  );
}

// DisagreementSummary is how a consensus proposal shows its own history: the panel's
// opening split, then each challenge round's endorsements and revisions, so a single
// agreed outcome does not read as one nobody argued about. The hub assembles it from
// the rounds it recorded — no model wrote it.
export function DisagreementSummary({
  summary,
}: {
  summary: GrillDisagreement;
}) {
  const split = summary.initial
    .map((i) => `${i.provider} → ${dispositionLabel(i.disposition)}`)
    .join(", ");

  return (
    <details className="rounded-md border bg-card px-3 py-2 text-sm">
      <summary className="cursor-pointer text-xs text-muted-foreground">
        Panel consensus
        {summary.winner ? ` on ${summary.winner}'s proposal` : ""} —{" "}
        {summary.rounds.length === 1
          ? "1 challenge round"
          : `${summary.rounds.length} challenge rounds`}
      </summary>
      <div className="mt-2 flex flex-col gap-2 text-xs">
        {split !== "" && (
          <p>
            <span className="text-muted-foreground">Opening split: </span>
            {split}
          </p>
        )}
        {summary.rounds.map((round) => (
          <div key={round.round} className="flex flex-col gap-0.5">
            <span className="text-muted-foreground">Round {round.round}</span>
            {round.turns.map((turn, i) => (
              <p key={i} className="whitespace-pre-wrap">
                <span className="font-mono">{turn.provider}</span>{" "}
                {turn.endorse
                  ? `endorsed ${turn.endorse}`
                  : `revised to ${dispositionLabel(turn.disposition ?? "")}`}
                {turn.note ? ` — ${turn.note}` : ""}
              </p>
            ))}
          </div>
        ))}
        {summary.notes.map((note, i) => (
          <p key={i} className="text-amber-600 dark:text-amber-500">
            {note}
          </p>
        ))}
      </div>
    </details>
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
  reportShown = false,
  onSession,
  onApplied,
  onDiscarded,
  onAskFollowUp,
}: {
  repo: string;
  issueId: string;
  session: GrillSession;
  outcome: OutcomePayload;
  // reportShown marks a host already showing the outcome as a document, so the card
  // is the decision alone rather than a second copy of the report and its summary.
  reportShown?: boolean;
  onSession: (session: GrillSession) => void;
  onApplied?: (applied: GrillAppliedOutcome) => void;
  onDiscarded?: () => void;
  // onAskFollowUp is what the follow-up button offers; hosts withhold it once the
  // composer it reopens is already showing.
  onAskFollowUp?: () => void;
}) {
  const queryClient = useQueryClient();
  const { publish } = useCreatedBanner();
  const issue = useQuery(issueQueryOptions(repo, issueId));
  const sessions = useQuery(grillSessionsQueryOptions(repo));
  const tracker = sessions.data?.tracker ?? "";
  const isRewrite = outcome.disposition === "rewrite";
  const isSplit = outcome.disposition === "split";
  const isCreate = outcome.disposition === "create";
  const isResearch = outcome.disposition === "research";
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
  const [destination, setDestination] = useState<GrillDestination>(
    session.issue_destination === "internal" ? "internal" : "tracker",
  );
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [workItemType, setWorkItemType] = useState("");
  const [parent, setParent] = useState("");

  // A rewrite or split writes to the anchor it was opened on, so switching it to
  // the internal store is a conversion of that ticket rather than a filing choice —
  // offered only while the anchor still belongs to the tracker. A create converts
  // the parent an earlier pass filed there, which is the only anchor it owns; the
  // ticket a create was merely opened on is left alone and files beside.
  const anchorSource = issue.data?.source ?? "";
  const rewrites = isRewrite || isSplit;
  const converts =
    rewrites || (isCreate && session.issue_destination === "tracker");
  const detachable =
    converts && anchorSource !== "" && anchorSource !== "internal";
  const anchorInternal = rewrites && anchorSource === "internal";

  // The probe shares its cache entry with the picker's own, so gating the control on
  // it costs nothing and hides it entirely on a tracker with nobody to assign.
  const creates = isCreate || isSplit;
  const assignable = useQuery({
    ...assignableUsersQueryOptions(repo, ""),
    enabled: creates && destination === "tracker",
  });

  // Azure DevOps files a create at requirement level under a Feature the board
  // already has, so the choice is only offered where one exists to make. The
  // create-options answer is cached per repo and shared with every session the
  // panel opens, so the picker rides the query's own gate rather than its success
  // alone — the hub reads the hierarchy on a create and nowhere else.
  const placesInHierarchy =
    isCreate && destination === "tracker" && tracker === "azure";
  const hierarchy = useQuery({
    ...azureCreateOptionsQueryOptions(repo),
    enabled: placesInHierarchy,
  });

  // The queue intent belongs to the card once picked — a retry and the internal
  // fallback carry it too — and rides a ref so the settling apply reads it whether
  // or not the click's own re-render has landed.
  const queueAfterCreate = useRef(false);

  // The create is never rolled back, so an add the queue refused is a caveat on an
  // issue that exists rather than a failed apply — the drawer's Add to queue is the
  // retry path.
  const queueCreated = useMutation({
    mutationFn: (req: EnqueueRequest) => enqueueOnce(repo, req),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
    onError: (err, req) =>
      reportApplyCaveats(
        [],
        [
          `${req.id} was created but adding it to the queue failed: ${err.message}`,
        ],
      ),
  });

  // Losing the hub's guard to another tab is not a failure to report: the card joins
  // that apply in applying mode until the hub's own indicator takes over. The bridge
  // expires on its own, so an apply that settles before the card ever hears it
  // announced leaves a reviewable session rather than a stuck one.
  const [guarded, setGuarded] = useState(false);
  useEffect(() => {
    if (!guarded) return;
    if (session.applying) {
      setGuarded(false);
      return;
    }
    const timer = setTimeout(() => setGuarded(false), guardBridgeMs);
    return () => clearTimeout(timer);
  }, [guarded, session.applying]);

  // The session's new state rides onSession (and the hub's SSE state frame), so the
  // grill list is left to go stale on its own — invalidating it here would drop the
  // panel's now-settled active session back to a preview. Only the issue and board
  // are refreshed, which is what makes the issue leave the unclear set once its
  // triage labels are gone. A create publishes its receipt to the global banner
  // outside the applied gate, so a partial apply the user can still retry leaves a
  // trace of what did land.
  const apply = useMutation({
    mutationFn: (destination: GrillDestination) => {
      const internal = destination === "internal";
      return applyGrill(
        session.id,
        carriesDescription ? draft : "",
        carriesSubs ? toSubIssues(subs) : undefined,
        isCreate ? title.trim() : undefined,
        internal ? destination : undefined,
        internal ? null : assignee,
        internal ? undefined : { workItemType, parent },
      );
    },
    onSuccess: (res) => {
      onSession(res.session);
      const filed = res.session.issue_id ?? "";
      if (isCreate && filed !== "") {
        const filedTitle = res.session.issue_title || title.trim();
        publish({
          repo,
          id: filed,
          title: filedTitle,
          subCount: isCreateEpic ? subs.length : 0,
          failedSteps: res.steps
            .filter((step) => step.status === "failed")
            .map(stepLabel),
        });
        if (queueAfterCreate.current) {
          queueCreated.mutate({
            id: filed,
            kind: isCreateEpic ? "epic" : "ticket",
            title: filedTitle,
          });
        }
      }
      if (res.applied) {
        void queryClient.invalidateQueries({
          queryKey: ["issue", repo, issueId],
        });
        void queryClient.invalidateQueries({ queryKey: ["backlog", repo] });
        reportApplyCaveats(res.steps, res.warnings ?? []);
        onApplied?.(
          grillAppliedOutcome(res, outcome.disposition, title.trim()),
        );
      }
    },
    onError: (err) => setGuarded(isApplyInProgress(err)),
  });

  const discard = useMutation({
    mutationFn: () => abandonGrill(session.id),
    onSuccess: (settled) => {
      onSession(settled);
      onDiscarded?.();
    },
  });

  // A reload and a second tab have the hub's indicator instead of a mutation of their
  // own.
  const applying = apply.isPending || guarded || session.applying === true;
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    if (!applying) {
      setSlow(false);
      return;
    }
    const timer = setTimeout(() => setSlow(true), slowApplyMs);
    return () => clearTimeout(timer);
  }, [applying]);

  // A settled session is the only source the card can trust: the mutation that
  // applied it is gone on a remount, and the host retires the review the moment it
  // lands, so the destination and the caveats are read back off the session the hub
  // stamped rather than off the picker state or the response.
  if (session.state === "applied") {
    return (
      <AppliedCard
        repo={repo}
        issueId={session.issue_id ?? ""}
        mode={session.mode}
        outcome={outcome}
        steps={apply.data?.steps ?? []}
        warnings={session.apply_warnings ?? []}
        internal={session.issue_destination === "internal"}
      />
    );
  }

  const failedSteps = apply.data && !apply.data.applied ? apply.data.steps : [];
  const warnings = apply.data?.warnings ?? [];
  const applyError =
    apply.error && !isApplyInProgress(apply.error) ? apply.error : null;
  const busy = applying || discard.isPending;
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

      {outcome.disagreement && (
        <DisagreementSummary summary={outcome.disagreement} />
      )}

      {isRewrite ? (
        <RewriteBody
          current={issue.data?.description ?? ""}
          draft={draft}
          editing={editing}
          loading={issue.isLoading}
          disabled={busy}
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
          disabled={busy}
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
          disabled={busy}
          onTitleChange={setTitle}
          onDraftChange={setDraft}
          onEdit={() => setEditing(true)}
          onPreview={() => setEditing(false)}
          onSubsChange={setSubs}
        />
      ) : isResearch ? (
        <ResearchBody
          findings={outcome.findings ?? ""}
          anchored={issueId !== ""}
          reportShown={reportShown}
        />
      ) : (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {outcome.disposition === "no_change"
            ? "No changes are needed. Close this session out — nothing is written to the tracker."
            : "Marks the issue needs-split and posts the summary comment. The description is left unchanged."}
        </p>
      )}

      {!reportShown && <SummaryPreview summary={outcome.summary} />}

      {(isCreate || detachable) && tracker !== "" && (
        <DestinationPicker
          tracker={tracker}
          anchor={detachable ? issueId : ""}
          destination={destination}
          disabled={busy}
          onChange={setDestination}
        />
      )}

      {anchorInternal && <InternalAnchorNote anchor={issueId} />}

      {placesInHierarchy && hierarchy.isSuccess && (
        <HierarchyPicker
          options={hierarchy.data}
          workItemType={workItemType}
          parent={parent}
          disabled={busy}
          isEpic={isCreateEpic}
          onTypeChange={setWorkItemType}
          onParentChange={setParent}
        />
      )}

      {creates && destination === "tracker" && assignable.isSuccess && (
        <div className="flex flex-col items-start gap-1">
          <span className="text-xs font-medium text-muted-foreground">
            Assign to
          </span>
          <AssigneePicker
            repo={repo}
            assignee={assignee}
            onSelect={setAssignee}
            disabled={busy}
          />
        </div>
      )}

      {failedSteps.length > 0 && <StepList steps={failedSteps} />}

      {warnings.length > 0 && <WarningList warnings={warnings} />}

      {applyError && (
        <p className="text-xs text-destructive">{applyError.message}</p>
      )}
      {discard.error && (
        <p className="text-xs text-destructive">
          {(discard.error as Error).message}
        </p>
      )}

      {applying && (
        <div
          role="status"
          className="flex items-center gap-2 rounded-md border border-info/50 bg-info/10 px-3 py-2 text-xs font-medium text-info"
        >
          <Loader2
            className="size-3.5 shrink-0 animate-spin"
            aria-hidden="true"
          />
          <span>
            {applyStatusLine({
              disposition: outcome.disposition,
              tracker,
              destination,
              anchor: issueId,
              subCount: isCreateEpic ? subs.length : 0,
              slow,
            })}
          </span>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <div className="flex items-center">
          <Button
            size="sm"
            onClick={() => apply.mutate(destination)}
            disabled={blockApply}
            className={cn(isCreate && "rounded-r-none")}
          >
            {applying ? <Loader2 className="animate-spin" /> : <Check />}
            {applyLabel(outcome.disposition, applying, apply.data)}
          </Button>
          {isCreate && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  size="icon-sm"
                  disabled={blockApply}
                  aria-label="More create actions"
                  className="rounded-l-none border-l border-primary-foreground/25"
                >
                  <ChevronDown />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start">
                <DropdownMenuItem
                  onSelect={() => {
                    queueAfterCreate.current = true;
                    apply.mutate(destination);
                  }}
                >
                  <ListPlus />
                  Create and queue
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
        {(isCreate || detachable) &&
          destination === "tracker" &&
          tracker !== "internal" &&
          failedSteps.length > 0 && (
            <Button
              variant="outline"
              size="sm"
              disabled={blockApply}
              onClick={() => {
                setDestination("internal");
                apply.mutate("internal");
              }}
            >
              <Inbox />
              {isCreate
                ? "File internally instead"
                : "Convert and apply internally"}
            </Button>
          )}
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

const subFieldClass = "h-auto px-2 py-1";

// SplitBody is the split review: the parent's epic-framing description shown as an
// editable old→new diff, then the proposed slices as cards the user can edit, add,
// remove, and re-wire before Apply files them.
function SplitBody({
  current,
  draft,
  editing,
  loading,
  disabled,
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
  disabled: boolean;
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
        disabled={disabled}
        onChange={onDraftChange}
        onEdit={onEdit}
        onPreview={onPreview}
      />
      <SubIssueList
        subs={subs}
        disabled={disabled}
        onSubsChange={onSubsChange}
      />
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
  disabled,
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
  disabled: boolean;
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
        <Input
          value={title}
          onChange={(e) => onTitleChange(e.target.value)}
          placeholder="Issue title"
          disabled={disabled}
          className={subFieldClass}
        />
      </div>
      <NewBody
        draft={draft}
        editing={editing}
        disabled={disabled}
        onChange={onDraftChange}
        onEdit={onEdit}
        onPreview={onPreview}
      />
      {isEpic ? (
        <SubIssueList
          subs={subs}
          disabled={disabled}
          onSubsChange={onSubsChange}
        />
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
  disabled,
  onSubsChange,
}: {
  subs: SubIssueDraft[];
  disabled: boolean;
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
          disabled={disabled}
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
            disabled={disabled}
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
  disabled,
  onChange,
  onRemove,
  onToggleDep,
}: {
  index: number;
  sub: SubIssueDraft;
  siblings: SubIssueDraft[];
  disabled: boolean;
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
          disabled={disabled}
        >
          <X />
          Remove
        </Button>
      </div>
      <Input
        value={sub.title}
        onChange={(e) => onChange(sub.key, { title: e.target.value })}
        placeholder="Title"
        disabled={disabled}
        className={subFieldClass}
      />
      <Textarea
        value={sub.description}
        onChange={(e) => onChange(sub.key, { description: e.target.value })}
        rows={3}
        placeholder="Description an agent can implement without guessing"
        disabled={disabled}
        className={cn(subFieldClass, "min-h-20 resize-y font-mono text-xs")}
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
                  disabled={disabled}
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
  disabled,
  onChange,
  onEdit,
  onPreview,
}: {
  current: string;
  draft: string;
  editing: boolean;
  loading: boolean;
  disabled: boolean;
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
            disabled={disabled}
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
            disabled={disabled}
          >
            <Pencil />
            Edit
          </Button>
        )}
      </div>
      {editing ? (
        <Textarea
          value={draft}
          onChange={(e) => onChange(e.target.value)}
          rows={10}
          disabled={disabled}
          className="min-h-40 resize-y font-mono text-xs"
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
  disabled,
  onChange,
  onEdit,
  onPreview,
}: {
  draft: string;
  editing: boolean;
  disabled: boolean;
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
            disabled={disabled}
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
            disabled={disabled}
          >
            <Pencil />
            Edit
          </Button>
        )}
      </div>
      {editing ? (
        <Textarea
          value={draft}
          onChange={(e) => onChange(e.target.value)}
          rows={10}
          disabled={disabled}
          className="min-h-40 resize-y font-mono text-xs"
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

// ResearchBody renders the report read-only — it is delivered as written. A host
// showing the document itself keeps only the note saying what approving does.
function ResearchBody({
  findings,
  anchored,
  reportShown,
}: {
  findings: string;
  anchored: boolean;
  reportShown: boolean;
}) {
  const report = findings.trim();
  const note = anchored
    ? "Posts the report as a comment on the issue. The description and labels are left unchanged."
    : "Keeps the report on this session — nothing is written to the tracker.";
  if (reportShown) {
    return (
      <p className="text-xs leading-relaxed text-muted-foreground">{note}</p>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      <span className="text-xs font-medium text-muted-foreground">
        Research report
      </span>
      {report === "" ? (
        <p className="rounded-md border bg-card px-3 py-2 text-xs text-muted-foreground">
          {noReport}
        </p>
      ) : (
        <div className="max-h-72 overflow-auto rounded-md border bg-card px-3 py-2 text-sm">
          <Markdown>{report}</Markdown>
        </div>
      )}
      <p className="text-xs leading-relaxed text-muted-foreground">{note}</p>
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

const TRACKER_NAMES: Record<string, string> = {
  jira: "Jira",
  linear: "Linear",
  github: "GitHub",
};

// DestinationPicker is the outcome's destination choice: the repo's external
// tracker — named, and the default — or the hub's internal backlog. anchor names
// the ticket the outcome writes to — a rewrite or split's, or the parent a create
// already filed on the tracker — which the internal option converts rather than
// copies, and is empty for a create with nothing to convert. A repo on the
// internal provider has only one destination, so it is stated rather than offered
// as a fake choice.
function DestinationPicker({
  tracker,
  anchor,
  destination,
  disabled,
  onChange,
}: {
  tracker: string;
  anchor: string;
  destination: GrillDestination;
  disabled: boolean;
  onChange: (destination: GrillDestination) => void;
}) {
  const name = TRACKER_NAMES[tracker] ?? tracker;
  if (tracker === "internal") {
    return anchor === "" ? (
      <DestinationNote>Files to this repo's internal backlog.</DestinationNote>
    ) : (
      <InternalAnchorNote anchor={anchor} />
    );
  }
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs font-medium text-muted-foreground">
        Destination
      </span>
      <div className="flex flex-wrap gap-1">
        <DestinationOption
          label={
            anchor === "" ? `File to ${name}` : `Apply to ${anchor} on ${name}`
          }
          on={destination === "tracker"}
          disabled={disabled}
          onPick={() => onChange("tracker")}
        />
        <DestinationOption
          label={
            anchor === ""
              ? "File internally"
              : `Convert ${anchor} (and its sub-issues) to internal and apply there`
          }
          on={destination === "internal"}
          disabled={disabled}
          onPick={() => onChange("internal")}
        />
      </div>
      {anchor !== "" && destination === "internal" && (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {anchor} and everything under it move into trau's backlog under new
          ids. The {name} tickets keep a note saying so and stop driving trau.
        </p>
      )}
    </div>
  );
}

// HierarchyPicker places an Azure DevOps create in the board's Epic → Feature →
// User Story/Bug → Task hierarchy: the requirement-level type it files as, and the
// Feature it hangs off. Only Features already on the board are offered — trau
// never creates one — and picking none files the work item top-level, leaving the
// re-parenting to Azure DevOps. Its slices become Tasks under it either way.
function HierarchyPicker({
  options,
  workItemType,
  parent,
  disabled,
  isEpic,
  onTypeChange,
  onParentChange,
}: {
  options: AzureCreateOptions;
  workItemType: string;
  parent: string;
  disabled: boolean;
  isEpic: boolean;
  onTypeChange: (type: string) => void;
  onParentChange: (parent: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2">
      {options.types.length > 1 && (
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium text-muted-foreground">
            Work item type
          </span>
          <div className="flex flex-wrap gap-1">
            {options.types.map((type, i) => (
              <DestinationOption
                key={type}
                label={type}
                on={workItemType === type || (workItemType === "" && i === 0)}
                disabled={disabled}
                onPick={() => onTypeChange(type)}
              />
            ))}
          </div>
        </div>
      )}
      {options.features.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium text-muted-foreground">
            Feature
          </span>
          <select
            value={parent}
            disabled={disabled}
            onChange={(e) => onParentChange(e.target.value)}
            className="w-full rounded-md border bg-card px-2 py-1 text-sm text-foreground"
          >
            <option value="">No Feature — file top-level</option>
            {options.features.map((feature) => (
              <option key={feature.id} value={feature.id}>
                {feature.id} · {feature.title}
              </option>
            ))}
          </select>
          {isEpic && (
            <p className="text-xs leading-relaxed text-muted-foreground">
              The slices below are filed as Tasks under the new work item.
            </p>
          )}
        </div>
      )}
    </div>
  );
}

// InternalAnchorNote is the single destination an anchored outcome has once the
// ticket it writes to already belongs to the internal store — either because the
// repo has no external tracker, or because an earlier apply converted it.
function InternalAnchorNote({ anchor }: { anchor: string }) {
  return (
    <DestinationNote>
      Applies to <span className="font-mono">{anchor}</span> in this repo's
      internal backlog.
    </DestinationNote>
  );
}

function DestinationNote({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs font-medium text-muted-foreground">
        Destination
      </span>
      <p className="text-xs text-muted-foreground">{children}</p>
    </div>
  );
}

function DestinationOption({
  label,
  on,
  disabled,
  onPick,
}: {
  label: string;
  on: boolean;
  disabled: boolean;
  onPick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onPick}
      disabled={disabled}
      className={cn(
        "rounded border px-2 py-0.5 text-[11px]",
        on
          ? "border-info/50 bg-info/10 text-foreground"
          : "border-border text-muted-foreground",
      )}
    >
      {label}
    </button>
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
  findings: "Research comment",
  labels: "Labels",
  relations: "Blocking relations",
};

function stepLabel(step: GrillApplyStep): string {
  return STEP_LABELS[step.step] ?? step.step;
}

// reportApplyCaveats raises what an apply that still landed did not do — a tracker
// refusing an assignment on an issue it did create, or the superseded note a detach
// could not post. The host retires the review the moment the session settles, so the
// applied card's lists are gone before they can be read; a toast outlives the queue
// moving on.
function reportApplyCaveats(steps: GrillApplyStep[], warnings: string[]) {
  const failed = steps.filter((step) => step.status === "failed");
  if (failed.length === 0 && warnings.length === 0) return;
  toast.custom(
    (id) => (
      <ApplyCaveatsCard
        steps={failed}
        warnings={warnings}
        onDismiss={() => toast.dismiss(id)}
      />
    ),
    { duration: 10_000 },
  );
}

function ApplyCaveatsCard({
  steps,
  warnings,
  onDismiss,
}: {
  steps: GrillApplyStep[];
  warnings: string[];
  onDismiss: () => void;
}) {
  return (
    <div className="flex w-[356px] max-w-[calc(100vw-2rem)] items-start gap-3 rounded-lg border border-border bg-popover p-3 shadow-lg">
      <TriangleAlert className="mt-0.5 size-4 shrink-0 text-fail" aria-hidden />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <p className="text-sm text-popover-foreground">
          {steps.length === 0
            ? "Applied, with caveats."
            : `Applied, but ${steps.length === 1 ? "a step" : `${steps.length} steps`} did not land.`}
        </p>
        {steps.length > 0 && <StepList steps={steps} />}
        {warnings.length > 0 && <WarningList warnings={warnings} />}
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss"
        className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
      >
        <X className="size-4" aria-hidden />
      </button>
    </div>
  );
}

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
                {stepLabel(step)}
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

// WarningList raises what the apply could not do on the side but never gated on —
// a detached ticket the tracker refused the superseded note on. The outcome landed,
// so these read as caveats rather than failures.
export function WarningList({ warnings }: { warnings: string[] }) {
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-card px-3 py-2">
      {warnings.map((warning) => (
        <div key={warning} className="flex items-start gap-2 text-xs">
          <TriangleAlert
            className="mt-0.5 size-3.5 shrink-0 text-warn"
            aria-hidden="true"
          />
          <span className="text-muted-foreground">{warning}</span>
        </div>
      ))}
    </div>
  );
}

// AppliedCard is what a reopened Done today row shows, so a create names and links
// the issue it filed — the reference stays useful after the toast is gone. internal
// marks an outcome the review just wrote to the internal backlog, so the card does
// not claim a tracker write that never happened.
function AppliedCard({
  repo,
  issueId,
  mode,
  outcome,
  steps,
  warnings,
  internal,
}: {
  repo: string;
  issueId: string;
  mode?: GrillMode;
  outcome: OutcomePayload;
  steps: GrillApplyStep[];
  warnings: string[];
  internal: boolean;
}) {
  const created = outcome.disposition === "create" && issueId !== "";
  const destination = internal ? "internally" : "on the tracker";
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
        {outcome.disposition === "no_change" ? (
          "Session closed out — nothing was written to the tracker."
        ) : created ? (
          <>
            <span className="font-mono text-foreground">{issueId}</span> filed{" "}
            {destination}.
          </>
        ) : outcome.disposition === "create" ? (
          `The new issue was filed ${destination}.`
        ) : outcome.disposition === "research" ? (
          issueId !== "" ? (
            "The research report was posted as a comment on the issue."
          ) : (
            "Report saved — it stays here on the Research page. Nothing was written to the tracker."
          )
        ) : internal ? (
          <>
            The outcome was written to{" "}
            <span className="font-mono text-foreground">{issueId}</span> in the
            internal backlog. This issue is cleared.
          </>
        ) : (
          "The outcome was written to the tracker. This issue is cleared."
        )}
      </p>
      {created && (
        <Link
          to="/backlog"
          search={{ issue: issueId }}
          className="self-start text-xs font-medium text-primary underline-offset-2 hover:underline"
        >
          View in backlog
        </Link>
      )}
      {steps.length > 0 && <StepList steps={steps} />}
      {warnings.length > 0 && <WarningList warnings={warnings} />}
      {mode === "fix" && issueId !== "" && (
        <RequeueAction repo={repo} ticket={issueId} />
      )}
    </div>
  );
}

// RequeueAction is what an applied fix session earns: the guidance is rewritten,
// so the ticket can go back to the loop without a trip to the CLI. It drives the
// same trau --requeue — quarantine label off, attempt PR closed, branches
// dropped, checkpoint cleared — and settles into a receipt of what changed.
// Starting the loop stays the Loop page's gesture (ADR 0034), so the receipt
// links there rather than arming the drain itself.
function RequeueAction({ repo, ticket }: { repo: string; ticket: string }) {
  const queryClient = useQueryClient();
  const requeue = useMutation({
    mutationFn: () => requeueRun(repo, ticket),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["runs", repo] });
      void queryClient.invalidateQueries({ queryKey: ["run", repo, ticket] });
      void queryClient.invalidateQueries({ queryKey: ["queue", repo] });
    },
  });

  if (requeue.data) {
    return (
      <div
        role="status"
        className="flex flex-col gap-1.5 rounded-md border bg-card px-3 py-2 text-xs"
      >
        {requeue.data.changed.map((change) => (
          <div key={change} className="flex items-start gap-2">
            <Check
              className="mt-0.5 size-3.5 shrink-0 text-done"
              aria-hidden="true"
            />
            <span className="text-muted-foreground">{change}</span>
          </div>
        ))}
        <p className="text-muted-foreground">
          <span className="font-mono text-foreground">{ticket}</span> is
          eligible again —{" "}
          <Link
            to="/loop"
            className="font-medium text-primary underline-offset-2 hover:underline"
          >
            Start the loop
          </Link>{" "}
          to pick it up
        </p>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <Button
        size="sm"
        className="self-start font-mono"
        onClick={() => requeue.mutate()}
        disabled={requeue.isPending}
      >
        <RotateCcw
          className={cn("size-3.5", requeue.isPending && "animate-spin")}
          aria-hidden="true"
        />
        {requeue.isPending ? "Requeueing…" : `Requeue ${ticket}`}
      </Button>
      {requeue.error && (
        <p role="status" className="text-xs text-fail">
          {(requeue.error as Error).message}
        </p>
      )}
    </div>
  );
}

const slowApplyMs = 8_000;

const guardBridgeMs = 2_000;

const PENDING_LABELS: Record<string, string> = {
  create: "Creating…",
  no_change: "Closing out…",
  research: "Approving…",
};

export function applyLabel(
  disposition: string,
  pending: boolean,
  result?: GrillApplyResponse,
): string {
  if (pending) return PENDING_LABELS[disposition] ?? "Applying…";
  if (result && !result.applied) return "Retry";
  if (disposition === "no_change") return "Close out";
  if (disposition === "create") return "Create";
  if (disposition === "research") return "Approve";
  return "Apply";
}

// subCount is the epic's slice count, and 0 for anything filing a single issue.
export function applyStatusLine({
  disposition,
  tracker,
  destination,
  anchor,
  subCount,
  slow,
}: {
  disposition: string;
  tracker: string;
  destination: GrillDestination;
  anchor: string;
  subCount: number;
  slow: boolean;
}): string {
  const internal = destination === "internal" || tracker === "internal";
  const name = TRACKER_NAMES[tracker] ?? tracker;
  const place = internal ? "the internal backlog" : name;

  let line =
    anchor === ""
      ? "Applying…"
      : `Applying to ${anchor} ${internal ? "in" : "on"} ${place}…`;
  if (disposition === "create") {
    const noun = subCount === 1 ? "sub-issue" : "sub-issues";
    const epic = subCount === 0 ? "" : ` epic + ${subCount} ${noun}`;
    line = `Filing${epic} to ${place}…`;
  } else if (disposition === "no_change") {
    line = "Closing out…";
  }

  if (!slow) return line;
  return internal
    ? `${line} Still working — this can take a moment.`
    : `${line} Still working — ${name} can be slow.`;
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
    case "research":
      return "Research";
    case "no_change":
      return "No change";
    default:
      return disposition || "Outcome";
  }
}
