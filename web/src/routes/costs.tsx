import { createFileRoute } from '@tanstack/react-router'

import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

export const Route = createFileRoute('/costs')({
  component: Costs,
})

function Costs() {
  return (
    <Card className="max-w-md">
      <CardHeader>
        <CardTitle>Costs</CardTitle>
        <CardDescription>
          Token spend across providers and runs will be summarized here.
        </CardDescription>
      </CardHeader>
    </Card>
  )
}
