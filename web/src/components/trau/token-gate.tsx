import {
  useCallback,
  useEffect,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'
import { useQueryClient } from '@tanstack/react-query'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Eyebrow } from '@/components/trau/eyebrow'
import { TerminalCard } from '@/components/trau/terminal-card'
import { onUnauthorized, serveToken, setServeToken } from '@/lib/auth'
import { hubHealth } from '@/lib/connectivity'

type Phase = 'checking' | 'ok' | 'prompt'

// only a 401 blocks the app; an unreachable hub is the HubGate's business
async function probe(): Promise<boolean> {
  const res = await hubHealth()
  return res === null || res.status !== 401
}

export function AuthGate({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [phase, setPhase] = useState<Phase>('checking')
  const [staleToken, setStaleToken] = useState(false)

  useEffect(() => {
    let active = true
    probe().then((ok) => {
      if (!active) return
      if (ok) {
        setPhase('ok')
      } else {
        setStaleToken(serveToken() !== '')
        setPhase('prompt')
      }
    })
    return () => {
      active = false
    }
  }, [])

  useEffect(
    () =>
      onUnauthorized(() => {
        setStaleToken(false)
        setPhase('prompt')
      }),
    [],
  )

  const submit = useCallback(
    async (token: string): Promise<boolean> => {
      setServeToken(token)
      if (!(await probe())) return false
      setPhase('ok')
      queryClient.invalidateQueries()
      return true
    },
    [queryClient],
  )

  if (phase === 'ok') return <>{children}</>
  return (
    <div className="furrow-grid flex min-h-screen items-center justify-center p-6">
      {phase === 'prompt' && (
        <TokenEntry staleToken={staleToken} onSubmit={submit} />
      )}
    </div>
  )
}

function TokenEntry({
  staleToken,
  onSubmit,
}: {
  staleToken: boolean
  onSubmit: (token: string) => Promise<boolean>
}) {
  const [value, setValue] = useState('')
  const [pending, setPending] = useState(false)
  const [rejected, setRejected] = useState(staleToken)

  const handle = async (e: FormEvent) => {
    e.preventDefault()
    const token = value.trim()
    if (token === '' || pending) return
    setPending(true)
    setRejected(false)
    if (!(await onSubmit(token))) {
      setRejected(true)
      setPending(false)
    }
  }

  return (
    <TerminalCard title="trau serve — locked" className="w-full max-w-md">
      <form onSubmit={handle} className="flex flex-col gap-4">
        <Eyebrow glyph="action">serve token</Eyebrow>
        <p className="text-sm text-muted-foreground">
          This hub is reachable off-loopback and is gated behind a bearer token.
          Paste the serve token to continue.
        </p>
        <Input
          type="password"
          autoFocus
          autoComplete="off"
          value={value}
          onChange={(e) => {
            setValue(e.target.value)
            setRejected(false)
          }}
          placeholder="SERVE_TOKEN"
          aria-invalid={rejected}
          className="font-mono aria-invalid:border-fail"
        />
        {rejected && (
          <p className="font-mono text-xs text-fail">
            That token was not accepted.
          </p>
        )}
        <Button type="submit" disabled={pending || value.trim() === ''}>
          {pending ? 'Verifying…' : 'Unlock'}
        </Button>
      </form>
    </TerminalCard>
  )
}
