import type { ComponentType } from 'react'
import { Database } from 'lucide-react'

import type { TrackerProvider } from '@/lib/onboarding'
import { cn } from '@/lib/utils'

export type TrackerProviderIcon = ComponentType<{ className?: string }>

export interface TrackerProviderMeta {
  id: TrackerProvider
  name: string
  blurb: string
  Icon: TrackerProviderIcon
}

// Linear mark vendored from simple-icons (CC0 1.0 Universal).
export function LinearIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="currentColor"
      aria-hidden="true"
      className={cn('size-4', className)}
    >
      <path d="M2.886 4.18A11.982 11.982 0 0 1 11.99 0C18.624 0 24 5.376 24 12.009c0 3.64-1.62 6.903-4.18 9.105L2.887 4.18ZM1.817 5.626l16.556 16.556c-.524.33-1.075.62-1.65.866L.951 7.277c.247-.575.537-1.126.866-1.65ZM.322 9.163l14.515 14.515c-.71.172-1.443.282-2.195.322L0 11.358a12 12 0 0 1 .322-2.195Zm-.17 4.862 9.823 9.824a12.02 12.02 0 0 1-9.824-9.824Z" />
    </svg>
  )
}

// Jira mark vendored from simple-icons (CC0 1.0 Universal).
export function JiraIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="currentColor"
      aria-hidden="true"
      className={cn('size-4', className)}
    >
      <path d="M11.571 11.513H0a5.218 5.218 0 0 0 5.232 5.215h2.13v2.057A5.215 5.215 0 0 0 12.575 24V12.518a1.005 1.005 0 0 0-1.005-1.005zm5.723-5.756H5.736a5.215 5.215 0 0 0 5.215 5.214h2.129v2.058a5.218 5.218 0 0 0 5.215 5.214V6.758a1.001 1.001 0 0 0-1.001-1.001zM23.013 0H11.455a5.215 5.215 0 0 0 5.215 5.215h2.129v2.057A5.215 5.215 0 0 0 24 12.483V1.005A1.001 1.001 0 0 0 23.013 0Z" />
    </svg>
  )
}

// Azure DevOps mark vendored from microsoft/vscode-codicons (CC BY 4.0);
// simple-icons dropped every Microsoft brand icon in v13.0.0.
export function AzureDevOpsIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden="true"
      className={cn('size-4', className)}
    >
      <path d="M15 3.62172V12.1336L11.5 15L6.075 13.025V14.9825L3.00375 10.9713L11.955 11.6704V4.00624L15 3.62172ZM12.0163 4.04994L6.99375 1V3.00125L2.3825 4.35581L1 6.12984V10.1586L2.9775 11.0325V5.86767L12.0163 4.04994Z" />
    </svg>
  )
}

export function InternalTrackerIcon({ className }: { className?: string }) {
  return <Database className={cn('size-4', className)} aria-hidden="true" />
}

export const TRACKER_PROVIDERS: TrackerProviderMeta[] = [
  {
    id: 'linear',
    name: 'Linear',
    blurb: 'Sync issues from a Linear team. Needs an API key.',
    Icon: LinearIcon,
  },
  {
    id: 'jira',
    name: 'Jira',
    blurb: 'Sync from a Jira project. Needs a site URL, email + token.',
    Icon: JiraIcon,
  },
  {
    id: 'azure',
    name: 'Azure DevOps',
    blurb: 'Sync from an Azure DevOps team project. Needs an organization URL + PAT.',
    Icon: AzureDevOpsIcon,
  },
  {
    id: 'internal',
    name: 'Internal',
    blurb: "No external tracker — issues live in trau's own store.",
    Icon: InternalTrackerIcon,
  },
]

export function trackerProviderMeta(
  id: TrackerProvider | null,
): TrackerProviderMeta | null {
  return TRACKER_PROVIDERS.find((p) => p.id === id) ?? null
}
