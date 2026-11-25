import {
  ActivityLine,
  type MessagePaste,
  NoticeCard,
  SessionView,
  type SessionViewHandle,
  type SessionViewProps,
} from '@parallelworks/ui/ai'
import {
  promptHistory,
  type PwCodeSubagent,
  type PwCodeSubagentUsage,
  type PwCodeSwitcherAgent,
  usePwCodeSubagent,
} from '@parallelworks/code'
import { ListError } from '@parallelworks/ui/list'
import { type ReactNode, useMemo, useRef, useState } from 'react'
import { useTranslations } from 'use-intl'
import { pwCode } from '../api'
import { describeError } from '../describeError'
import { type StatusActivity, SubagentBar } from './AgentSwitcher'
import { ComposerImages } from './ComposerImages'
import { type KeptImages, useComposerImages } from './useComposerImages'
import { useTakeHandedBack } from './useTakeHandedBack'
import './SubagentView.css'

type SubagentOptions = Parameters<typeof usePwCodeSubagent>[0]
/** The tasks a subagent's transcript holds. */
export type SubagentTasks = NonNullable<PwCodeSubagent['transcript']>['todos']

export interface SubagentViewProps {
  machine: string
  sessionId: string
  subId: string
  /** Its row in the session's list, which the feed keeps current. */
  listed: PwCodeSwitcherAgent | undefined
  usage: PwCodeSubagentUsage | undefined
  /** Hands over each frame of the session's feed until the returned call: the conversation's `subscribe`. */
  subscribe: SubagentOptions['subscribe']
  /** The composer's `loadPalette`. */
  loadPalette: SubagentOptions['loadPalette']
  /** Why nothing can be sent to the session at all (the composer's `sessionLock`). */
  sessionLock: SubagentOptions['sessionLock']
  sessionBanner: ReactNode
  /** The session's notice, shown over whichever conversation is in view. */
  notice: ReactNode
  /** The composer's status row around this agent's activity line and tasks. */
  status: (activity: StatusActivity, extra: { tasks?: SubagentTasks | undefined }) => ReactNode
  /** Where the session runs, as the main conversation's composer shows it. */
  context: ReactNode
  modelNames: Record<string, string> | undefined
  pastes?: NonNullable<SessionViewProps['composer']>['pastes']
  /** The counter the session's composers number images from, so this one never reuses the main conversation's. */
  imageNumbers: { current: number }
  /** Where this composer's waiting images go while another conversation is in view. */
  keptImages: KeptImages
  username: string
  /** The subagent took a message, so its row in the list reads differently. */
  onSent: () => void
  onBack: () => void
}

/** One subagent's conversation in place of the main one: its transcript, kept
 *  current from the session's feed, and a composer that talks to it. */
export function SubagentView({
  machine, sessionId, subId, listed, usage, subscribe, loadPalette, sessionLock, sessionBanner, notice,
  status, context, modelNames, pastes, imageNumbers, keptImages, username, onSent, onBack,
}: SubagentViewProps) {
  const t = useTranslations('agents')
  const subagent = pwCode.useSubagentDetail(machine, sessionId, subId)
  const [imageError, setImageError] = useState('')
  const sentTexts = useMemo(() => subagent.data?.history?.map(item => ({ content: item.text })), [subagent.data?.history])
  const images = useComposerImages({
    machine,
    sessionId,
    messages: sentTexts,
    numbering: imageNumbers,
    keep: { in: keptImages, as: subId },
    onError: setImageError,
  })
  const view = usePwCodeSubagent({
    api: pwCode,
    machine,
    sessionId,
    subId,
    subagent,
    listed,
    usage,
    subscribe,
    loadPalette,
    describeError,
    sessionLock,
    modelNames,
    images,
    onSent,
  })
  const { transcript, messages, composer, activity, historyId } = view
  const history = useMemo(
    () => promptHistory(username, { id: historyId, messages: transcript?.messages }),
    [username, historyId, transcript?.messages],
  )
  const sessionView = useRef<SessionViewHandle>(null)
  useTakeHandedBack(sessionView, view, { holdsImages: images.images.length > 0 })

  return (
    <SessionView
      ref={sessionView}
      sessionKey={view.draftKey}
      foldEveryRun
      messages={messages}
      streamingMessage={view.streaming}
      queuedMessages={view.queued}
      canRemoveQueued={view.canRemoveQueued}
      queueDelivery={view.queueDelivery}
      turn={{ running: view.turn.running, onStop: () => void view.turn.stop(), stopping: view.turn.stopping }}
      onSend={(text, _ids, _meta, sent?: MessagePaste[]) => view.send(text, sent)}
      onApprovalAnswer={view.answerApproval}
      showAuthor={view.showAuthor}
      currentUsername={username}
      error={view.failed ? (
        <div className="pwcode-subagent-failed">
          <ListError message={view.failed.message} noun={t('detail.transcriptNoun')} />
        </div>
      ) : undefined}
      banners={<>
        {sessionBanner}
        <SubagentBar agent={view.agent} stopping={view.turn.stopping} onStop={() => void sessionView.current?.stop()} onBack={onBack} />
      </>}
      label={t('switcher.transcriptLabel')}
      flush
      find
      workingIndicator={false}
      dropFiles
      composer={{
        attachments: false,
        files: images.files,
        tools: false,
        requireModel: false,
        history,
        recall: view.recall,
        disabled: composer.disabled,
        placeholder: composer.placeholder,
        inputLabel: composer.placeholder,
        sendLabel: t('detail.send'),
        pastes,
        activity: status(
          activity
            ? brief => (
                <ActivityLine testId="subagent-activity" startedAt={activity.startedAt}
                  inputTokens={brief ? undefined : activity.inputTokens} outputTokens={brief ? undefined : activity.outputTokens} />
              )
            : null,
          { tasks: transcript?.todos },
        ),
        notices: <>
          {view.notice && <NoticeCard tone="warning" onDismiss={view.dismissNotice}>{view.notice}</NoticeCard>}
          {notice}
          {imageError && <NoticeCard tone="warning" onDismiss={() => setImageError('')}>{imageError}</NoticeCard>}
          <ComposerImages images={images.images} onRemove={images.remove} />
        </>,
        // Every row the main composer has, so the status row and the box stay
        // put as the view changes between them.
        context,
        reserveContext: true,
        settingsRight: <span className="pwcode-subagent-model"><span className="pwcode-ellipsis">{view.modelLabel}</span></span>,
        settingsLeft: <p data-testid="subagent-composer-note" className="pwcode-subagent-note">{composer.hint}</p>,
      }}
    />
  )
}
