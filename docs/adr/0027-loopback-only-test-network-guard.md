# ADR 0027 — Loopback-only test network guard

- **Status:** Accepted
- **Date:** 2026-07-31
- **Deciders:** Romas (sole maintainer)

## Context

"No test hits a real provider" was a convention with nothing behind it. An audit
of all test-bearing packages found the isolation resting on ~25 incidental
argument-check guards in `jiraapi`/`azureapi`/`linearapi` — blank project keys
and missing tokens returning `ErrNotEnabled` before the request was built. They
were written as API-contract checks, not network guards: moving one below the
request build turns a unit test into a live API call, silently.

The convention was already broken. A cluster of `internal/webserver` tests
resolved a repo's config against the developer's real `~/.trau.ini`, found a
`QUEUED_LABEL` and Linear credentials there, and posted to
`api.linear.app/graphql` on every run of a machine whose home config carries a
full tracker section — invisibly, because a successful write looks like a
passing test. A second hazard was timing: `Server.Start` arms the daily
release check against `api.github.com`, and only a 5-second `startupDelay`
racing the test body kept it from firing.

One fact made enforcement cheap: every production `http.Client` in the repo sets
`Timeout` and nothing else. All of them ride `http.DefaultTransport`, so a guard
installed there sees every in-process HTTP request the codebase can make.

## Decision

**A test that dials a non-loopback host fails its binary, immediately and
unrecoverably. There is no opt-out.**

1. `internal/netguard`'s `init` replaces `http.DefaultTransport` with a
   deny-by-default wrapper. The decision is made on the URL host before any
   resolution: a literal loopback address (`localhost`, `127.0.0.0/8`, `::1`, any
   port) passes to the wrapped transport untouched; everything else — including a
   name `/etc/hosts` points at loopback — is refused. `httptest` servers and the
   `http://127.0.0.1:1` connection-refused tests are unaffected.
2. A violation prints the method and URL to stderr and calls `os.Exit(1)`. Not a
   panic: `net/http` recovers handler panics, which would soften a violation
   inside an `httptest` handler into a 500 instead of a failed run.
3. The guard arms only when `testing.Testing()` reports a test binary, so an
   accidental production import cannot ship a transport that exits the hub
   mid-request.
4. Coverage is a build-time check, not a convention. Every test-bearing package
   carries a `netguard_test.go` holding nothing but a blank import, and
   `go run ./internal/netguard/check` — a `make test` prerequisite — fails the
   build when a package with test files omits it, or when production code reaches
   the guard at all. It reads `go list -json ./...`, which already skips
   `node_modules`, dot-directories, `web/` and nested modules on every OS
   (ADR 0023).
5. No env var, build tag, or setter disables any of this. If live integration
   tests are ever wanted, that is a new decision, not a flag someone flips.

## Consequences

- The live Linear writes are gone: the offending tests now isolate `HOME` like
  the rest of the package, and `drainServer` disables the release check before
  `Start`, so the update checker's goroutine returns before it can dial. Which
  helpers isolate `HOME` is no longer what keeps a write off the wire, though:
  one that misses it dies at dial time instead of reaching a real tracker. That
  matters on Windows, where `HOME` isolation is a no-op — `os.UserHomeDir` reads
  `USERPROFILE` there — so on a tracker-configured Windows box the guard turns
  those tests red instead of letting the write through, and the fix is to
  isolate `USERPROFILE` alongside `HOME`, as `browseServer` already does.
- A test that reaches an external API fails loudly on the author's machine the
  first time it runs, rather than passing everywhere except a machine whose home
  config happens to hold credentials.
- Paths with no fake seam — `update.Checker.endpoint`, the webserver's attachment
  client, `usage/probe`, `doctor.checkGitHub` — need none pre-emptively. The
  guard makes them self-enforcing: the first test to touch one fails at dial
  time, and the author adds the seam then.
- The guard covers in-process HTTP only. Subprocess egress (`gh`, `curl`, `brew`,
  `npx`) stays covered by the fakeRunner convention — audited clean, with all
  test `git` operations against local temp repos. PATH-shimming those binaries is
  a follow-up if the audit ever stops holding.
- Raw `net.Dial` and a custom `Transport` bypass a transport-level guard. No code
  builds its own `Transport`, and the tree's one raw dial — `portOccupied`
  probing this machine's own hub port — reaches no provider; the invariant
  extends to both by convention, with no mechanism behind it.
