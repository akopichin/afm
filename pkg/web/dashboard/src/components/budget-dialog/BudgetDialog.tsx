import { useCallback, useEffect, useId, useRef, useState, type ReactElement, type RefObject } from 'react'
import {
  BUDGET_ERROR_STILL_LIMITED,
  BudgetApiError,
  fetchBudgetView,
  resumeBudget,
  updateBudget,
  type BudgetViewResponse,
} from '../../api/budget-client'
import { useModalDialog } from '../../lib/focus-trap'
import {
  POLICY_DESCRIPTIONS,
  POLICY_TITLES,
  sourceText,
  blockingParent,
  budgetFigures,
  holdText,
  limitText,
  remainingText,
  scopeKindWord,
  scopeLabel,
  spendFigures,
  stateText,
} from '../../lib/budget-format'
import type { BudgetOverride, BudgetPolicy, BudgetView } from '../../types/budget'
import { useBudgetUi } from './budget-context'
import { draftFromView, isDraftClean, resolveDraft, sameSpec, type BudgetDraft, type DraftMode } from './budget-draft'

export type BudgetDialogProps = {
  scope: string
  // opener — элемент, открывший окно: на закрытии он получает фокус, пока он в документе.
  opener: HTMLElement | null
  onClose: () => void
  // onView — свежий view из любого ответа: списки и индикаторы обновляются без ожидания следующего опроса.
  onView: (view: BudgetView) => void
  // onChanged — что-то изменилось на сервере (сохранение, продолжение): App перечитывает статус.
  onChanged: () => void
}

type Load = { phase: 'loading' } | { phase: 'error'; message: string } | { phase: 'ready'; response: BudgetViewResponse }

// Итог последней операции: что показать над кнопками.
type Outcome =
  | { kind: 'conflict' }
  | { kind: 'still_limited'; message: string }
  | { kind: 'resume_failed'; message: string }
  | { kind: 'export_failed'; file: string; reason: string }
  | { kind: 'export_unavailable'; file: string; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'closed'; message: string }

const POLICIES: BudgetPolicy[] = ['hard', 'medium', 'info']

// BudgetDialog — одно окно бюджета: сводка области, выбор лимита, Save / Save and continue. Показывает решения engine
// и ничего не пересчитывает: «ещё ограничено» приходит ответом, а не вычисляется здесь.
export function BudgetDialog({ scope, opener, onClose, onView, onChanged }: BudgetDialogProps): ReactElement {
  const ui = useBudgetUi()
  const modalRef = useRef<HTMLDivElement | null>(null)
  const firstRadioRef = useRef<HTMLInputElement | null>(null)
  const ids = useId()
  const [load, setLoad] = useState<Load>({ phase: 'loading' })
  const [draft, setDraft] = useState<BudgetDraft | null>(null)
  const [saveToYaml, setSaveToYaml] = useState(false)
  const [busy, setBusy] = useState(false)
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const [saved, setSaved] = useState(false)
  const outcomeRef = useRef<HTMLParagraphElement>(null)
  // alive — диалог ещё на экране: ответ запроса, пришедший после закрытия, не должен закрывать уже другое окно.
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  useModalDialog(modalRef, onClose, () => opener)

  const view = load.phase === 'ready' ? load.response.view : null
  const yaml = load.phase === 'ready' ? load.response.yaml : null
  const label = scopeLabel(scope, ui.names, view?.kind ?? ui.views.get(scope)?.kind)

  // Окно открывается по актуальному view области, а не по значениям notice, из которого его открыли.
  useEffect(() => {
    let live = true
    fetchBudgetView(scope).then(
      (response) => {
        if (!live) return
        setLoad({ phase: 'ready', response })
        setDraft(draftFromView(response.view))
        onView(response.view)
      },
      (err: unknown) => {
        if (live) setLoad({ phase: 'error', message: err instanceof Error ? err.message : 'Could not load the budget' })
      },
    )
    return () => {
      live = false
    }
    // Только scope: повторная загрузка при смене onView стёрла бы черновик.
  }, [scope])

  // Когда view пришёл, фокус переходит с кнопки Cancel на выбранную радиокнопку: с неё начинается правка.
  const loadedOnce = load.phase === 'ready'
  useEffect(() => {
    if (loadedOnce) firstRadioRef.current?.focus()
  }, [loadedOnce])

  // Кнопка, на которой стоял фокус, исчезает после ответа: итог операции принимает фокус, чтобы Tab остался в окне.
  useEffect(() => {
    if (outcome !== null) outcomeRef.current?.focus()
  }, [outcome])

  // adopt принимает свежий view из ответа: ревизия, от которой считаются следующие запросы, берётся только отсюда.
  const adopt = useCallback(
    (next: BudgetView, nextYaml: BudgetViewResponse['yaml'] = yaml) => {
      setLoad({ phase: 'ready', response: { view: next, yaml: nextYaml } })
      onView(next)
    },
    [onView, yaml],
  )

  if (view === null || draft === null) {
    return (
      <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={`Budget · ${label}`}>
        <div className="modal-content budget-dialog" ref={modalRef}>
          <h2 className="budget-dialog-title">Budget · {label}</h2>
          {load.phase === 'error' ? (
            <p className="budget-dialog-message budget-dialog-message--danger" role="alert">{load.message}</p>
          ) : (
            <p className="budget-dialog-muted" role="status">Loading budget…</p>
          )}
          <div className="modal-actions">
            <button type="button" className="btn btn-cancel" onClick={onClose}>Cancel</button>
          </div>
        </div>
      </div>
    )
  }

  const current = view
  const { override, errors } = resolveDraft(draft)
  const clean = isDraftClean(draft, current)
  const canUpdate = current.actions.includes('update')
  const canResume = current.actions.includes('resume')
  const closed = outcome?.kind === 'closed'
  const blocker = blockingParent(current)
  const file = yaml?.file !== undefined && yaml.file !== '' ? yaml.file : 'flow.yaml'
  const yamlReason = yamlDisabledReason()
  const exportable = yamlReason === null
  const invalid = override === null
  const idle = !busy && !closed
  // Экспорт не зависит от правки: существующий runtime-лимит можно записать в файл без нового Save.
  const exportExisting = clean && exportable && saveToYaml

  function yamlDisabledReason(): string | null {
    if (yaml === null || !current.actions.includes('save_to_yaml')) return `This budget is not declared in a file that can be edited from here.`
    if (!yaml.available) return yaml.reason !== '' ? yaml.reason : `${file} cannot be edited from here.`
    if (draft?.mode !== 'custom') return `Only a custom limit can be saved to ${file}.`
    return null
  }

  function patchDraft(patch: Partial<BudgetDraft>): void {
    setDraft((d) => (d === null ? d : { ...d, ...patch }))
  }

  function chooseMode(mode: DraftMode): void {
    patchDraft({ mode })
  }

  function failure(err: unknown): void {
    if (err instanceof BudgetApiError) {
      if (err.code === 'budget_revision_conflict') {
        // Конфликт: показываем текущие значения, черновик остаётся — нужен повторный Save.
        setOutcome({ kind: 'conflict' })
        fetchBudgetView(scope).then(
          (response) => adopt(response.view, response.yaml),
          () => undefined,
        )
        return
      }
      if (err.code === 'budget_closed' || err.status === 503) {
        setOutcome({ kind: 'closed', message: 'The run is closed or unavailable, so the budget can no longer be changed.' })
        return
      }
      if (err.code === BUDGET_ERROR_STILL_LIMITED) {
        setOutcome({ kind: 'still_limited', message: err.message })
        return
      }
      setOutcome({ kind: 'error', message: err.message })
      return
    }
    setOutcome({ kind: 'error', message: err instanceof Error ? err.message : 'Request failed' })
  }

  function closeIfOpen(): void {
    if (alive.current) onClose()
  }

  // refreshYaml перечитывает доступность экспорта после неудачной записи: файл мог измениться.
  function refreshYaml(): void {
    fetchBudgetView(scope).then(
      (response) => adopt(response.view, response.yaml),
      () => undefined,
    )
  }

  // save — одно сохранение override. continueWork и exportToYaml задают, что ещё попросить у сервера в том же запросе.
  function save(next: BudgetOverride, options: { continueWork: boolean; exportToYaml: boolean }): void {
    setBusy(true)
    setOutcome(null)
    updateBudget({ scope, expectedRevision: current.revision, override: next, saveToYaml: options.exportToYaml, resume: options.continueWork }).then(
      (result) => {
        setBusy(false)
        setSaved(true)
        adopt(result.view)
        setDraft(draftFromView(result.view))
        onChanged()
        if (result.export !== null && result.export.status !== 'saved') {
          const exportFile = result.export.path !== '' ? result.export.path : file
          setSaveToYaml(false)
          refreshYaml()
          if (result.export.status === 'failed') {
            setOutcome({ kind: 'export_failed', file: exportFile, reason: result.export.error })
          } else {
            setOutcome({ kind: 'export_unavailable', file: exportFile, reason: result.export.error })
          }
          return
        }
        if (result.resumeError !== null) {
          const stillLimited = result.resumeError.code === BUDGET_ERROR_STILL_LIMITED
          setOutcome({ kind: stillLimited ? 'still_limited' : 'resume_failed', message: result.resumeError.message })
          return
        }
        closeIfOpen()
      },
      (err: unknown) => {
        setBusy(false)
        failure(err)
      },
    )
  }

  function continueOnly(): void {
    setBusy(true)
    setOutcome(null)
    resumeBudget(scope, current.revision).then(
      (next) => {
        setBusy(false)
        adopt(next)
        onChanged()
        if (next.hold === null) closeIfOpen()
        else setOutcome({ kind: 'still_limited', message: holdText(next, ui.names) })
      },
      (err: unknown) => {
        setBusy(false)
        failure(err)
      },
    )
  }

  function retryExport(): void {
    if (current.override.kind !== 'limit') return
    save(current.override, { continueWork: false, exportToYaml: true })
  }

  const declaredDiffers = yaml?.declared !== undefined && yaml.declared !== null && !sameSpec(yaml.declared, current.base)

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-labelledby={`${ids}-title`}>
      <div className="modal-content budget-dialog" ref={modalRef}>
        <h2 id={`${ids}-title`} className="budget-dialog-title">Budget · {label}</h2>

        <dl className="budget-summary">
          <div><dt>Spent</dt><dd>{spendFigures(current.spent)}</dd></div>
          <div>
            <dt>Limit</dt>
            <dd>{current.effective === null ? 'No limit' : `${limitText(current.effective)} · ${POLICY_TITLES[current.effective.stopPolicy]}`}</dd>
          </div>
          {current.effective !== null && <div><dt>Remaining</dt><dd>{remainingText(current.effective, current.spent)}</dd></div>}
          <div><dt>State</dt><dd>{stateText(current)}</dd></div>
        </dl>
        {current.hold !== null && <p className="budget-dialog-message budget-dialog-message--danger">{holdText(current, ui.names)}</p>}

        <fieldset className="budget-field-group" disabled={!canUpdate || closed}>
          <legend>Limit for this {scopeKindWord(current.kind)}</legend>
          <label className="budget-radio">
            <input ref={draft.mode === 'declared' ? firstRadioRef : undefined} type="radio" name={`${ids}-mode`} checked={draft.mode === 'declared'} onChange={() => chooseMode('declared')} />
            <span>Use declared limit — {current.base === null ? 'none' : limitText(current.base)}</span>
          </label>
          <label className="budget-radio">
            <input ref={draft.mode === 'custom' ? firstRadioRef : undefined} type="radio" name={`${ids}-mode`} checked={draft.mode === 'custom'} onChange={() => chooseMode('custom')} />
            <span>Custom limit</span>
          </label>
          {draft.mode === 'custom' && (
            <div className="budget-custom">
              <div className="budget-custom-fields">
                <label className="budget-field">
                  <span>Tokens</span>
                  <input
                    type="text"
                    inputMode="text"
                    value={draft.tokens}
                    placeholder="e.g. 500k"
                    aria-invalid={errors.tokens !== undefined}
                    aria-describedby={errors.tokens !== undefined ? `${ids}-tokens-error` : undefined}
                    onChange={(e) => patchDraft({ tokens: e.target.value })}
                  />
                  {errors.tokens !== undefined && <span id={`${ids}-tokens-error`} className="budget-field-error">{errors.tokens}</span>}
                </label>
                <label className="budget-field">
                  <span>USD</span>
                  <input
                    type="text"
                    inputMode="decimal"
                    value={draft.usd}
                    placeholder="e.g. 10"
                    aria-invalid={errors.usd !== undefined}
                    aria-describedby={`${ids}-usd-hint${errors.usd !== undefined ? ` ${ids}-usd-error` : ''}`}
                    onChange={(e) => patchDraft({ usd: e.target.value })}
                  />
                  <span id={`${ids}-usd-hint`} className="budget-field-hint">Needs explicit rates in config.yaml</span>
                  {errors.usd !== undefined && <span id={`${ids}-usd-error`} className="budget-field-error">{errors.usd}</span>}
                </label>
              </div>
              {errors.form !== undefined && <p className="budget-field-error" role="alert">{errors.form}</p>}
              <fieldset className="budget-policy">
                <legend>When the limit is reached</legend>
                {POLICIES.map((policy) => (
                  <label key={policy} className="budget-radio budget-radio--policy">
                    <input type="radio" name={`${ids}-policy`} checked={draft.policy === policy} onChange={() => patchDraft({ policy })} />
                    <span>
                      {POLICY_TITLES[policy]}
                      <span className="budget-radio-hint">{POLICY_DESCRIPTIONS[policy]}</span>
                    </span>
                  </label>
                ))}
              </fieldset>
            </div>
          )}
          <label className="budget-radio">
            <input type="radio" name={`${ids}-mode`} checked={draft.mode === 'unlimited'} onChange={() => chooseMode('unlimited')} />
            <span>
              No limit for this {scopeKindWord(current.kind)}
              <span className="budget-radio-hint">Parent limits still apply.</span>
            </span>
          </label>
        </fieldset>

        <p className="budget-dialog-muted">
          Declared limit: {current.base === null ? 'none' : limitText(current.base)} ({sourceText(current)}).
        </p>

        {current.parentLimits.length > 0 && (
          <section className="budget-parents" aria-label="Parent limits">
            <h3>Parent limits</h3>
            <ul>
              {current.parentLimits.map((parent) => (
                <li key={parent.scope}>
                  {scopeLabel(parent.scope, ui.names)}: {budgetFigures(parent.limit, parent.spent, parent.evaluation)} · {POLICY_TITLES[parent.limit.stopPolicy]}
                </li>
              ))}
            </ul>
            <p className="budget-dialog-muted">A limit here never lifts a parent limit.</p>
          </section>
        )}

        <div className="budget-yaml">
          <label className="budget-yaml-option">
            <input
              type="checkbox"
              checked={exportable && saveToYaml}
              disabled={!exportable || !canUpdate || closed}
              aria-describedby={`${ids}-yaml-note`}
              onChange={(e) => setSaveToYaml(e.target.checked)}
            />
            <span>Also save to {file}</span>
          </label>
          <p id={`${ids}-yaml-note`} className="budget-dialog-muted">
            {yamlReason ?? 'Writes this limit into the declaration; the run limit is saved first.'}
          </p>
          {declaredDiffers && yaml?.declared != null && (
            <p className="budget-dialog-muted" role="note">
              {file} declares {limitText(yaml.declared)}, but this run started with {current.base === null ? 'no limit' : limitText(current.base)}. After a
              restart the value in the file replaces a limit saved only for this run.
            </p>
          )}
        </div>

        {blocker !== undefined && (
          <p id={`${ids}-blocker`} className="budget-dialog-message budget-dialog-message--danger" role="note">
            Blocked by the budget of {scopeLabel(blocker.scope, ui.names)}. Changing the limit here does not unblock it — raise that limit to continue.
          </p>
        )}
        <OutcomeMessage outcome={outcome} messageRef={outcomeRef} />

        <div className="modal-actions budget-actions">
          <button type="button" className="btn btn-cancel" onClick={onClose}>{saved ? 'Close' : 'Cancel'}</button>
          {outcome?.kind === 'export_failed' && (
            <button type="button" className="btn" disabled={!idle || current.override.kind !== 'limit'} onClick={retryExport}>
              Retry saving to {outcome.file}
            </button>
          )}
          {canResume && clean && (
            <button
              type="button"
              className="btn btn-send"
              disabled={!idle || blocker !== undefined}
              aria-describedby={blocker !== undefined ? `${ids}-blocker` : undefined}
              onClick={() => (exportExisting ? save(current.override, { continueWork: true, exportToYaml: true }) : continueOnly())}
            >
              Continue
            </button>
          )}
          {canUpdate && exportExisting && (
            <button type="button" className="btn" disabled={!idle} onClick={() => save(current.override, { continueWork: false, exportToYaml: true })}>
              Save to {file}
            </button>
          )}
          {canUpdate && !clean && (
            <button type="button" className="btn" disabled={!idle || invalid} onClick={() => override !== null && save(override, { continueWork: false, exportToYaml: exportable && saveToYaml })}>
              Save
            </button>
          )}
          {canResume && !clean && (
            <button
              type="button"
              className="btn btn-send"
              disabled={!idle || invalid || blocker !== undefined}
              aria-describedby={blocker !== undefined ? `${ids}-blocker` : undefined}
              onClick={() => override !== null && save(override, { continueWork: true, exportToYaml: exportable && saveToYaml })}
            >
              Save and continue
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function OutcomeMessage({ outcome, messageRef }: { outcome: Outcome | null; messageRef: RefObject<HTMLParagraphElement> }): ReactElement | null {
  if (outcome === null) return null
  const props = { ref: messageRef, tabIndex: -1, className: 'budget-dialog-message budget-dialog-message--danger', role: 'alert' } as const
  switch (outcome.kind) {
    case 'conflict':
      return (
        <p {...props}>
          This budget changed while you were editing. The current values are shown and your draft is kept — review it and save again.
        </p>
      )
    case 'still_limited':
      return (
        <p {...props}>
          Limit saved. Work is still stopped: {outcome.message}
        </p>
      )
    case 'resume_failed':
      return (
        <p {...props}>
          Limit saved. Work was not continued: {outcome.message}
        </p>
      )
    case 'export_failed':
      return (
        <p {...props}>
          Run limit saved, {outcome.file} was not changed: {outcome.reason}
        </p>
      )
    case 'export_unavailable':
      return (
        <p {...props}>
          Run limit saved, {outcome.file} was not changed{outcome.reason !== '' ? `: ${outcome.reason}` : ' — it cannot be edited from here.'}
        </p>
      )
    case 'closed':
    case 'error':
      return (
        <p {...props}>
          {outcome.message}
        </p>
      )
  }
}
