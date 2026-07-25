import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { useNavigate } from '@tanstack/react-router'
import { CheckCircle2, TriangleAlert, X } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import {
  describeCreated,
  describeCreatedFailures,
  type CreatedIssue,
} from '@/lib/created-banner'
import { cn } from '@/lib/utils'

interface CreatedBannerValue {
  created: CreatedIssue | null
  publish: (created: CreatedIssue) => void
  dismiss: () => void
}

const CreatedBannerContext = createContext<CreatedBannerValue | null>(null)

// CreatedBannerProvider holds the one filed-issue receipt the shell renders above
// every page. It is in-memory only — a reload starts empty — and nothing but the
// banner's own View/X clears it, so it outlives arbitrary route changes.
export function CreatedBannerProvider({ children }: { children: ReactNode }) {
  const [created, setCreated] = useState<CreatedIssue | null>(null)

  const dismiss = useCallback(() => setCreated(null), [])

  const value = useMemo<CreatedBannerValue>(
    () => ({ created, publish: setCreated, dismiss }),
    [created, dismiss],
  )

  return (
    <CreatedBannerContext.Provider value={value}>
      {children}
    </CreatedBannerContext.Provider>
  )
}

export function useCreatedBanner(): CreatedBannerValue {
  const ctx = useContext(CreatedBannerContext)
  if (!ctx) {
    throw new Error(
      'useCreatedBanner must be used within a CreatedBannerProvider',
    )
  }
  return ctx
}

export function CreatedBanner() {
  const { created, dismiss } = useCreatedBanner()
  const { scope, setScope } = useActiveRepo()
  const navigate = useNavigate()

  if (!created) return null

  const failures = describeCreatedFailures(created.failedSteps)
  const warn = failures !== ''

  // The banner outlives a scope switch, so View restores the repo it was filed
  // under before opening the drawer there.
  const view = () => {
    if (created.repo !== scope) setScope(created.repo)
    dismiss()
    void navigate({ to: '/backlog', search: { issue: created.id } })
  }

  return (
    <div
      role="status"
      className={cn(
        'mb-6 flex items-center gap-3 rounded-lg border px-4 py-2.5 text-sm',
        warn ? 'border-warn/40 bg-warn/5' : 'border-done/40 bg-done/5',
      )}
    >
      {warn ? (
        <TriangleAlert className="size-4 shrink-0 text-warn" aria-hidden />
      ) : (
        <CheckCircle2 className="size-4 shrink-0 text-done" aria-hidden />
      )}
      <p className="min-w-0 truncate text-foreground">
        {describeCreated(created)}
        {warn && <span className="text-muted-foreground"> — {failures}</span>}
      </p>
      <div className="ml-auto flex shrink-0 items-center gap-1">
        <button
          type="button"
          onClick={view}
          className="rounded-md border px-2.5 py-1 text-xs font-medium transition-colors hover:bg-accent"
        >
          View
        </button>
        <button
          type="button"
          onClick={dismiss}
          aria-label="Dismiss created banner"
          className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <X className="size-4" />
        </button>
      </div>
    </div>
  )
}
