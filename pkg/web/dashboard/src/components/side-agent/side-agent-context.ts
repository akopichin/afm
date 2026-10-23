import { createContext, useContext } from 'react'

// Листовой модуль: единственный импорт — react. SideAgentProvider.tsx импортирует контекст отсюда вместо
// того чтобы определять его сам — иначе FileBrowserProvider (который тоже зовёт useSideAgentContext())
// замкнул бы цикл через SideAgentModal → SideAgentHistory → feed-workspace → FeedComposer →
// pasteable-textarea → file-browser (barrel) → FileBrowserProvider.

// Контекст для шапки/оболочки: показать ли кнопку, открыть беседу (с цитатой или без), есть ли
// непросмотренный результат и идёт ли запрос. Дефолт — всё выключено (capability off / провайдер не
// смонтирован), openWith — безопасный no-op, чтобы потребители вне провайдера (например,
// FileBrowserProvider в своих тестах) не падали.
export type SideAgentContextValue = {
  enabled: boolean
  isOpen: boolean
  open: () => void
  openWith: (quote: string) => void
  // sendExactMessage — отправить Assistant ровно этот пакет под id вызывающего (файловые комментарии): после
  // принятия обновляются running/история/индикатор как у обычного send; busy и API-ошибки уходят в reject.
  sendExactMessage: (clientMessageId: string, body: string) => Promise<void>
  unseenResult: boolean
  active: boolean
}

export const SideAgentContext = createContext<SideAgentContextValue>({
  enabled: false,
  isOpen: false,
  open: () => {},
  openWith: () => {},
  sendExactMessage: () => Promise.reject(new Error('Assistant unavailable')),
  unseenResult: false,
  active: false,
})

export function useSideAgentContext(): SideAgentContextValue {
  return useContext(SideAgentContext)
}
