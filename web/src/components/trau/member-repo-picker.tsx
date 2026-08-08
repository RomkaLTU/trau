import { useEffect, useState, type ReactNode } from 'react'
import { Check, ChevronDown, GitBranch } from 'lucide-react'

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import type { RepoView } from '@/lib/instances'
import { useStartRepo } from '@/lib/start-repo'
import { cn } from '@/lib/utils'

// Keyed by root rather than name so two members sharing a basename stay
// separate rows the highlight can step between.
function MemberOptions({
  members,
  value,
  suggested,
  onPick,
}: {
  members: readonly RepoView[]
  value: string
  suggested: string
  onPick: (repo: string) => void
}) {
  return (
    <Command>
      <CommandInput placeholder="Search repos…" />
      <CommandList>
        <CommandEmpty>No matching repo.</CommandEmpty>
        <CommandGroup>
          {members.map((member) => (
            <CommandItem
              key={member.root}
              value={member.root}
              keywords={[member.name]}
              onSelect={() => onPick(member.name)}
            >
              <Check
                className={cn(
                  'size-4',
                  member.name === value ? 'opacity-100' : 'opacity-0',
                )}
              />
              <span className="flex-1 truncate font-mono">{member.name}</span>
              {member.name === suggested && <SuggestedTag />}
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </Command>
  )
}

function SuggestedTag() {
  return (
    <span className="shrink-0 rounded border border-border bg-muted/60 px-1 font-mono text-[0.6rem] uppercase tracking-[0.12em] text-muted-foreground">
      suggested
    </span>
  )
}

/**
 * Target-repo picker for launch forms in a project with several member repos.
 * The run has to land in one member, so beside the shell's scoped repo the
 * ticket carries a target of its own — opened on the hub's suggestion, marked as
 * one until the user picks.
 */
export function MemberRepoField({
  members,
  value,
  suggested,
  picked,
  ticket,
  onPick,
  className,
}: {
  members: readonly RepoView[]
  value: string
  suggested: string
  picked: boolean
  ticket: string
  onPick: (repo: string) => void
  className?: string
}) {
  const [open, setOpen] = useState(false)

  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        target repo · this ticket
      </span>
      <div className={cn('w-56', className)}>
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <button
              type="button"
              aria-label="target repo"
              className="flex w-full items-center justify-between gap-2 rounded-md border border-border bg-input px-2.5 py-1.5 hover:border-ring/50"
            >
              <span className="inline-flex min-w-0 items-center gap-2 font-mono text-sm text-foreground">
                <GitBranch
                  className="size-3.5 shrink-0 text-primary"
                  aria-hidden="true"
                />
                <span className="truncate">{value || '—'}</span>
              </span>
              <span className="inline-flex shrink-0 items-center gap-1.5">
                {!picked && value === suggested && <SuggestedTag />}
                <ChevronDown
                  className="size-3.5 text-muted-foreground"
                  aria-hidden="true"
                />
              </span>
            </button>
          </PopoverTrigger>
          <PopoverContent
            align="start"
            className="w-(--radix-popover-trigger-width) min-w-56 p-0"
          >
            <MemberOptions
              members={members}
              value={value}
              suggested={suggested}
              onPick={(repo) => {
                setOpen(false)
                onPick(repo)
              }}
            />
          </PopoverContent>
        </Popover>
      </div>
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        This project has {members.length} repos — {ticket} lands in the queue of
        the one you pick.
      </p>
    </div>
  )
}

/**
 * Wraps a start action that has no room for a field. A repo standing on its own
 * starts straight away, rendering nothing but the caller's own button. A repo in
 * a project with several members asks which one first — and starts straight away
 * after all once the hint narrows those members down to the one the ticket lives
 * in, since the hint only arrives after the click that asked for the start.
 */
export function StartIn({
  repo,
  ticket,
  onStart,
  children,
}: {
  repo: string
  ticket: string
  onStart: (repo: string) => void
  children: (begin: () => void) => ReactNode
}) {
  const [asked, setAsked] = useState(false)
  const start = useStartRepo(repo, ticket, asked)

  useEffect(() => {
    if (!asked || start.choose) return
    setAsked(false)
    onStart(start.target)
  }, [asked, start.choose, start.target, onStart])

  if (!start.choose) return <>{children(() => onStart(start.target))}</>
  return (
    <Popover open={asked} onOpenChange={setAsked}>
      <PopoverTrigger asChild>{children(() => setAsked(true))}</PopoverTrigger>
      <PopoverContent side="top" align="end" className="w-64 p-0">
        <MemberOptions
          members={start.members}
          value={start.target}
          suggested={start.suggested}
          onPick={(member) => {
            setAsked(false)
            onStart(member)
          }}
        />
      </PopoverContent>
    </Popover>
  )
}
