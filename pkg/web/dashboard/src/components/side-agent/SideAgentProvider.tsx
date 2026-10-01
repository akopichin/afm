import { createContext, useContext, useEffect, useMemo, type ReactElement, type ReactNode } from 'react'
import { useSideAgent, type SideAgentDeps } from '../../hooks/use-side-agent'
import { useSideAgentShortcut } from '../../hooks/use-side-agent-shortcut'
import { SideAgentModal } from './SideAgentModal'

// Контекст для шапки/оболочки: показать ли кнопку (enabled), открыть беседу
// (open), есть ли непросмотренный результат (unseenResult) и идёт ли запрос
// (active). Дефолт — всё выключено (capability off / провайдер не смонтирован).
export type SideAgentContextValue = {
  enabled: boolean
  isOpen: boolean
  open: () => void
  unseenResult: boolean
  active: boolean
}

const SideAgentContext = createContext<SideAgentContextValue>({
  enabled: false,
  isOpen: false,
  open: () => {},
  unseenResult: false,
  active: false,
})

export function useSideAgentContext(): SideAgentContextValue {
  return useContext(SideAgentContext)
}

export type SideAgentProviderProps = {
  // run_id текущего прогона (поколение беседы: смена run_id инвалидирует старую).
  runId: string
  // capability side_agent с бэкенда: выключено → провайдер инертен (ни API-
  // вызовов, ни модалки, ни хоткея).
  enabled: boolean
  // blocked — поверх воркспейса открыт другой модальный ввод (Files/Review/
  // pre-note): Cmd/Ctrl+` тогда не открывает беседу (не крадём хоткей).
  blocked?: boolean
  // onOpenChange — App использует для anyModalOpen (подавление авто-открытия
  // attention за модалкой) и подсветки.
  onOpenChange?: (open: boolean) => void
  // attentionShortcut — ждущее действие стадии; пробрасывается в модалку, чтобы
  // явный переход к ожиданию скрывал Side agent, сохраняя его работу. null → нет.
  attentionShortcut?: { label: string; onActivate: () => void } | null
  // deps — тест-шов useSideAgent (SSE/HTTP за инъектируемыми функциями). В
  // проде опускается → дефолтные deps (реальные fetch/EventSource).
  deps?: SideAgentDeps
  children: ReactNode
}

// SideAgentProvider — владелец состояния беседы на уровне приложения. Держит
// useSideAgent (source of truth), вешает глобальный хоткей и рендерит модалку
// (смонтирована, пока enabled; видимостью управляет сама). Выключенная
// capability делает провайдер полностью инертным, не меняя дерево для App.
export function SideAgentProvider({ runId, enabled, blocked = false, onOpenChange, attentionShortcut = null, deps, children }: SideAgentProviderProps): ReactElement {
  const api = useSideAgent(runId, enabled, deps)
  const { isOpen, open, close } = api

  useSideAgentShortcut({
    isOpen: enabled && isOpen,
    onToggle: () => (isOpen ? close() : open()),
    onClose: close,
    blocked,
  })

  // Сообщаем наверх о видимости беседы (anyModalOpen/подавление attention).
  useEffect(() => {
    onOpenChange?.(enabled && isOpen)
  }, [enabled, isOpen, onOpenChange])

  const value = useMemo<SideAgentContextValue>(
    () => ({ enabled, isOpen, open, unseenResult: api.unseenResult, active: api.requestState === 'running' }),
    [enabled, isOpen, open, api.unseenResult, api.requestState],
  )

  return (
    <SideAgentContext.Provider value={value}>
      {children}
      {enabled && <SideAgentModal api={api} attentionShortcut={attentionShortcut} />}
    </SideAgentContext.Provider>
  )
}
