import { useEffect, useRef } from 'react'

export type SideAgentShortcutOptions = {
  // Открыт ли сейчас Side agent — определяет, тоглит ли тот же хоткей скрытие и
  // потребляет ли Escape (верхнее окно).
  isOpen: boolean
  // Переключить видимость Side agent (открыть/скрыть).
  onToggle: () => void
  // Скрыть Side agent (для Escape).
  onClose: () => void
  // blocked — поверх воркспейса открыт ДРУГОЙ модальный ввод (Files / Review
  // notes / pre-note). Тогда Cmd/Ctrl+` НЕ открывает Side agent — не крадём
  // хоткей у чужого оверлея. Уже открытый Side agent всё равно закрывается (он
  // и есть верхний слой).
  blocked?: boolean
}

// useSideAgentShortcut — глобальный хоткей показа/скрытия Side agent.
//
// Toggle: Cmd+` (macOS; Shift-вариант, воспринимаемый как Cmd+~, тоже проходит)
// или запасной Ctrl+`. Клавиша опознаётся по ФИЗИЧЕСКОМУ event.code
// ('Backquote'), поэтому раскладка (RU/EN) не влияет. Повтор при удержании
// (event.repeat), IME-composition и лишний Alt игнорируются.
//
// Escape скрывает ТОЛЬКО когда Side agent открыт, и потребляет событие
// (preventDefault + stopPropagation), чтобы не закрыть нижележащую
// максимизированную панель.
export function useSideAgentShortcut({ isOpen, onToggle, onClose, blocked = false }: SideAgentShortcutOptions): void {
  // Через ref, чтобы слушатель ставился один раз и не переподписывался на каждую
  // смену isOpen/blocked (иначе на каждый тик стейта — add/removeEventListener).
  const isOpenRef = useRef(isOpen)
  isOpenRef.current = isOpen
  const blockedRef = useRef(blocked)
  blockedRef.current = blocked
  const onToggleRef = useRef(onToggle)
  onToggleRef.current = onToggle
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent): void {
      // IME composition (e.isComposing или legacy keyCode 229) — не хоткей.
      if (e.isComposing || e.keyCode === 229) return

      if (e.key === 'Escape') {
        if (!isOpenRef.current) return
        e.preventDefault()
        e.stopPropagation()
        onCloseRef.current()
        return
      }

      // Физическая клавиша `, один из Cmd/Ctrl, без Alt, не автоповтор.
      if (e.code !== 'Backquote' || e.altKey || e.repeat) return
      if (!e.metaKey && !e.ctrlKey) return

      // Открывать поверх другого модального оверлея нельзя; закрыть открытый —
      // можно (он верхний слой).
      if (!isOpenRef.current && blockedRef.current) return
      e.preventDefault()
      onToggleRef.current()
    }

    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])
}
