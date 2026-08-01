import { Link } from '@tanstack/react-router'
import { TriangleAlert } from 'lucide-react'

export function NoBrowserBanner() {
  return (
    <div
      role="status"
      className="flex items-start gap-2.5 rounded-lg border border-warn/50 bg-warn/12 px-4 py-3"
    >
      <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" aria-hidden="true" />
      <div className="flex flex-col gap-0.5">
        <p className="font-mono text-sm font-medium text-warn">Browser verify skipped</p>
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          This slice changed a UI surface, but verify never drove it in a browser. Review whether the
          UI behavior was actually checked, and add the repo&apos;s{' '}
          <Link
            to="/app-urls"
            className="text-primary underline-offset-4 hover:underline"
          >
            app URL
          </Link>{' '}
          from the Overview card if it is missing.
        </p>
      </div>
    </div>
  )
}
