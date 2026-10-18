import { useEffect, useRef, useState, type MouseEvent, type ReactElement } from 'react'
import { createPortal } from 'react-dom'
import { SideAgentHistory } from './SideAgentHistory'
import { SideAgentComposer } from './SideAgentComposer'
import type { SideAgentApi } from '../../hooks/use-side-agent'
import type { AssistantProfile } from '../../types/assistant-profile'
import { visibleFocusables } from '../../lib/focus-trap'

// Таймер, после которого закрывающееся окно уходит из hit-testing/a11y. 220 оставляет запас, чтобы фолбэк
// не срезал анимацию.
const CLOSE_ANIM_MS = 220

type Phase = 'closed' | 'open' | 'closing'

export type SideAgentModalProps = {
  api: SideAgentApi
  // attentionShortcut — ждущее действие стадии: кнопка в шапке СНАЧАЛА скрывает Side agent, затем
  // открывает нужный attention-воркспейс. null → нет ожидания.
  attentionShortcut?: { label: string; onActivate: () => void } | null
  // stages — известные пары id/title стадий, для селектора «Context: …» (T4.3). Пусто/не передано → адресат
  // показывается как есть (raw stage id), без выпадающего списка — ровно то, что известно на этот момент.
  stages?: { id: string; title: string }[]
  // assistantProfiles — роли, доступные для @mention в композере (T4.2). Пусто → меню недостижимо.
  assistantProfiles?: AssistantProfile[]
}

// SideAgentModal — центрированная модалка беседы поверх дашборда. Крестик = только скрытие (работа агента
// продолжается под ctx менеджера на бэкенде).
export function SideAgentModal({ api, attentionShortcut = null, stages = [], assistantProfiles = [] }: SideAgentModalProps): ReactElement {
  const {
    isOpen, close, command, events, draft, setDraft, send, interrupt, requestState, error, unavailable,
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
    } else if (phase === 'closed') {
      openerRef.current?.focus?.()
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

        {/* Адресат хода (T4.3): видно, какой стадии касается сообщение, и можно сменить ДО отправки — после
            Send это уже не изменит отправленный ход (см. use-side-agent.ts's contextStageIdRef). Рендерится,
            только если есть что показывать: либо уже есть адресат, либо есть из чего выбирать. */}
        {(contextStageId !== '' || stages.length > 0) && (
          <div className="side-agent-context-row">
            <span className="side-agent-context-label">Context:</span>
            {stages.length > 0 ? (
              <select
                className="side-agent-context-select"
                aria-label="Conversation context stage"
                value={contextStageId}
                onChange={(e) => setContextStageId(e.target.value)}
              >
                <option value="">No stage</option>
                {stages.map((s) => (
                  // Fall back to the id when the stage has no display name — state.Store.SetStageNames stores
                  // empty names as-is and documents that "the UI handles them" (the stage rail already does
                  // this); without the fallback the context dropdown renders a blank, unselectable-looking row.
                  <option key={s.id} value={s.id}>{s.title || s.id}</option>
                ))}
              </select>
            ) : (
              <span className="side-agent-context-value">{contextStageId}</span>
            )}
          </div>
        )}

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
          quote={quote}
          onCancelQuote={clearQuote}
          assistantProfiles={assistantProfiles}
        />
      </div>
    </div>,
    document.body,
  )
}
