import type { QueryClient } from '@tanstack/react-query'
import { Outlet, createRootRouteWithContext } from '@tanstack/react-router'
import { NuqsAdapter } from 'nuqs/adapters/tanstack-router'

import { ActiveRepoProvider } from '@/components/trau/active-repo'
import { AppShell } from '@/components/trau/app-shell'
import { AwayRecap } from '@/components/away-recap'
import { NotificationToaster } from '@/components/notification-toaster'
import { Toaster } from '@/components/ui/sonner'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootLayout,
})

function RootLayout() {
  return (
    <NuqsAdapter>
      <ActiveRepoProvider>
        <AppShell>
          {/* A column so a route can claim the height left under the recap banner
              instead of guessing at it; routes that just stack keep their own
              content height. */}
          <div className="flex min-h-screen w-full flex-col px-6 py-8">
            <AwayRecap />
            <Outlet />
          </div>
          <NotificationToaster />
          <Toaster />
        </AppShell>
      </ActiveRepoProvider>
    </NuqsAdapter>
  )
}
