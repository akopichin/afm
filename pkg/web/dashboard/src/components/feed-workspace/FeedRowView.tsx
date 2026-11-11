import { memo, type ReactElement, type ReactNode } from 'react'
import type { FeedItem } from './feed-view-model'
import type { VirtualFeedRow } from './feed-virtual-model'
import { reportKey, type FeedRowActions, type FeedRowState } from './feed-row-state'
import type { ReportCache } from './report-cache'
import { FeedGroupView, FeedItemRow, ReportToggle, STAGE_AGENT_ROLE, ToolCallLine, ToolSummaryLabel, sameItem, sameItems, type ImageRetries } from './feed-rows'
import { MessageTime } from '../message-time/MessageTime'
import { MeshAvatar } from '../team/MeshAvatar'

// Строка виртуального списка ленты: оформление группы (шапка автора, отступы) собирается по метаданным строки, а
// не берётся из обёртки группы.

// RowEnv — всё, что строкам нужно от ленты. Объект пересоздаётся только когда что-то из него изменилось, чтобы
// строки, равные по содержимому, не перерисовывались.
export type RowEnv = {
  imagePolicy: 'stage' | 'none'
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  onReplyToThought?: (key: string, text: string) => void
  quoteRestarts: boolean
  activeReplyKey: string | null
  onEntered?: () => void
  state: FeedRowState
  actions: FeedRowActions
  imageRetries: ImageRetries
  reports: ReportCache
  // countScope — стадия и фильтр, которым принадлежит счётчик серии.
  countScope: string
  // onToggleRow — читатель раскрыл или закрыл строку (серию, отчёт, Raw): лента снимает follow и держит якорь.
  onToggleRow: (rowKey: string, open: boolean) => void
}

type RowFrameProps = { row: VirtualFeedRow; isFirst: boolean; children: ReactNode }

// RowFrame — элемент строки с ключом данных; отступы между строками — его padding, а не gap скроллера.
export function RowFrame({ row, isFirst, children }: RowFrameProps): ReactElement {
  const classes = ['feed-vrow', `feed-vrow--${row.kind}`, row.start ? 'feed-vrow--start' : '', row.end ? 'feed-vrow--end' : '', isFirst ? 'feed-vrow--first' : '', row.actor === 'system' ? 'feed-vrow--flow' : '', row.kind === 'conversation' ? 'feed-conversation' : '']
  return (
    <div className={classes.filter(Boolean).join(' ')} data-virtual-row-key={row.key}>
      {children}
    </div>
  )
}

// groupFirstItem — первое событие группы, время которого стоит в шапке автора.
function groupFirstItem(row: Exclude<VirtualFeedRow, { kind: 'conversation' }>): FeedItem | undefined {
  switch (row.kind) {
    case 'event':
    case 'tool-call':
      return row.item
    case 'tool-summary':
      return row.items[0]
    case 'framed':
      return row.group.items[0]
  }
}

// AuthoredShell — строки агента стадии: квадратный аватар слева, шапка автора только у первой строки группы.
function AuthoredShell({ row, children }: { row: VirtualFeedRow; children: ReactNode }): ReactElement {
  if (row.actor === 'system') {
    return (
      <div className="feed-group feed-left feed-flow" data-actor="system">
        <div className="feed-rows">{children}</div>
      </div>
    )
  }
  const first = row.kind === 'conversation' || row.kind === 'framed' ? undefined : groupFirstItem(row)
  return (
    <div className="feed-group feed-left feed-authored" data-actor="agent">
      {row.start ? <MeshAvatar name={row.stageId} kind="stage_agent" /> : <span aria-hidden="true" />}
      <div className="feed-authored-body">
        {row.start && (
          <div className="feed-group-head feed-author-row">
            <strong className="feed-actor">{row.stageId}</strong>
            <span className="feed-role">{STAGE_AGENT_ROLE}</span>
            {first && <MessageTime timestamp={first.ts} gap={first.gap} firstEvent={first.firstEvent} focusable />}
          </div>
        )}
        <div className="feed-rows">{children}</div>
      </div>
    </div>
  )
}

type FeedRowViewProps = {
  row: Exclude<VirtualFeedRow, { kind: 'conversation' }>
  env: RowEnv
  isFirst: boolean
  // enteringKey — ключ свежей строки, если она в этой строке списка и должна сыграть анимацию появления.
  enteringKey: string | null
}

function FeedRowViewImpl({ row, env, isFirst, enteringKey }: FeedRowViewProps): ReactElement {
  if (row.kind === 'framed') {
    return (
      <RowFrame row={row} isFirst={isFirst}>
        <FeedGroupView
          group={row.group}
          showStageBadges={false}
          imagePolicy={env.imagePolicy}
          enteringKey={enteringKey}
          onEntered={enteringKey === null ? undefined : env.onEntered}
          toolCountScope={env.countScope}
          onOpenDialog={env.onOpenDialog}
          onReplyToThought={env.onReplyToThought}
          quoteRestarts={env.quoteRestarts}
          activeReplyKey={env.activeReplyKey}
        />
      </RowFrame>
    )
  }
  if (row.kind === 'tool-summary') {
    const memberKeys = row.items.map((item) => item.key)
    return (
      <RowFrame row={row} isFirst={isFirst}>
        <AuthoredShell row={row}>
          <div className={`feed-tool-group${row.open ? ' feed-tool-group--open' : ''}`}>
            <button
              type="button"
              className="feed-tool-summary"
              aria-expanded={row.open}
              data-feed-item=""
              onClick={() => {
                env.onToggleRow(row.key, !row.open)
                env.actions.toggleSeries(memberKeys)
              }}
            >
              <ToolSummaryLabel items={row.items} countScope={env.countScope} />
            </button>
          </div>
        </AuthoredShell>
      </RowFrame>
    )
  }
  if (row.kind === 'tool-call') {
    return (
      <RowFrame row={row} isFirst={isFirst}>
        <AuthoredShell row={row}>
          <div className={`feed-tool-group feed-tool-group--call feed-tool-rows${row.last ? ' feed-tool-group--last' : ''}`}>
            <ToolCallLine item={row.item} />
          </div>
        </AuthoredShell>
      </RowFrame>
    )
  }
  const item = row.item
  const reportId = item.reportVerificationId
  return (
    <RowFrame row={row} isFirst={isFirst}>
      <AuthoredShell row={row}>
        <FeedItemRow
          item={item}
          timeInHead={row.actor === 'agent' && row.start}
          imagePolicy={env.imagePolicy}
          onOpenDialog={env.onOpenDialog}
          onReplyToThought={env.onReplyToThought}
          quoteRestarts={env.quoteRestarts}
          activeReplyKey={env.activeReplyKey}
          entering={enteringKey === item.key}
          onEntered={env.onEntered}
          imageRetries={env.imageRetries}
          report={
            reportId === undefined ? undefined : (
              <ReportToggle
                stageId={item.stageId}
                verificationId={reportId}
                open={env.state.openReports.has(reportKey(item.stageId, reportId))}
                onToggle={(open) => {
                  env.onToggleRow(row.key, open)
                  env.actions.toggleReport(reportKey(item.stageId, reportId))
                }}
                cache={env.reports}
              />
            )
          }
        />
      </AuthoredShell>
    </RowFrame>
  )
}

// sameRow — items пересобираются на каждое событие, поэтому строка сравнивается по содержимому: перерисовывается
// только изменившаяся.
function sameRow(a: FeedRowViewProps['row'], b: FeedRowViewProps['row']): boolean {
  if (a.kind !== b.kind || a.key !== b.key || a.start !== b.start || a.end !== b.end || a.stageId !== b.stageId || a.actor !== b.actor) return false
  switch (a.kind) {
    case 'event':
    case 'tool-call':
      return sameItem(a.item, (b as typeof a).item)
    case 'tool-summary':
      return a.open === (b as typeof a).open && sameItems(a.items, (b as typeof a).items)
    case 'framed':
      return sameItems(a.group.items, (b as typeof a).group.items)
  }
}

export const FeedRowView = memo(FeedRowViewImpl, (prev, next) => prev.env === next.env && prev.isFirst === next.isFirst && prev.enteringKey === next.enteringKey && sameRow(prev.row, next.row))
