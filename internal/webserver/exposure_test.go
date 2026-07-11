package webserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// apiRoutes is every route the token gate must cover on a non-loopback bind.
var apiRoutes = []string{
	APIPrefix + "/health",
	APIPrefix + "/instances",
	APIPrefix + "/repos",
	APIPrefix + "/repos/demo/runs",
	APIPrefix + "/repos/demo/events",
	APIPrefix + "/repos/demo/events/stream",
	APIPrefix + "/events/stream",
}

func exposedServer(t *testing.T, bind, token string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(New("1.2.3", bind, token, nil, false, testStores(t)).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// statusWithToken issues a GET without reading the (possibly streaming) body, so
// it works uniformly for the SSE routes too.
func statusWithToken(t *testing.T, ts *httptest.Server, path, token string) int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func TestLoopback(t *testing.T) {
	cases := []struct {
		bind string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.5", true},
		{"::1", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"0.0.0.0", false},
		{"::", false},
		{"", false},
		{"192.168.1.10", false},
		{"example.internal", false},
	}
	for _, c := range cases {
		if got := Loopback(c.bind); got != c.want {
			t.Errorf("Loopback(%q) = %v, want %v", c.bind, got, c.want)
		}
	}
}

func TestCheckExposure(t *testing.T) {
	cases := []struct {
		bind, token string
		wantErr     bool
	}{
		{"127.0.0.1", "", false},
		{"localhost", "", false},
		{"::1", "", false},
		{"0.0.0.0", "", true},
		{"", "", true},
		{"192.168.1.10", "", true},
		{"0.0.0.0", "secret", false},
		{"192.168.1.10", "secret", false},
	}
	for _, c := range cases {
		err := CheckExposure(c.bind, c.token)
		if c.wantErr {
			if err == nil {
				t.Errorf("CheckExposure(%q, %q) = nil, want error", c.bind, c.token)
				continue
			}
			if !errors.Is(err, ErrTokenRequired) {
				t.Errorf("CheckExposure(%q, %q) error = %v, want ErrTokenRequired", c.bind, c.token, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("CheckExposure(%q, %q) = %v, want nil", c.bind, c.token, err)
		}
	}
}

func TestLoopbackBindNeedsNoToken(t *testing.T) {
	ts := exposedServer(t, "127.0.0.1", "")
	for _, route := range []string{APIPrefix + "/health", APIPrefix + "/instances", APIPrefix + "/repos"} {
		res, _ := get(t, ts, route)
		if res.StatusCode != http.StatusOK {
			t.Errorf("loopback GET %s = %d, want 200 (no token required)", route, res.StatusCode)
		}
	}
	if res, _ := get(t, ts, "/"); res.StatusCode != http.StatusOK {
		t.Errorf("loopback GET / = %d, want 200", res.StatusCode)
	}
}

func TestNonLoopbackWithoutTokenReturns401(t *testing.T) {
	ts := exposedServer(t, "0.0.0.0", "s3cret")

	for _, route := range apiRoutes {
		res, body := get(t, ts, route)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("no-token GET %s = %d, want 401", route, res.StatusCode)
		}
		if wa := res.Header.Get("WWW-Authenticate"); !strings.Contains(wa, "Bearer") {
			t.Errorf("%s WWW-Authenticate = %q, want Bearer challenge", route, wa)
		}
		if !strings.Contains(body, "unauthorized") {
			t.Errorf("%s body = %q, want JSON error", route, body)
		}
	}

	if got := statusWithToken(t, ts, apiRoutes[0], "wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong-token GET = %d, want 401", got)
	}

	if res, _ := get(t, ts, "/"); res.StatusCode != http.StatusOK {
		t.Errorf("exposed GET / = %d, want 200 (SPA shell stays public)", res.StatusCode)
	}
}

func TestNonLoopbackWithTokenAuthorizes(t *testing.T) {
	const token = "s3cret"
	ts := exposedServer(t, "0.0.0.0", token)

	for _, route := range apiRoutes {
		if got := statusWithToken(t, ts, route, token); got == http.StatusUnauthorized {
			t.Errorf("authorized GET %s = 401, want the request to pass the token gate", route)
		}
	}

	if got := statusWithToken(t, ts, APIPrefix+"/health", token); got != http.StatusOK {
		t.Errorf("authorized GET health = %d, want 200", got)
	}
}
