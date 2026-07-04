import { createFileRoute } from '@tanstack/react-router'

import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

export const Route = createFileRoute('/instances')({
  component: Instances,
})

function Instances() {
  return (
    <Card className="max-w-md">
      <CardHeader>
        <CardTitle>Instances</CardTitle>
        <CardDescription>
          Running trau loops on this machine will appear here.
        </CardDescription>
      </CardHeader>
    </Card>
  )
}
