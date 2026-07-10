import { createFileRoute, redirect } from '@tanstack/react-router'

// The Backlog board was retired: build the queue on the Loop card (which resolves
// bare ids and lists eligible work), and file issues under Author. Old links land
// on Loop.
export const Route = createFileRoute('/backlog')({
  beforeLoad: () => {
    throw redirect({ to: '/loop' })
  },
})
