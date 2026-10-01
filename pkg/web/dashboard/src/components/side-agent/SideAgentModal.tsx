import { useEffect, useRef, useState, type MouseEvent, type ReactElement } from 'react'
import { createPortal } from 'react-dom'
import { SideAgentHistory } from './SideAgentHistory'
import { SideAgentComposer } from './SideAgentComposer'
import type { SideAgentApi } from '../../hooks/use-side-agent'
import { visibleFocusables } from '../../lib/focus-trap'

// Таймер (мс), после которого закрывающееся окно уходит из hit-testing/a11y
// (phase closing → closed). Путь ЧИСТО таймерный — на transitionend/animationend
// он не завязан (поэтому надёжен и в reduced-motion, и в фоновой вкладке),
// значит значение ДОЛЖНО быть ≥ самой долгой закрывающей анимации в
// side-agent.css (сейчас 160ms у *-out). 220 оставляет запас, чтобы фолбэк не
// срезал анимацию.
const CLOSE_ANIM_MS = 220

type Phase = 'closed' | 'open' | 'closing'

export type SideAgentModalProps = {
  api: SideAgentApi
  // attentionShortcut — ждущее действие стадии (как у других оверлеев): кнопка в
  // шапке СНАЧАЛА скрывает Side agent (работа агента продолжается под ctx
  // менеджера), затем открывает нужный attention-воркспейс. null → нет ожидания.
  attentionShortcut?: { label: string; onActivate: () => void } | null
}

// SideAgentModal — центрированная модалка беседы поверх дашборда. Монтируется
// один раз (порталом в document.body) и остаётся смонтированной: open/close
// меняют только видимость (phase), сохраняя историю и позицию прокрутки. Крестик
// = только скрытие (работа агента продолжается под ctx менеджера на бэкенде).
export function SideAgentModal({ api, attentionShortcut = null }: SideAgentModalProps): ReactElement {
  const { isOpen, close, command, events, draft, setDraft, send, interrupt, requestState, error, unavailable } = api

  // phase: closed (скрыто, inert) → open → closing (играет закрывающая анимация,
  // уже inert) → closed. Разведение open/closing нужно, чтобы закрытие
  // доигрывалось, но окно сразу переставало быть интерактивным.
  const [phase, setPhase] = useState<Phase>('closed')
  const overlayRef = useRef<HTMLDivElement | null>(null)
  const modalRef = useRef<HTMLDivElement | null>(null)
  const openerRef = useRef<HTMLElement | null>(null)
  const downOnScrim = useRef(false)
  const closeTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Синхронизация isOpen → phase. Открытие прерывает идущее закрытие (быстрый
  // toggle разворачивается). Закрытие запускает анимацию с таймером-фолбэком.
  useEffect(() => {
    if (closeTimer.current !== null) {
      clearTimeout(closeTimer.current)
      closeTimer.current = null
    }
    if (isOpen) {
      // Запоминаем инициатора ДО показа, чтобы вернуть ему фокус при скрытии.
      if (phase === 'closed') openerRef.current = document.activeElement as HTMLElement | null
      setPhase('open')
      return
    }
    // isOpen === false
    setPhase((prev) => {
      if (prev === 'closed') return 'closed'
      closeTimer.current = setTimeout(() => {
        closeTimer.current = null
        setPhase('closed')
      }, CLOSE_ANIM_MS)
      return 'closing'
    })
  }, [isOpen]) // eslint-disable-line react-hooks/exhaustive-deps

  // Начальный фокус в поле ввода при открытии + возврат фокуса инициатору при
  // полном закрытии.
  useEffect(() => {
    if (phase === 'open') {
      modalRef.current?.querySelector<HTMLTextAreaElement>('.side-agent-textarea')?.focus()
    } else if (phase === 'closed') {
      openerRef.current?.focus?.()
    }
  }, [phase])

  // inert/hidden, пока окно не полностью открыто: закрытое и закрывающееся окно не
  // должно ловить клики/таб/скринридер.
  useEffect(() => {
    const el = overlayRef.current
    if (el === null) return
    el.inert = phase !== 'open'
  }, [phase])

  useEffect(
    () => () => {
      if (closeTimer.current !== null) clearTimeout(closeTimer.current)
    },
    [],
  )

  function onScrimDown(e: MouseEvent): void {
    // Закрытие по клику снаружи только если взаимодействие и началось, и
    // закончилось на самом scrim (overlay === target). Выделение текста с
    // отпусканием за окном (down внутри модалки) не закрывает.
    downOnScrim.current = e.target === e.currentTarget
  }
  function onScrimUp(e: MouseEvent): void {
    if (downOnScrim.current && e.target === e.currentTarget) close()
    downOnScrim.current = false
  }

  function onKeyDown(e: React.KeyboardEvent): void {
    if (e.key !== 'Tab' || modalRef.current === null) return
    const items = visibleFocusables(modalRef.current)
    const first = items[0]
    const last = items[items.length - 1]
    if (first === undefined || last === undefined) return
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }

  const running = requestState === 'running'

  return createPortal(
    <div
      ref={overlayRef}
      className={`side-agent-overlay side-agent-${phase}`}
      hidden={phase === 'closed'}
      onMouseDown={onScrimDown}
      onMouseUp={onScrimUp}
    >
      <div
        ref={modalRef}
        className="side-agent-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="side-agent-title"
        onKeyDown={onKeyDown}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="side-agent-header">
          <div className="side-agent-title-wrap">
            <h2 id="side-agent-title" className="side-agent-title">Side agent</h2>
            {command !== '' && <span className="side-agent-command" title="The configured agent command">{command}</span>}
          </div>
          <div className="side-agent-header-actions">
            <span className="side-agent-status" aria-live="polite">
              {running ? 'Working…' : ''}
            </span>
            {attentionShortcut !== null && (
              <button
                type="button"
                className="side-agent-attention-shortcut"
                onClick={() => { close(); attentionShortcut.onActivate() }}
              >
                <span className="attn-dot" aria-hidden="true" />
                {attentionShortcut.label}
              </button>
            )}
            <button type="button" className="icon-btn side-agent-close" aria-label="Hide side agent" onClick={close}>
              ✕
            </button>
          </div>
        </header>

        {unavailable && (
          <div className="side-agent-banner side-agent-banner-warning" role="status">
            The conversation store is unavailable; new messages are paused.
          </div>
        )}
        {error !== null && (
          <div className="side-agent-banner side-agent-banner-error" role="alert">
            {error}
          </div>
        )}

        <div className="side-agent-body">
          <SideAgentHistory events={events} />
        </div>

        <SideAgentComposer
          draft={draft}
          onDraftChange={setDraft}
          onSend={() => void send(draft)}
          onStop={interrupt}
          running={running}
        />
      </div>
    </div>,
    document.body,
  )
}
