// Политика follow ленты: follow — намерение читателя, а не геометрия. Включают его только жест вниз, закончившийся у низа,
// свёртывание, после раскладки вставшее к низу, Jump to latest и reset; scroll-событие без жеста (коррекции списка,
// вытеснение головы, программные прокрутки) follow не меняет в обе стороны.

export const FOLLOW_NEAR_BOTTOM_PX = 40

export type FollowEvent =
  // scroll — scroll-событие: nearBottom по геометрии, user — направление недавнего жеста пользователя (колесо, касание, клавиши,
  // ползунок) или null, если прокрутку сделал не он.
  | { type: 'scroll'; nearBottom: boolean; user: 'up' | 'down' | null }
  | { type: 'expanded' }
  | { type: 'collapsed'; nearBottom: boolean }
  | { type: 'jump-to-latest' }
  | { type: 'navigate' }
  | { type: 'reset' }

export function followReducer(following: boolean, event: FollowEvent): boolean {
  switch (event.type) {
    case 'scroll':
      if (event.user === 'down' && event.nearBottom) return true
      if (event.user === 'up' && !event.nearBottom) return false
      return following
    case 'expanded':
    case 'navigate':
      return false
    case 'collapsed':
      // Вызывается после раскладки, когда список уже встал к низу; до неё геометрия ещё старая.
      return event.nearBottom ? true : following
    case 'jump-to-latest':
    case 'reset':
      return true
  }
}

// isNearBottom — до низа не больше порога; тот же порог, что у atBottomThreshold списка.
export function isNearBottom(scroller: { scrollHeight: number; scrollTop: number; clientHeight: number }): boolean {
  return scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < FOLLOW_NEAR_BOTTOM_PX
}
