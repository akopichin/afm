import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useDesktopNotifications } from './use-desktop-notifications'
import type { Stage } from '../../types'

const stage = (status: Stage['status'], id = 's', updatedAt = ''): Stage =>
  ({ id, name: `Stage ${id}`, status, updatedAt, interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] })

class MockNotification {
  static permission: NotificationPermission = 'granted'
  static requestPermission = vi.fn<() => Promise<NotificationPermission>>()
  static instances: MockNotification[] = []
  onclick: (() => void) | null = null
  close = vi.fn()
  title: string
  options?: NotificationOptions
  constructor(title: string, options?: NotificationOptions) {
    this.title = title
    this.options = options
    MockNotification.instances.push(this)
  }
}

function setIconLink(href: string): void {
  document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  const link = document.createElement('link')
  link.rel = 'icon'
  link.href = href
  document.head.appendChild(link)
}

describe('useDesktopNotifications', () => {
  beforeEach(() => {
    MockNotification.instances = []
    MockNotification.permission = 'granted'
    MockNotification.requestPermission = vi.fn().mockResolvedValue('granted')
    vi.stubGlobal('Notification', MockNotification)
    window.localStorage.setItem('afm-notifications-enabled', '1')
    setIconLink('/favicon.svg')
    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    window.localStorage.clear()
    document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  })

  it('уведомляет один раз на новый переход в attention, пока вкладка скрыта, и не дублирует на повторном рендере', () => {
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })
    expect(MockNotification.instances).toHaveLength(0)

    rerender({ stages: [stage('awaiting_approval')] })
    expect(MockNotification.instances).toHaveLength(1)
    expect(MockNotification.instances[0]?.title).toBe('Need Approve')

    rerender({ stages: [stage('awaiting_approval')] })
    expect(MockNotification.instances).toHaveLength(1)
  })

  it('не уведомляет, пока вкладка видима — но уведомляет при уходе со вкладки, если стадия всё ещё ждёt', () => {
    Object.defineProperty(document, 'hidden', { value: false, configurable: true })
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })

    rerender({ stages: [stage('awaiting_user_input')] })
    expect(MockNotification.instances).toHaveLength(0)

    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    expect(MockNotification.instances).toHaveLength(1)
    expect(MockNotification.instances[0]?.title).toBe('You have a question')
  })

  it('повторный вопрос ТОЙ ЖЕ стадии уведомляет заново, даже если промежуточный не-attention снимок не наблюдался (баг: полёшка пропустила running между q1 и q2)', () => {
    // Реальный сценарий из репорта: агент интерактивной стадии задаёт вопрос
    // (q1), пользователь отвечает (awaiting_user_input → running), агент тут же
    // задаёт следующий (q2, running → awaiting_user_input). Обе транзакции
    // меняют updatedAt, но опрос /api/status (раз в 3с) может НЕ застать
    // промежуточный `running` — фронт видит awaiting(q1) → awaiting(q2). Раньше
    // notifiedStageIds ключился только по stage.id и очищался лишь когда фронт
    // НАБЛЮДАЛ выход из attention, поэтому q2 глушился как «уже уведомляли».
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('awaiting_user_input', 's', 't1')] },
    })
    expect(MockNotification.instances).toHaveLength(1)

    // q2 у той же стадии — статус тот же, но updatedAt новый; промежуточного
    // running-снимка фронт не увидел.
    rerender({ stages: [stage('awaiting_user_input', 's', 't2')] })
    expect(MockNotification.instances).toHaveLength(2)
  })

  it('уведомляет заново, если стадия вышла из attention и зашла снова', () => {
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('failed')] },
    })
    expect(MockNotification.instances).toHaveLength(1)

    rerender({ stages: [stage('retrying')] })
    rerender({ stages: [stage('failed')] })
    expect(MockNotification.instances).toHaveLength(2)
  })

  it('не уведомляет, если enabled=false (флаг не выставлен в localStorage)', () => {
    window.localStorage.removeItem('afm-notifications-enabled')
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })
    rerender({ stages: [stage('failed')] })
    expect(MockNotification.instances).toHaveLength(0)
  })

  it('requestEnable() при granted включает enabled и сохраняет в localStorage', async () => {
    window.localStorage.removeItem('afm-notifications-enabled')
    MockNotification.permission = 'default'
    const { result } = renderHook(() => useDesktopNotifications([stage('running')], vi.fn()))
    expect(result.current.enabled).toBe(false)

    await act(async () => {
      result.current.requestEnable()
      await Promise.resolve()
    })

    expect(result.current.enabled).toBe(true)
    expect(result.current.permission).toBe('granted')
    expect(window.localStorage.getItem('afm-notifications-enabled')).toBe('1')
  })

  it('клик по нотификации фокусирует окно, выбирает стадию и закрывает нотификацию', () => {
    const onFocusStage = vi.fn()
    const focusSpy = vi.spyOn(window, 'focus').mockImplementation(() => {})
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })
    rerender({ stages: [stage('awaiting_approval', 'xyz')] })

    const n = MockNotification.instances[0]!
    n.onclick?.()

    expect(focusSpy).toHaveBeenCalled()
    expect(onFocusStage).toHaveBeenCalledWith('xyz')
    expect(n.close).toHaveBeenCalled()
    focusSpy.mockRestore()
  })
})
