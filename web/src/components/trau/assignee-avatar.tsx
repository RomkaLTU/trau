import { avatarInitials, avatarTone, type Assignee } from '@/lib/assignee'
import { cn } from '@/lib/utils'

export function AssigneeAvatar({
  assignee,
  className,
}: {
  assignee: Assignee
  className?: string
}) {
  return (
    <span
      title={assignee.name}
      aria-label={`Assignee: ${assignee.me ? 'Me' : assignee.name}`}
      className={cn(
        'inline-flex size-6 shrink-0 select-none items-center justify-center rounded-full text-[0.65rem] font-semibold text-white',
        avatarTone(assignee.name),
        className,
      )}
    >
      {avatarInitials(assignee)}
    </span>
  )
}
