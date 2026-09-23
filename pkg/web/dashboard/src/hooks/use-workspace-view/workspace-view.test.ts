import { describe, it, expect } from 'vitest'
import {
  workspaceReducer,
  initialWorkspaceState,
  deriveAttentionItems,
  attentionKindForStatus,
  activeItem,
  countByKind,
  sig,
  type WorkspaceState,
  type AttentionItem,
} from './workspace-view'
import type { Stage, StageStatus } from '../../types'

function stage(id: string, status: StageStatus): Stage {
  return {
    id,
    name: id,
    status,
    updatedAt: '',
    interactive: false,
    autonomous: false,
    autoApprove: false,
    hasDialog: false,
    showPlan: false,
    showDialog: false,
    isScript: false,
    pausedFrom: '',
    preNote: '',
    buttons: [],
  }
}

const item = (stageId: string, kind: AttentionItem['kind'], episode = ''): AttentionItem => ({ stageId, kind, episode })

// sync — короткий помощник: применить снимок очереди (по умолчанию не suppressed).
function sync(state: WorkspaceState, items: AttentionItem[], suppressed = false): WorkspaceState {
  return workspaceReducer(state, { type: 'sync', items, suppressed })
}

describe('attentionKindForStatus', () => {
  it('maps each actionable status to its kind, others to null', () => {
    expect(attentionKindForStatus('awaiting_approval')).toBe('approval')
    expect(attentionKindForStatus('awaiting_user_input')).toBe('question')
    expect(attentionKindForStatus('failed')).toBe('failed')
    expect(attentionKindForStatus('hook_failed')).toBe('hook_failed')
    expect(attentionKindForStatus('paused')).toBe('paused')
    expect(attentionKindForStatus('running')).toBeNull()
    expect(attentionKindForStatus('done')).toBeNull()
    expect(attentionKindForStatus('pending')).toBeNull()
  })
})

describe('deriveAttentionItems', () => {
  it('keeps server (topological) order and filters non-actionable stages', () => {
    const stages = [
      stage('a', 'done'),
      stage('b', 'awaiting_approval'),
      stage('c', 'running'),
      stage('d', 'awaiting_user_input'),
      stage('e', 'paused'),
    ]
    expect(deriveAttentionItems(stages)).toEqual([
      item('b', 'approval'),
      item('d', 'question'),
      item('e', 'paused'),
    ])
  })

  it('returns empty when nothing needs attention', () => {
    expect(deriveAttentionItems([stage('a', 'running'), stage('b', 'done')])).toEqual([])
  })
})

describe('workspaceReducer — auto-open (rule 9)', () => {
  it('feed is the default and stays feed with no attention items', () => {
    expect(initialWorkspaceState.view).toBe('feed')
    const s = sync(initialWorkspaceState, [])
    expect(s.view).toBe('feed')
    expect(s.activeStageId).toBeNull()
  })

  it('auto-opens attention once when a new item arrives', () => {
    const s = sync(initialWorkspaceState, [item('b', 'approval')])
    expect(s.view).toBe('attention')
    expect(s.activeStageId).toBe('b')
  })

  it('does NOT steal focus when suppressed (typing / reviewing a file) — only glows', () => {
    const s = sync(initialWorkspaceState, [item('b', 'approval')], true)
    expect(s.view).toBe('feed')
    // и позже, когда suppression снят, задним числом НЕ открывает
    const s2 = sync(s, [item('b', 'approval')], false)
    expect(s2.view).toBe('feed')
  })

  it('auto-opens only once — returning to Feed is respected on the next poll', () => {
    const opened = sync(initialWorkspaceState, [item('b', 'approval')])
    expect(opened.view).toBe('attention')
    const backToFeed = workspaceReducer(opened, { type: 'openFeed' })
    expect(backToFeed.view).toBe('feed')
    // тот же элемент всё ещё в очереди — повторный poll НЕ переоткрывает
    const polled = sync(backToFeed, [item('b', 'approval')])
    expect(polled.view).toBe('feed')
  })

  it('a genuinely new arrival after a resolved one re-opens', () => {
    const first = sync(initialWorkspaceState, [item('b', 'approval')])
    const backToFeed = workspaceReducer(first, { type: 'openFeed' })
    // b решён, приходит d — новое прибытие → авто-открытие
    const second = sync(backToFeed, [item('d', 'question')])
    expect(second.view).toBe('attention')
    expect(second.activeStageId).toBe('d')
  })

  it('re-arrival of the same stage+kind after it left the queue counts as new (forgets consumed sig)', () => {
    const first = sync(initialWorkspaceState, [item('b', 'approval')])
    const backToFeed = workspaceReducer(first, { type: 'openFeed' })
    const gone = sync(backToFeed, []) // b решён, очередь пуста
    expect(gone.view).toBe('feed')
    const again = sync(gone, [item('b', 'approval')]) // b снова awaiting_approval
    expect(again.view).toBe('attention')
    expect(again.activeStageId).toBe('b')
  })
})

describe('workspaceReducer — resolution & advance (rule 10)', () => {
  it('advances to the next remaining item when the active one resolves', () => {
    const s = sync(initialWorkspaceState, [item('b', 'approval'), item('d', 'question')])
    expect(s.activeStageId).toBe('b')
    // b решён, остаётся d
    const advanced = sync(s, [item('d', 'question')])
    expect(advanced.view).toBe('attention')
    expect(advanced.activeStageId).toBe('d')
  })

  it('returns to Feed when the last item resolves', () => {
    const s = sync(initialWorkspaceState, [item('b', 'approval')])
    const resolved = sync(s, [])
    expect(resolved.view).toBe('feed')
    expect(resolved.activeStageId).toBeNull()
  })

  it('keeps focus on the active item while it is still pending', () => {
    const s = sync(initialWorkspaceState, [item('b', 'approval'), item('d', 'question')])
    const same = sync(s, [item('b', 'approval'), item('d', 'question')])
    expect(same.activeStageId).toBe('b')
  })
})

describe('workspaceReducer — explicit navigation', () => {
  it('openAttention focuses the first item when none specified', () => {
    const base = sync(initialWorkspaceState, [item('b', 'approval'), item('d', 'question')])
    const feed = workspaceReducer(base, { type: 'openFeed' })
    const reopened = workspaceReducer(feed, { type: 'openAttention' })
    expect(reopened.view).toBe('attention')
    expect(reopened.activeStageId).toBe('b')
  })

  it('openAttention can focus a specific queued stage', () => {
    const base = sync(initialWorkspaceState, [item('b', 'approval'), item('d', 'question')])
    const focused = workspaceReducer(base, { type: 'openAttention', stageId: 'd' })
    expect(focused.activeStageId).toBe('d')
  })

  it('openAttention is a no-op when the queue is empty', () => {
    const empty = workspaceReducer(initialWorkspaceState, { type: 'openAttention' })
    expect(empty.view).toBe('feed')
    expect(empty.activeStageId).toBeNull()
  })

  it('history views are transient and do not require attention (rule 13)', () => {
    const s = workspaceReducer(initialWorkspaceState, { type: 'openHistory', view: 'plan-history' })
    expect(s.view).toBe('plan-history')
    expect(activeItem(s)).toBeNull()
  })
})

describe('selectors', () => {
  it('activeItem returns the focused item only in attention view', () => {
    const s = sync(initialWorkspaceState, [item('b', 'approval')])
    expect(activeItem(s)).toEqual(item('b', 'approval'))
    const feed = workspaceReducer(s, { type: 'openFeed' })
    expect(activeItem(feed)).toBeNull()
  })

  it('countByKind counts unresolved items of a kind', () => {
    const items = [item('a', 'approval'), item('b', 'question'), item('c', 'question')]
    expect(countByKind(items, 'question')).toBe(2)
    expect(countByKind(items, 'approval')).toBe(1)
    expect(countByKind(items, 'paused')).toBe(0)
  })

  it('sig encodes stage, kind and episode', () => {
    expect(sig(item('a', 'approval'))).toBe('a:approval:')
    expect(sig(item('a', 'approval', 't2'))).toBe('a:approval:t2')
    // Разный эпизод той же стадии/вида = разная подпись (повторный вопрос).
    expect(sig(item('a', 'question', 't1'))).not.toBe(sig(item('a', 'question', 't2')))
  })

  it('a repeat attention episode (same stage/kind, new updatedAt) is a fresh arrival that auto-opens', () => {
    // q1: awaiting_user_input, updatedAt=t1 → авто-открытие attention на 'a'.
    let s = sync(initialWorkspaceState, [item('a', 'question', 't1')])
    expect(s.view).toBe('attention')
    // Пользователь вернулся в Feed.
    s = workspaceReducer(s, { type: 'openFeed' })
    expect(s.view).toBe('feed')
    // q2: та же стадия/вид, но новый updatedAt=t2 — новое прибытие → авто-открытие.
    s = sync(s, [item('a', 'question', 't2')])
    expect(s.view).toBe('attention')
    expect(s.activeStageId).toBe('a')
  })
})

describe('workspaceReducer — cost view + returnView contract', () => {
  it('openCost/openFeed set both view and returnView', () => {
    const cost = workspaceReducer(initialWorkspaceState, { type: 'openCost' })
    expect(cost.view).toBe('cost')
    expect(cost.returnView).toBe('cost')

    const feed = workspaceReducer(cost, { type: 'openFeed' })
    expect(feed.view).toBe('feed')
    expect(feed.returnView).toBe('feed')
  })

  it('cost → attention → resolve → cost (returnView recorded on entry into attention)', () => {
    const cost = workspaceReducer(initialWorkspaceState, { type: 'openCost' })
    const attn = sync(cost, [item('b', 'approval')])
    expect(attn.view).toBe('attention')
    expect(attn.returnView).toBe('cost')
    const resolved = sync(attn, [])
    expect(resolved.view).toBe('cost')
    expect(resolved.returnView).toBe('cost')
  })

  it('feed → attention → resolve → feed', () => {
    const attn = sync(initialWorkspaceState, [item('b', 'approval')])
    expect(attn.returnView).toBe('feed')
    const resolved = sync(attn, [])
    expect(resolved.view).toBe('feed')
    expect(resolved.returnView).toBe('feed')
  })

  it('history → attention → resolve → feed (history is stage-scoped, not restorable)', () => {
    const history = workspaceReducer(initialWorkspaceState, { type: 'openHistory', view: 'plan-history' })
    const attn = sync(history, [item('b', 'approval')])
    expect(attn.view).toBe('attention')
    expect(attn.returnView).toBe('feed')
    const resolved = sync(attn, [])
    expect(resolved.view).toBe('feed')
    expect(resolved.returnView).toBe('feed')
  })

  it('manual openCost mid-attention updates returnView so a later resolve lands on cost', () => {
    const attn = sync(initialWorkspaceState, [item('b', 'approval')])
    expect(attn.view).toBe('attention')
    expect(attn.returnView).toBe('feed')
    // Пользователь вручную переключился на Cost, не дожидаясь резолюции.
    const midway = workspaceReducer(attn, { type: 'openCost' })
    expect(midway.view).toBe('cost')
    expect(midway.returnView).toBe('cost')
    // Если пользователь снова откроет attention и оно разрешится — приземлится на cost.
    const reopened = workspaceReducer(midway, { type: 'openAttention' })
    const resolved = sync(reopened, [])
    expect(resolved.view).toBe('cost')
    expect(resolved.returnView).toBe('cost')
  })

  it('manual openFeed mid-attention updates returnView so a later resolve lands on feed', () => {
    const cost = workspaceReducer(initialWorkspaceState, { type: 'openCost' })
    const attn = sync(cost, [item('b', 'approval')])
    expect(attn.returnView).toBe('cost')
    const midway = workspaceReducer(attn, { type: 'openFeed' })
    expect(midway.returnView).toBe('feed')
    const reopened = workspaceReducer(midway, { type: 'openAttention' })
    const resolved = sync(reopened, [])
    expect(resolved.view).toBe('feed')
    expect(resolved.returnView).toBe('feed')
  })

  it('a second attention arriving while already in attention does not overwrite returnView', () => {
    const cost = workspaceReducer(initialWorkspaceState, { type: 'openCost' })
    const attn = sync(cost, [item('b', 'approval')])
    expect(attn.view).toBe('attention')
    expect(attn.returnView).toBe('cost')
    // Второе прибытие, пока уже в attention — returnView не должен измениться.
    const second = sync(attn, [item('b', 'approval'), item('d', 'question')])
    expect(second.view).toBe('attention')
    expect(second.returnView).toBe('cost')
    const resolved = sync(second, [])
    expect(resolved.view).toBe('cost')
  })

  it('openFullFeed switches to full-feed and records it as returnView', () => {
    const s = workspaceReducer(initialWorkspaceState, { type: 'openFullFeed' })
    expect(s.view).toBe('full-feed')
    expect(s.returnView).toBe('full-feed')
  })

  it('attention opened over full-feed returns to full-feed when the queue drains', () => {
    let s = workspaceReducer(initialWorkspaceState, { type: 'openFullFeed' })
    s = sync(s, [item('s1', 'question')]) // auto-open attention
    expect(s.view).toBe('attention')
    s = sync(s, []) // drains
    expect(s.view).toBe('full-feed')
  })
})
