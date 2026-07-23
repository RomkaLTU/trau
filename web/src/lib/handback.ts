import { apiFetch } from "./api";

// Handback is a run's pending hand-back choice (ADR 0018): a terminal takeover
// steered the ticket by hand and no phase transition has cleared the stamp
// since. `phase` names the phase a hand-back re-enters; `advance` is the
// checkpoint value the operator can promote it to instead, absent when the
// recorded phase has no completed value.
export interface Handback {
  at: string;
  phase: string;
  advance?: string;
}

export interface HandbackResult {
  ticket: string;
  from: string;
  phase: string;
}

// settleHandback resolves the takeover stamp before the ticket re-enters the
// loop: re-running leaves the phase where it is, otherwise the interrupted phase
// is recorded as finished.
export async function settleHandback(
  repo: string,
  ticket: string,
  rerun: boolean,
): Promise<HandbackResult> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/advance`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ rerun }),
    },
  );
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string;
    } | null;
    throw new Error(detail?.error ?? `hand-back failed: ${res.status}`);
  }
  return res.json();
}

// pendingHandback finds a ticket's hand-back choice among a repo's runs.
export function pendingHandback(
  runs: readonly { ticket: string; handback?: Handback }[] | undefined,
  ticket: string,
): Handback | null {
  return runs?.find((r) => r.ticket === ticket)?.handback ?? null;
}

// A stamp written without a session phase still has to read as a sentence.
const unnamedStep = "the interrupted step";

// handbackChoices renders the two options a pending hand-back offers; the
// advance option is absent when the recorded phase has no completed value.
export function handbackChoices(handback: Handback): {
  rerun: string;
  advance: string | null;
} {
  const step = handback.phase || unnamedStep;
  return {
    rerun: `Re-run ${step}`,
    advance: handback.advance
      ? `${step.charAt(0).toUpperCase()}${step.slice(1)} is done — continue to the next step`
      : null,
  };
}

// handbackStep names the interrupted phase in prose.
export function handbackStep(handback: Handback): string {
  return handback.phase ? `the ${handback.phase} step` : unnamedStep;
}
