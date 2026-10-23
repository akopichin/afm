import { Fragment, useMemo, useRef, useState, type ReactElement, type ReactNode } from 'react'
import { copyToClipboard } from '../../lib/clipboard'
import { CommentCard } from '../comments/CommentCard'
import { CommentEditor } from '../comments/CommentEditor'
import { commentKey, isDraft, type CommentAnchor, type CommentEntry } from '../comments/comment-state'
import { useComments } from '../comments/CommentProvider'
import { renderAnchoredSections, type Block, type Section } from './markdown'
import { useLineComments } from './use-line-comments'

// Горизонтальное правило (---, ***, ___, с любыми пробелами между символами и по краям) — как и пустая
// строка, не показывается как quote-превью: цитировать разделитель бессмысленно.
const HR_LINE_RE = /^\s*([-*_])(\s*\1){2,}\s*$/

// quotedSourceLine — чистая функция вычисления quote-превью для формы комментария. Пустая/hr-строка → ''
// (нет quote).
export function quotedSourceLine(sourceLines: string[], line: number): string {
  const raw = sourceLines[line - 1] ?? ''
  const trimmed = raw.trim()
  if (trimmed === '' || HR_LINE_RE.test(trimmed)) return ''
  return trimmed
}

// DocumentScope — какую версию документа показывает компонент: комментарии другой версии не прикрепляются.
export type DocumentScope =
  | { kind: 'plan'; version: string }
  | { kind: 'question'; phase: string; questionId: string }

function anchorFor(stageId: string, scope: DocumentScope, line: number): CommentAnchor {
  if (scope.kind === 'plan') return { kind: 'plan', stageId, document: 'plan', version: scope.version, line }
  return { kind: 'question', stageId, phase: scope.phase, questionId: scope.questionId, line }
}

function inDocument(anchor: CommentAnchor, stageId: string, scope: DocumentScope): boolean {
  if (anchor.kind === 'plan' && scope.kind === 'plan') return anchor.stageId === stageId && anchor.version === scope.version
  if (anchor.kind === 'question' && scope.kind === 'question') {
    return anchor.stageId === stageId && anchor.phase === scope.phase && anchor.questionId === scope.questionId
  }
  return false
}

// staleComments — записи этой стадии от прошлой версии плана / закрытого вопроса (current === null: открытого
// вопроса нет, устарели все).
export function staleComments(
  all: readonly CommentEntry[],
  stageId: string,
  kind: DocumentScope['kind'],
  current: DocumentScope | null,
): CommentEntry[] {
  return all.filter(({ anchor }) => {
    if (anchor.kind === 'file' || anchor.kind !== kind || anchor.stageId !== stageId) return false
    return current === null || !inDocument(anchor, stageId, current)
  })
}

// DocumentComment — непустой комментарий к строке документа; key/revision нужны для подтверждения отправки.
export type DocumentComment = { line: number; key: string; revision: number; text: string }

// API, который владелец отдаёт потребителю через render-prop.
export type LineCommentDocumentApi = {
  // body — готовый к рендеру markdown-контейнер (со всеми блоками, формами и
  // индикаторами внутри). Потребитель кладёт его в свой лейаут где нужно.
  body: ReactNode
  // comments — все непустые комментарии (и сохранённые, и черновики), по возрастанию строки.
  comments: DocumentComment[]
  commentCount: number
  activeCommentLine: number | null
  // hasOpenDraft — открыта форма с НЕПУСТЫМ черновиком.
  hasOpenDraft: boolean
}

type LineCommentedDocumentProps = {
  text: string
  specialSections: boolean
  stageId: string
  scope: DocumentScope
  // beforeOpen — синхронный колбэк ПЕРЕД открытием формы (Dialog передаёт feed.release, чтобы стик-к-низу
  // не утащил канал после автоскролла к форме; Plan — noop/не передаёт).
  beforeOpen?: () => void
  allowFileReferences?: boolean
  bodyId?: string
  bodyClassName?: string
  children: (doc: LineCommentDocumentApi) => ReactNode
}

// LineCommentedDocument — одна версия документа; тексты комментариев живут в run-scoped CommentProvider,
// поэтому переживают размонтирование (смена вкладки/стадии).
export function LineCommentedDocument({
  text,
  specialSections,
  stageId,
  scope,
  beforeOpen,
  allowFileReferences = true,
  bodyId,
  bodyClassName,
  children,
}: LineCommentedDocumentProps): ReactElement {
  const { comments: all, open, edit, fold, saveAndFold, discard } = useComments()
  const [rovingLine, setRovingLine] = useState<number | null>(null)
  const [collapsedSections, setCollapsedSections] = useState<Set<number>>(new Set())

  const sections = useMemo(() => renderAnchoredSections(text, { specialSections }), [text, specialSections])
  const sourceLines = useMemo(() => text.split('\n'), [text])

  // entries — записи этого документа по строкам; scope читаем по его содержимому, а не по идентичности.
  const scopeId = JSON.stringify(scope)
  const entries = useMemo(() => {
    const byLine = new Map<number, CommentEntry>()
    for (const entry of all) {
      const { anchor } = entry
      if (anchor.kind !== 'file' && anchor.line !== null && inDocument(anchor, stageId, scope)) byLine.set(anchor.line, entry)
    }
    return byLine
  }, [all, stageId, scopeId])

  const documentComments = useMemo(
    () =>
      Array.from(entries, ([line, entry]) => ({ line, key: entry.key, revision: entry.revision, text: entry.text.trim() }))
        .filter((comment) => comment.text !== '')
        .sort((a, b) => a.line - b.line),
    [entries],
  )
  const commentedLines = useMemo(
    () => Object.fromEntries(documentComments.map((comment) => [comment.line, comment.text])),
    [documentComments],
  )

  const openEntry = Array.from(entries).find(([, entry]) => entry.editorOpen)
  const activeCommentLine = openEntry?.[0] ?? null
  const hasOpenDraft = openEntry !== undefined && openEntry[1].text.trim() !== ''

  function openLine(line: number): void {
    // ПЕРВЫМ синхронный beforeOpen (release стика), затем открытие формы.
    beforeOpen?.()
    setRovingLine(line)
    const anchor = anchorFor(stageId, scope, line)
    open(commentKey(anchor), { anchor, quote: quotedSourceLine(sourceLines, line) })
  }

  // Крестик/Escape прячет форму, но текст остаётся; пустая форма (или стёртый текст) исчезает совсем.
  function foldLine(line: number, entry: CommentEntry): void {
    fold(entry.key)
    focusAnchor(line)
  }

  // onActivate — реакция на клик/Enter по якорю: форма с непустым текстом игнорирует клики по строкам;
  // повторный клик по активной строке (с пустым текстом) закрывает форму; иначе открываем/переключаемся.
  function onActivate(line: number): void {
    if (hasOpenDraft) return
    if (openEntry !== undefined) {
      foldLine(openEntry[0], openEntry[1])
      if (openEntry[0] === line) return
    }
    openLine(line)
  }

  // Edit на карточке: другая открытая форма сворачивается (текст остаётся карточкой), поэтому клик не теряется.
  function editLine(line: number): void {
    if (openEntry !== undefined && openEntry[0] !== line) foldLine(openEntry[0], openEntry[1])
    openLine(line)
  }

  const { containerRef, focusAnchor } = useLineComments({
    comments: commentedLines,
    activeCommentLine,
    rovingLine,
    setRovingLine,
    onActivate,
    contentSignal: sections,
    visibilitySignal: collapsedSections,
  })

  function saveLine(line: number, entry: CommentEntry): void {
    saveAndFold(entry.key)
    focusAnchor(line)
  }

  function deleteLine(line: number, entry: CommentEntry): void {
    discard(entry.key)
    focusAnchor(line)
  }

  // Collapse секции с активным непустым черновиком запрещён — он пропал бы из виду.
  function sectionHasOpenDraft(section: Section): boolean {
    if (!hasOpenDraft || activeCommentLine === null) return false
    return section.blocks.some((b) => activeCommentLine >= b.startLine && activeCommentLine < b.endLineExclusive)
  }

  function toggleSection(index: number, section: Section, headerEl: HTMLButtonElement | null): void {
    setCollapsedSections((prev) => {
      const collapsing = !prev.has(index)
      // Фокус на заголовок, секция остаётся раскрытой, черновик на месте.
      if (collapsing && sectionHasOpenDraft(section)) {
        headerEl?.focus()
        return prev
      }
      const next = new Set(prev)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }

  const renderBlock = (block: Block): ReactNode => {
    const blockEntries = Array.from(entries)
      .filter(([line]) => line >= block.startLine && line < block.endLineExclusive)
      .sort(([a], [b]) => a - b)

    return (
      <Fragment key={`block-${block.startLine}`}>
        <div className="md" dangerouslySetInnerHTML={{ __html: block.html }} />
        {blockEntries.map(([line, entry]) => {
          // Редактируемая запись показывается формой (цитата не задваивается карточкой).
          if (entry.editorOpen) {
            return (
              <CommentEditor
                key={`form-${line}`}
                context={scope.kind}
                stageId={stageId}
                allowAttachments={allowFileReferences}
                title={`Comment on line ${line}`}
                line={line}
                quote={entry.quote}
                placeholder={`Comment on line ${line}...`}
                value={entry.text}
                onChange={(value) => edit(entry.key, value)}
                onSave={() => saveLine(line, entry)}
                onFold={() => foldLine(line, entry)}
                onDiscard={entry.saved !== '' ? () => deleteLine(line, entry) : undefined}
                saveLabel={entry.saved !== '' ? 'Update' : 'Add'}
                discardLabel="Delete"
              />
            )
          }
          if (entry.text.trim() === '') return null
          return (
            <CommentCard
              key={`display-${line}`}
              title={`Comment on line ${line}`}
              line={line}
              quote={entry.quote}
              text={entry.text}
              draft={isDraft(entry)}
              onEdit={() => editLine(line)}
              onRemove={() => deleteLine(line, entry)}
            />
          )
        })}
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

  return (
    <>
      {children({ body, comments: documentComments, commentCount: documentComments.length, activeCommentLine, hasOpenDraft })}
    </>
  )
}

// StaleComments — комментарии от прошлой версии плана / закрытого вопроса: их можно посмотреть, скопировать
// или убрать, но к новой версии они не прикрепляются. С unsentReason (документ не загрузился) устарелость
// неизвестна: комментарии подписываются как неотправленные, а удаление называется Discard.
export function StaleComments({
  entries,
  source,
  unsentReason,
}: {
  entries: readonly CommentEntry[]
  source: string
  unsentReason?: string
}): ReactElement | null {
  const { discard } = useComments()
  const [shown, setShown] = useState(false)
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const stale = entries.filter((entry) => entry.text.trim() !== '')
  if (stale.length === 0) return null

  const noun = stale.length === 1 ? 'comment' : 'comments'
  const caption = unsentReason === undefined ? `${stale.length} ${noun} from ${source}` : `${stale.length} unsent ${noun} - ${unsentReason}`
  const lineOf = (entry: CommentEntry): number | null => (entry.anchor.kind === 'file' ? null : entry.anchor.line)
  const copyAll = async (): Promise<void> => {
    const lines = stale.map((entry) => (lineOf(entry) === null ? entry.text : `Line ${lineOf(entry)}: ${entry.text}`))
    setCopyState((await copyToClipboard(lines.join('\n\n'))) ? 'copied' : 'failed')
  }

  return (
    <div className="stale-comments" data-comment-ui="">
      <div className="comment-caption">
        <span>{caption}</span>
        {' · '}
        <button type="button" className="btn btn-edit" onClick={() => setShown(!shown)}>
          {shown ? 'Hide' : 'Show'}
        </button>
        <button type="button" className="btn btn-edit" onClick={() => void copyAll()}>
          {copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy'}
        </button>
        <button type="button" className="btn btn-cancel" onClick={() => stale.forEach((entry) => discard(entry.key))}>
          {unsentReason === undefined ? 'Dismiss' : 'Discard'}
        </button>
      </div>
      {shown &&
        stale.map((entry) => (
          <CommentCard
            key={entry.key}
            title={lineOf(entry) === null ? 'Answer draft' : `Comment on line ${lineOf(entry)}`}
            quote={entry.quote}
            text={entry.text}
            onRemove={() => discard(entry.key)}
          />
        ))}
    </div>
  )
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
