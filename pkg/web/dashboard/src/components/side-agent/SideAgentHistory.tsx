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

// SideAgentHistory — прокручиваемая история беседы. Переиспользует FeedGroupView
// (тот же пузырь/markdown/tool-строки, что и лента стадий) через mapper
// sideAgentFeedGroups — БЕЗ подделки событий стадии. systemAuthorLabel="Side
// agent" (turn_failed/interrupted подписываются не «Flow»), imagePolicy="none"
// (у беседы нет артефактов стадии — [AFM image: …] остаётся текстом).
// stick-to-bottom + Jump to latest — как в FeedWorkspace.
export function SideAgentHistory({ events, emptyHint }: SideAgentHistoryProps): ReactElement {
  const feed = useStickToBottom<HTMLDivElement>()
  const groups = useMemo(() => sideAgentFeedGroups(events), [events])

  return (
    <div className="side-agent-history" ref={feed.ref}>
      {groups.length === 0 ? (
        <div className="empty-hint side-agent-empty">{emptyHint ?? 'Ask the agent anything about this run.'}</div>
      ) : (
        groups.map((g) => (
          <FeedGroupView key={g.key} group={g} showStageBadges={false} systemAuthorLabel="Side agent" imagePolicy="none" />
        ))
      )}
      {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} />}
    </div>
  )
}
