package netguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBlocked(t *testing.T) {
	cases := []struct {
		name     string
		hostport string
		want     bool
	}{
		{"localhost with port", "localhost:8080", false},
		{"localhost without port", "localhost", false},
		{"loopback v4", "127.0.0.1:8728", false},
		{"loopback v4 closed port", "127.0.0.1:1", false},
		{"loopback v4 without port", "127.0.0.1", false},
		{"loopback v4 elsewhere in /8", "127.4.5.6:80", false},
		{"loopback v6", "[::1]:9", false},
		{"loopback v6 without port", "[::1]", false},
		{"private v4", "10.0.0.1", true},
		{"bind-all v4", "0.0.0.0:8728", true},
		{"linear", "api.linear.app", true},
		{"jira", "acme.atlassian.net:443", true},
		{"azure devops", "dev.azure.com", true},
		{"github", "api.github.com:443", true},
		{"github without port", "api.github.com", true},
		{"name ending in localhost", "notlocalhost:8080", true},
		{"subdomain of localhost", "api.localhost", true},
		{"empty host", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blocked(tc.hostport); got != tc.want {
				t.Errorf("blocked(%q) = %v, want %v", tc.hostport, got, tc.want)
			}
		})
	}
}

func TestGuardArmedInTestBinary(t *testing.T) {
	if _, ok := http.DefaultTransport.(*guard); !ok {
		t.Fatalf("http.DefaultTransport = %T, want the guard installed by init", http.DefaultTransport)
	}
}

// TestGuardServesLoopback proves the allowed path still reaches the wrapped
// transport: every httptest server in the suite depends on it.
func TestGuardServesLoopback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer ts.Close()

	res, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", ts.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusTeapot)
	}
}
