import { useCallback, useEffect, useRef, useState } from 'react'
import type { Stage } from '../../types'
import { stagesNeedingAttention, type AttentionEntry, type AttentionKind } from '../use-attention'

export type NotificationPermissionState = 'unsupported' | 'default' | 'granted' | 'denied'

const STORAGE_KEY = 'afm-notifications-enabled'

const TITLES: Record<AttentionKind, string> = {
  plan: 'Need Approve',
  dialog: 'You have a question',
  failed: 'Stage failed',
  paused: 'Stage paused',
}

function readInitialPermission(): NotificationPermissionState {
  return 'Notification' in window ? Notification.permission : 'unsupported'
}

function readInitialEnabled(): boolean {
  return window.localStorage.getItem(STORAGE_KEY) === '1'
}

// showNotification — единая точка создания браузерной нотификации: best-effort, иконка из favicon, клик
// фокусирует окно + опциональный колбэк.
function showNotification(title: string, body: string, tag: string, onClick?: () => void): void {
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href
  let n: Notification
  try {
    n = new Notification(title, { body, icon, tag })
  } catch {
    return
  }
  n.onclick = () => {
    window.focus()
    onClick?.()
    n.close()
  }
}

function fireNotification(entry: AttentionEntry, onFocusStage: (stageId: string) => void): void {
  const time = new Date().toLocaleTimeString()
  showNotification(TITLES[entry.kind], `${entry.stage.name} — click to view\n${time}`, `afm-stage-${entry.stage.id}`, () =>
    onFocusStage(entry.stage.id),
  )
}

// Терминальные статусы стадии: работы больше не будет. Флоу «закончился», когда
// ВСЕ стадии терминальны (нет running/planning/pending/awaiting/paused/…).
const TERMINAL_STATUSES: ReadonlySet<Stage['status']> = new Set(['done', 'failed'])

function flowFinished(stages: Stage[]): boolean {
  return stages.length > 0 && stages.every((s) => TERMINAL_STATUSES.has(s.status))
}

// Десктоп-уведомления о стадиях, которым нужно действие, пока вкладка дашборда не активна.
export function useDesktopNotifications(
  stages: Stage[],
  onFocusStage: (stageId: string) => void,
  flowName = 'Flow',
): {
  enabled: boolean
  permission: NotificationPermissionState
  requestEnable: () => void
  disable: () => void
} {
  const [permission, setPermission] = useState<NotificationPermissionState>(readInitialPermission)
  const [enabled, setEnabled] = useState<boolean>(readInitialEnabled)
  // stageId → updatedAt того attention-эпизода, о котором уже уведомили. Прежний Set<stageId> в этом
  // случае глушил q2 как «уже уведомляли».
  const notifiedStages = useRef<Map<string, string>>(new Map())
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

    // Забываем стадии, вышедшие из attention — тогда повторный заход уведомит заново.
    for (const id of notifiedStages.current.keys()) {
      if (!currentIds.has(id)) notifiedStages.current.delete(id)
    }

    if (!enabledRef.current || !document.hidden) return
    for (const entry of current) {
      // Уже уведомляли об ЭТОМ же эпизоде (тот же updatedAt)? — пропускаем.
      // Другой updatedAt у той же стадии = новый attention-эпизод → уведомляем.
      if (notifiedStages.current.get(entry.stage.id) === entry.stage.updatedAt) continue
      notifiedStages.current.set(entry.stage.id, entry.stage.updatedAt)
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

  // Нотификация о завершении флоу: когда ВСЕ стадии стали терминальными — т.е. ран дошёл до конца, прямо
  // перед выходом afm.
  const sawActive = useRef(false)
  // lastFinishSig — подпись последнего ОБРАБОТАННОГО завершения.
  const lastFinishSig = useRef('')
  const flowNameRef = useRef(flowName)
  useEffect(() => {
    flowNameRef.current = flowName
  }, [flowName])
  useEffect(() => {
    if (stages.length === 0) return // данных ещё нет — ни активно, ни финишно
    if (!flowFinished(stages)) {
      sawActive.current = true
      return
    }
    const sig = stages.map((s) => `${s.id}:${s.status}:${s.updatedAt}`).join('|')
    if (sig === lastFinishSig.current) return // это завершение уже обработано
    const shouldFire = sawActive.current && enabled
    lastFinishSig.current = sig // консумим независимо от enabled/sawActive
    if (!shouldFire) return
    const failed = stages.some((s) => s.status === 'failed')
    const title = failed ? 'Flow finished with failures' : 'Flow completed'
    showNotification(title, `${flowNameRef.current}\n${new Date().toLocaleTimeString()}`, 'afm-flow-finished')
  }, [stages, enabled])

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
