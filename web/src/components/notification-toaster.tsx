import { useRef } from "react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { fireNotification, useNotifications } from "@/lib/notifications";
import {
  notificationTarget,
  useNotificationEvents,
  useNotificationNavigate,
} from "@/lib/notification-center";
import { isConversationOpen } from "@/lib/open-conversation";

// NotificationToaster is the headless bridge from the hub's live needs-attention
// frames to a toast — and, when the tab is hidden, an OS notification. It renders
// nothing; the toasts land in the root <Toaster />.
export function NotificationToaster() {
  const navigateToNotification = useNotificationNavigate();
  const { enabled } = useNotifications();
  const enabledRef = useRef(enabled);
  enabledRef.current = enabled;

  useNotificationEvents((notification, repo) => {
    if (
      notification.kind === "grill_question" &&
      isConversationOpen(notification.ref)
    ) {
      return;
    }

    const target = notificationTarget(notification, repo);
    toast.custom((id) => (
      <NotificationCard
        title={notification.title}
        repo={repo}
        body={notification.body}
        onOpen={() => {
          toast.dismiss(id);
          navigateToNotification(target);
        }}
      />
    ));

    if (document.hidden && enabledRef.current) {
      fireNotification(
        notification.title,
        notification.body,
        notification.kind + notification.ref,
      );
    }
  });

  return null;
}

function NotificationCard({
  title,
  repo,
  body,
  onOpen,
}: {
  title: string;
  repo: string;
  body: string;
  onOpen: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex w-[356px] max-w-[calc(100vw-2rem)] flex-col gap-1.5 rounded-lg border border-border bg-popover px-4 py-3 text-left shadow-lg transition-colors hover:bg-accent"
    >
      <div className="flex items-center gap-2">
        <Badge variant="outline" className="font-mono text-[10px]">
          {repo}
        </Badge>
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-popover-foreground">
          {title}
        </span>
      </div>
      {body && (
        <p className="line-clamp-2 text-xs leading-relaxed text-muted-foreground">
          {body}
        </p>
      )}
    </button>
  );
}
