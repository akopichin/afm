import { useMemo, type ReactElement } from 'react'
import type { AfmEvent, LogEntry } from '../../types'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { useFeedMode } from '../../hooks/use-feed-mode'
import { toFeedItems, groupFeedItems, type FeedActor, type FeedGroup } from './feed-view-model'

type FeedWorkspaceProps = {
  events: AfmEvent[]
  // Лог выбранной стадии — второй режим той же панели (Feed/Log). Приходит из
  // useStageLog (App.tsx), очищается при смене стадии.
  logEntries: LogEntry[]
}

const ACTOR_LABEL: Record<FeedActor, string> = {
  agent: 'Agent',
  user: 'You',
  system: 'Flow',
}

// FeedWorkspace — единый воркспейс ленты: мессенджер-презентация РЕАЛЬНЫХ событий
// (агент/флоу слева, пользователь справа; tool/script — компактные mono-строки;
// success/warning/failure — семантические тональные поверхности) плюс локальный
// сегмент-переключатель Feed/Log. Пришёл на смену EventFeedPanel: без PanelFrame/
// Maximizable (воркспейс и так на всю ширину). Сохранены useFeedMode
// (`afm-feed-mode`), независимый stick-to-bottom для ленты и лога, Jump to latest.
export function FeedWorkspace({ events, logEntries }: FeedWorkspaceProps): ReactElement {
  const feed = useStickToBottom<HTMLDivElement>()
  const log = useStickToBottom<HTMLPreElement>()
  const { mode, toggle } = useFeedMode()
  const groups = useMemo(() => groupFeedItems(toFeedItems(events)), [events])
  const hasLogEntries = logEntries.length > 0

  const showFeed = () => { if (mode !== 'feed') toggle() }
  const showLog = () => { if (mode !== 'log') toggle() }

  return (
    <section className="feed-workspace" aria-label="Feed">
      <div className="feed-switch" role="group" aria-label="Feed or log">
        <button type="button" className={`feed-switch-btn${mode === 'feed' ? ' active' : ''}`} aria-pressed={mode === 'feed'} onClick={showFeed}>Feed</button>
        <button type="button" className={`feed-switch-btn${mode === 'log' ? ' active' : ''}`} aria-pressed={mode === 'log'} onClick={showLog}>Log</button>
      </div>

      {mode === 'feed' ? (
        <div id="feed-content" className="feed-scroll" ref={feed.ref}>
          {groups.length === 0 ? (
            <div className="empty-hint feed-empty">No events yet</div>
          ) : (
            groups.map((g) => <FeedGroupView key={g.key} group={g} />)
          )}
          {!feed.stick && (
            <button type="button" className="jump-latest" onClick={feed.jumpToBottom}>↓ Jump to latest</button>
          )}
        </div>
      ) : (
        <div className="feed-scroll log-scroll">
          <pre id="log-content" ref={log.ref} className={`log-content${hasLogEntries ? '' : ' hidden'}`}>
            {logEntries.map((entry) => entry.message).join('\n')}
          </pre>
          {hasLogEntries && !log.stick && (
            <button type="button" className="jump-latest" onClick={log.jumpToBottom}>↓ Jump to latest</button>
          )}
          <div id="log-empty" className={`empty-hint${hasLogEntries ? ' hidden' : ''}`}>Log is empty</div>
        </div>
      )}
    </section>
  )
}

// FeedGroupView — один пузырь: шапка (стадия + актор) + стопка item-строк.
function FeedGroupView({ group }: { group: FeedGroup }): ReactElement {
  return (
    <div className={`feed-group feed-${group.side}`} data-actor={group.actor}>
      <div className="feed-group-head">
        {group.stageId !== '' && <span className="feed-stage-badge">{group.stageId}</span>}
        <span className="feed-actor">{ACTOR_LABEL[group.actor]}</span>
      </div>
      <div className="feed-bubble">
        {group.items.map((item) => (
          <div
            key={item.key}
            className={`feed-item feed-kind-${item.kind} tone-${item.tone}${item.mono ? ' mono' : ''}`}
          >
            <span className="feed-item-text">{item.text}</span>
            {item.gap !== '—' && <span className="feed-item-gap">{item.gap}</span>}
          </div>
        ))}
      </div>
    </div>
  )
}
