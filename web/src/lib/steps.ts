import type { RunState } from '@/components/trau/status-pill'

export type StepState = 'done' | 'active' | 'todo' | 'fail'

export interface Step {
  label: string
  state: StepState
}

export interface LiveSteps {
  steps: Step[]
  subLabel?: string
}

const STEP_LABELS = ['Build', 'Verify', 'Ship'] as const
const NONE = -1
const DONE = STEP_LABELS.length

// ACTIVITY_STEP groups the present-tense Activities into the three display Steps
// of ADR 0009. It is the display edge: the map never crosses the protocol, so
// regrouping Steps later stays a display change.
const ACTIVITY_STEP: Record<string, number> = {
  build: 0,
  lintfix: 0,
  cleanup: 0,
  handoff: 0,
  verify: 1,
  repair: 1,
  bugfix: 1,
  commit: 2,
  pr: 2,
  'ci-wait': 2,
  merge: 2,
  'merge-wait': 2,
}

// CHECKPOINT_STEP reads a past-tense checkpoint as the Step a run is actually in
// once that checkpoint is written — the corrected interpretation of ADR 0009.
// building/built are still inside Build; handed_off means the run has moved on to
// Verify; verified/pr_open mean it is Shipping. It is the fallback for a
// heartbeat without an Activity and the mapping for a stopped run.
const CHECKPOINT_STEP: Record<string, number> = {
  '': NONE,
  building: 0,
  built: 0,
  handed_off: 1,
  verified: 2,
  pr_open: 2,
  merged: DONE,
}

export function activityStep(activity: string): number {
  return ACTIVITY_STEP[activity] ?? NONE
}

export function checkpointStep(phase: string): number {
  return CHECKPOINT_STEP[phase] ?? NONE
}

function stepsAt(active: number, failed = false): Step[] {
  return STEP_LABELS.map((label, i) => {
    if (i < active) return { label, state: 'done' }
    if (i === active) return { label, state: failed ? 'fail' : 'active' }
    return { label, state: 'todo' }
  })
}

// activityText renders an Activity with its optional raw detail as a compact
// human label: "repair2" becomes "repair 2", a bare Activity stays as it is.
export function activityText(activity: string, detail?: string): string {
  const base = (detail ?? '').trim() || activity
  return base.replace(/([a-z])(\d)/i, '$1 $2')
}

// stepName is the active Step under the corrected reading — the Activity when the
// heartbeat carries one, else the checkpoint. Empty while queued or once merged.
export function stepName(activity: string | undefined, phase: string): string {
  const act = (activity ?? '').trim()
  const step = act ? activityStep(act) : checkpointStep(phase)
  return step >= 0 && step < DONE ? STEP_LABELS[step] : ''
}

// liveSteps drives a running loop's stepper from its present-tense Activity, with
// the sub-label the active Step shows ("Verify · repair 2"). A heartbeat without
// an Activity (older CLI) derives the Step from the checkpoint under the
// corrected mapping, so it never renders a completion rank as the active step.
export function liveSteps(
  activity: string | undefined,
  detail: string | undefined,
  phase: string,
): LiveSteps {
  const act = (activity ?? '').trim()
  const step = act ? activityStep(act) : NONE
  if (step >= 0) {
    return {
      steps: stepsAt(step),
      subLabel: `${STEP_LABELS[step]} · ${activityText(act, detail)}`,
    }
  }
  return { steps: stepsAt(checkpointStep(phase)) }
}

// checkpointSteps maps a stopped run's checkpoint to Step progress under the same
// corrected reading; a failed run marks the Step it stopped in.
export function checkpointSteps(phase: string, failed = false): Step[] {
  return stepsAt(checkpointStep(phase), failed)
}

// stepPill labels a live loop by its active Step, staying on the teal active
// palette the running pills already use.
export function stepPill(
  activity: string | undefined,
  phase: string,
): { state: RunState; label: string } {
  const name = stepName(activity, phase)
  return { state: 'active', label: name ? name.toLowerCase() : 'running' }
}
