import { useMemo, type ReactElement } from 'react'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { FeedGroupView } from '../feed-workspace'
import { JumpToLatestButton } from '../jump-to-latest'
import { sideAgentFeedGroups } from './side-agent-feed-model'
import type { SideAgentEvent } from '../../types/side-agent'

export type SideAgentHistoryProps = {
  events: SideAgentEvent[]
  // Пустой разговор/капабилити есть, но ещё ничего не отправлено.
  emptyHint?: string
}

// SideAgentHistory — прокручиваемая история беседы. stick-to-bottom + Jump to latest — как в
// FeedWorkspace.
export function SideAgentHistory({ events, emptyHint }: SideAgentHistoryProps): ReactElement {
  const feed = useStickToBottom<HTMLDivElement>()
  const groups = useMemo(() => sideAgentFeedGroups(events), [events])

  return (
    <div className="side-agent-history" ref={feed.ref}>
      {groups.length === 0 ? (
        <div className="empty-hint side-agent-empty">{emptyHint ?? 'Ask the assistant anything about this run.'}</div>
      ) : (
        groups.map((g) => (
          <FeedGroupView key={g.key} group={g} showStageBadges={false} systemAuthorLabel="Assistant" imagePolicy="none" />
        ))
      )}
      {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} />}
    </div>
  )
}
