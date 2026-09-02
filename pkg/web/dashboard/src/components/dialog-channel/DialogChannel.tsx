import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react'
import { answerDialog, cancelDialog } from '../../api/run-client'
import { PasteableTextarea } from '../pasteable-textarea'
import type { Stage } from '../../types'
import { Maximizable, useMaximize } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { MarkdownRenderer } from '../plan-panel'
import { parseLineBlocks, type LineBlock } from '../plan-panel/markdown'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'

type DialogChannelProps = {
  stage: Stage | null
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
  const stageId = stage?.id ?? ''
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
  // submitting/submitError гейтят двойную отправку и делают ошибку мутирующего
  // действия видимой (finding #8): без них кнопка SEND не блокировалась на время
  // POST, показывала «✓ Sent» до ответа сервера, а reject (напр. 409 на второй
  // клик) оставался необработанным промисом.
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [flash, setFlash] = useState(false)

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
    // stage === null: панель смонтирована для стабильности лейаута, но
    // реальной стадии нет — не гоняем пустые опросы GET /api/stages//dialog.
    const current = stage
    if (current === null) return

    let cancelled = false
    let lastPendingId: string | undefined
    // Опрос issue независимые fetch каждые 2 c; ответы приходят не обязательно в
    // порядке отправки. requestGen — счётчик поколений (как latestRequestId в
    // useStatus): каждый refresh запоминает свой номер ДО await и применяет
    // ответ, только если он всё ещё самый свежий — иначе более старый
    // (до-ответа) response, резолвнувшийся после нового (уже-отвеченного), снова
    // открыл бы отвеченный вопрос.
    let requestGen = 0

    setEntries([])
    setSelectedOption(null)
    setCustomText('')
    setComments({})
    setActiveCommentLine(null)

    const refresh = (): void => {
      const requestId = ++requestGen
      void loadDialog(current.id).then((data) => {
        if (cancelled) return
        if (requestId !== requestGen) return // более свежий запрос уже применён
        if (data === null) return // транзиентная ошибка — сохраняем последнее успешное состояние
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
  }, [stage?.id])

  const pending = useMemo(() => findPending(entries), [entries])
  // stage.hasDialog — серверный сигнал «на диске уже есть хотя бы один
  // <phase>.dialog.jsonl» (см. stage_has_dialog в /api/status): держим панель
  // видимой, даже если локальный fetch дублирующего /dialog ещё не догнал
  // (или в проде вернул пустой список из-за гонки с записью файла) — иначе
  // возможна ложная секунда пустой панели/мигание для стадии, у которой
  // диалоговая история уже реально есть.
  const hasContent = stage !== null && (entries.length > 0 || stage.status === 'awaiting_user_input' || stage.hasDialog)
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
    if (stage === null) return
    const data = await loadDialog(stage.id)
    if (data === null) return // транзиентная ошибка — сохраняем последнее успешное состояние
    setEntries(data)
    setSelectedOption(null)
    setCustomText('')
    setComments({})
    setActiveCommentLine(null)
  }

  async function sendAnswer() {
    if (stage === null) return
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

    if (submitting) return // не даём второму клику отправить дубликат (→ 409)
    setSubmitting(true)
    setSubmitError(null)
    try {
      await answerDialog(stage.id, question.phase ?? '', question.id, answer, fromOptions)
      setClickedSend(true)
      window.setTimeout(() => setClickedSend(false), 1200)
      await reload()
    } catch (e) {
      setSubmitError(e instanceof Error ? e.message : 'failed to send answer')
    } finally {
      setSubmitting(false)
    }
  }

  // Как только у вопроса есть хотя бы один комментарий, ответ пользователя —
  // это сами комментарии (killer feature #2): опции и свободный ответ прячутся,
  // единственное действие — отправить собранный feedback тем же эндпоинтом
  // /dialog/answer, что и обычный ответ (from_options всегда false — это не
  // выбор из options).
  async function sendFeedback() {
    if (stage === null) return
    const question = pending
    if (question === null || question.id === undefined) return

    const feedback = buildFeedback(comments, question.question ?? '')
    if (feedback === '') return

    if (submitting) return
    setSubmitting(true)
    setSubmitError(null)
    try {
      await answerDialog(stage.id, question.phase ?? '', question.id, feedback, false)
      setClickedSend(true)
      window.setTimeout(() => setClickedSend(false), 1200)
      await reload()
    } catch (e) {
      setSubmitError(e instanceof Error ? e.message : 'failed to send feedback')
    } finally {
      setSubmitting(false)
    }
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
    if (stage === null) return
    if (!window.confirm('Cancel stage?')) return
    void cancelDialog(stage.id)
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
            <PasteableTextarea
              stageId={stageId}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={setDraft}
              autoFocus
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
                          disabled={submitting}
                          style={{ animationDelay: `${index * 40}ms` }}
                          onClick={() => selectOption(option)}
                        >
                          {option}
                        </button>
                      ))}
                    </div>

                    <PasteableTextarea
                      stageId={stageId}
                      className="dialog-custom"
                      placeholder="Or type your own answer…"
                      value={customText}
                      disabled={pending.allow_custom !== true}
                      onChange={onCustomInput}
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
                      disabled={(activeCommentLine !== null && draft.trim() !== '') || submitting}
                      onClick={sendAnswer}
                    >
                      <span className="btn-ripple" aria-hidden="true" />
                      <span className="btn-label">▸ SEND</span>
                      <span className="btn-done" aria-hidden="true">✓ Sent</span>
                    </button>
                  ) : (
                    <button className={`btn btn-send${clickedSend ? ' ok' : ''}`} type="button" disabled={submitting} onClick={sendFeedback}>
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
                {submitError !== null && (
                  <div className="dialog-error" role="alert">
                    {`Failed to send: ${submitError}. Try again.`}
                  </div>
                )}
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

  // Ключи по entry.phase/entry.id, а не по index в entries: buildDialogEntries
  // (сервер) перепарсивает растущий stream-лог стадии на каждый опрос и
  // условно пропускает вопрос, ещё не попавший в <phase>.dialog.jsonl (см.
  // "emitted"-гейт в pkg/server/handlers.go) — как только он туда попадает на
  // следующем опросе, он вставляется в исходную позицию, СДВИГАЯ index всех
  // последующих записей. С key=index React считал бы уже отрисованные (и не
  // изменившиеся по содержимому) qa-блоки новыми элементами и пересоздавал их
  // DOM целиком на ровном месте — отсюда рывки скролла по ходу диалога.
  entries.forEach((entry) => {
    if (entry.phase !== undefined && entry.phase !== lastPhase) {
      lastPhase = entry.phase
      nodes.push(
        <div className="phase-divider" key={`phase-${entry.phase}`}>
          {entry.phase}
        </div>,
      )
    }

    // «Мысли» агента (text-блоки из stream-json лога) не показываем в диалоге:
    // для GLM это рассуждения вслух, которые дублируют панель log. Секция диалога
    // — только вопросы/ответы. Контекст рассуждений остаётся в Log-режиме EventFeedPanel.
    if (entry.type === 'agent_text') {
      return
    }

    if (entry.answer !== null && entry.answer !== undefined) {
      nodes.push(
        <div className={`qa${entry.auto_answered === true ? ' qa-auto' : ''}`} key={`qa-${entry.phase ?? ''}-${entry.id ?? ''}`}>
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

// loadDialog возвращает null при транзиентной ошибке (сеть/не-OK), чтобы
// вызывающий отличил её от реально пустого диалога ([]) и НЕ затирал последнее
// успешное состояние пустотой на одну неудачную попытку опроса.
async function loadDialog(stageId: string): Promise<DialogEntry[] | null> {
  let response: Response
  try {
    response = await fetch(`/api/stages/${encodeURIComponent(stageId)}/dialog`)
  } catch {
    return null
  }

  if (!response.ok) return null

  // Единственная точка приведения типа для внешнего JSON.
  const data: unknown = await response.json()

  return Array.isArray(data) ? (data as DialogEntry[]) : []
}
