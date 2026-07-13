import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react'
import type { Stage } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { MarkdownRenderer } from '../plan-panel'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'

type DialogChannelProps = {
  stage: Stage
  attention?: boolean
}

type DialogEntry = {
  type?: string
  phase?: string
  question?: string
  answer?: string | null
  id?: string
  allow_custom?: boolean
  options?: string[]
  text?: string
}

// Диалоговый канал стадии: история вопросов/ответов по фазам, текущий вопрос
// (опции и/или свободный ответ), отмена. Поведение перенесено из loadDialog /
// renderDialog / renderPendingQuestion в текущем app.js.
export function DialogChannel({ stage, attention = false }: DialogChannelProps): ReactElement {
  const [entries, setEntries] = useState<DialogEntry[]>([])
  const [selectedOption, setSelectedOption] = useState<string | null>(null)
  const [customText, setCustomText] = useState('')
  const [historyCollapsed, setHistoryCollapsed] = useState(false)

  // Автоскролл канала к хвосту при появлении новых сообщений/вопросов,
  // пока пользователь сам не уехал вверх. Контейнер охватывает и историю,
  // и pending-вопрос — кнопка «↓ к последнему» возвращает к актуальному.
  const feed = useStickToBottom<HTMLDivElement>()

  // Грузим диалог при открытии стадии и опрашиваем каждые 2 c — агент может
  // дописать новый вопрос/answer в любой момент, и канал должен обновляться live.
  // Без опроса новый вопрос виден только после ручной перезагрузки страницы.
  // Выбор пользователя (option/customText) сбрасываем только при смене
  // pending-вопроса, чтобы опрос не затирал ввод посреди ответа.
  useEffect(() => {
    // NO_STAGE sentinel: панель смонтирована для стабильности лейаута, но
    // реальной стадии нет — не гоняем пустые опросы GET /api/stages//dialog.
    if (stage.id === '') return

    let cancelled = false
    let lastPendingId: string | undefined

    setEntries([])
    setSelectedOption(null)
    setCustomText('')

    const refresh = (): void => {
      void loadDialog(stage.id).then((data) => {
        if (cancelled) return
        setEntries(data)
        const nextPendingId = findPending(data)?.id
        if (nextPendingId !== lastPendingId) {
          lastPendingId = nextPendingId
          setSelectedOption(null)
          setCustomText('')
        }
      })
    }

    refresh()
    const interval = window.setInterval(refresh, 2000)

    return () => {
      cancelled = true
      window.clearInterval(interval)
    }
  }, [stage.id])

  const pending = useMemo(() => findPending(entries), [entries])
  const hasContent = entries.length > 0 || stage.status === 'awaiting_user_input'
  const hasAnswered = entries.some((entry) => entry.answer !== null && entry.answer !== undefined)
  const jumpToBottom = feed.jumpToBottom

  // Ждущий ответа вопрос — всегда в конце истории. Проматываем к нему диалог:
  // и при загрузке страницы (пользователь сразу видит опции ответа), и при
  // появлении нового вопроса от агента. rAF — к следующему кадру, после layout
  // (панель могла только что пересчитать высоту), чтобы scrollHeight был финальным.
  useEffect(() => {
    if (pending === null) return
    const handle = requestAnimationFrame(() => jumpToBottom())
    return () => cancelAnimationFrame(handle)
  }, [pending?.id, jumpToBottom])

  if (!hasContent) return <></>

  async function reload() {
    const data = await loadDialog(stage.id)
    setEntries(data)
    setSelectedOption(null)
    setCustomText('')
  }

  async function sendAnswer() {
    const question = pending
    if (question === null || question.id === undefined) return

    const trimmed = customText.trim()
    let answer: string | null = null
    let fromOptions = false

    if (trimmed !== '') {
      answer = trimmed
      fromOptions = false
    } else if (selectedOption !== null) {
      answer = selectedOption
      fromOptions = true
    } else {
      return
    }

    await postJson(`/api/stages/${encodeURIComponent(stage.id)}/dialog/answer`, {
      id: question.id,
      phase: question.phase ?? '',
      answer,
      from_options: fromOptions,
    })

    await reload()
  }

  function cancel() {
    if (!window.confirm('Cancel stage?')) return
    void postJson(`/api/stages/${encodeURIComponent(stage.id)}/dialog/cancel`, null)
  }

  function selectOption(option: string) {
    setSelectedOption(option)
    setCustomText('')
  }

  function onCustomInput(value: string) {
    setCustomText(value)
    if (value.length > 0) {
      setSelectedOption(null)
    }
  }

  return (
    <Maximizable id="dialog">
      <PanelFrame title="Communication channel" maximizeId="dialog" attention={attention}>
        <div id="dialog-section" className="section">
          <div id="dialog-scroll" className="dialog-scroll" ref={feed.ref}>
            <div id="dialog-history" className={`dialog-history${historyCollapsed ? ' collapsed' : ''}`}>
              {renderHistory(entries)}
            </div>

            {pending !== null && (
              <div id="dialog-pending" className="dialog-pending">
                <div className="dialog-question">
                  <MarkdownRenderer source={pending.question ?? ''} />
                </div>

                <div className={`dialog-options${selectedOption !== null ? ' dimmed' : ''}`}>
                  {(pending.options ?? []).map((option, index) => (
                    <button
                      key={option}
                      type="button"
                      className={selectedOption === option ? 'selected' : ''}
                      aria-pressed={selectedOption === option}
                      style={{ animationDelay: `${index * 40}ms` }}
                      onClick={() => selectOption(option)}
                    >
                      {option}
                    </button>
                  ))}
                </div>

                <textarea
                  className="dialog-custom"
                  placeholder="Or type your own answer…"
                  value={customText}
                  disabled={pending.allow_custom !== true}
                  onChange={(event) => onCustomInput(event.target.value)}
                />

                <div className="dialog-actions">
                  <button className="btn btn-send" type="button" onClick={sendAnswer}>
                    ▸ SEND
                  </button>
                  <button className="btn btn-cancel-dialog" type="button" onClick={cancel}>
                    CANCEL STAGE
                  </button>
                  <span className="typing-indicator">
                    AGENT IS WAITING <span className="blink" />
                  </span>
                </div>
              </div>
            )}

            {!feed.stick && (
              <button type="button" className="jump-latest" onClick={feed.jumpToBottom}>
                ↓ к последнему
              </button>
            )}
          </div>

          {hasAnswered && (
            <button
              id="dialog-toggle"
              className="dialog-toggle"
              type="button"
              onClick={() => setHistoryCollapsed((prev) => !prev)}
            >
              {historyCollapsed ? '▾ EXPAND HISTORY' : '▴ COLLAPSE HISTORY'}
            </button>
          )}
        </div>
      </PanelFrame>
    </Maximizable>
  )
}

function renderHistory(entries: DialogEntry[]): ReactNode[] {
  const nodes: ReactNode[] = []
  let lastPhase = ''

  entries.forEach((entry, index) => {
    if (entry.phase !== undefined && entry.phase !== lastPhase) {
      lastPhase = entry.phase
      nodes.push(
        <div className="phase-divider" key={`phase-${index}`}>
          {entry.phase}
        </div>,
      )
    }

    // «Мысли» агента (text-блоки из stream-json лога) не показываем в диалоге:
    // для GLM это рассуждения вслух, которые дублируют панель log. Секция диалога
    // — только вопросы/ответы. Контекст рассуждений остаётся в LogPanel.
    if (entry.type === 'agent_text') {
      return
    }

    if (entry.answer !== null && entry.answer !== undefined) {
      nodes.push(
        <div className="qa" key={`qa-${index}`}>
          <div className="q">
            <MarkdownRenderer source={entry.question ?? ''} />
          </div>
          <div className="a">{`→ ${entry.answer}`}</div>
        </div>,
      )
    }
  })

  return nodes
}

function findPending(entries: DialogEntry[]): DialogEntry | null {
  for (let i = entries.length - 1; i >= 0; i--) {
    const entry = entries[i]
    if (entry === undefined) continue
    if (entry.type === 'agent_text') continue
    if (entry.answer === null || entry.answer === undefined) return entry
  }

  return null
}

async function loadDialog(stageId: string): Promise<DialogEntry[]> {
  let response: Response
  try {
    response = await fetch(`/api/stages/${encodeURIComponent(stageId)}/dialog`)
  } catch {
    return []
  }

  if (!response.ok) return []

  // Единственная точка приведения типа для внешнего JSON.
  const data: unknown = await response.json()

  return Array.isArray(data) ? (data as DialogEntry[]) : []
}

async function postJson(url: string, body: unknown): Promise<void> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === null ? null : JSON.stringify(body),
  })

  if (!response.ok) {
    throw new Error(`POST ${url} -> ${response.status}`)
  }
}
