import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement, type ReactNode } from 'react'
import { answerDialog, cancelDialog, FlowApiError } from '../../api/run-client'
import { PasteableTextarea } from '../pasteable-textarea'
import type { Stage } from '../../types'
import { Maximizable, useMaximize } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { MarkdownRenderer } from '../plan-panel'
import { LineCommentedDocument, type LineCommentDocumentApi } from '../plan-panel/LineCommentedDocument'
import { JumpToLatestButton } from '../jump-to-latest'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'

type DialogChannelProps = {
  stage: Stage | null
  attention?: boolean
  // banner прокручивается с вопросами внутри #dialog-scroll; панель ответа остаётся sticky.
  banner?: ReactNode
  // scrollTarget — переход из ленты: к какому именно Q&A прокрутить канал при открытии.
  scrollTarget?: { stageId: string; phase: string; id: string } | null
  // onTargetConsumed — вызывается ровно один раз, сразу после того как scrollTarget был успешно применён.
  onTargetConsumed?: () => void
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

// questionKey — составной ключ вопроса: id уникален только В ПРЕДЕЛАХ фазы, поэтому сравнение «сменился ли
// pending-вопрос» по одному id путало planning/q1 и implementation/q1.
function questionKey(e: { phase?: string; id?: string } | null | undefined): string | undefined {
  return e == null ? undefined : `${e.phase ?? ''}/${e.id ?? ''}`
}

// R2: сколько ждём, что якорь прицельного скролла появится в DOM, прежде чем сдаться.
const TARGET_GIVE_UP_MS = 4000

// Диалоговый канал стадии: история вопросов/ответов по фазам, текущий вопрос, отмена. Поведение перенесено
// из loadDialog / renderDialog / renderPendingQuestion в текущем app.js.
export function DialogChannel({ stage, attention = false, banner, scrollTarget = null, onTargetConsumed }: DialogChannelProps): ReactElement {
  const stageId = stage?.id ?? ''
  const [entries, setEntries] = useState<DialogEntry[]>([])
  const [selectedOption, setSelectedOption] = useState<string | null>(null)
  const [customText, setCustomText] = useState('')
  // Пока есть активный вопрос, сначала показываем только его. Если вопрос
  // исчез (ответили/история открыта), история раскрывается автоматически.
  const [historyCollapsed, setHistoryCollapsed] = useState(false)

  // Построчные комментарии к pending-вопросу живут внутри keyed-владельца LineCommentedDocument: смена
  // размонтирует его и атомарно сбрасывает комментарии/черновик.
  const [clickedSend, setClickedSend] = useState(false)
  // submitting блокирует повторную отправку; submitError показывает отказ POST вместо ложного Sent.
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [flash, setFlash] = useState(false)

  // Автоскролл канала к хвосту при появлении новых сообщений/вопросов, пока пользователь сам не уехал
  // вверх.
  const feed = useStickToBottom<HTMLDivElement>()

  // Признак «панель dialog развёрнута на весь экран» берём прямо из контекста Maximizable — расширять
  // Maximizable не нужно.
  const { maximizedKey } = useMaximize()
  const maximized = maximizedKey === 'dialog'

  // requestGenRef/lastPendingKeyRef — поколение живёт на уровне компонента, потому что reload — ОТДЕЛЬНАЯ
  // точка входа, применяющая тот же /dialog поверх того же состояния.
  const requestGenRef = useRef(0)
  const lastPendingKeyRef = useRef<string | undefined>(undefined)

  // currentStageIdRef — какая стадия сейчас смонтирована в этой панели.
  const currentStageIdRef = useRef<string | null>(null)

  // applyDialog сбрасывает черновик только при смене pending (phase,id), сохраняя ввод между poll/reload.
  const applyDialog = useCallback((data: DialogEntry[]) => {
    setEntries(data)
    const nextKey = questionKey(findPending(data))
    if (nextKey !== lastPendingKeyRef.current) {
      lastPendingKeyRef.current = nextKey
      setSelectedOption(null)
      setCustomText('')
      setHistoryCollapsed(nextKey !== undefined)
    }
  }, [])

  // Грузим диалог при открытии стадии и опрашиваем каждые 2 c — агент может дописать новый вопрос/answer в
  // любой момент, и канал должен обновляться live.
  useEffect(() => {
    // stage === null: панель смонтирована для стабильности лейаута, но
    // реальной стадии нет — не гоняем пустые опросы GET /api/stages//dialog.
    const current = stage
    if (current === null) return

    let cancelled = false

    // Смена стадии инвалидирует любой ещё не резолвнувшийся запрос предыдущей стадии и сбрасывает
    // lastPendingKeyRef, чтобы первый ответ новой стадии не сравнивался с ключом чужого pending-вопроса.
    requestGenRef.current++
    lastPendingKeyRef.current = undefined
    currentStageIdRef.current = current.id

    setEntries([])
    setSelectedOption(null)
    setCustomText('')

    const refresh = (): void => {
      // Опрос issue независимые fetch каждые 2 c; ответы приходят не обязательно в порядке отправки.
      const requestId = ++requestGenRef.current
      void loadDialog(current.id).then((data) => {
        if (cancelled) return
        if (requestId !== requestGenRef.current) return // более свежий запрос уже применён
        if (data === null) return // транзиентная ошибка — сохраняем последнее успешное состояние
        applyDialog(data)
      })
    }

    refresh()
    const interval = window.setInterval(refresh, 2000)

    return () => {
      cancelled = true
      window.clearInterval(interval)
      // Панель переключается на другую стадию (или размонтируется) — reload(), замкнувшийся на текущей
      // стадии, больше не должен применять свой результат сюда.
      currentStageIdRef.current = null
    }
  }, [stage?.id, applyDialog])

  const pending = useMemo(() => findPending(entries), [entries])
  // stage.hasDialog сохраняет видимость панели, пока локальный fetch истории не догнал сервер.
  const hasContent = stage !== null && (entries.length > 0 || stage.status === 'awaiting_user_input' || stage.hasDialog)
  const hasAnswered = entries.some((entry) => entry.answer !== null && entry.answer !== undefined)
  const jumpToBottom = feed.jumpToBottom
  const releaseStick = feed.release

  // Прицельный скролл к конкретному Q&A. Объявлен ДО эффекта автопрыжка к pending ниже — тому эффекту (F5)
  // нужно читать retainedTarget, чтобы уступить прицельной навигации.
  const [retainedTarget, setRetainedTarget] = useState<{ stageId: string; phase: string; id: string } | null>(null)
  useEffect(() => {
    if (scrollTarget !== null && stage !== null && scrollTarget.stageId === stage.id) {
      setRetainedTarget(scrollTarget)
    } else {
      setRetainedTarget(null)
    }
  }, [scrollTarget, stage?.id])

  // Читаем цель через ref: её очистка не должна перезапускать pending-эффект и стирать прицельный скролл.
  const retainedTargetRef = useRef(retainedTarget)
  retainedTargetRef.current = retainedTarget

  // onTargetConsumed читаем через ref по той же причине: таймер отказа не должен перезапускаться из-за
  // смены идентичности колбэка родителя — только из-за смены самой цели.
  const onTargetConsumedRef = useRef(onTargetConsumed)
  onTargetConsumedRef.current = onTargetConsumed

  // Ограничиваем поиск отсутствующего якоря по времени, затем освобождаем цель и обычный автоскролл.
  useEffect(() => {
    if (retainedTarget === null) return
    const timeoutId = window.setTimeout(() => {
      setRetainedTarget(null)
      onTargetConsumedRef.current?.()
    }, TARGET_GIVE_UP_MS)
    return () => window.clearTimeout(timeoutId)
  }, [retainedTarget])

  // Ждущий ответа вопрос — всегда в конце истории. rAF — к следующему кадру, после layout (панель могла
  // только что пересчитать высоту), чтобы scrollHeight был финальным.
  useEffect(() => {
    if (pending === null) return
    const target = retainedTargetRef.current
    if (target !== null && !(target.phase === pending.phase && target.id === pending.id)) return
    const handle = requestAnimationFrame(() => jumpToBottom())
    return () => cancelAnimationFrame(handle)
  }, [questionKey(pending), jumpToBottom])

  // One-shot glow рамки диалога при появлении нового pending-вопроса: класс dialog-flash навешивается на
  // смену pending.id и снимается через 2.5s.
  useEffect(() => {
    if (pending === null) return
    setFlash(true)
    const t = window.setTimeout(() => setFlash(false), 2500)
    return () => window.clearTimeout(t)
  }, [questionKey(pending)])

  // При разворачивании панели на весь экран канал должен показать хвост диалога, а не то место, на котором
  // застал скролл в компактном режиме.
  useEffect(() => {
    if (!maximized) return
    const handle = requestAnimationFrame(() => jumpToBottom())
    return () => cancelAnimationFrame(handle)
  }, [maximized, jumpToBottom])

  useEffect(() => {
    if (retainedTarget === null) return

    // Сверяем phase и id: одинаковые id в разных фазах обозначают разные вопросы.
    if (pending !== null && pending.phase === retainedTarget.phase && pending.id === retainedTarget.id) {
      const el = document.getElementById('dialog-pending')
      if (el !== null) {
        // block:'start' — выравниваем ВЕРХ нужного Q&A к верху контейнера
        // (переход из ленты к конкретному вопросу/ответу), а не по центру.
        el.scrollIntoView({ block: 'start' })
        setRetainedTarget(null)
        onTargetConsumed?.()
        return
      }
    }

    const el = document.getElementById(`qa-${retainedTarget.phase}-${retainedTarget.id}`)
    if (el !== null) {
      // Отпускаем «прилипание» к низу ПЕРЕД скроллом: цель — исторический Q&A выше pending-вопроса, а
      // stick-to-bottom на ближайшей мутации вернул бы scrollTop к низу и отменил бы переход.
      releaseStick()
      el.scrollIntoView({ block: 'start' })
      setRetainedTarget(null)
      onTargetConsumed?.()
    }
    // Промах — не очищаем retainedTarget, следующий рендер entries/pending
    // (очередной опрос /dialog) повторит попытку.
  }, [entries, pending, retainedTarget, onTargetConsumed, releaseStick])

  if (!hasContent) return <></>

  async function reload() {
    // stageId — стадия, ДЛЯ КОТОРОЙ вызван этот reload. Проверяем и до await (не тратим запрос впустую), и
    // после (стадия могла смениться, пока fetch летел).
    const stageId = stage?.id
    if (stageId === undefined || stageId !== currentStageIdRef.current) return
    const requestId = ++requestGenRef.current
    const data = await loadDialog(stageId)
    if (stageId !== currentStageIdRef.current) return // панель уже переключилась на другую стадию
    if (requestId !== requestGenRef.current) return // superseded by a newer request
    if (data === null) return // транзиентная ошибка — сохраняем последнее успешное состояние
    applyDialog(data)
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
      if (e instanceof FlowApiError && e.code === 'answer_out_of_order') {
        await reload() // stale view — resync to the real current question
      }
    } finally {
      setSubmitting(false)
    }
  }

  // При наличии комментариев отправляем их как feedback через /dialog/answer с from_options=false.
  async function sendFeedback(comments: Record<number, string>) {
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
      if (e instanceof FlowApiError && e.code === 'answer_out_of_order') {
        await reload() // stale view — resync to the real current question
      }
    } finally {
      setSubmitting(false)
    }
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

  // renderPending переключает ответ на feedback при наличии комментариев; открытый редактор строки
  // блокирует отправку.
  function renderPending(doc: LineCommentDocumentApi): ReactNode {
    if (pending === null) return null
    return (
      <>
        {doc.body}

        {doc.commentCount === 0 && (
          <>
            <div className={`dialog-options${selectedOption !== null ? ' dimmed' : ''}`}>
              {(pending.options ?? []).map((option, index) => {
                const selected = selectedOption === option
                // Опция вида "Заголовок — описание" разбивается на жирный заголовок + описание (как в
                // макете-карточке). Без разделителя — вся строка как заголовок.
                const sep = option.indexOf(' — ')
                const title = sep >= 0 ? option.slice(0, sep) : option
                const desc = sep >= 0 ? option.slice(sep + 3) : ''
                return (
                  <button
                    key={option}
                    type="button"
                    className={selected ? 'selected' : ''}
                    aria-pressed={selected}
                    disabled={submitting}
                    style={{ animationDelay: `${index * 40}ms` }}
                    onClick={() => selectOption(option)}
                  >
                    <span className="dialog-option-radio" aria-hidden="true" />
                    <span className="dialog-option-body">
                      <span className="dialog-option-title">{title}</span>
                      {desc !== '' && <span className="dialog-option-desc">{desc}</span>}
                    </span>
                  </button>
                )
              })}
            </div>

            <PasteableTextarea
              stageId={stageId}
              className="dialog-custom"
              placeholder="Or type your own answer…"
              value={customText}
              disabled={pending.allow_custom !== true}
              onChange={onCustomInput}
              // M3: скрепка на основном поле ответа — прикрепить ссылку на файл проекта + вставленные
              // скриншоты уже работают через use-image-paste.
              allowFileReferences
              onSubmit={() => {
                // codex MEDIUM: Enter is a keyboard-submit path that a
                // disabled SEND button does not intercept — guard it separately.
                if (doc.activeCommentLine !== null) return
                void sendAnswer()
              }}
            />
          </>
        )}

        <div className="dialog-actions">
          {doc.commentCount === 0 ? (
            <button
              className={`btn btn-send${clickedSend ? ' ok' : ''}`}
              type="button"
              disabled={doc.activeCommentLine !== null || submitting}
              onClick={sendAnswer}
            >
              <span className="btn-ripple" aria-hidden="true" />
              <span className="btn-label">▸ SEND</span>
              <span className="btn-done" aria-hidden="true">✓ Sent</span>
            </button>
          ) : (
            <button
              className={`btn btn-send${clickedSend ? ' ok' : ''}`}
              type="button"
              disabled={submitting || doc.activeCommentLine !== null}
              onClick={() => void sendFeedback(doc.comments)}
            >
              <span className="btn-ripple" aria-hidden="true" />
              <span className="btn-label">{`Send feedback (${doc.commentCount})`}</span>
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
      </>
    )
  }


  return (
    <Maximizable id="dialog">
      <PanelFrame title="Communication channel" maximizeId="dialog" attention={attention}>
        <div id="dialog-section" className="section">
          <div id="dialog-scroll" className="dialog-scroll" ref={feed.ref}>
            {banner}
            <div id="dialog-history" className={`dialog-history${pending !== null && historyCollapsed ? ' collapsed' : ''}`}>
              {renderHistory(entries)}
            </div>

            {pending !== null && (
              <div id="dialog-pending" className={`dialog-pending${flash ? ' dialog-flash' : ''}`}>
                <LineCommentedDocument
                  key={`${stageId}::${pending.phase ?? ''}::${pending.id ?? ''}::${pending.question ?? ''}`}
                  text={pending.question ?? ''}
                  specialSections={false}
                  stageId={stageId}
                  beforeOpen={releaseStick}
                  bodyClassName="dialog-question line-commented"
                >
                  {(doc) => renderPending(doc)}
                </LineCommentedDocument>
              </div>
            )}

            {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} raised />}
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

// Собираем ответ из цитат строк и комментариев, отсортированных по номеру строки.
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

  // Ключ phase/id сохраняет DOM при вставке записей между опросами; index сдвигается и вызывает рывки
  // скролла.
  entries.forEach((entry) => {
    if (entry.phase !== undefined && entry.phase !== lastPhase) {
      lastPhase = entry.phase
      nodes.push(
        <div className="phase-divider" key={`phase-${entry.phase}`}>
          {entry.phase}
        </div>,
      )
    }

    // «Мысли» агента не показываем в диалоге: для GLM это рассуждения вслух, которые дублируют панель log.
    // Секция диалога — только вопросы/ответы.
    if (entry.type === 'agent_text') {
      return
    }

    if (entry.answer !== null && entry.answer !== undefined) {
      nodes.push(
        <div
          className={`qa${entry.auto_answered === true ? ' qa-auto' : ''}`}
          key={`qa-${entry.phase ?? ''}-${entry.id ?? ''}`}
          id={`qa-${entry.phase ?? ''}-${entry.id ?? ''}`}
        >
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

// loadDialog возвращает null при временной ошибке, чтобы не затирать историю пустым списком.
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
