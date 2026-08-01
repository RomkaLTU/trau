import { useState } from 'react'

import { TerminalCard } from '@/components/trau/terminal-card'
import { type SyncResponse } from '@/lib/instances'
import {
  joinProject,
  laterStep,
  onboardPath,
  stepLabel,
  type EssentialsFields,
  type MemberRepo,
  type TrackerFields,
  type TrackerProvider,
  type WizardStepId,
} from '@/lib/onboarding'
import { renameProject } from '@/lib/projects'

import { StepAppURLs } from './step-appurls'
import { StepDetect } from './step-detect'
import { StepDone } from './step-done'
import { StepEssentials } from './step-essentials'
import { StepPath } from './step-path'
import { StepSync } from './step-sync'
import { StepTracker } from './step-tracker'
import { WizardStepper } from './wizard-stepper'

export function OnboardingWizard({ initialPath = '' }: { initialPath?: string }) {
  const [step, setStep] = useState<WizardStepId>('path')
  const [maxReached, setMaxReached] = useState<WizardStepId>('path')
  const [members, setMembers] = useState<MemberRepo[]>([])
  const [project, setProject] = useState<string | null>(null)
  const [projectName, setProjectName] = useState('')
  const [provider, setProvider] = useState<TrackerProvider | null>(null)
  const [trackerFields, setTrackerFields] = useState<TrackerFields | null>(null)
  const [essentials, setEssentials] = useState<EssentialsFields | null>(null)
  const [syncResult, setSyncResult] = useState<SyncResponse | null>(null)

  const primary = members[0] ?? null

  function go(next: WizardStepId) {
    setStep(next)
    setMaxReached((prev) => laterStep(prev, next))
  }

  // joinProject names the group only when it creates it, so a name edited after
  // that has to reach the project as a rename. A blank name goes to the hub too:
  // its refusal is what the field reports, rather than the edit vanishing.
  async function renameGroup(name: string) {
    if (project === null || name === projectName) return
    await renameProject(project, name)
    setProjectName(name)
  }

  // Members are grouped into a project only once there are two of them. Re-picking
  // a folder already taken re-registers it but never lists it twice.
  async function addPaths(paths: string[], groupName?: string) {
    const first = members.length === 0
    const known = new Set(members.map((m) => m.root))
    const added: MemberRepo[] = []
    for (const path of paths) {
      const member = await onboardPath(path)
      if (known.has(member.root)) continue
      known.add(member.root)
      added.push(member)
    }
    const all = [...members, ...added]
    if (all.length > 1) {
      const name = groupName ?? all[0].repo
      const joining = project ? added : all
      await renameGroup(name)
      setProject(await joinProject(project, name, joining.map((m) => m.root)))
      setProjectName(name)
    }
    if (first) {
      setProvider(null)
      setTrackerFields(null)
      setEssentials(null)
      setSyncResult(null)
    }
    setMembers(all)
    go('detect')
  }

  return (
    <div className="flex w-full max-w-page flex-col gap-6">
      <div className="flex flex-col gap-6 lg:flex-row lg:items-start">
        <div className="shrink-0 lg:w-40 lg:pt-12">
          <WizardStepper current={step} maxReached={maxReached} onNavigate={go} />
        </div>
        <TerminalCard
          title={`new-project · ${stepLabel(step)}`}
          className="min-w-0 flex-1"
          bodyClassName="p-5"
        >
          {step === 'path' && (
            <StepPath
              initialPath={members.length === 0 ? initialPath : ''}
              members={members}
              projectName={projectName}
              onAdd={addPaths}
              onRename={renameGroup}
            />
          )}
          {step === 'detect' && primary && (
            <StepDetect
              members={members}
              onAddPath={() => go('path')}
              onBack={() => go('path')}
              onContinue={() => go('tracker')}
            />
          )}
          {step === 'tracker' && primary && (
            <StepTracker
              inspection={primary.inspection}
              repo={primary.repo}
              project={project ?? primary.project}
              onBack={() => go('detect')}
              onContinue={(p, fields) => {
                setProvider(p)
                setTrackerFields(fields)
                go('essentials')
              }}
            />
          )}
          {step === 'essentials' && primary && (
            <StepEssentials
              inspection={primary.inspection}
              repo={primary.repo}
              onBack={() => go('tracker')}
              onContinue={(fields) => {
                setEssentials(fields)
                go('appurls')
              }}
            />
          )}
          {step === 'appurls' && primary && (
            <StepAppURLs
              repo={primary.repo}
              onBack={() => go('essentials')}
              onContinue={() => go('sync')}
            />
          )}
          {step === 'sync' && primary && provider && (
            <StepSync
              repo={primary.repo}
              provider={provider}
              onSynced={setSyncResult}
              onBackToTracker={() => go('tracker')}
              onContinue={() => go('done')}
            />
          )}
          {step === 'done' && primary && provider && trackerFields && essentials && (
            <StepDone
              repo={primary.repo}
              members={members.map((m) => m.repo)}
              provider={provider}
              trackerFields={trackerFields}
              essentials={essentials}
              syncResult={syncResult}
            />
          )}
        </TerminalCard>
      </div>
    </div>
  )
}
