import { TriangleAlert } from 'lucide-react'
import { Link } from '@tanstack/react-router'

export function NoSkillsBanner() {
  return (
    <div
      role="status"
      className="flex items-start gap-2.5 rounded-lg border border-warn/50 bg-warn/12 px-4 py-3"
    >
      <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" aria-hidden="true" />
      <div className="flex flex-col gap-0.5">
        <p className="font-mono text-sm font-medium text-warn">
          Build loaded none of its planned skills
        </p>
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          The build prompt named skills to load and the agent used none of them. Check their
          activation on the{' '}
          <Link to="/skills" className="text-warn underline-offset-4 hover:underline">
            Skills page
          </Link>
          .
        </p>
      </div>
    </div>
  )
}
