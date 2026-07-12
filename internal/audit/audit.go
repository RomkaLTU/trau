// Package audit holds the repo-wide guardrail that keeps trau DB-first: after the
// run-data epic (ADR 0008), the only runtime disk reads left are the §6 exemption
// list — config, repo-owned content, provider-owned files, ephemeral
// agent-interface temp files, the timelog, the databases, and diagnostic probes —
// plus the one-shot legacy importers that migrate the file era on first hub touch.
// The enforcing check is TestNoRuntimeFileReadsOutsideExemptions; this file exists
// only to carry that documentation and give the package a home.
package audit
