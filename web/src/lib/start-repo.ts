import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { useActiveRepo } from '@/components/trau/active-repo'
import type { RepoView } from './instances'
import {
  projectMembers,
  projectsQueryOptions,
  startRepoQueryOptions,
  type StartRepoHint,
} from './projects'

export interface StartRepo {
  // choose is true only when there is a choice to make: more than one member of
  // the repo's project can take the ticket. Every surface gates its selector on
  // it, so a repo standing alone keeps the UI and the calls it had before
  // projects existed.
  choose: boolean
  members: RepoView[]
  target: string
  // suggested is the member the hub proposes and the selector tags as such. A
  // repo the ticket is only remembered in stays untagged: it is where the ticket
  // already ran, not a guess about where it belongs.
  suggested: string
  picked: boolean
  pick: (repo: string) => void
}

// startChoice reads the hub's hint into the state the start surfaces render. The
// hint's own member list is the one to offer rather than the project's: a ticket
// that lives in a single member's store cannot be started in the others, and the
// picker must not propose a repo whose queue would refuse it.
export function startChoice(
  repo: string,
  members: readonly RepoView[],
  hint: StartRepoHint | undefined,
  picked: string,
): Omit<StartRepo, 'pick'> {
  const startable = members.filter(
    (member) => !hint || hint.repos.some((r) => r.root === member.root),
  )
  const hinted = hint?.suggested ?? ''
  const known = (name: string) => startable.some((m) => m.name === name)
  return {
    choose: startable.length > 1,
    members: startable,
    target: [picked, hinted].find(known) ?? repo,
    suggested: hint?.reason === 'remembered' ? '' : hinted,
    picked: picked !== '' && picked !== hinted,
  }
}

// useStartRepo answers which member repo a ticket's run should be created in. A
// pick holds for as long as that ticket is in hand rather than being overwritten
// by the hint that arrives after it. Surfaces that ask per row — a backlog page,
// say — pass active only while their picker is open, so listing tickets costs
// nothing extra.
export function useStartRepo(
  repo: string,
  ticket: string,
  active = true,
): StartRepo {
  const { repos } = useActiveRepo()
  const projects = useQuery(projectsQueryOptions)
  const { project, members } = projectMembers(
    repo,
    repos,
    projects.data?.projects ?? [],
  )
  const hint = useQuery(
    startRepoQueryOptions(members.length > 1 && active ? project : '', ticket),
  )

  const [picked, setPicked] = useState('')
  useEffect(() => setPicked(''), [repo, ticket])

  return { ...startChoice(repo, members, hint.data, picked), pick: setPicked }
}
