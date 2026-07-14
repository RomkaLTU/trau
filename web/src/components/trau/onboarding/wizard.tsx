import { useState } from 'react'

import { TerminalCard } from '@/components/trau/terminal-card'
import {
  laterStep,
  stepLabel,
  type EssentialsFields,
  type RepoInspection,
  type SyncResponse,
  type TrackerFields,
  type TrackerProvider,
  type WizardStepId,
} from '@/lib/onboarding'
import { StepDetect } from './step-detect'
import { StepDone } from './step-done'
import { StepEssentials } from './step-essentials'
import { StepPath } from './step-path'
import { StepSync } from './step-sync'
import { StepTracker } from './step-tracker'
import { WizardStepper } from './wizard-stepper'

export function OnboardingWizard() {
  const [step, setStep] = useState<WizardStepId>('path')
  const [maxReached, setMaxReached] = useState<WizardStepId>('path')
  const [inspection, setInspection] = useState<RepoInspection | null>(null)
  const [repo, setRepo] = useState<string | null>(null)
  const [provider, setProvider] = useState<TrackerProvider | null>(null)
  const [trackerFields, setTrackerFields] = useState<TrackerFields | null>(null)
  const [essentials, setEssentials] = useState<EssentialsFields | null>(null)
  const [syncResult, setSyncResult] = useState<SyncResponse | null>(null)

  function go(next: WizardStepId) {
    setStep(next)
    setMaxReached((prev) => laterStep(prev, next))
  }

  return (
    <div className="flex w-full max-w-3xl flex-col gap-6">
      <div className="flex flex-col gap-6 lg:flex-row lg:items-start">
        <div className="shrink-0 lg:w-40 lg:pt-12">
          <WizardStepper current={step} maxReached={maxReached} onNavigate={go} />
        </div>
        <TerminalCard
          title={`add-project · ${stepLabel(step)}`}
          className="min-w-0 flex-1"
          bodyClassName="p-5"
        >
          {step === 'path' && (
            <StepPath
              initialPath={inspection?.path ?? ''}
              onInspected={(insp, name) => {
                setInspection(insp)
                setRepo(name)
                setProvider(null)
                setTrackerFields(null)
                setEssentials(null)
                setSyncResult(null)
                setStep('detect')
                setMaxReached('detect')
              }}
            />
          )}
          {step === 'detect' && inspection && (
            <StepDetect
              inspection={inspection}
              onBack={() => go('path')}
              onContinue={() => go('tracker')}
            />
          )}
          {step === 'tracker' && inspection && repo && (
            <StepTracker
              inspection={inspection}
              repo={repo}
              onBack={() => go('detect')}
              onContinue={(p, fields) => {
                setProvider(p)
                setTrackerFields(fields)
                go('essentials')
              }}
            />
          )}
          {step === 'essentials' && inspection && repo && (
            <StepEssentials
              inspection={inspection}
              repo={repo}
              onBack={() => go('tracker')}
              onContinue={(fields) => {
                setEssentials(fields)
                go('sync')
              }}
            />
          )}
          {step === 'sync' && repo && provider && (
            <StepSync
              repo={repo}
              provider={provider}
              onSynced={setSyncResult}
              onBackToTracker={() => go('tracker')}
              onContinue={() => go('done')}
            />
          )}
          {step === 'done' && repo && provider && trackerFields && essentials && (
            <StepDone
              repo={repo}
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
