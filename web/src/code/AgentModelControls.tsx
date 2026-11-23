import type { Dispatch, SetStateAction } from 'react'
import { lastModelPreference, type PwCodeApi, type PwCodePicker, type PwCodePlatformModelList, useLastModelPreference, useModelPicker } from '@parallelworks/code'
import { Combobox } from '@parallelworks/ui'
import { AllocationSelector, type BoundSetting, EffortPicker, useChat, useChatConfig } from '@parallelworks/ui/ai'
import { ChevronDownIcon } from '@parallelworks/ui/icons'
import { describeError } from '../pwcode/PWCodeSession'
import { useSavedPreference } from './parts'

/** The model last chosen for a new session, kept in this browser. */
export function useLastAgentModel() {
  return useLastModelPreference(useSavedPreference(lastModelPreference))
}

/**
 * The model, effort and allocation a session runs with, chosen as PW Code
 * on the web chooses them (its AgentModelControls, without the Claude
 * account chip): the platform's models from `platform`, else from the
 * surrounding ChatProvider's adapter (useChat), and Claude's models from the machine where it runs
 * Claude Code, through api. Used by the Code page's start and a pw code
 * chat's session composer.
 */
export function AgentModelControls({ api, platform, machine, sessionId, claudeReady, canStart, model, modelNames, effort, allocation, account, panel, onPanelChange, onAccounts }: {
  /** The client whose claude-code/models route the picker reads. */
  api: Pick<PwCodeApi, 'useClaudeModels'>
  /** The platform models pw code can run, in place of the surrounding
   *  ChatProvider's: a Studio chat's are the Studio assistant's, which may
   *  be an OpenAI-compatible endpoint's that pw code cannot use. */
  platform?: PwCodePlatformModelList | undefined
  machine: string
  sessionId?: string | undefined
  /** The machine runs Claude Code and allows it. */
  claudeReady: boolean
  canStart: boolean
  model: BoundSetting
  modelNames?: Readonly<Record<string, string>> | undefined
  effort: BoundSetting
  allocation: BoundSetting
  /** The Claude account Claude's models are listed for. */
  account: string | undefined
  /** Which picker is open: 'models' is this one's. */
  panel: PwCodePicker
  onPanelChange: Dispatch<SetStateAction<PwCodePicker>>
  /** Opens the Claude account panel, where the host has one. */
  onAccounts?: () => void
}) {
  const { models, providerIssues, refreshModels, isLoadingModels, modelsError, currentUser } = useChat()
  const { strings } = useChatConfig()
  const { remember } = useLastAgentModel()
  const picker = useModelPicker(api, {
    machine,
    sessionId,
    claudeReady,
    canStart,
    account,
    open: panel === 'models',
    model,
    allocation,
    platform: platform ?? { models, providerIssues, isLoading: isLoadingModels, error: modelsError, refresh: refreshModels },
    reader: currentUser?.username ?? '',
    names: modelNames,
    errorMessage: describeError,
    remember,
    onAccounts: onAccounts ?? (() => {}),
  })
  return (
    <>
      <Combobox
        items={picker.entries}
        value={model.value}
        disabled={model.disabled}
        open={panel === 'models'}
        onOpenChange={open => {
          if (open) picker.opened()
          onPanelChange(current => (open ? 'models' : current === 'models' ? null : current))
        }}
        onValueChange={value => picker.pick(value ?? '')}
        aria-label={strings.modelPicker.choose}
        placeholder={strings.modelPicker.search}
        trigger={
          <button type="button" data-testid="agents-model-chip" className="code-setting-chip">
            <span className="code-chip-label">{picker.label}</span>
            <ChevronDownIcon className="code-chip-caret" aria-hidden />
          </button>
        }
      />
      <EffortPicker effort={effort.value} onPick={effort.onChange} disabled={effort.disabled} className="code-setting-chip" />
      {picker.allocation && <AllocationSelector model={model.value} allocation={picker.allocation} />}
    </>
  )
}
