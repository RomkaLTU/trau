import { createFileRoute, redirect } from '@tanstack/react-router'

// The Queue is the web's only start path (ADR 0015): Run next on the Loop card
// replaced the direct-spawn Run once page. Old links land there.
export const Route = createFileRoute('/run-once')({
  beforeLoad: () => {
    throw redirect({ to: '/loop' })
  },
})
