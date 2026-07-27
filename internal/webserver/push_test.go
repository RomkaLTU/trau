package webserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// pushCall is one delivery the fake sender saw.
type pushCall struct {
	endpoint string
	payload  string
}

// newPushTestServer wires a hub whose push sender answers with status per
// endpoint, so subscribe, delivery, and pruning are asserted without reaching a
// browser vendor. An endpoint missing from status is accepted with a 201.
func newPushTestServer(t *testing.T, status map[string]int) (*httptest.Server, *hubstore.Stores, *[]pushCall) {
	t.Helper()
	stores := testStores(t)
	s := New("test", "127.0.0.1", "", nil, false, stores)
	calls := []pushCall{}
	s.pushSend = func(
		_ context.Context,
		sub hubstore.PushSubscription,
		vapid hubstore.VAPID,
		payload []byte,
	) (int, error) {
		if vapid.PublicKey == "" || vapid.PrivateKey == "" {
			t.Errorf("sender got an unsigned identity for %s", sub.Endpoint)
		}
		calls = append(calls, pushCall{endpoint: sub.Endpoint, payload: string(payload)})
		if code, ok := status[sub.Endpoint]; ok {
			return code, nil
		}
		return http.StatusCreated, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, stores, &calls
}

func subscribeRequest(endpoint, p256dh, auth string) PushSubscriptionRequest {
	req := PushSubscriptionRequest{Endpoint: endpoint}
	req.Keys.P256dh = p256dh
	req.Keys.Auth = auth
	return req
}

func subscribePushTest(t *testing.T, ts *httptest.Server, req PushSubscriptionRequest) *http.Response {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/push/subscriptions", req)
	_ = res.Body.Close()
	return res
}

func unsubscribePushTest(t *testing.T, ts *httptest.Server, endpoint string) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]string{"endpoint": endpoint})
	if err != nil {
		t.Fatalf("marshal unsubscribe body: %v", err)
	}
	req, err := http.NewRequest(
		http.MethodDelete,
		ts.URL+APIPrefix+"/push/subscriptions",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("build DELETE subscriptions: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE subscriptions: %v", err)
	}
	_ = res.Body.Close()
	return res
}

func pushKey(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/push/key")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET push key status = %d (%s)", res.StatusCode, body)
	}
	var out PushKeyResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode push key: %v", err)
	}
	return out.PublicKey
}

func sendPushTest(t *testing.T, ts *httptest.Server) PushSendResponse {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/push/test", nil)
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("read push test body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST push test status = %d (%s)", res.StatusCode, body)
	}
	var out PushSendResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode push test: %v", err)
	}
	return out
}

func pushSubscriptions(t *testing.T, stores *hubstore.Stores) []hubstore.PushSubscription {
	t.Helper()
	subs, err := stores.Push().Subscriptions()
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	return subs
}

// TestPushKeyGeneratesOneStableIdentity pins the guarantee a browser depends on:
// a subscription is bound to the key it subscribed under, so the hub must mint
// the pair once and keep answering with it.
func TestPushKeyGeneratesOneStableIdentity(t *testing.T) {
	ts, stores, _ := newPushTestServer(t, nil)

	first := pushKey(t, ts)
	if first == "" {
		t.Fatal("push key is empty")
	}
	raw, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("push key is not base64url: %v", err)
	}
	if len(raw) != 65 || raw[0] != 4 {
		t.Errorf("push key is %d bytes starting %#x, want a 65-byte uncompressed P-256 point", len(raw), raw[0])
	}
	if second := pushKey(t, ts); second != first {
		t.Errorf("push key = %q on the second read, want the stored %q", second, first)
	}
	stored, err := stores.Push().VAPID()
	if err != nil {
		t.Fatalf("read stored identity: %v", err)
	}
	if stored.PublicKey != first || stored.PrivateKey == "" {
		t.Errorf("stored identity = %+v, want the served public key and a private key", stored)
	}
}

func TestPushSubscribeStoresAndRefreshesSubscription(t *testing.T) {
	ts, stores, _ := newPushTestServer(t, nil)

	res := subscribePushTest(t, ts, subscribeRequest("https://push.example/abc", "p256-a", "auth-a"))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("subscribe status = %d, want 201", res.StatusCode)
	}
	subs := pushSubscriptions(t, stores)
	if len(subs) != 1 || subs[0].Endpoint != "https://push.example/abc" {
		t.Fatalf("subscriptions = %+v, want the one endpoint", subs)
	}

	subscribePushTest(t, ts, subscribeRequest("https://push.example/abc", "p256-b", "auth-b"))
	subs = pushSubscriptions(t, stores)
	if len(subs) != 1 {
		t.Fatalf("re-subscribing stacked %d rows, want 1", len(subs))
	}
	if subs[0].P256dh != "p256-b" || subs[0].Auth != "auth-b" {
		t.Errorf("keys = %q/%q, want the refreshed pair", subs[0].P256dh, subs[0].Auth)
	}
}

func TestPushUnsubscribeDropsSubscription(t *testing.T) {
	ts, stores, _ := newPushTestServer(t, nil)
	subscribePushTest(t, ts, subscribeRequest("https://push.example/abc", "p256", "auth"))

	if res := unsubscribePushTest(t, ts, "https://push.example/abc"); res.StatusCode != http.StatusOK {
		t.Fatalf("unsubscribe status = %d, want 200", res.StatusCode)
	}
	if subs := pushSubscriptions(t, stores); len(subs) != 0 {
		t.Fatalf("subscriptions = %+v, want none left", subs)
	}
	if res := unsubscribePushTest(t, ts, "https://push.example/abc"); res.StatusCode != http.StatusOK {
		t.Errorf("repeat unsubscribe status = %d, want 200 — dropping an unknown endpoint is a no-op", res.StatusCode)
	}
}

func TestPushSubscribeRejectsIncompleteBody(t *testing.T) {
	ts, stores, _ := newPushTestServer(t, nil)

	cases := []struct {
		name string
		req  PushSubscriptionRequest
	}{
		{name: "no endpoint", req: subscribeRequest("  ", "p256", "auth")},
		{name: "no p256dh", req: subscribeRequest("https://push.example/abc", "", "auth")},
		{name: "no auth", req: subscribeRequest("https://push.example/abc", "p256", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := subscribePushTest(t, ts, tc.req)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", res.StatusCode)
			}
		})
	}
	if subs := pushSubscriptions(t, stores); len(subs) != 0 {
		t.Errorf("subscriptions = %+v, want nothing stored for a rejected body", subs)
	}
}

// TestPushTestPrunesExpiredSubscriptions covers the endpoint's whole contract:
// every stored subscription gets the payload, a push service reporting the
// endpoint gone has it dropped rather than retried forever, and the live ones
// survive.
func TestPushTestPrunesExpiredSubscriptions(t *testing.T) {
	expired := "https://push.example/gone"
	missing := "https://push.example/missing"
	live := "https://push.example/live"
	ts, stores, calls := newPushTestServer(t, map[string]int{
		expired: http.StatusGone,
		missing: http.StatusNotFound,
	})
	for _, endpoint := range []string{expired, missing, live} {
		subscribePushTest(t, ts, subscribeRequest(endpoint, "p256", "auth"))
	}

	out := sendPushTest(t, ts)
	if out.Sent != 1 || out.Pruned != 2 {
		t.Fatalf("send = %+v, want 1 sent and 2 pruned", out)
	}
	if len(*calls) != 3 {
		t.Fatalf("sender saw %d deliveries, want 3", len(*calls))
	}
	var payload PushPayload
	if err := json.Unmarshal([]byte((*calls)[0].payload), &payload); err != nil {
		t.Fatalf("decode delivered payload: %v", err)
	}
	if payload.Title == "" || payload.Body == "" || payload.URL == "" {
		t.Errorf("payload = %+v, want a title, body, and click target", payload)
	}

	subs := pushSubscriptions(t, stores)
	if len(subs) != 1 || subs[0].Endpoint != live {
		t.Fatalf("subscriptions = %+v, want only %s to survive", subs, live)
	}

	out = sendPushTest(t, ts)
	if out.Sent != 1 || out.Pruned != 0 {
		t.Errorf("second send = %+v, want 1 sent and nothing left to prune", out)
	}
}

func TestPushTestWithNoSubscriptionsSendsNothing(t *testing.T) {
	ts, _, calls := newPushTestServer(t, nil)

	if out := sendPushTest(t, ts); out.Sent != 0 || out.Pruned != 0 {
		t.Errorf("send = %+v, want zeroes", out)
	}
	if len(*calls) != 0 {
		t.Errorf("sender saw %d deliveries, want none", len(*calls))
	}
}

func TestPushRejectsWrongMethods(t *testing.T) {
	ts, _, _ := newPushTestServer(t, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/push/key", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST push key status = %d, want 405", res.StatusCode)
	}
	if res, _ := get(t, ts, APIPrefix+"/push/subscriptions"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET subscriptions status = %d, want 405", res.StatusCode)
	}
	if res, _ := get(t, ts, APIPrefix+"/push/test"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET push test status = %d, want 405", res.StatusCode)
	}
}
