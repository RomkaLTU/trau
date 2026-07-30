# Contributing

Thanks for your interest in Trau.

**We are not accepting external contributions at this time.** Trau is a
company-owned tool published under the Apache License 2.0 so others can use,
study, and fork it — but we don't currently take outside pull requests, and
incoming PRs will be closed. There is no CLA.

You are welcome to:

- **Use** the project under the terms of the [LICENSE](LICENSE).
- **Fork** it and adapt it for your own needs.
- **Open an issue** to report a bug or ask a question — no promise of a fix,
  but we read them.

If this policy changes, this file will be updated.

## For team members

Read [AGENTS.md](AGENTS.md) first — commands, the package seams, and the
invariants worth failing a review over. Then [CONTEXT.md](CONTEXT.md), which
defines the domain vocabulary; each term carries an explicit *Avoid* list of
banned near-synonyms, and those are enforced in code, comments, commit messages
and UI copy alike.

Work on a branch, open a PR, squash-merge to `main`.

### Run the gate locally — nothing else will

**There is no PR-triggered CI.** The only check that runs on a pull request is
GitGuardian, a secrets scanner. A PR can break every test in the tree and still
show all-green, so a green PR is not evidence the code works:

```bash
make fmt && make vet && make test
```

`make vet` runs the `GOOS=windows` compile gate before `go vet`, so a
Windows-only compile break surfaces there rather than at release time. `make
test` is `go test -race ./...`, and on Windows it needs a C compiler because
`-race` has no pure-Go implementation there (`scoop install mingw`).

### Say which OS you actually ran

Trau is developed on **Windows, Linux and macOS**, and nothing verifies the
other two before merge. `make -n <target> HOST_GOOS=linux` prints the emitted
command but never executes it. Report the platform you tested and call the
others **unverified** rather than implying all three passed.

### Commits

[Conventional Commits](https://www.conventionalcommits.org) (`feat(scope):`,
`fix:`, `docs:`, `chore:`) — release notes are generated from them
([ADR 0002](docs/adr/0002-release-and-distribution-strategy.md)). Include the
tracker ticket ID; commits and PR comments act as the design changelog.

Architectural decisions get an ADR under [`docs/adr/`](docs/adr/).
