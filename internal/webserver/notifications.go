package webserver

import (
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// notificationBodyMax bounds a notification body — the pending question or the
// event message — so a long prompt never bloats the row or the live frame.
const notificationBodyMax = 200

// notificationKind is the SSE frame kind the live wake-up carries, distinct from
// the event kinds the feed persists so the web can route it to the notification
// store instead of the run feed.
const notificationKind = "notification"

// stateChangeKind is the event kind whose terminal states the run producer watches,
// matching the web recap's classifier (web/src/lib/recap.ts).
const stateChangeKind = "state_change"

// NotificationsResponse is the GET /api/v1/notifications resource: the recent
// notifications and the unread count the web badges.
type NotificationsResponse struct {
	Notifications []hubstore.Notification `json:"notifications"`
	UnreadCount   int                     `json:"unread_count"`
}

// handleNotifications lists the recent notifications and the unread count (GET
// /api/v1/notifications?limit=100). It is global, not repo-scoped, so one badge
// covers every repo the hub tracks.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	items, err := s.stores.Notifications().List(notificationLimit(r.URL.Query().Get("limit")))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	unread, err := s.stores.Notifications().UnreadCount()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, NotificationsResponse{Notifications: items, UnreadCount: unread})
}

// handleNotificationRead marks one notification read (POST
// /api/v1/notifications/{id}/read).
func (s *Server) handleNotificationRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid notification id"})
		return
	}
	if err := s.stores.Notifications().MarkRead(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.respondUnread(w)
}

// handleNotificationsReadAll marks every unread notification read (POST
// /api/v1/notifications/read-all).
func (s *Server) handleNotificationsReadAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := s.stores.Notifications().MarkAllRead(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.respondUnread(w)
}

func (s *Server) respondUnread(w http.ResponseWriter) {
	unread, err := s.stores.Notifications().UnreadCount()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "unread_count": unread})
}

// notifyGrillAwaiting records the session's needs-you notification when it enters
// the awaiting-answer set and pushes a live wake-up. body is the pending question
// text when the caller has it; empty falls back to the session's stored question or
// its park reason. A session outside the awaiting set is ignored — leaving the set
// is cleared centrally in publishGrillState.
func (s *Server) notifyGrillAwaiting(sess hubstore.GrillSession, body string) {
	if !grillAwaiting(sess.State) {
		return
	}
	if body == "" {
		body = s.grillNotificationBody(sess)
	}
	notif, err := s.stores.Notifications().NotifyGrillQuestion(
		sess.Repo, sess.ID, sess.IssueID, grillNotificationTitle(sess), truncateBody(body, notificationBodyMax),
	)
	if err != nil {
		logger.Verbosef("grill %d: notify: %v", sess.ID, err)
		return
	}
	s.publishNotification(notif)
}

// notifyRunEvent records a needs-attention notification for a run that paused,
// faulted, or was quarantined, keyed off the same state_change fields the web recap
// derives (web/src/lib/recap.ts). Every other event kind and state produces nothing.
func (s *Server) notifyRunEvent(repo registry.Repo, row hubstore.EventRow) {
	if row.Kind != stateChangeKind {
		return
	}
	fields := unmarshalFields(row.Fields)
	state, _ := fields["state"].(string)
	kind := runNotificationKind(state)
	if kind == "" {
		return
	}
	ticket, _ := fields["ticket"].(string)
	notif, err := s.stores.Notifications().NotifyRunAttention(
		repo.Root, kind, ticket, ticket, runNotificationTitle(state, repo.Name), truncateBody(row.Msg, notificationBodyMax),
	)
	if err != nil {
		logger.Verbosef("run notification %s: %v", ticket, err)
		return
	}
	s.publishNotification(notif)
}

// publishNotification pushes a live wake-up frame for notif on the all-events SSE
// broadcaster (ADR 0008). The frame is in-process only — never appended to the
// events table, whose rows the notifications table already supersedes — and carries
// no id so it leaves a streaming client's resume cursor untouched.
func (s *Server) publishNotification(notif hubstore.Notification) {
	unread, err := s.stores.Notifications().UnreadCount()
	if err != nil {
		logger.Verbosef("notification unread count: %v", err)
	}
	name := filepath.Base(notif.Repo)
	if repo, ok := s.findRepoByRoot(notif.Repo); ok {
		name = repo.Name
	}
	s.events.publish(liveEvent{
		Root: notif.Repo,
		Name: name,
		Event: FeedEvent{
			Event: event.Event{
				Time: notif.UpdatedAt,
				Kind: notificationKind,
				Msg:  notif.Title,
				Fields: map[string]any{
					"notification": notif,
					"unread_count": unread,
				},
			},
		},
	})
}

// grillAwaiting reports whether a session in state is waiting on the user — the set
// a needs-you notification tracks (grilling-prd.md).
func grillAwaiting(state string) bool {
	switch state {
	case hubstore.GrillWaiting, hubstore.GrillParked, hubstore.GrillStalled:
		return true
	}
	return false
}

// grillNotificationBody is the body for a grill notification when the caller has no
// question text in hand: the session's last stored question, else its park reason.
func (s *Server) grillNotificationBody(sess hubstore.GrillSession) string {
	msgs, err := s.stores.Grill().Messages(sess.ID, 0)
	if err == nil {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Kind == hubstore.GrillKindQuestion {
				if text := grillMessageText(msgs[i].Payload); text != "" {
					return text
				}
			}
		}
	}
	return sess.ParkedReason
}

func grillNotificationTitle(sess hubstore.GrillSession) string {
	subject := sess.IssueTitle
	if subject == "" {
		subject = sess.IssueID
	}
	if subject == "" {
		subject = "new issue"
	}
	return "Grilling needs you — " + subject
}

func runNotificationKind(state string) string {
	switch state {
	case "paused":
		return hubstore.NotificationRunPaused
	case "faulted":
		return hubstore.NotificationRunFaulted
	case "quarantined":
		return hubstore.NotificationRunQuarantined
	case "awaiting_merge":
		return hubstore.NotificationRunAwaitingMerge
	}
	return ""
}

func runNotificationTitle(state, repo string) string {
	switch state {
	case "paused":
		return "Run paused — " + repo
	case "faulted":
		return "Run faulted — " + repo
	case "quarantined":
		return "Run quarantined — " + repo
	case "awaiting_merge":
		return "PR awaiting merge — " + repo
	}
	return "Run needs attention — " + repo
}

func notificationLimit(raw string) int {
	if raw == "" {
		return 100
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 100
	}
	return n
}

// truncateBody caps s at max runes, appending an ellipsis when it trims.
func truncateBody(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
