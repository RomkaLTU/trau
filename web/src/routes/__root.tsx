import type { QueryClient } from '@tanstack/react-query'
import { Outlet, createRootRouteWithContext } from '@tanstack/react-router'
import { NuqsAdapter } from 'nuqs/adapters/tanstack-router'

import { ActiveRepoProvider } from '@/components/trau/active-repo'
import { AppShell } from '@/components/trau/app-shell'
import {
  CreatedBanner,
  CreatedBannerProvider,
} from '@/components/trau/created-banner'
import { AwayRecap } from '@/components/away-recap'
import { InterviewDock } from '@/components/grill/dock'
import { NotificationToaster } from '@/components/notification-toaster'
import { Toaster } from '@/components/ui/sonner'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootLayout,
})

function RootLayout() {
  return (
    <NuqsAdapter>
      <ActiveRepoProvider>
        <CreatedBannerProvider>
          <AppShell>
            {/* A column so a route can claim the height left under the recap banner
                instead of guessing at it; routes that just stack keep their own
                content height. */}
            <div className="flex min-h-screen w-full flex-col px-6 py-8">
              <CreatedBanner />
              <AwayRecap />
              <Outlet />
            </div>
            <NotificationToaster />
          </AppShell>
          {/* Outside the shell: its main column is a z-[1] stacking context that
              would paint every toast — and the dock — under an open sheet or dialog. */}
          <InterviewDock />
          <Toaster />
        </CreatedBannerProvider>
      </ActiveRepoProvider>
    </NuqsAdapter>
  )
}
