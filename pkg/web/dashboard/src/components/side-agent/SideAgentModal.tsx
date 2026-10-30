import { useEffect, useRef, useState, type MouseEvent, type ReactElement } from 'react'
import { createPortal } from 'react-dom'
import { SideAgentHistory } from './SideAgentHistory'
import { SideAgentComposer } from './SideAgentComposer'
import type { SideAgentApi } from '../../hooks/use-side-agent'
import type { AssistantProfile } from '../../types/assistant-profile'
import { trapTab } from '../../lib/focus-trap'

// Таймер, после которого закрывающееся окно уходит из hit-testing/a11y. 220 оставляет запас, чтобы фолбэк
// не срезал анимацию.
const CLOSE_ANIM_MS = 220

type Phase = 'closed' | 'open' | 'closing'

export type SideAgentModalProps = {
  api: SideAgentApi
  // attentionShortcut — ждущее действие стадии: кнопка в шапке СНАЧАЛА скрывает Side agent, затем
  // открывает нужный attention-воркспейс. null → нет ожидания.
  attentionShortcut?: { label: string; onActivate: () => void } | null
  // Without a stage list, keep the known context visible as its raw ID.
  stages?: { id: string; title: string }[]
  // An empty profile list keeps helper mentions unavailable.
  assistantProfiles?: AssistantProfile[]
}

// SideAgentModal — центрированная модалка беседы поверх дашборда. Крестик = только скрытие (работа агента
// продолжается под ctx менеджера на бэкенде).
export function SideAgentModal({ api, attentionShortcut = null, stages = [], assistantProfiles = [] }: SideAgentModalProps): ReactElement {
  const {
    isOpen, close, command, events, draft, setDraft, send, interrupt, requestState, activeSource, error, unavailable,
    quote, clearQuote, contextStageId, setContextStageId,
  } = api

  // phase: closed → open → closing → closed. Разведение open/closing нужно, чтобы закрытие доигрывалось,
  // но окно сразу переставало быть интерактивным.
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
      if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return 'closed'
      closeTimer.current = setTimeout(() => {
        closeTimer.current = null
        setPhase('closed')
      }, CLOSE_ANIM_MS)
      return 'closing'
    })
  }, [isOpen]) // eslint-disable-line react-hooks/exhaustive-deps

  // inert снимается ДО фокуса: focus() внутри inert-поддерева в браузере no-op
  // (jsdom это не соблюдает, поэтому порядок проверяется отдельным тестом).
  useEffect(() => {
    const el = overlayRef.current
    if (el !== null) el.inert = phase !== 'open'
    if (phase === 'open') {
      modalRef.current?.querySelector<HTMLTextAreaElement>('.side-agent-composer .chat-composer-input')?.focus()
    } else if (modalRef.current?.contains(document.activeElement) || document.activeElement === document.body) {
      openerRef.current?.focus()
    }
  }, [phase])

  useEffect(
    () => () => {
      if (closeTimer.current !== null) clearTimeout(closeTimer.current)
    },
    [],
  )

  function onScrimDown(e: MouseEvent): void {
    // Закрытие по клику снаружи только если взаимодействие и началось, и закончилось на самом scrim.
    // Выделение текста с отпусканием за окном (down внутри модалки) не закрывает.
    downOnScrim.current = e.target === e.currentTarget
  }
  function onScrimUp(e: MouseEvent): void {
    if (downOnScrim.current && e.target === e.currentTarget) close()
    downOnScrim.current = false
  }

  function onKeyDown(e: React.KeyboardEvent): void {
    if (phase !== 'open' || e.nativeEvent.isComposing || e.keyCode === 229) return
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      close()
    } else if (modalRef.current !== null) {
      trapTab(e, modalRef.current)
    }
  }

  const running = requestState === 'running'
  const statusText = running ? activeSource?.kind === 'mesh' ? `Replying to ${activeSource.sender}…` : 'Working…' : ''

  return createPortal(
    <div
      ref={overlayRef}
      className={`side-agent-overlay side-agent-${phase}`}
      hidden={phase === 'closed'}
      aria-hidden={phase !== 'open' ? true : undefined}
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
            <h2 id="side-agent-title" className="side-agent-title">Assistant</h2>
          </div>
          <div className="side-agent-header-actions">
            <span className="side-agent-status" aria-live="polite" title={statusText}>
              {statusText}
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
            <button type="button" className="icon-btn side-agent-close" aria-label="Hide assistant" onClick={close}>
              ✕
            </button>
          </div>
        </header>

        {/* Context is captured when sending; changing it here only affects the next turn. */}
          <div className="side-agent-context-row">
            <span className="side-agent-context-label">{contextStageId !== '' || stages.length > 0 ? 'Context:' : 'Run-wide context'}</span>
            {stages.length > 0 ? (
              <select
                className="side-agent-context-select"
                aria-label="Conversation context stage"
                value={contextStageId}
                onChange={(e) => setContextStageId(e.target.value)}
              >
                <option value="">No stage</option>
                {stages.map((s) => (
                  // Keep nameless stages selectable with a visible ID.
                  <option key={s.id} value={s.id}>{s.title || s.id}</option>
                ))}
              </select>
            ) : (
              <span className="side-agent-context-value">{contextStageId}</span>
            )}
            {command !== '' && <span className="side-agent-command" title="The configured agent command">{command}</span>}
          </div>

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
          onStop={activeSource?.kind === 'modal' ? interrupt : undefined}
          running={running}
          quote={quote}
          onCancelQuote={clearQuote}
          assistantProfiles={assistantProfiles}
        />
      </div>
    </div>,
    document.body,
  )
}
