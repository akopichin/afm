import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import type { Stage } from '../../types'
import { Maximizable, useMaximize } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { MarkdownRenderer } from '../plan-panel'
import { parseLineBlocks, type LineBlock } from '../plan-panel/markdown'
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
  auto_answered?: boolean
}

// Диалоговый канал стадии: история вопросов/ответов по фазам, текущий вопрос
// (опции и/или свободный ответ), отмена. Поведение перенесено из loadDialog /
// renderDialog / renderPendingQuestion в текущем app.js.
export function DialogChannel({ stage, attention = false }: DialogChannelProps): ReactElement {
  const [entries, setEntries] = useState<DialogEntry[]>([])
  const [selectedOption, setSelectedOption] = useState<string | null>(null)
  const [customText, setCustomText] = useState('')
  const [historyCollapsed, setHistoryCollapsed] = useState(false)

  // Комментарии к строкам pending-вопроса — тот же паттерн, что у PlanPanel
  // (comments/activeCommentLine/draft): клик по строке открывает форму
  // add/update/delete, комментарии живут только до отправки feedback.
  const [comments, setComments] = useState<Record<number, string>>({})
  const [activeCommentLine, setActiveCommentLine] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const [clickedSend, setClickedSend] = useState(false)
  const [flash, setFlash] = useState(false)

  const commentTextareaRef = useAutoGrowTextarea(draft, 400)
  const customTextareaRef = useAutoGrowTextarea(customText, 400)

  // Автоскролл канала к хвосту при появлении новых сообщений/вопросов,
  // пока пользователь сам не уехал вверх. Контейнер охватывает и историю,
  // и pending-вопрос — кнопка «↓ к последнему» возвращает к актуальному.
  const feed = useStickToBottom<HTMLDivElement>()

  // Признак «панель dialog развёрнута на весь экран» берём прямо из контекста
  // Maximizable (useMaximize уже экспортирован публично и предназначен именно для
  // этого — см. его использование в PanelFrame) — расширять Maximizable не нужно.
  const { maximizedKey } = useMaximize()
  const maximized = maximizedKey === 'dialog'

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
    setComments({})
    setActiveCommentLine(null)

    const refresh = (): void => {
      void loadDialog(stage.id).then((data) => {
        if (cancelled) return
        setEntries(data)
        const nextPendingId = findPending(data)?.id
        if (nextPendingId !== lastPendingId) {
          lastPendingId = nextPendingId
          setSelectedOption(null)
          setCustomText('')
          setComments({})
          setActiveCommentLine(null)
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
  // stage.hasDialog — серверный сигнал «на диске уже есть хотя бы один
  // <phase>.dialog.jsonl» (см. stage_has_dialog в /api/status): держим панель
  // видимой, даже если локальный fetch дублирующего /dialog ещё не догнал
  // (или в проде вернул пустой список из-за гонки с записью файла) — иначе
  // возможна ложная секунда пустой панели/мигание для стадии, у которой
  // диалоговая история уже реально есть.
  const hasContent = entries.length > 0 || stage.status === 'awaiting_user_input' || stage.hasDialog
  const hasAnswered = entries.some((entry) => entry.answer !== null && entry.answer !== undefined)
  const jumpToBottom = feed.jumpToBottom
  const commentCount = Object.keys(comments).length

  // Ждущий ответа вопрос — всегда в конце истории. Проматываем к нему диалог:
  // и при загрузке страницы (пользователь сразу видит опции ответа), и при
  // появлении нового вопроса от агента. rAF — к следующему кадру, после layout
  // (панель могла только что пересчитать высоту), чтобы scrollHeight был финальным.
  useEffect(() => {
    if (pending === null) return
    const handle = requestAnimationFrame(() => jumpToBottom())
    return () => cancelAnimationFrame(handle)
  }, [pending?.id, jumpToBottom])

  // One-shot glow рамки диалога при появлении нового pending-вопроса (B3):
  // класс dialog-flash навешивается на смену pending.id и снимается через 2.5s
  // (совпадает с длительностью CSS-анимации .dialog-flash, чтобы глоу успевал доиграть).
  useEffect(() => {
    if (pending === null) return
    setFlash(true)
    const t = window.setTimeout(() => setFlash(false), 2500)
    return () => window.clearTimeout(t)
  }, [pending?.id])

  // При разворачивании панели на весь экран (меняется высота контейнера) канал
  // должен показать хвост диалога, а не то место, на котором застал скролл в компактном
  // режиме. rAF — после layout оверлея, чтобы scrollHeight уже отражал итоговую
  // (полноэкранную) высоту.
  useEffect(() => {
    if (!maximized) return
    const handle = requestAnimationFrame(() => jumpToBottom())
    return () => cancelAnimationFrame(handle)
  }, [maximized, jumpToBottom])

  if (!hasContent) return <></>

  async function reload() {
    const data = await loadDialog(stage.id)
    setEntries(data)
    setSelectedOption(null)
    setCustomText('')
    setComments({})
    setActiveCommentLine(null)
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

    setClickedSend(true)
    window.setTimeout(() => setClickedSend(false), 1200)

    await postJson(`/api/stages/${encodeURIComponent(stage.id)}/dialog/answer`, {
      id: question.id,
      phase: question.phase ?? '',
      answer,
      from_options: fromOptions,
    })

    await reload()
  }

  // Как только у вопроса есть хотя бы один комментарий, ответ пользователя —
  // это сами комментарии (killer feature #2): опции и свободный ответ прячутся,
  // единственное действие — отправить собранный feedback тем же эндпоинтом
  // /dialog/answer, что и обычный ответ (from_options всегда false — это не
  // выбор из options).
  async function sendFeedback() {
    const question = pending
    if (question === null || question.id === undefined) return

    const feedback = buildFeedback(comments, question.question ?? '')
    if (feedback === '') return

    setClickedSend(true)
    window.setTimeout(() => setClickedSend(false), 1200)

    await postJson(`/api/stages/${encodeURIComponent(stage.id)}/dialog/answer`, {
      id: question.id,
      phase: question.phase ?? '',
      answer: feedback,
      from_options: false,
    })

    await reload()
  }

  function handleLineClick(line: number) {
    if (activeCommentLine !== null && draft.trim() !== '') return

    if (activeCommentLine === line) {
      setActiveCommentLine(null)
      return
    }

    setActiveCommentLine(line)
    setDraft(comments[line] ?? '')
  }

  function closeCommentForm() {
    setActiveCommentLine(null)
    setDraft('')
  }

  function saveComment(line: number) {
    const text = draft.trim()
    setComments((prev) => {
      const next = { ...prev }
      if (text === '') {
        delete next[line]
      } else {
        next[line] = text
      }
      return next
    })
    setActiveCommentLine(null)
  }

  function deleteComment(line: number) {
    setComments((prev) => {
      const next = { ...prev }
      delete next[line]
      return next
    })
    setActiveCommentLine(null)
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

  function renderCommentHeader(label: string, ariaLabel: string, title: string, onClick: () => void): ReactNode {
    return (
      <div className="comment-display-header">
        <span style={{ color: 'var(--c-awaiting)', fontSize: '12px' }}>{label}</span>
        <button type="button" className="comment-remove" aria-label={ariaLabel} title={title} onClick={onClick}>
          ✕
        </button>
      </div>
    )
  }

  // Строка вопроса рендерится как строка ревью-плана (renderPlanLine в PlanPanel) —
  // те же CSS-классы (plan-line/line-num/line-content/line-comment-*) ради
  // визуальной консистентности между комментариями к плану и к вопросу.
  function renderQuestionLine(item: LineBlock): ReactNode {
    const hasComment = comments[item.line] !== undefined

    return (
      <div
        key={`q-line-${item.line}`}
        className={`plan-line${hasComment ? ' has-comment' : ''}`}
        data-line={item.line}
        onClick={() => handleLineClick(item.line)}
      >
        <span className="line-num">{item.line}</span>
        <span className="line-content" dangerouslySetInnerHTML={{ __html: item.html }} />
        <span className="line-comment-marker">●</span>

        {hasComment && (
          <div className="line-comment-form line-comment-display" onClick={(event) => event.stopPropagation()}>
            {renderCommentHeader(`Comment on line ${item.line}`, `Remove comment on line ${item.line}`, 'Remove comment', () => deleteComment(item.line))}
            <div style={{ color: 'var(--text)', whiteSpace: 'pre-wrap' }}>{comments[item.line]}</div>
          </div>
        )}

        {activeCommentLine === item.line && (
          <div className="line-comment-form" onClick={(event) => event.stopPropagation()}>
            {renderCommentHeader(`Comment on line ${item.line}`, `Close comment on line ${item.line}`, 'Close', closeCommentForm)}
            <textarea
              ref={commentTextareaRef}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                  e.preventDefault()
                  saveComment(item.line)
                }
              }}
            />
            <div className="comment-actions">
              <button className="btn btn-send" type="button" onClick={() => saveComment(item.line)}>
                {hasComment ? 'Update' : 'Add'}
              </button>
              {hasComment && (
                <button className="btn btn-cancel" type="button" onClick={() => deleteComment(item.line)}>
                  Delete
                </button>
              )}
            </div>
          </div>
        )}
      </div>
    )
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
              <div id="dialog-pending" className={`dialog-pending${flash ? ' dialog-flash' : ''}`}>
                <div className="dialog-question">
                  {parseLineBlocks(pending.question ?? '').map((item) => renderQuestionLine(item))}
                </div>

                {commentCount === 0 && (
                  <>
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
                      ref={customTextareaRef}
                      className="dialog-custom"
                      placeholder="Or type your own answer…"
                      value={customText}
                      disabled={pending.allow_custom !== true}
                      onChange={(event) => onCustomInput(event.target.value)}
                      onKeyDown={(e) => {
                        if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                          e.preventDefault()
                          void sendAnswer()
                        }
                      }}
                    />
                  </>
                )}

                <div className="dialog-actions">
                  {commentCount === 0 ? (
                    <button
                      className={`btn btn-send${clickedSend ? ' ok' : ''}`}
                      type="button"
                      disabled={activeCommentLine !== null && draft.trim() !== ''}
                      onClick={sendAnswer}
                    >
                      <span className="btn-ripple" aria-hidden="true" />
                      <span className="btn-label">▸ SEND</span>
                      <span className="btn-done" aria-hidden="true">✓ Sent</span>
                    </button>
                  ) : (
                    <button className={`btn btn-send${clickedSend ? ' ok' : ''}`} type="button" onClick={sendFeedback}>
                      <span className="btn-ripple" aria-hidden="true" />
                      <span className="btn-label">{`Send feedback (${commentCount})`}</span>
                      <span className="btn-done" aria-hidden="true">✓ Sent</span>
                    </button>
                  )}
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
                ↓ latest
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

// Собирает текст ответа из комментариев к строкам вопроса — аналог buildFeedback
// в PlanPanel: для каждой прокомментированной строки цитата исходной строки
// вопроса + «Line N: комментарий», отсортировано по номеру строки.
function buildFeedback(comments: Record<number, string>, question: string): string {
  const lines = question.split('\n')

  return Object.keys(comments)
    .map(Number)
    .sort((a, b) => a - b)
    .map((line) => `> ${lines[line - 1] ?? ''}\nLine ${line}: ${comments[line]}`)
    .join('\n\n')
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
        <div className={`qa${entry.auto_answered === true ? ' qa-auto' : ''}`} key={`qa-${index}`}>
          <div className="q">
            <MarkdownRenderer source={entry.question ?? ''} />
          </div>
          <div className="a">
            {entry.auto_answered === true && (
              <span className="auto-answered-badge" title="Answered automatically by afm">⚙</span>
            )}
            {`→ ${entry.answer}`}
          </div>
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
