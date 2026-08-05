import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow } from '@/components/trau'
import { MCPConnectSection } from '@/components/trau/mcp-connect'
import { PushNotificationsSection } from '@/components/trau/push-notifications-section'
import { UpdatesSection } from '@/components/trau/updates-section'
import { standardTitle, usePageTitle } from '@/lib/page-title'

export const Route = createFileRoute('/hub')({
  component: Hub,
})

function Hub() {
  usePageTitle(standardTitle('Hub'))

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          CONFIGURE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Hub
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          The running hub itself — updates, notification delivery, and how
          external agents connect.
        </p>
      </header>

      <UpdatesSection />

      <PushNotificationsSection />

      <MCPConnectSection />
    </div>
  )
}
