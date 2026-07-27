package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
)

// pushSubscriber is the `sub` claim of the VAPID JWT — the contact a push service
// would use to reach the sender. The hub is a loopback process with no public
// address, so it names itself rather than a person.
const pushSubscriber = "mailto:trau@localhost"

// pushTTL bounds how long a push service holds an undelivered notification. A
// needs-attention wake-up is stale long before then, so it is deliberately short.
const pushTTL = 300

// pushSender delivers one encrypted payload to a subscription's push service and
// reports the status the service answered with. It is the seam tests swap so no
// test ever reaches a browser vendor.
type pushSender func(ctx context.Context, sub hubstore.PushSubscription, vapid hubstore.VAPID, payload []byte) (int, error)

// PushKeyResponse is the GET /api/v1/push/key resource: the VAPID public key a
// browser must subscribe under for this hub's pushes to decrypt.
type PushKeyResponse struct {
	PublicKey string `json:"public_key"`
}

// PushSubscriptionRequest is a browser push subscription, shaped as
// PushSubscription.toJSON() reports it. A DELETE needs only the endpoint.
type PushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// PushSendResponse reports a delivery attempt: how many subscriptions accepted
// the payload, and how many expired ones the attempt pruned.
type PushSendResponse struct {
	Sent   int `json:"sent"`
	Pruned int `json:"pruned"`
}

// PushPayload is the notification body the service worker receives and shows.
type PushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag,omitempty"`
	URL   string `json:"url,omitempty"`
}

// handlePushKey returns the hub's VAPID public key (GET /api/v1/push/key),
// generating the identity on the first request.
func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	vapid, err := s.vapid()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, PushKeyResponse{PublicKey: vapid.PublicKey})
}

// handlePushSubscriptions stores a browser push subscription (POST) or drops one
// (DELETE, same body). Subscriptions are hub-global: a push wakes this machine,
// not a particular repo.
func (s *Server) handlePushSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.subscribePush(w, r)
	case http.MethodDelete:
		s.unsubscribePush(w, r)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	req, ok := decodePushSubscription(w, r)
	if !ok {
		return
	}
	if req.Keys.P256dh == "" || req.Keys.Auth == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "p256dh and auth keys are required"})
		return
	}
	sub, err := s.stores.Push().Subscribe(req.Endpoint, req.Keys.P256dh, req.Keys.Auth)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store subscription: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	req, ok := decodePushSubscription(w, r)
	if !ok {
		return
	}
	if err := s.stores.Push().Unsubscribe(req.Endpoint); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "drop subscription: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func decodePushSubscription(w http.ResponseWriter, r *http.Request) (PushSubscriptionRequest, bool) {
	var req PushSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return PushSubscriptionRequest{}, false
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Endpoint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required"})
		return PushSubscriptionRequest{}, false
	}
	return req, true
}

// handlePushTest sends a test notification to every stored subscription (POST
// /api/v1/push/test), so the operator can prove delivery reaches the OS with
// every trau window closed.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sent, pruned, err := s.deliverPush(r.Context(), PushPayload{
		Title: "trau",
		Body:  "Push notifications are working — you'll hear from the hub even with trau closed.",
		Tag:   "trau-push-test",
		URL:   "/",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, PushSendResponse{Sent: sent, Pruned: pruned})
}

// deliverPush encrypts payload to every stored subscription and reports how many
// accepted it. A push service answering 404 or 410 has retired that endpoint, so
// the subscription is pruned instead of retried forever; any other failure is a
// transient the next push retries.
func (s *Server) deliverPush(ctx context.Context, payload PushPayload) (sent, pruned int, err error) {
	subs, err := s.stores.Push().Subscriptions()
	if err != nil {
		return 0, 0, fmt.Errorf("list push subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return 0, 0, nil
	}
	vapid, err := s.vapid()
	if err != nil {
		return 0, 0, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, 0, fmt.Errorf("encode push payload: %w", err)
	}
	for _, sub := range subs {
		status, err := s.pushSend(ctx, sub, vapid, body)
		switch {
		case err != nil:
			logger.Verbosef("push %s: %v", sub.Endpoint, err)
		case status == http.StatusNotFound || status == http.StatusGone:
			if err := s.stores.Push().Unsubscribe(sub.Endpoint); err != nil {
				logger.Verbosef("prune push subscription %s: %v", sub.Endpoint, err)
				continue
			}
			pruned++
		case status >= 200 && status < 300:
			sent++
		default:
			logger.Verbosef("push %s: push service returned %d", sub.Endpoint, status)
		}
	}
	return sent, pruned, nil
}

// vapid returns the hub's Web Push identity, generating and persisting a keypair
// the first time one is needed.
func (s *Server) vapid() (hubstore.VAPID, error) {
	stored, err := s.stores.Push().VAPID()
	if err != nil {
		return hubstore.VAPID{}, fmt.Errorf("read vapid identity: %w", err)
	}
	if stored.PublicKey != "" {
		return stored, nil
	}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return hubstore.VAPID{}, fmt.Errorf("generate vapid keys: %w", err)
	}
	saved, err := s.stores.Push().SaveVAPID(hubstore.VAPID{PublicKey: public, PrivateKey: private})
	if err != nil {
		return hubstore.VAPID{}, fmt.Errorf("store vapid identity: %w", err)
	}
	return saved, nil
}

// sendWebPush is the production sender: RFC 8291 payload encryption plus a VAPID
// signature, POSTed to the browser vendor's push service.
func sendWebPush(
	ctx context.Context,
	sub hubstore.PushSubscription,
	vapid hubstore.VAPID,
	payload []byte,
) (int, error) {
	res, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		Subscriber:      pushSubscriber,
		VAPIDPublicKey:  vapid.PublicKey,
		VAPIDPrivateKey: vapid.PrivateKey,
		TTL:             pushTTL,
	})
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	return res.StatusCode, nil
}
