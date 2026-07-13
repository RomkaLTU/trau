import type { QueryClient } from '@tanstack/react-query'
import { Outlet, createRootRouteWithContext } from '@tanstack/react-router'
import { NuqsAdapter } from 'nuqs/adapters/tanstack-router'

import { ActiveRepoProvider } from '@/components/trau/active-repo'
import { AppShell } from '@/components/trau/app-shell'
import { AwayRecap } from '@/components/away-recap'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootLayout,
})

function RootLayout() {
  return (
    <NuqsAdapter>
      <ActiveRepoProvider>
        <AppShell>
          <div className="w-full px-6 py-8">
            <AwayRecap />
            <Outlet />
          </div>
        </AppShell>
      </ActiveRepoProvider>
    </NuqsAdapter>
  )
}
