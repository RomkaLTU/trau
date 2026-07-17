import { useEffect } from 'react'

// The grill conversations open on screen right now, keyed by session id. The
// toaster reads it to stay quiet about a question the user is already answering.
const openSessions = new Set<string>()

export function useOpenConversation(sessionId: string): void {
  useEffect(() => {
    openSessions.add(sessionId)
    return () => {
      openSessions.delete(sessionId)
    }
  }, [sessionId])
}

export function isConversationOpen(sessionId: string): boolean {
  return openSessions.has(sessionId)
}
