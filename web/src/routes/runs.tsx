import { createFileRoute } from '@tanstack/react-router'

import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

export const Route = createFileRoute('/runs')({
  component: Runs,
})

function Runs() {
  return (
    <Card className="max-w-md">
      <CardHeader>
        <CardTitle>Runs</CardTitle>
        <CardDescription>
          Ticket runs and their outcomes will be listed here.
        </CardDescription>
      </CardHeader>
    </Card>
  )
}
