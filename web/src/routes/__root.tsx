import type { QueryClient } from '@tanstack/react-query'
import {
  Outlet,
  createRootRouteWithContext,
  useRouterState,
} from '@tanstack/react-router'
import { NuqsAdapter } from 'nuqs/adapters/tanstack-router'

import { ActiveRepoProvider } from '@/components/trau/active-repo'
import { AppShell } from '@/components/trau/app-shell'
import {
  CreatedBanner,
  CreatedBannerProvider,
} from '@/components/trau/created-banner'
import { InstallBanner } from '@/components/trau/install-banner'
import { UpdateNotifier } from '@/components/trau/update-notifier'
import { AwayRecap } from '@/components/away-recap'
import { NotificationToaster } from '@/components/notification-toaster'
import { Toaster } from '@/components/ui/sonner'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootLayout,
})

// The run screens spend their vertical budget on the transcript and diff, so the
// recap rides them as a one-line strip instead of the full breakdown.
const RUN_ROUTE_IDS = new Set([
  '/live/$repo/$ticket',
  '/runs_/$repo/$ticket',
  '/team-runs/$repo/$writer/$ticket',
])

function RootLayout() {
  const onRunRoute = useRouterState({
    select: (s) => s.matches.some((m) => RUN_ROUTE_IDS.has(m.routeId)),
  })

  return (
    <NuqsAdapter>
      <ActiveRepoProvider>
        <CreatedBannerProvider>
          <AppShell>
            {/* A column so a route can claim the height left under the recap banner
                instead of guessing at it; routes that just stack keep their own
                content height. */}
            <div className="mx-auto flex min-h-screen w-full max-w-[96rem] flex-col px-6 py-8">
              <CreatedBanner />
              <AwayRecap collapsible={onRunRoute} />
              <InstallBanner />
              <Outlet />
            </div>
            <NotificationToaster />
          </AppShell>
          {/* Outside the shell: its main column is a z-[1] stacking context that
              would paint every toast under an open sheet or dialog. */}
          <UpdateNotifier />
          <Toaster />
        </CreatedBannerProvider>
      </ActiveRepoProvider>
    </NuqsAdapter>
  )
}
