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

export function stopRunWarning(id: string): string {
  return (
    `The run stops now. Work in progress is saved at the last checkpoint and ${id} ` +
    'stays resumable — Start picks it up from there. Every other row stays queued. ' +
    'To take it out of the queue for good, stop it first, then remove the parked row.'
  )
}
