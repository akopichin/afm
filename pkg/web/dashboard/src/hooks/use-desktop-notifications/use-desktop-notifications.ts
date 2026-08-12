import { useCallback, useEffect, useRef, useState } from 'react'
import type { Stage } from '../../types'
import { stagesNeedingAttention, type AttentionEntry, type AttentionKind } from '../use-attention'

export type NotificationPermissionState = 'unsupported' | 'default' | 'granted' | 'denied'

const STORAGE_KEY = 'afm-notifications-enabled'

const TITLES: Record<AttentionKind, string> = {
  plan: 'Need Approve',
  dialog: 'You have a question',
  failed: 'Stage failed',
}

function readInitialPermission(): NotificationPermissionState {
  return 'Notification' in window ? Notification.permission : 'unsupported'
}

function readInitialEnabled(): boolean {
  return window.localStorage.getItem(STORAGE_KEY) === '1'
}

function fireNotification(entry: AttentionEntry, onFocusStage: (stageId: string) => void): void {
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href
  const time = new Date().toLocaleTimeString()
  let n: Notification
  try {
    n = new Notification(TITLES[entry.kind], {
      body: `${entry.stage.name} — click to view\n${time}`,
      icon,
      tag: `afm-stage-${entry.stage.id}`,
    })
  } catch {
    // best-effort — некоторые окружения кидают синхронно (например нет
    // разрешения на ОС-уровне); не роняем остальной UI из-за нотификации.
    return
  }
  n.onclick = () => {
    window.focus()
    onFocusStage(entry.stage.id)
    n.close()
  }
}

// Десктоп-уведомления о стадиях, которым нужно действие (approve/question/fail),
// пока вкладка дашборда не активна. Один reconciliation-проход (reconcile)
// покрывает и "новый переход в attention, пока вкладка уже скрыта", и "стадия
// уже ждала, когда пользователь ушёл со вкладки" — оба случая вызывают одну и
// ту же функцию, разница только в том, что её триггерит (обновление stages или
// visibilitychange). Стадии, вышедшие из attention, забываются из
// notifiedStageIds, чтобы повторный заход (упала → retried → упала снова)
// уведомил заново.
export function useDesktopNotifications(
  stages: Stage[],
  onFocusStage: (stageId: string) => void,
): {
  enabled: boolean
  permission: NotificationPermissionState
  requestEnable: () => void
  disable: () => void
} {
  const [permission, setPermission] = useState<NotificationPermissionState>(readInitialPermission)
  const [enabled, setEnabled] = useState<boolean>(readInitialEnabled)
  const notifiedStageIds = useRef<Set<string>>(new Set())
  const stagesRef = useRef<Stage[]>(stages)
  const enabledRef = useRef<boolean>(enabled)
  const onFocusStageRef = useRef(onFocusStage)

  useEffect(() => {
    stagesRef.current = stages
  }, [stages])
  useEffect(() => {
    enabledRef.current = enabled
  }, [enabled])
  useEffect(() => {
    onFocusStageRef.current = onFocusStage
  }, [onFocusStage])

  const reconcile = useCallback((currentStages: Stage[]) => {
    const current = stagesNeedingAttention(currentStages)
    const currentIds = new Set(current.map((e) => e.stage.id))

    for (const id of notifiedStageIds.current) {
      if (!currentIds.has(id)) notifiedStageIds.current.delete(id)
    }

    if (!enabledRef.current || !document.hidden) return
    for (const entry of current) {
      if (notifiedStageIds.current.has(entry.stage.id)) continue
      notifiedStageIds.current.add(entry.stage.id)
      fireNotification(entry, onFocusStageRef.current)
    }
  }, [])

  useEffect(() => {
    reconcile(stages)
  }, [stages, enabled, reconcile])

  useEffect(() => {
    const onVisibility = () => {
      if (document.hidden) reconcile(stagesRef.current)
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => document.removeEventListener('visibilitychange', onVisibility)
  }, [reconcile])

  const requestEnable = useCallback(() => {
    if (!('Notification' in window)) return
    void Notification.requestPermission().then((result) => {
      setPermission(result)
      if (result === 'granted') {
        setEnabled(true)
        window.localStorage.setItem(STORAGE_KEY, '1')
      }
    })
  }, [])

  const disable = useCallback(() => {
    setEnabled(false)
    window.localStorage.removeItem(STORAGE_KEY)
  }, [])

  return { enabled, permission, requestEnable, disable }
}
