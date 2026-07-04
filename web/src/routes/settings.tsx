import { createFileRoute } from '@tanstack/react-router'

import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

export const Route = createFileRoute('/settings')({
  component: SettingsPage,
})

function SettingsPage() {
  return (
    <Card className="max-w-md">
      <CardHeader>
        <CardTitle>Settings</CardTitle>
        <CardDescription>
          Configuration for the serve hub will live here.
        </CardDescription>
      </CardHeader>
    </Card>
  )
}
