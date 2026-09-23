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
  // banner — attention-шапка («Agent needs your input»), рендерится ПЕРВЫМ
  // ребёнком внутри скролл-области #dialog-scroll, а не над панелью: уезжает
  // вместе с контентом при скролле и не съедает высоту у больших вопросов;
  // sticky-панель ответа остаётся на месте.
  banner?: ReactNode
  // scrollTarget — переход из ленты (клик по dialog_question/dialog_answer
  // элементу в FeedWorkspace, App.tsx's handleOpenDialogFromFeed): к какому
  // именно Q&A прокрутить канал при открытии. /dialog грузится асинхронно —
  // на момент маунта якорь обычно ещё не существует в DOM, поэтому цель
  // сохраняется и попытка повторяется при каждом изменении entries/pending,
  // пока скролл не удастся (см. эффект ниже). stageId — чья это цель: без неё
  // цель, оставшаяся от предыдущей стадии (attention auto-advance, повторное
  // открытие того же диалога), могла бы дождаться случайного совпадения
  // phase/id на ДРУГОЙ стадии и проскроллить не туда (F6).
  scrollTarget?: { stageId: string; phase: string; id: string } | null
  // onTargetConsumed — вызывается ровно один раз, сразу после того как
  // scrollTarget был успешно применён (скролл состоялся). Родитель должен
  // сбросить свой scrollToDialogTarget в ответ — иначе цель переживёт своё
  // назначение и повторное открытие того же диалога проиграет старый скролл
  // заново (F6).
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

// questionKey — составной ключ вопроса (phase,id): id уникален только В
// ПРЕДЕЛАХ фазы (см. комментарий F4 ниже), поэтому сравнение «сменился ли
// pending-вопрос» по одному id путало planning/q1 и implementation/q1.
// '/' — безопасный разделитель (фазы — фиксированные слова без '/'), в
// отличие от литерального NUL, который пометил бы файл как бинарный.
function questionKey(e: { phase?: string; id?: string } | null | undefined): string | undefined {
  return e == null ? undefined : `${e.phase ?? ''}/${e.id ?? ''}`
}

// R2: сколько ждём, что якорь прицельного скролла (scrollTarget) появится в
// DOM, прежде чем сдаться. Опрос /dialog идёт раз в 2с — этого времени
// хватает на несколько попыток, но цель не остаётся навязанной навсегда,
// если якорь в принципе не существует (id не из этой стадии).
const TARGET_GIVE_UP_MS = 4000

// Диалоговый канал стадии: история вопросов/ответов по фазам, текущий вопрос
// (опции и/или свободный ответ), отмена. Поведение перенесено из loadDialog /
// renderDialog / renderPendingQuestion в текущем app.js.
export function DialogChannel({ stage, attention = false, banner, scrollTarget = null, onTargetConsumed }: DialogChannelProps): ReactElement {
  const stageId = stage?.id ?? ''
  const [entries, setEntries] = useState<DialogEntry[]>([])
  const [selectedOption, setSelectedOption] = useState<string | null>(null)
  const [customText, setCustomText] = useState('')
  // Пока есть активный вопрос, сначала показываем только его. Если вопрос
  // исчез (ответили/история открыта), история раскрывается автоматически.
  const [historyCollapsed, setHistoryCollapsed] = useState(false)

  // Построчные комментарии к pending-вопросу живут внутри keyed-владельца
  // LineCommentedDocument (см. рендер ниже): смена (stage,phase,id,текст вопроса)
  // размонтирует его и атомарно сбрасывает комментарии/черновик. Поэтому в самом
  // DialogChannel этого state больше нет — комментарии читаются из render-prop.
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

  // requestGenRef/lastPendingKeyRef — поколение живёт на уровне компонента (не
  // локально внутри эффекта опроса), потому что reload() (вызывается после
  // успешного ответа и на answer_out_of_order) — ОТДЕЛЬНАЯ точка входа,
  // применяющая тот же /dialog поверх того же состояния. Без общих refs
  // reload() не мог ни (а) отменить уже летящий poll-запрос предыдущего
  // вопроса (тот применился бы ПОСЛЕ reload и откатил бы к старому pending),
  // ни (б) сообщить следующему poll «pending уже сменился» — тот увидел бы
  // смену ключа сам и второй раз стёр черновик, который пользователь только
  // начал печатать на новом вопросе.
  const requestGenRef = useRef(0)
  const lastPendingKeyRef = useRef<string | undefined>(undefined)

  // currentStageIdRef — какая стадия сейчас смонтирована в этой панели.
  // reload() (вызывается из sendAnswer/sendFeedback после успешного POST)
  // замыкает stage.id того РЕНДЕРА, в котором был вызван, — то есть стадию A,
  // на которой пользователь отправил ответ. Если пользователь переключился на
  // стадию B ДО того, как POST зарезолвился, reload() всё равно выполнится (в
  // рамках того же React-замыкания) и применит ответ A поверх уже
  // смонтированной панели B — бампая ОБЩИЙ requestGenRef, что попутно
  // инвалидирует легитимный поллинг B. Ref обновляется только в эффекте
  // опроса (а не в reload — иначе он обновлял бы сам себя), поэтому reload()
  // может сверить «стадия, для которой я вызван» с «стадия, которая сейчас
  // реально смонтирована», и отказаться применять устаревший результат.
  const currentStageIdRef = useRef<string | null>(null)

  // applyDialog — единая точка применения ответа /dialog, общая для опроса и
  // reload(): пишет entries и сбрасывает черновик/выбор ТОЛЬКО когда
  // (phase,id) pending-вопроса реально изменился — иначе повторное применение
  // одного и того же pending (например, poll сразу после reload) не трогает
  // то, что пользователь уже начал вводить.
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

    // Смена стадии инвалидирует любой ещё не резолвнувшийся запрос предыдущей
    // стадии (общий requestGenRef — reload() тоже его бампает) и сбрасывает
    // lastPendingKeyRef, чтобы первый ответ новой стадии не сравнивался с
    // ключом чужого pending-вопроса.
    requestGenRef.current++
    lastPendingKeyRef.current = undefined
    currentStageIdRef.current = current.id

    setEntries([])
    setSelectedOption(null)
    setCustomText('')

    const refresh = (): void => {
      // Опрос issue независимые fetch каждые 2 c; ответы приходят не обязательно
      // в порядке отправки. requestGenRef — счётчик поколений (как
      // latestRequestId в useStatus): каждый refresh запоминает свой номер ДО
      // await и применяет ответ, только если он всё ещё самый свежий — иначе
      // более старый (до-ответа) response, резолвнувшийся после более нового
      // (уже применённого reload() или другим poll), снова открыл бы
      // отвеченный вопрос.
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
      // Панель переключается на другую стадию (или размонтируется) —
      // reload(), замкнувшийся на текущей стадии, больше не должен применять
      // свой результат сюда.
      currentStageIdRef.current = null
    }
  }, [stage?.id, applyDialog])

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
  const releaseStick = feed.release

  // Прицельный скролл к конкретному Q&A (переход из ленты). Целится в
  // отдельный state (retainedTarget), а не напрямую в проп: /dialog грузится
  // асинхронно, поэтому якорь (#dialog-pending либо #qa-<phase>-<id>) обычно
  // отсутствует в DOM на момент прихода target — цель нужно удержать и
  // повторить попытку на следующих изменениях entries/pending, а не бросать
  // после первого промаха. Ресинк с пропом — на смену scrollTarget (в т.ч.
  // повторный клик по тому же элементу ленты — App.tsx каждый раз создаёт
  // новый объект-литерал) и на смену стадии (иначе цель, оставшаяся от
  // предыдущей стадии, могла бы дождаться случайного совпадения id и
  // проскроллить не туда). Объявлен ДО эффекта автопрыжка к pending ниже —
  // тому эффекту (F5) нужно читать retainedTarget, чтобы уступить прицельной
  // навигации.
  //
  // Адопция гейтится по stageId (F6): scrollTarget может пережить смену
  // рендерящейся стадии (attention auto-advance, повторное открытие того же
  // диалога) — если он предназначен ДРУГОЙ стадии, отбрасываем его вместо
  // того чтобы ждать случайного совпадения phase/id на этой.
  const [retainedTarget, setRetainedTarget] = useState<{ stageId: string; phase: string; id: string } | null>(null)
  useEffect(() => {
    if (scrollTarget !== null && stage !== null && scrollTarget.stageId === stage.id) {
      setRetainedTarget(scrollTarget)
    } else {
      setRetainedTarget(null)
    }
  }, [scrollTarget, stage?.id])

  // R1: pending-эффект ниже читает текущую цель через ref, а не как свою
  // зависимость — иначе очистка retainedTarget эффектом попадания (ниже)
  // сама пересоздаёт pending-эффект, и тот, увидев retainedTarget===null,
  // планирует requestAnimationFrame(jumpToBottom) на пустом месте, стирая
  // только что сделанный прицельный скролл на кадр позже. Присваивание в
  // теле рендера (а не в отдельном эффекте) — стандартный паттерн «читать
  // свежее значение без объявления зависимости»: коммитится к моменту, когда
  // эффекты того же рендера начинают выполняться.
  const retainedTargetRef = useRef(retainedTarget)
  retainedTargetRef.current = retainedTarget

  // onTargetConsumed читаем через ref по той же причине: таймер отказа (R2,
  // ниже) не должен перезапускаться из-за смены идентичности колбэка
  // родителя — только из-за смены самой цели (retainedTarget в его deps).
  const onTargetConsumedRef = useRef(onTargetConsumed)
  onTargetConsumedRef.current = onTargetConsumed

  // R2: если якорь цели никогда не появляется (id не существует в диалоге
  // этой стадии), retainedTarget иначе оставался бы навязан навсегда — это
  // не только держит скролл «застрявшим», но и (вместе с ref выше) подавляет
  // обычный автопрыжок на ЛЮБОМ будущем pending-вопросе, а родитель никогда
  // не получает onTargetConsumed, чтобы сбросить свою собственную цель.
  // Даём цели ограниченное время на самостоятельную попытку найтись — опрос
  // /dialog идёт раз в 2с, окна в TARGET_GIVE_UP_MS хватает на несколько
  // попыток. Таймер отменяется/пересоздаётся при смене retainedTarget —
  // в т.ч. когда эффект попадания (ниже) сам очищает цель при успехе.
  useEffect(() => {
    if (retainedTarget === null) return
    const timeoutId = window.setTimeout(() => {
      setRetainedTarget(null)
      onTargetConsumedRef.current?.()
    }, TARGET_GIVE_UP_MS)
    return () => window.clearTimeout(timeoutId)
  }, [retainedTarget])

  // Ждущий ответа вопрос — всегда в конце истории. Проматываем к нему диалог:
  // и при загрузке страницы (пользователь сразу видит опции ответа), и при
  // появлении нового вопроса от агента. rAF — к следующему кадру, после layout
  // (панель могла только что пересчитать высоту), чтобы scrollHeight был финальным.
  //
  // F5/R1: пока retainedTargetRef указывает НЕ на текущий pending (прицельная
  // навигация из ленты ещё не доехала до своего Q&A), не перебиваем её этим
  // автопрыжком — иначе он срабатывает на каждое изменение pending.id и
  // перетягивает скролл обратно вниз, отменяя переход к более старому
  // отвеченному вопросу. retainedTarget НЕ в зависимостях эффекта (R1) —
  // только его ref: иначе очистка цели эффектом попадания (ниже) сама
  // пересоздаёт этот эффект и планирует лишний прыжок в конец кадром позже.
  // Once retainedTarget указывает на pending (или снят) — обычное поведение
  // продолжает работать как раньше, потому что pending?.id меняется на
  // следующий реальный pending-вопрос и заново триггерит этот эффект.
  useEffect(() => {
    if (pending === null) return
    const target = retainedTargetRef.current
    if (target !== null && !(target.phase === pending.phase && target.id === pending.id)) return
    const handle = requestAnimationFrame(() => jumpToBottom())
    return () => cancelAnimationFrame(handle)
  }, [questionKey(pending), jumpToBottom])

  // One-shot glow рамки диалога при появлении нового pending-вопроса (B3):
  // класс dialog-flash навешивается на смену pending.id и снимается через 2.5s
  // (совпадает с длительностью CSS-анимации .dialog-flash, чтобы глоу успевал доиграть).
  useEffect(() => {
    if (pending === null) return
    setFlash(true)
    const t = window.setTimeout(() => setFlash(false), 2500)
    return () => window.clearTimeout(t)
  }, [questionKey(pending)])

  // При разворачивании панели на весь экран (меняется высота контейнера) канал
  // должен показать хвост диалога, а не то место, на котором застал скролл в компактном
  // режиме. rAF — после layout оверлея, чтобы scrollHeight уже отражал итоговую
  // (полноэкранную) высоту.
  useEffect(() => {
    if (!maximized) return
    const handle = requestAnimationFrame(() => jumpToBottom())
    return () => cancelAnimationFrame(handle)
  }, [maximized, jumpToBottom])

  useEffect(() => {
    if (retainedTarget === null) return

    // F4: id уникален только В ПРЕДЕЛАХ фазы — сверяем И phase, И id, иначе
    // клик по отвеченному planning/q1 при live-ожидающем implementation/q1
    // ошибочно уводил бы к pending-вопросу вместо истории planning/q1.
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
      // Отпускаем «прилипание» к низу ПЕРЕД скроллом: цель — исторический Q&A
      // выше pending-вопроса, а stick-to-bottom (MutationObserver) на ближайшей
      // мутации вернул бы scrollTop к низу и отменил бы переход. release() ставит
      // stick=false синхронно, поэтому наблюдатель больше не докручивает вниз, и
      // пользователь остаётся на выбранном вопросе/ответе.
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
    // stageId — стадия, ДЛЯ КОТОРОЙ вызван этот reload (замыкание на stage из
    // рендера, где был нажат SEND). Если к моменту резолва fetch панель уже
    // переключилась на другую стадию (currentStageIdRef изменился — обновляется
    // только эффектом опроса выше), этот reload устарел вместе со своей
    // стадией: бампать ОБЩИЙ requestGenRef и применять ответ стадии A поверх
    // уже смонтированной панели стадии B — баг (codex MAJOR). Проверяем и до
    // await (не тратим запрос впустую), и после (стадия могла смениться, пока
    // fetch летел).
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

  // Как только у вопроса есть хотя бы один комментарий, ответ пользователя —
  // это сами комментарии (killer feature #2): опции и свободный ответ прячутся,
  // единственное действие — отправить собранный feedback тем же эндпоинтом
  // /dialog/answer, что и обычный ответ (from_options всегда false — это не
  // выбор из options).
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

  // renderPending — тело pending-вопроса, вызывается из render-prop
  // LineCommentedDocument: doc.body — заякоренный вопрос (с формами построчных
  // комментариев внутри), doc.commentCount переключает обычный UI ответа
  // (опции + свободный текст + SEND) на «Send feedback», doc.activeCommentLine
  // гейтит SEND/Send feedback — форма комментария ОТКРЫТА (add или edit),
  // независимо от того, есть ли уже в ней текст (клик по строке вопроса, затем
  // сразу SEND/Send feedback, иначе стирал бы начатый комментарий).
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
                // Опция вида "Заголовок — описание" разбивается на жирный
                // заголовок + описание (как в макете-карточке). Без
                // разделителя — вся строка как заголовок.
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
              // M3: скрепка на основном поле ответа — прикрепить ссылку на
              // файл проекта (Docker file browser) + вставленные скриншоты
              // уже работают через use-image-paste. Гейтится capability
              // внутри PasteableTextarea (showAttachButton), так что на
              // host-прогоне без file browser кнопка не рендерится.
              allowFileReferences
              onSubmit={() => {
                // codex MEDIUM: Ctrl/Cmd+Enter is a keyboard-submit path that a
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
