import { useMemo, type ReactElement } from 'react'
import type { AfmEvent } from '../../types'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { useFeedScope } from '../../hooks/use-feed-scope'
import { toFeedItems, groupFeedItems, type FeedActor, type FeedGroup } from './feed-view-model'
import { JumpToLatestButton } from '../jump-to-latest'
import { FeedComposer } from './FeedComposer'

type FeedWorkspaceProps = {
  events: AfmEvent[]
  // Разрешённый id стадии воркспейса (уже сверен со списком stages в App), либо
  // null, если стадия не выбрана / устарела. Основа фильтра «This stage».
  stageId: string | null
  // Клик по navigable-элементу ленты (dialog_question/dialog_answer) — открыть
  // диалог соответствующей стадии на нужном вопросе. Сама навигация — задача
  // другой таски (T6); здесь только проброс координат из FeedItem. Без пропа
  // navigable-элементы рендерятся как обычные (не интерактивные) строки.
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  // noteTarget — стадия, живому агенту которой можно послать заметку прямо из
  // ленты (running и не скрипт; вычисляется в App). null → messenger-поле не
  // показываем. onSendNote доставляет заметку (через Revise) и ДОЛЖЕН
  // отклоняться при неудаче, чтобы FeedComposer сохранил текст.
  noteTarget?: string | null
  onSendNote?: (stageId: string, text: string) => Promise<void>
}

const ACTOR_LABEL: Record<FeedActor, string> = {
  agent: 'Agent',
  user: 'You',
  system: 'Flow',
}

// FeedWorkspace — единый воркспейс ленты: мессенджер-презентация РЕАЛЬНЫХ событий
// (агент/флоу слева, пользователь справа; tool/script — компактные mono-строки;
// success/warning/failure — семантические тональные поверхности). Пришёл на смену
// EventFeedPanel: без PanelFrame/Maximizable (воркспейс и так на всю ширину),
// stick-to-bottom + Jump to latest. Scope-фильтр This stage | All (useFeedScope,
// `afm-feed-scope`): по умолчанию лента показывает события выбранной стадии, All —
// весь флоу. Мысли агента (agent_action tool="text") рендерятся здесь как проза —
// это событие ленты, отдельного лог-режима больше нет.
export function FeedWorkspace({ events, stageId, onOpenDialog, noteTarget = null, onSendNote }: FeedWorkspaceProps): ReactElement {
  const feed = useStickToBottom<HTMLDivElement>()
  const { scope, toggle: toggleScope } = useFeedScope()

  // Эффективный scope: «только эта стадия» работает лишь когда стадия реально
  // выбрана (stageId !== null). Без выбранной стадии фильтровать нечего — лента
  // глобальная, а тумблер скрыт.
  const stageScopeActive = scope === 'stage' && stageId !== null

  // Фильтр ДО toFeedItems, в одном useMemo: gap между событиями считается уже в
  // пределах отфильтрованного списка (осмысленнее per-stage). Flow-level события
  // (stageId === '') видны всегда — это события всего флоу, не конкретной стадии.
  const groups = useMemo(() => {
    const visible = stageScopeActive
      ? events.filter((e) => e.stageId === stageId || e.stageId === '')
      : events
    return groupFeedItems(toFeedItems(visible))
  }, [events, scope, stageId])

  const showStageScope = () => { if (scope !== 'stage') toggleScope() }
  const showAllScope = () => { if (scope !== 'all') toggleScope() }

  // Тумблер scope имеет смысл только при выбранной стадии.
  const showScopeSwitch = stageId !== null

  return (
    <section className="feed-workspace" aria-label="Feed">
      {showScopeSwitch && (
        <div className="feed-controls">
          <div className="feed-switch" role="group" aria-label="Feed scope">
            <button type="button" className={`feed-switch-btn${scope === 'stage' ? ' active' : ''}`} aria-pressed={scope === 'stage'} onClick={showStageScope}>This stage</button>
            <button type="button" className={`feed-switch-btn${scope === 'all' ? ' active' : ''}`} aria-pressed={scope === 'all'} onClick={showAllScope}>All</button>
          </div>
        </div>
      )}

      <div id="feed-content" className="feed-scroll" ref={feed.ref}>
        {groups.length === 0 ? (
          <div className="empty-hint feed-empty">{stageScopeActive ? 'No events for this stage yet' : 'No events yet'}</div>
        ) : (
          groups.map((g) => <FeedGroupView key={g.key} group={g} onOpenDialog={onOpenDialog} />)
        )}
        {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} />}
      </div>

      {/* Messenger-поле заметки живому агенту — прибито к низу ленты, только когда
          стадия принимает заметки (noteTarget != null: running и не скрипт).
          key=noteTarget: смена стадии даёт свежий пустой черновик (черновик не
          «переедет» в другую стадию). */}
      {noteTarget !== null && onSendNote !== undefined && (
        <FeedComposer key={noteTarget} stageId={noteTarget} onSend={(text) => onSendNote(noteTarget, text)} />
      )}
    </section>
  )
}

type FeedGroupViewProps = {
  group: FeedGroup
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
}

// FeedGroupView — один пузырь: шапка (стадия + актор) + стопка item-строк.
// navigable-элементы (dialog_question/dialog_answer, см. feed-view-model) при
// наличии onOpenDialog рендерятся кнопкой — переход к вопросу диалога стадии
// (сама навигация — задача T6, здесь только клик → проброс координат); без
// onOpenDialog или для остальных item — неизменный <div> как раньше.
function FeedGroupView({ group, onOpenDialog }: FeedGroupViewProps): ReactElement {
  return (
    <div className={`feed-group feed-${group.side}`} data-actor={group.actor}>
      <div className="feed-group-head">
        {group.stageId !== '' && <span className="feed-stage-badge">{group.stageId}</span>}
        <span className="feed-actor">{ACTOR_LABEL[group.actor]}</span>
      </div>
      <div className="feed-bubble">
        {group.items.map((item) => {
          const className = `feed-item feed-kind-${item.kind} tone-${item.tone}${item.mono ? ' mono' : ''}`
          const content = (
            <>
              <span className="feed-item-text">{item.text}</span>
              {item.gap !== '—' && <span className="feed-item-gap">{item.gap}</span>}
            </>
          )
          if (item.navigable === true && onOpenDialog) {
            return (
              <button
                key={item.key}
                type="button"
                className={`${className} feed-item-navigable`}
                title="Open in dialog"
                onClick={() => onOpenDialog(item.stageId, item.phase ?? '', item.id ?? '')}
              >
                {content}
              </button>
            )
          }
          return (
            <div key={item.key} className={className}>
              {content}
            </div>
          )
        })}
      </div>
    </div>
  )
}
