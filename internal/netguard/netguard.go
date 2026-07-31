// Package netguard makes "no test reaches a real external API" a hard
// requirement rather than a convention (ADR 0027). A blank import from every
// test-bearing package installs a deny-by-default http.DefaultTransport: a
// request to anything but a literal loopback address kills the test binary on
// the spot. There is no opt-out — no env var, no build tag, no setter.
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
)

// The guard is armed only inside a test binary, so an accidental production
// import can never ship a transport that exits the hub mid-request.
func init() {
	if !testing.Testing() {
		return
	}
	http.DefaultTransport = &guard{next: http.DefaultTransport}
}

type guard struct{ next http.RoundTripper }

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if blocked(req.URL.Host) {
		refuse(req) // never returns
	}
	return g.next.RoundTrip(req)
}

// refuse kills the test binary. It exits rather than panics: net/http recovers
// handler panics, which would soften a violation inside an httptest handler
// into a 500 instead of a failed run.
func refuse(req *http.Request) {
	fmt.Fprintf(os.Stderr, "\n✗ netguard: a test tried to reach a non-loopback host\n"+
		"    %s %s\n"+
		"  Tests must never reach a real external API. Serve the endpoint from an\n"+
		"  httptest server on loopback, or go through the package's fake seam.\n"+
		"  See AGENTS.md.\n\n", req.Method, req.URL)
	os.Exit(1)
}

// blocked reports whether hostport names anything but a literal loopback
// address. The decision is made on the URL host, before any resolution, so a
// name /etc/hosts points at 127.0.0.1 is refused like any other name.
func blocked(hostport string) bool {
	host := (&url.URL{Host: hostport}).Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
