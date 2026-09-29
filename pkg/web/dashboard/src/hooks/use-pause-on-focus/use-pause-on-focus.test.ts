import { StrictMode } from 'react'
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePauseOnFocus } from './use-pause-on-focus'

// makeDeferred — контролируемый промис: тест сам решает, когда он резолвится.
// Нужен, чтобы моделировать «Send/blur/toggle пока pause ещё не завершился».
function makeDeferred<T = void>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

// Общий журнал вызовов — им проверяем ПОРЯДОК (continue до revise) в handleSend.
function makeDeps() {
  const log: string[] = []
  const pauseStage = vi.fn((id: string) => {
    log.push(`pause:${id}`)
    return Promise.resolve()
  })
  const continueStage = vi.fn((id: string) => {
    log.push(`continue:${id}`)
    return Promise.resolve()
  })
  const reviseStage = vi.fn((id: string, t: string) => {
    log.push(`revise:${id}:${t}`)
    return Promise.resolve()
  })
  return { log, pauseStage, continueStage, reviseStage }
}

describe('usePauseOnFocus', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('reads the persisted preference from localStorage on mount', () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    expect(result.current.enabled).toBe(true)
  })

  it('defaults to disabled when nothing is stored', () => {
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    expect(result.current.enabled).toBe(false)
  })

  it('setEnabled persists the preference to localStorage', () => {
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    act(() => result.current.setEnabled(true))
    expect(result.current.enabled).toBe(true)
    expect(localStorage.getItem('afm.pauseOnFocus')).toBe('true')
    act(() => result.current.setEnabled(false))
    expect(localStorage.getItem('afm.pauseOnFocus')).toBe('false')
  })

  it('onFocus with enabled + running pauses and marks the stage owned', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    expect(deps.pauseStage).toHaveBeenCalledWith('s1')
    expect(result.current.isOwned('s1')).toBe(true)
  })

  it('onFocus does nothing when disabled', async () => {
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    expect(deps.pauseStage).not.toHaveBeenCalled()
    expect(result.current.isOwned('s1')).toBe(false)
  })

  it('onFocus does nothing when the stage is not running', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'paused'))
    expect(deps.pauseStage).not.toHaveBeenCalled()
    expect(result.current.isOwned('s1')).toBe(false)
  })

  it('handleSend when owned continues THEN revises, in order, and clears ownership', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    deps.log.length = 0 // сбрасываем pause из журнала — проверяем только send-порядок

    await act(async () => {
      await result.current.handleSend('s1', 'do X')
    })

    expect(deps.log).toEqual(['continue:s1', 'revise:s1:do X'])
    expect(result.current.isOwned('s1')).toBe(false)
  })

  it('handleSend when not owned calls only reviseStage', async () => {
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => {
      await result.current.handleSend('s1', 'plain note')
    })
    expect(deps.continueStage).not.toHaveBeenCalled()
    expect(deps.reviseStage).toHaveBeenCalledWith('s1', 'plain note')
  })

  it('handleSend re-throws when reviseStage rejects (draft preserved)', async () => {
    const deps = makeDeps()
    deps.reviseStage.mockRejectedValueOnce(new Error('409'))
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await expect(result.current.handleSend('s1', 'note')).rejects.toThrow('409')
  })

  it('onBlur with an empty draft continues and clears when owned', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    await act(async () => result.current.onBlur('s1', false))
    expect(deps.continueStage).toHaveBeenCalledWith('s1')
    expect(result.current.isOwned('s1')).toBe(false)
  })

  it('onBlur with a draft does nothing', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    await act(async () => result.current.onBlur('s1', true))
    expect(deps.continueStage).not.toHaveBeenCalled()
    expect(result.current.isOwned('s1')).toBe(true)
  })

  it('resumeNow continues and clears ownership', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    await act(async () => result.current.resumeNow('s1'))
    expect(deps.continueStage).toHaveBeenCalledWith('s1')
    expect(result.current.isOwned('s1')).toBe(false)
  })

  it('setEnabled(false) continues every currently-owned stage', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    await act(async () => result.current.onFocus('s2', 'running'))
    expect(result.current.isOwned('s1')).toBe(true)
    expect(result.current.isOwned('s2')).toBe(true)

    await act(async () => result.current.setEnabled(false))
    expect(deps.continueStage).toHaveBeenCalledWith('s1')
    expect(deps.continueStage).toHaveBeenCalledWith('s2')
    expect(result.current.isOwned('s1')).toBe(false)
    expect(result.current.isOwned('s2')).toBe(false)
  })

  // --- Async-race hardening (review High/Medium/Low findings) ---

  it('handleSend awaits an in-flight pause before continue/revise (High #1: no revise before pause)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const log: string[] = []
    const paused = makeDeferred()
    const pauseStage = vi.fn((id: string) => {
      log.push(`pause:${id}`)
      return paused.promise
    })
    const continueStage = vi.fn((id: string) => {
      log.push(`continue:${id}`)
      return Promise.resolve()
    })
    const reviseStage = vi.fn((id: string, t: string) => {
      log.push(`revise:${id}:${t}`)
      return Promise.resolve()
    })
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))

    // Фокус запускает pause, но он ещё НЕ завершился (deferred).
    act(() => result.current.onFocus('s1', 'running'))
    expect(result.current.hasActiveOp('s1')).toBe(true)

    await act(async () => {
      const sending = result.current.handleSend('s1', 'do X')
      // Пока pause висит — ни continue, ни revise не должны были стартовать.
      expect(log).toEqual(['pause:s1'])
      paused.resolve()
      await sending
    })

    // Порядок строго pause → continue → revise; стадия не осталась на паузе.
    expect(log).toEqual(['pause:s1', 'continue:s1', 'revise:s1:do X'])
    // После успешной доставки владение снимается сразу (промис, не polling).
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('onBlur (empty draft) before pause resolves still resumes once pause settles (High #1)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const paused = makeDeferred()
    const pauseStage = vi.fn(() => paused.promise)
    const continueStage = vi.fn(() => Promise.resolve())
    const reviseStage = vi.fn(() => Promise.resolve())
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))

    act(() => result.current.onFocus('s1', 'running'))
    let blurring!: Promise<unknown>
    act(() => {
      result.current.onBlur('s1', false)
      // Pause ещё не завершился — resume не имеет права опередить его.
      expect(continueStage).not.toHaveBeenCalled()
    })
    await act(async () => {
      blurring = Promise.resolve()
      paused.resolve()
      await blurring
      await Promise.resolve()
    })

    expect(continueStage).toHaveBeenCalledWith('s1')
    // После continue стадия в 'resuming' (композер жив); владение снимает reconcile
    // по observed running.
    expect(result.current.isOwned('s1')).toBe(false)
    act(() => result.current.reconcile([{ id: 's1', status: 'running' }]))
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('setEnabled(false) before pause resolves still resumes the stage once pause settles (High #1)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const paused = makeDeferred()
    const pauseStage = vi.fn(() => paused.promise)
    const continueStage = vi.fn(() => Promise.resolve())
    const reviseStage = vi.fn(() => Promise.resolve())
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))

    act(() => result.current.onFocus('s1', 'running'))
    act(() => {
      result.current.setEnabled(false)
      expect(continueStage).not.toHaveBeenCalled()
    })
    await act(async () => {
      paused.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(continueStage).toHaveBeenCalledWith('s1')
    expect(result.current.isOwned('s1')).toBe(false)
    act(() => result.current.reconcile([{ id: 's1', status: 'running' }]))
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('handleSend tolerates a stale continue rejection and still delivers the note (Medium #3)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const pauseStage = vi.fn(() => Promise.resolve())
    // Стадия уже уехала в running (внешний Continue) — continue 400-ит, но заметка
    // всё равно должна доставиться.
    const continueStage = vi.fn(() => Promise.reject(new Error('400 not paused')))
    const reviseStage = vi.fn(() => Promise.resolve())
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))

    await act(async () => result.current.onFocus('s1', 'running'))
    await act(async () => {
      await result.current.handleSend('s1', 'note')
    })

    expect(reviseStage).toHaveBeenCalledWith('s1', 'note')
    // Доставлено, несмотря на устаревший continue: владение снимается СРАЗУ после
    // успешного revise (драйвится промисом, не polling).
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('handleSend keeps ownership when the resume fails and revise cannot proceed (High #2, retry-able)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const pauseStage = vi.fn(() => Promise.resolve())
    const continueStage = vi.fn(() => Promise.reject(new Error('continue failed')))
    const reviseStage = vi.fn(() => Promise.reject(new Error('revise failed')))
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))

    await act(async () => result.current.onFocus('s1', 'running'))
    expect(result.current.isOwned('s1')).toBe(true)

    await act(async () => {
      await expect(result.current.handleSend('s1', 'note')).rejects.toThrow('revise failed')
    })

    // Владение НЕ выброшено — пользователь может повторить отправку.
    expect(result.current.hasActiveOp('s1')).toBe(true)
    expect(result.current.isOwned('s1')).toBe(true)
  })

  it('reconcile: a stale pre-pause running snapshot does NOT drop ownership; a genuine external Continue does (High stale-running)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))
    expect(result.current.isOwned('s1')).toBe(true)

    // Устаревший до-паузный снапшот /api/status: 'running' ДО того, как мы увидели
    // свою паузу применённой → владение НЕ снимаем (иначе композер бы схлопнулся).
    act(() => result.current.reconcile([{ id: 's1', status: 'running' }]))
    expect(result.current.isOwned('s1')).toBe(true)

    // Увидели свою паузу применённой…
    act(() => result.current.reconcile([{ id: 's1', status: 'paused' }]))
    expect(result.current.isOwned('s1')).toBe(true)

    // …теперь 'running' — это настоящий внешний Continue → снимаем владение.
    act(() => result.current.reconcile([{ id: 's1', status: 'running' }]))
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('resume is single-flight: blur then Resume-now fire only ONE continueStage', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const resumed = makeDeferred()
    const pauseStage = vi.fn(() => Promise.resolve())
    const continueStage = vi.fn(() => resumed.promise as Promise<void>)
    const reviseStage = vi.fn(() => Promise.resolve())
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))
    await act(async () => result.current.onFocus('s1', 'running'))

    // Клик «Resume now» на пустом поле сначала снимает фокус (blur), затем кликает —
    // два resume подряд, пока первый continue ещё в полёте. Должен уйти ОДИН запрос.
    await act(async () => {
      result.current.onBlur('s1', false)
      result.current.resumeNow('s1')
      await Promise.resolve()
    })
    expect(continueStage).toHaveBeenCalledTimes(1)
    await act(async () => {
      resumed.resolve()
      await Promise.resolve()
    })
  })

  it('continue-success + revise-failure clears ownership (no false paused banner) — High #2', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const pauseStage = vi.fn(() => Promise.resolve())
    const continueStage = vi.fn(() => Promise.resolve()) // continue OK → stage running
    const reviseStage = vi.fn(() => Promise.reject(new Error('revise 500')))
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))
    await act(async () => result.current.onFocus('s1', 'running'))
    await act(async () => {
      await expect(result.current.handleSend('s1', 'note')).rejects.toThrow('revise 500')
    })
    // Стадия уже running (continue прошёл) — владение снято, ложного баннера паузы нет.
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('toggle-off during an in-flight send does not resurrect ownership after revise fails — High #1/gen', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const revised = makeDeferred()
    const pauseStage = vi.fn(() => Promise.resolve())
    const continueStage = vi.fn(() => Promise.resolve())
    const reviseStage = vi.fn(() => revised.promise)
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))
    await act(async () => result.current.onFocus('s1', 'running'))

    let sending!: Promise<void>
    await act(async () => {
      sending = result.current.handleSend('s1', 'x').catch(() => {}) // swallow — we assert state
      await Promise.resolve()
    })
    // Пользователь выключает toggle, пока revise в полёте — новая интенция (resume).
    await act(async () => {
      result.current.setEnabled(false)
      await Promise.resolve()
    })
    // Просроченный revise отклоняется — НЕ должен воскресить владение (gen сменился).
    await act(async () => {
      revised.reject(new Error('late revise'))
      await sending
      await Promise.resolve()
    })
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('toggle-off after a successful Send-continue issues NO second continue and does not strand ownership (round5 High)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const revised = makeDeferred()
    const pauseStage = vi.fn(() => Promise.resolve())
    // Первый continue успешен; ЛЮБОЙ второй continue 400-ит («уже running»).
    let continueCalls = 0
    const continueStage = vi.fn(() => {
      continueCalls += 1
      return continueCalls === 1 ? Promise.resolve() : Promise.reject(new Error('400 not paused'))
    })
    const reviseStage = vi.fn(() => revised.promise)
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))
    await act(async () => result.current.onFocus('s1', 'running'))

    let sending!: Promise<void>
    await act(async () => {
      sending = result.current.handleSend('s1', 'x').catch(() => {})
      await Promise.resolve()
      await Promise.resolve() // дать continue (успех) развязаться → resumedRef.add
    })
    // Пользователь выключает toggle, пока revise в полёте.
    await act(async () => {
      result.current.setEnabled(false)
      await Promise.resolve()
    })
    await act(async () => {
      revised.resolve()
      await sending
      await Promise.resolve()
    })
    // Второй continue НЕ ушёл (эпизод уже резюмирован), ложного владения нет.
    expect(continueStage).toHaveBeenCalledTimes(1)
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('reconcile does NOT touch an in-flight (resuming) stage — promises drive it, not polling', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const revised = makeDeferred()
    const pauseStage = vi.fn(() => Promise.resolve())
    const continueStage = vi.fn(() => Promise.resolve())
    const reviseStage = vi.fn(() => revised.promise)
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))
    await act(async () => result.current.onFocus('s1', 'running'))
    let sending!: Promise<void>
    await act(async () => {
      sending = result.current.handleSend('s1', 'x')
      await Promise.resolve()
    })
    // revise ещё в полёте (op='resuming'). Промежуточный опрос статуса НЕ должен
    // снять владение — иначе композер размонтировался бы во время Send.
    expect(result.current.hasActiveOp('s1')).toBe(true)
    act(() => result.current.reconcile([{ id: 's1', status: 'running' }]))
    expect(result.current.hasActiveOp('s1')).toBe(true)
    // Завершаем revise — владение снимается промисом, сразу.
    await act(async () => {
      revised.resolve()
      await sending
    })
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('reconcile clears ownership on a terminal status (done)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))

    act(() => result.current.reconcile([{ id: 's1', status: 'done' }]))
    expect(result.current.isOwned('s1')).toBe(false)
  })

  it('reconcile leaves ownership intact while the stage stays paused', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    await act(async () => result.current.onFocus('s1', 'running'))

    act(() => result.current.reconcile([{ id: 's1', status: 'paused' }]))
    expect(result.current.isOwned('s1')).toBe(true)
  })

  it('setEnabled(false) issues exactly ONE continue per owned stage under StrictMode double-invoke (Low #5)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps), { wrapper: StrictMode })
    await act(async () => result.current.onFocus('s1', 'running'))
    await act(async () => result.current.onFocus('s2', 'running'))

    await act(async () => result.current.setEnabled(false))

    // Ни одного лишнего запроса от повторного прогона апдейтера в StrictMode.
    expect(deps.continueStage).toHaveBeenCalledTimes(2)
    expect(deps.continueStage).toHaveBeenCalledWith('s1')
    expect(deps.continueStage).toHaveBeenCalledWith('s2')
  })

  it('hasActiveOp stays true from focus through an in-flight send, false only after it resolves (High #2)', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const revised = makeDeferred()
    const pauseStage = vi.fn(() => Promise.resolve())
    const continueStage = vi.fn(() => Promise.resolve())
    const reviseStage = vi.fn(() => revised.promise)
    const { result } = renderHook(() => usePauseOnFocus({ pauseStage, continueStage, reviseStage }))

    await act(async () => result.current.onFocus('s1', 'running'))
    expect(result.current.hasActiveOp('s1')).toBe(true)

    let sending!: Promise<void>
    await act(async () => {
      sending = result.current.handleSend('s1', 'x')
      await Promise.resolve()
    })
    // revise ещё в полёте — композер обязан оставаться (hasActiveOp).
    expect(result.current.hasActiveOp('s1')).toBe(true)

    await act(async () => {
      revised.resolve()
      await sending
    })
    // Композер держался смонтированным весь in-flight Send (High #1: сохранность
    // черновика). На успехе владение снимается СРАЗУ (промис, не polling).
    expect(result.current.hasActiveOp('s1')).toBe(false)
  })

  it('shouldSuppressAttention mirrors hasActiveOp', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')
    const deps = makeDeps()
    const { result } = renderHook(() => usePauseOnFocus(deps))
    expect(result.current.shouldSuppressAttention('s1')).toBe(false)
    await act(async () => result.current.onFocus('s1', 'running'))
    expect(result.current.shouldSuppressAttention('s1')).toBe(true)
  })
})
