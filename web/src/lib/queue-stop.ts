// The copy for the per-row Stop: the gesture is the repo's own queue Stop, so it
// says what a Stop keeps — the checkpoint and the ticket's place in the queue —
// and names the two steps a removal takes, since the row's X is gone while it
// runs. The verb is Stop, never Pause: Paused names a failure class.
export const STOP_RUN_HINT =
  'Stop the run (progress is saved — resumable with Start)'

export const STOP_RUN_LABEL = 'Stop'

export function stopRunTitle(id: string): string {
  return `Stop ${id}?`
}

// running is how many lanes the repo has in flight. A Stop is the repo's queue
// Stop, so with several lanes it ends all of them — the copy has to say so rather
// than describe the one row the button sits on.
export function stopRunWarning(id: string, running = 1): string {
  const scope =
    running > 1
      ? `All ${running} runs in flight stop now. Work in progress is saved at each ticket's ` +
        'last checkpoint and every one of them stays resumable — Start picks them up from there. '
      : `The run stops now. Work in progress is saved at the last checkpoint and ${id} ` +
        'stays resumable — Start picks it up from there. '
  return (
    scope +
    'Every other row stays queued. To take it out of the queue for good, stop it ' +
    'first, then remove the parked row.'
  )
}
