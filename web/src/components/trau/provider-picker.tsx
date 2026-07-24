import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Check, ChevronsUpDown } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Command,
  CommandGroup,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { configQueryOptions } from '@/lib/config'
import {
  clearedProviderPin,
  providerPinLabel,
  resolveProviderPin,
  type ProviderPinSource,
} from '@/lib/provider-pin'
import { cn } from '@/lib/utils'

export function ProviderPinDisplay({ issue }: { issue: ProviderPinSource }) {
  const pin = resolveProviderPin(issue)
  if (pin.kind === 'pinned') {
    return <span className="font-mono text-foreground">{pin.provider}</span>
  }
  return <span className="text-muted-foreground">{providerPinLabel(pin)}</span>
}

// Owns the provider list but not the write: the caller decides what a choice
// means. The list is the server-driven one the queue form uses, so a provider
// trau gains is offered here without a web change.
export function ProviderPinPicker({
  repo,
  issue,
  onSelect,
  disabled,
}: {
  repo: string
  issue: ProviderPinSource
  onSelect: (next: string) => void
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const config = useQuery(configQueryOptions(repo))
  const providers = config.data?.providers ?? []
  const provider = issue.provider_pin ?? ''
  const cleared = clearedProviderPin(issue)

  const choose = (next: string) => {
    setOpen(false)
    if (next !== provider) onSelect(next)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          disabled={disabled}
          aria-label="Change provider"
          className="-ml-2 h-7 gap-1.5 px-2 text-xs font-normal"
        >
          <ProviderPinDisplay issue={issue} />
          <ChevronsUpDown className="size-3 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64 p-0">
        <Command shouldFilter={false}>
          <CommandList>
            <CommandGroup>
              {providers.map((name) => (
                <CommandItem
                  key={name}
                  value={name}
                  onSelect={() => choose(name)}
                >
                  <Check
                    className={cn(
                      'size-4',
                      provider === name ? 'opacity-100' : 'opacity-0',
                    )}
                  />
                  <span className="flex-1 truncate font-mono">{name}</span>
                </CommandItem>
              ))}
            </CommandGroup>
            <CommandSeparator />
            <CommandGroup>
              <CommandItem value="repo-default" onSelect={() => choose('')}>
                <Check
                  className={cn('size-4', provider ? 'opacity-0' : 'opacity-100')}
                />
                <span className="flex-1 truncate text-muted-foreground">
                  {providerPinLabel(cleared)}
                </span>
              </CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
