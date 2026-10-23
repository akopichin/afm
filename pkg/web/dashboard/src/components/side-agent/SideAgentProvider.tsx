import { useEffect, useMemo, type ReactElement, type ReactNode } from 'react'
import { useSideAgent, type SideAgentDeps } from '../../hooks/use-side-agent'
import { useSideAgentShortcut } from '../../hooks/use-side-agent-shortcut'
import { useAssistantProfiles } from '../../hooks/use-assistant-profiles'
import { SideAgentModal } from './SideAgentModal'
import { SideAgentContext, useSideAgentContext, type SideAgentContextValue } from './side-agent-context'

// Контекст вынесен в листовой модуль side-agent-context.ts (см. комментарий там) — здесь только
// ре-экспорт, чтобы существующие импортёры (index.ts, GlobalHeader и т.п.) не менялись.
export { useSideAgentContext, type SideAgentContextValue }

export type SideAgentProviderProps = {
  // run_id текущего прогона (поколение беседы: смена run_id инвалидирует старую).
  runId: string
  // capability side_agent с бэкенда: выключено → провайдер инертен (ни API-
  // вызовов, ни модалки, ни хоткея).
  enabled: boolean
  // currentStageId — стадия, подсвеченная активной в рейле дашборда прямо сейчас (T4.3): дефолт для
  // "Context: …" в модалке, замораживается в момент Send (см. use-side-agent.ts's contextStageIdRef).
  // '' → адресата по умолчанию нет.
  currentStageId?: string
  // stages — id/title известных стадий, для селектора смены контекста ДО отправки (T4.3). Пусто → контекст
  // показывается как есть (raw id), без выпадающего списка.
  stages?: { id: string; title: string }[]
  // mentionsEnabled — capability collaboration с бэкенда (НЕЗАВИСИМАЯ от side_agent capability: ран может
  // иметь помощников без бокового чата, или наоборот). Выключено → @mention-меню (T4.2) недостижимо: список
  // профилей не грузится вовсе.
  mentionsEnabled?: boolean
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

// SideAgentProvider — владелец состояния беседы на уровне приложения. Выключенная capability делает
// провайдер полностью инертным, не меняя дерево для App.
export function SideAgentProvider({ runId, enabled, blocked = false, onOpenChange, attentionShortcut = null, currentStageId = '', stages = [], mentionsEnabled = false, deps, children }: SideAgentProviderProps): ReactElement {
  const api = useSideAgent(runId, enabled, deps, currentStageId)
  const assistantProfiles = useAssistantProfiles(enabled && mentionsEnabled)
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
    () => ({
      enabled,
      isOpen,
      open,
      openWith: api.openWith,
      sendExactMessage: api.sendExact,
      unseenResult: api.unseenResult,
      active: api.requestState === 'running',
    }),
    [enabled, isOpen, open, api.openWith, api.sendExact, api.unseenResult, api.requestState],
  )

  return (
    <SideAgentContext.Provider value={value}>
      {children}
      {enabled && <SideAgentModal api={api} attentionShortcut={attentionShortcut} stages={stages} assistantProfiles={assistantProfiles} />}
    </SideAgentContext.Provider>
  )
}
