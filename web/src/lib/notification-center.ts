import { useCallback, useEffect, useRef } from "react";
import { queryOptions, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";

import { useActiveRepo } from "@/components/trau/active-repo";

import { apiFetch } from "./api";
import { subscribeAllEvents, type RepoFeedEvent } from "./events";
import { draftItemId } from "./inbox";
import type { RepoView } from "./instances";

export type NotificationKind =
  "grill_question" | "run_paused" | "run_faulted" | "run_quarantined" | "run_awaiting_merge";

// HubNotification mirrors the hub's notifications resource — named apart from the
// browser Notification the OS layer raises (@/lib/notifications). Repo is the
// repo's root path, not its registry name; Ref is the grill session id or the
// run's ticket; ReadAt is empty while unread.
export interface HubNotification {
  id: number;
  repo: string;
  kind: NotificationKind;
  ref: string;
  issue_id?: string;
  title: string;
  body: string;
  created_at: string;
  updated_at: string;
  read_at?: string;
}

export interface NotificationsResponse {
  notifications: HubNotification[];
  unread_count: number;
}

export const notificationsQueryKey = ["notifications"] as const;

const NOTIFICATION_KIND = "notification";

async function fetchNotifications(): Promise<NotificationsResponse> {
  const res = await apiFetch("/api/v1/notifications");
  if (!res.ok) {
    throw new Error(`notifications request failed: ${res.status}`);
  }
  return res.json();
}

// notificationsQueryOptions reads the recent notifications and unread count. It has
// no refetchInterval — the SSE wake-up frame drives freshness (see
// useNotificationEvents).
export const notificationsQueryOptions = () =>
  queryOptions({
    queryKey: notificationsQueryKey,
    queryFn: fetchNotifications,
  });

export async function markNotificationRead(id: number): Promise<void> {
  const res = await apiFetch(`/api/v1/notifications/${id}/read`, {
    method: "POST",
  });
  if (!res.ok) {
    throw new Error(`mark notification read failed: ${res.status}`);
  }
}

export async function markAllNotificationsRead(): Promise<void> {
  const res = await apiFetch("/api/v1/notifications/read-all", {
    method: "POST",
  });
  if (!res.ok) {
    throw new Error(`mark all notifications read failed: ${res.status}`);
  }
}

// unreadBadgeLabel is the bell badge text: hidden at zero, the exact count through
// nine, then "9+" — the same overflow the sidebar count pills use.
export function unreadBadgeLabel(count: number): string | null {
  if (count <= 0) return null;
  return count > 9 ? "9+" : String(count);
}

// sortByNewest returns the notifications newest-first by updated_at, leaving the
// input untouched.
export function sortByNewest(
  notifications: HubNotification[],
): HubNotification[] {
  return [...notifications].sort(
    (a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at),
  );
}

// markReadInResponse settles one notification as read in a cached response. The
// unread count is decremented rather than recomputed — it can exceed the listed
// rows — and only when the row was actually unread.
export function markReadInResponse(
  data: NotificationsResponse,
  id: number,
  readAt: string,
): NotificationsResponse {
  let wasUnread = false;
  const notifications = data.notifications.map((n) => {
    if (n.id !== id || n.read_at) return n;
    wasUnread = true;
    return { ...n, read_at: readAt };
  });
  if (!wasUnread) return data;
  return { notifications, unread_count: Math.max(0, data.unread_count - 1) };
}

// markAllReadInResponse settles every listed notification as read and zeroes the
// unread count.
export function markAllReadInResponse(
  data: NotificationsResponse,
  readAt: string,
): NotificationsResponse {
  return {
    notifications: data.notifications.map((n) =>
      n.read_at ? n : { ...n, read_at: readAt },
    ),
    unread_count: 0,
  };
}

// useNotificationEvents taps the shared machine-wide stream for the hub's live
// wake-up frames — id-less, so never part of a reconnect backfill — refetching the
// notifications query and handing each frame's notification to onNotification with
// its repo label. Shared with the notification center.
export function useNotificationEvents(
  onNotification: (notification: HubNotification, repo: string) => void,
): void {
  const queryClient = useQueryClient();
  const callback = useRef(onNotification);
  callback.current = onNotification;

  useEffect(() => {
    return subscribeAllEvents(
      (ev) => {
        const notification = notificationOf(ev);
        if (!notification) return;
        void queryClient.invalidateQueries({ queryKey: notificationsQueryKey });
        callback.current(notification, ev.repo);
      },
      () => {},
    );
  }, [queryClient]);
}

function notificationOf(ev: RepoFeedEvent): HubNotification | null {
  if (ev.kind !== NOTIFICATION_KIND) return null;
  const notification = ev.fields?.notification as HubNotification | undefined;
  return notification ?? null;
}

export type NotificationTarget =
  | { kind: "inbox"; repo: string; issue: string }
  | { kind: "run"; repo: string; ticket: string }
  | null;

// notificationTarget is where clicking a notification lands: a grill question at
// its inbox item — an issue-less authoring session addressed by its draft row —
// and a run at its detail page.
export function notificationTarget(
  notification: HubNotification,
  repo: string,
): NotificationTarget {
  switch (notification.kind) {
    case "grill_question":
      return {
        kind: "inbox",
        repo,
        issue: notification.issue_id || draftItemId(notification.ref),
      };
    case "run_paused":
    case "run_faulted":
    case "run_quarantined":
    case "run_awaiting_merge":
      return { kind: "run", repo, ticket: notification.ref };
    default:
      return null;
  }
}

// knownRepo finds the RepoView a notification's repo addresses — the registry
// name on live frames, the repo root on persisted rows.
function knownRepo(
  repo: string,
  repos: readonly RepoView[],
): RepoView | undefined {
  return repos.find((r) => r.name === repo || r.root === repo);
}

// notificationRepoName resolves a notification's repo to its registry name — the
// vocabulary scope and the run routes speak. An unknown repo passes through
// unchanged.
export function notificationRepoName(
  repo: string,
  repos: readonly RepoView[],
): string {
  return knownRepo(repo, repos)?.name ?? repo;
}

// notificationScopeSwitch is the repo name to adopt before an inbox navigation:
// the target's repo when it differs from the active one and is still known — a
// repo the hub no longer lists never clobbers the stored scope. Run targets need
// no switch; their repo-bound route adopts the scope on entry.
export function notificationScopeSwitch(
  target: NotificationTarget,
  activeRepo: string | null,
  repos: readonly RepoView[],
): string | null {
  if (target?.kind !== "inbox") return null;
  const match = knownRepo(target.repo, repos);
  if (!match || match.name === activeRepo) return null;
  return match.name;
}

// useNotificationNavigate lands on a notification's target — the same inbox item
// or run detail the toast opens. Shared so the bell and the toaster never drift.
// A cross-project inbox target switches the active scope to its repo first, so
// the Inbox opens on the notification's project rather than the current one.
export function useNotificationNavigate(): (
  target: NotificationTarget,
) => void {
  const navigate = useNavigate();
  const { repo, repos, setRepo } = useActiveRepo();

  return useCallback(
    (target: NotificationTarget) => {
      if (!target) return;
      if (target.kind === "inbox") {
        const adopt = notificationScopeSwitch(target, repo, repos);
        if (adopt) setRepo(adopt);
        void navigate({ to: "/inbox", search: { issue: target.issue } });
        return;
      }
      void navigate({
        to: "/runs/$repo/$ticket",
        params: {
          repo: notificationRepoName(target.repo, repos),
          ticket: target.ticket,
        },
      });
    },
    [navigate, repo, repos, setRepo],
  );
}
