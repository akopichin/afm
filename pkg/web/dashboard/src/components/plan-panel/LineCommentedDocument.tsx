import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from 'react'
import { PasteableTextarea } from '../pasteable-textarea'
import { renderAnchoredSections, type Block, type Section } from './markdown'
import { useLineComments } from './use-line-comments'

// API, который владелец отдаёт потребителю через render-prop. Панель действий
// (Approve/Send revision / Send feedback) и commentCount читают state ОТСЮДА
// напрямую — зеркала state в родителе нет, поэтому смена documentIdentity (key на
// родителе) размонтирует владельца и обнулит всё состояние АТОМАРНО.
export type LineCommentDocumentApi = {
  // body — готовый к рендеру markdown-контейнер (со всеми блоками, формами и
  // индикаторами внутри). Потребитель кладёт его в свой лейаут где нужно.
  body: ReactNode
  comments: Record<number, string>
  commentCount: number
  activeCommentLine: number | null
  draft: string
  // hasOpenDraft — открыта форма с непустым черновиком (кнопки действий гейтятся).
  hasOpenDraft: boolean
  clearComments: () => void
}

type LineCommentedDocumentProps = {
  text: string
  specialSections: boolean
  stageId: string
  // beforeOpen — синхронный колбэк ПЕРЕД открытием формы (Dialog передаёт
  // feed.release, чтобы стик-к-низу не утащил канал после автоскролла к форме;
  // Plan — noop/не передаёт).
  beforeOpen?: () => void
  allowFileReferences?: boolean
  bodyId?: string
  bodyClassName?: string
  children: (doc: LineCommentDocumentApi) => ReactNode
}

// LineCommentedDocument — keyed-владелец ОДНОЙ версии документа. Родитель монтирует
// его как `<LineCommentedDocument key={documentIdentity} …>`; смена identity →
// remount → атомарный сброс всего state (comments/draft/active/collapsed/roving).
export function LineCommentedDocument({
  text,
  specialSections,
  stageId,
  beforeOpen,
  allowFileReferences = true,
  bodyId,
  bodyClassName,
  children,
}: LineCommentedDocumentProps): ReactElement {
  const [comments, setComments] = useState<Record<number, string>>({})
  const [activeCommentLine, setActiveCommentLine] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const [rovingLine, setRovingLine] = useState<number | null>(null)
  const [collapsedSections, setCollapsedSections] = useState<Set<number>>(new Set())

  const sections = useMemo(() => renderAnchoredSections(text, { specialSections }), [text, specialSections])

  const commentCount = Object.keys(comments).length
  const hasOpenDraft = activeCommentLine !== null && draft.trim() !== ''

  // Свежие значения для onActivate (вызывается из делегированного слушателя хука
  // через ref — без пересборки слушателей).
  const activeRef = useRef(activeCommentLine)
  activeRef.current = activeCommentLine
  const draftRef = useRef(draft)
  draftRef.current = draft
  const commentsRef = useRef(comments)
  commentsRef.current = comments

  const openComment = useCallback(
    (line: number) => {
      // ПЕРВЫМ синхронный beforeOpen (release стика), затем атомарно active+roving.
      beforeOpen?.()
      setActiveCommentLine(line)
      setRovingLine(line)
      setDraft(commentsRef.current[line] ?? '')
    },
    [beforeOpen],
  )

  // onActivate — реакция на клик/Enter по якорю: непустой черновик игнорирует любые
  // клики; повторный клик по активной строке (с пустым черновиком) закрывает форму;
  // иначе открываем/переключаемся.
  const onActivate = useCallback(
    (line: number) => {
      if (activeRef.current !== null && draftRef.current.trim() !== '') return
      if (activeRef.current === line) {
        setActiveCommentLine(null)
        setDraft('')
        return
      }
      openComment(line)
    },
    [openComment],
  )

  const { containerRef, focusAnchor } = useLineComments({
    comments,
    activeCommentLine,
    rovingLine,
    setRovingLine,
    onActivate,
    contentSignal: sections,
    visibilitySignal: collapsedSections,
  })

  const closeForm = useCallback(
    (line: number) => {
      setActiveCommentLine(null)
      setDraft('')
      focusAnchor(line)
    },
    [focusAnchor],
  )

  const saveComment = useCallback(
    (line: number) => {
      const text = draftRef.current.trim()
      setComments((prev) => {
        const next = { ...prev }
        if (text === '') delete next[line]
        else next[line] = text
        return next
      })
      setActiveCommentLine(null)
      setDraft('')
      focusAnchor(line)
    },
    [focusAnchor],
  )

  const deleteComment = useCallback(
    (line: number) => {
      setComments((prev) => {
        const next = { ...prev }
        delete next[line]
        return next
      })
      setActiveCommentLine(null)
      setDraft('')
      focusAnchor(line)
    },
    [focusAnchor],
  )

  const clearComments = useCallback(() => {
    setComments({})
    setActiveCommentLine(null)
    setDraft('')
  }, [])

  // Свёрнута ли секция, содержащая активный непустой черновик — collapse такой
  // секции запрещён (черновик потерялся бы визуально из виду).
  const draftSectionHasOpenDraft = useCallback(
    (section: Section): boolean => {
      if (activeCommentLine === null || draft.trim() === '') return false
      return section.blocks.some((b) => activeCommentLine >= b.startLine && activeCommentLine < b.endLineExclusive)
    },
    [activeCommentLine, draft],
  )

  const toggleSection = useCallback(
    (index: number, section: Section, headerEl: HTMLButtonElement | null) => {
      setCollapsedSections((prev) => {
        const collapsing = !prev.has(index)
        // Collapse с непустым черновиком в этой секции запрещён — оставляем
        // раскрытой, фокус на заголовок (draft сохраняется).
        if (collapsing && draftSectionHasOpenDraft(section)) {
          headerEl?.focus()
          return prev
        }
        const next = new Set(prev)
        if (next.has(index)) next.delete(index)
        else next.add(index)
        return next
      })
    },
    [draftSectionHasOpenDraft],
  )

  const commentedLinesIn = useCallback(
    (block: Block): number[] =>
      Object.keys(comments)
        .map(Number)
        .filter((line) => line >= block.startLine && line < block.endLineExclusive)
        .sort((a, b) => a - b),
    [comments],
  )

  const renderBlock = (block: Block): ReactNode => {
    const displays = commentedLinesIn(block)
    const showForm =
      activeCommentLine !== null &&
      activeCommentLine >= block.startLine &&
      activeCommentLine < block.endLineExclusive

    return (
      <Fragment key={`block-${block.startLine}`}>
        <div className="md" dangerouslySetInnerHTML={{ __html: block.html }} />
        {displays.map((line) => (
          <LineCommentDisplay key={`display-${line}`} line={line} text={comments[line] ?? ''} onRemove={() => deleteComment(line)} />
        ))}
        {showForm && activeCommentLine !== null && (
          <LineCommentForm
            key={`form-${activeCommentLine}`}
            line={activeCommentLine}
            stageId={stageId}
            allowFileReferences={allowFileReferences}
            hasComment={comments[activeCommentLine] !== undefined}
            draft={draft}
            onDraftChange={setDraft}
            onSave={() => saveComment(activeCommentLine)}
            onDelete={() => deleteComment(activeCommentLine)}
            onClose={() => closeForm(activeCommentLine)}
          />
        )}
      </Fragment>
    )
  }

  const body: ReactNode = (
    <div id={bodyId} className={bodyClassName} ref={containerRef}>
      {sections.map((section, index) => {
        if (section.kind === 'special' && section.special !== undefined) {
          const collapsed = collapsedSections.has(index)
          const special = section.special
          return (
            <SpecialSectionWrapper
              key={`section-${index}`}
              css={special.css}
              icon={special.icon}
              label={special.label}
              collapsed={collapsed}
              onToggle={(headerEl) => toggleSection(index, section, headerEl)}
            >
              {section.blocks.map(renderBlock)}
            </SpecialSectionWrapper>
          )
        }
        return <Fragment key={`section-${index}`}>{section.blocks.map(renderBlock)}</Fragment>
      })}
    </div>
  )

  return <>{children({ body, comments, commentCount, activeCommentLine, draft, hasOpenDraft, clearComments })}</>
}

function SpecialSectionWrapper({
  css,
  icon,
  label,
  collapsed,
  onToggle,
  children,
}: {
  css: string
  icon: string
  label: string
  collapsed: boolean
  onToggle: (headerEl: HTMLButtonElement | null) => void
  children: ReactNode
}): ReactElement {
  const headerRef = useRef<HTMLButtonElement | null>(null)
  return (
    <div className={`plan-section-wrapper ${css}${collapsed ? ' collapsed' : ''}`}>
      <button
        ref={headerRef}
        type="button"
        className="section-header"
        aria-expanded={!collapsed}
        onClick={() => onToggle(headerRef.current)}
      >
        {icon} {label} <span className="toggle" aria-hidden="true">▾</span>
      </button>
      <div className="plan-section-body" hidden={collapsed}>
        {children}
      </div>
    </div>
  )
}

function LineCommentDisplay({
  line,
  text,
  onRemove,
}: {
  line: number
  text: string
  onRemove: () => void
}): ReactElement {
  return (
    <div className="line-comment-form line-comment-display" data-comment-ui="" data-comment-line={line}>
      <div className="comment-display-header">
        <span className="comment-title">Comment on line {line}</span>
        <button type="button" className="comment-remove" aria-label={`Remove comment on line ${line}`} title="Remove comment" onClick={onRemove}>
          ✕
        </button>
      </div>
      <div style={{ color: 'var(--text)', whiteSpace: 'pre-wrap' }}>{text}</div>
    </div>
  )
}

function LineCommentForm({
  line,
  stageId,
  allowFileReferences,
  hasComment,
  draft,
  onDraftChange,
  onSave,
  onDelete,
  onClose,
}: {
  line: number
  stageId: string
  allowFileReferences: boolean
  hasComment: boolean
  draft: string
  onDraftChange: (value: string) => void
  onSave: () => void
  onDelete: () => void
  onClose: () => void
}): ReactElement {
  const wrapRef = useRef<HTMLDivElement | null>(null)

  // Автоскролл к форме при открытии (block:'nearest') — форма контейнера (списка/
  // таблицы) появляется ПОД всем блоком, поэтому её надо подтянуть в вид.
  useEffect(() => {
    const el = wrapRef.current
    if (el !== null && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ block: 'nearest' })
    }
  }, [])

  return (
    <div className="line-comment-form" data-comment-ui="" data-comment-line={line} ref={wrapRef}>
      <div className="comment-display-header">
        <span className="comment-title">Comment on line {line}</span>
        <button type="button" className="comment-remove" aria-label={`Close comment on line ${line}`} title="Close" onClick={onClose}>
          ✕
        </button>
      </div>
      <PasteableTextarea
        stageId={stageId}
        placeholder={`Comment on line ${line}...`}
        value={draft}
        onChange={onDraftChange}
        autoFocus
        allowFileReferences={allowFileReferences}
        onSubmit={onSave}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.preventDefault()
            onClose()
          }
        }}
      />
      <div className="comment-actions">
        <button className="btn btn-send" type="button" onClick={onSave}>
          {hasComment ? 'Update' : 'Add'}
        </button>
        {hasComment && (
          <button className="btn btn-cancel" type="button" onClick={onDelete}>
            Delete
          </button>
        )}
      </div>
    </div>
  )
}
