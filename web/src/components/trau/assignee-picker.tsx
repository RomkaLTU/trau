import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Check, ChevronsUpDown } from 'lucide-react'

import { AssigneeAvatar } from '@/components/trau/assignee-avatar'
import { Button } from '@/components/ui/button'
import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { assigneeLabel, type Assignee } from '@/lib/assignee'
import {
  assignableUsersQueryOptions,
  isAssignUnsupported,
} from '@/lib/assignees'
import { cn } from '@/lib/utils'

export function AssigneeDisplay({ assignee }: { assignee?: Assignee | null }) {
  if (!assignee) {
    return <span className="text-muted-foreground">Unassigned</span>
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-foreground">
      <AssigneeAvatar assignee={assignee} className="size-5 text-[0.6rem]" />
      {assigneeLabel(assignee)}
    </span>
  )
}

// Owns the assignable-users lookup but not the write: the caller decides what a
// choice means. A tracker that answers the lookup unsupported never offers the
// control at all — the row stays the plain read-only one.
export function AssigneePicker({
  repo,
  assignee,
  onSelect,
  disabled,
}: {
  repo: string
  assignee?: Assignee | null
  onSelect: (next: Assignee | null) => void
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [term, setTerm] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    const id = setTimeout(() => setDebounced(term.trim()), 150)
    return () => clearTimeout(id)
  }, [term])

  // The unfiltered lookup runs on mount as the support probe and shares its cache
  // entry with the first open, so the trigger appears only once the tracker has
  // answered rather than opening a popover that cannot be filled.
  const probe = useQuery(assignableUsersQueryOptions(repo, ''))

  const users = useQuery({
    ...assignableUsersQueryOptions(repo, debounced),
    enabled: open && repo !== '',
  })

  if (probe.isPending || isAssignUnsupported(probe.error)) {
    return <AssigneeDisplay assignee={assignee} />
  }

  const candidates = users.data?.users ?? []

  let status: string | null = null
  if (users.isFetching) status = 'Searching people…'
  else if (users.error) status = users.error.message
  else if (candidates.length === 0) status = 'No people found.'

  const choose = (next: Assignee | null) => {
    setOpen(false)
    if ((next?.id ?? '') !== (assignee?.id ?? '')) onSelect(next)
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) setTerm('')
      }}
    >
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          disabled={disabled}
          aria-label="Change assignee"
          className="-ml-2 h-7 gap-1.5 px-2 text-xs font-normal"
        >
          <AssigneeDisplay assignee={assignee} />
          <ChevronsUpDown className="size-3 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-60 p-0">
        <Command shouldFilter={false}>
          <CommandInput
            value={term}
            onValueChange={setTerm}
            placeholder="Search people…"
          />
          <CommandList>
            <CommandGroup>
              {candidates.map((user) => (
                <CommandItem
                  key={user.id}
                  value={user.id}
                  onSelect={() => choose(user)}
                >
                  <Check
                    className={cn(
                      'size-4',
                      assignee?.id === user.id ? 'opacity-100' : 'opacity-0',
                    )}
                  />
                  <AssigneeAvatar
                    assignee={user}
                    className="size-5 text-[0.6rem]"
                  />
                  <span className="flex-1 truncate">{assigneeLabel(user)}</span>
                </CommandItem>
              ))}
            </CommandGroup>
            {status && (
              <p className="py-6 text-center text-sm text-muted-foreground">
                {status}
              </p>
            )}
            <CommandSeparator />
            <CommandGroup>
              <CommandItem value="unassigned" onSelect={() => choose(null)}>
                <Check
                  className={cn('size-4', assignee ? 'opacity-0' : 'opacity-100')}
                />
                <span className="flex-1 truncate text-muted-foreground">
                  Unassigned
                </span>
              </CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
