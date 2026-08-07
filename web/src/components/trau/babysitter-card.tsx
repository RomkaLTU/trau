import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'

import { CopyButton } from '@/components/trau/copy-button'
import { SegmentedControl } from '@/components/trau/segmented-control'
import { TerminalCard } from '@/components/trau/terminal-card'
import {
  DEFAULT_QUARANTINE_LABEL,
  babysitterPrompt,
} from '@/lib/babysitter'
import { configQueryOptions } from '@/lib/config'
import { healthQueryOptions } from '@/lib/health'
import { mcpEndpoint, mcpSetups, type MCPClientID } from '@/lib/mcp'

function useQuarantineLabel(repo: string): string {
  const { data } = useQuery(configQueryOptions(repo))
  return useMemo(() => {
    const key = (data?.keys ?? []).find((k) => k.key === 'QUARANTINE_LABEL')
    return key?.value || DEFAULT_QUARANTINE_LABEL
  }, [data])
}

export function BabysitterCard({ repo, root }: { repo: string; root?: string }) {
  const health = useQuery(healthQueryOptions)
  const [client, setClient] = useState<MCPClientID>('claude')

  const url = mcpEndpoint(window.location.origin)
  const tokenRequired = health.data?.token_required ?? false
  const quarantineLabel = useQuarantineLabel(repo)
  const setups = mcpSetups(url, tokenRequired)
  const active = setups.find((s) => s.id === client) ?? setups[0]

  const prompt = babysitterPrompt({
    repo,
    origin: window.location.origin,
    tokenRequired,
    quarantineLabel,
    root,
  })

  return (
    <TerminalCard
      title="babysit from your terminal"
      className="max-w-page"
      bodyClassName="flex flex-col gap-4 p-4"
    >
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        Paste this into your own terminal agent to babysit this drain while
        you&apos;re away. It watches {repo} over MCP, repairs trivial
        operational faults, files{' '}
        <code className="font-mono text-xs text-foreground">
          {quarantineLabel}
        </code>{' '}
        tickets for suspected trau defects, and prints a highlights report when
        the drain finishes.
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <CopyButton value={prompt} label="Copy babysitter prompt" />
        <span className="font-mono text-xs text-faint">
          copy the prompt, paste it into Claude Code, Codex CLI or any agent
          with shell access
        </span>
      </div>

      <pre className="max-h-56 overflow-y-auto whitespace-pre-wrap break-words rounded-md border border-border bg-input p-3 font-mono text-xs leading-relaxed text-muted-foreground">
        {prompt}
      </pre>

      {tokenRequired && (
        <p className="inline-flex items-start gap-2 rounded-md border border-warn/40 bg-warn/[0.08] px-2.5 py-2 text-xs leading-relaxed text-muted-foreground">
          <ShieldCheck
            className="mt-0.5 size-3.5 shrink-0 text-warn"
            aria-hidden="true"
          />
          <span>
            This hub is exposed, so the babysitter needs the serve token. Export
            it as{' '}
            <code className="font-mono text-foreground">TRAU_SERVE_TOKEN</code>{' '}
            in the terminal you paste into — the prompt says so too.
          </span>
        </p>
      )}

      <details className="group rounded-md border border-border bg-input/40">
        <summary className="cursor-pointer list-none px-3 py-2 font-mono text-xs text-muted-foreground marker:content-none hover:text-foreground">
          no trau MCP server in that agent yet? add it first
        </summary>
        <div className="flex flex-col gap-3 border-t border-border px-3 py-3">
          <div className="flex flex-wrap items-center gap-3">
            <SegmentedControl
              aria-label="MCP client"
              options={setups.map((s) => ({ value: s.id, label: s.label }))}
              value={active.id}
              onChange={setClient}
            />
            <span className="font-mono text-xs text-faint">
              {active.target}
            </span>
          </div>
          <div className="relative">
            <pre className="overflow-x-auto rounded-md border border-border bg-input p-3 pr-12 font-mono text-xs leading-relaxed text-foreground">
              {active.snippet}
            </pre>
            <CopyButton
              value={active.snippet}
              label={`Copy ${active.label} snippet`}
              className="absolute right-2 top-2"
            />
          </div>
        </div>
      </details>
    </TerminalCard>
  )
}
