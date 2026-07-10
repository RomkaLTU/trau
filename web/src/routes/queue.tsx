import { createFileRoute, redirect } from '@tanstack/react-router'

// The standalone Queue screen was folded into the Loop card, which now builds
// and drains one ordered queue. Old links land there.
export const Route = createFileRoute('/queue')({
  beforeLoad: () => {
    throw redirect({ to: '/loop' })
  },
})
