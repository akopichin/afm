import { useCallback, useRef, useState } from 'react'

const STORAGE_KEY = 'afm.pauseOnFocus'

// OpState: pausing ждёт подтверждения, owned разрешает снять свою паузу, resuming ждёт Continue; нет ключа
// — нет владения.
type OpState = 'pausing' | 'owned' | 'resuming'

// PauseOnFocusApi — «поставить стадию на паузу, пока пишу заметку». Только три готовых вызова — без новых
// эндпоинтов и без изменений бэкенда.
export type PauseOnFocusApi = {
  enabled: boolean
  setEnabled: (v: boolean) => void
  // isOwned — стадия, пауза которой ПОДТВЕРЖДЕНА нами (op === 'owned'). Баннер
  // паузы = status==='paused' && isOwned.
  isOwned: (stageId: string) => boolean
  // hasActiveOp — есть ЛЮБАЯ незавершённая операция (pausing|owned|resuming). Композер держим
  // смонтированным всё это время, чтобы не потерять черновик — в т.ч. на весь in-flight Send.
  hasActiveOp: (stageId: string) => boolean
  // shouldSuppressAttention — не давать нашей же паузе авто-открыть attention и
  // выкинуть пользователя из композера. Равно hasActiveOp.
  shouldSuppressAttention: (stageId: string) => boolean
  onFocus: (stageId: string, status: string) => void
  onBlur: (stageId: string, hasDraft: boolean) => void
  resumeNow: (stageId: string) => void
  // reconcile — safety-net сверка ИДЛОВОГО 'owned' с авторитетным статусом: терминал → снять; running
  // ПОСЛЕ наблюдённой паузы → снять.
  reconcile: (stages: { id: string; status: string }[]) => void
  handleSend: (stageId: string, text: string) => Promise<void>
}

type Deps = {
  pauseStage: (id: string) => Promise<unknown>
  continueStage: (id: string) => Promise<void>
  reviseStage: (id: string, t: string) => Promise<void>
  runEpoch?: number
}

function readStored(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'true'
  } catch {
    return false
  }
}

export function usePauseOnFocus({ pauseStage, continueStage, reviseStage, runEpoch = 0 }: Deps): PauseOnFocusApi {
  const [enabled, setEnabledState] = useState<boolean>(readStored)

  // Всё per-стадийное состояние — refs (синхронный доступ, без гонок с React-
  // батчингом); version зеркалит в React для перерисовки селекторов.
  const opsRef = useRef<Map<string, OpState>>(new Map())
  const pendingRef = useRef<Map<string, Promise<unknown>>>(new Map())
  const resumingRef = useRef<Map<string, Promise<void>>>(new Map())
  const sawPausedRef = useRef<Map<string, boolean>>(new Map())
  const genRef = useRef<Map<string, number>>(new Map())
  // resumedRef — стадии, для которых continue в ТЕКУЩЕМ эпизоде владения уже прошёл успешно. Сбрасывается
  // в clearOp (конец эпизода) и onFocus (новый эпизод).
  const resumedRef = useRef<Set<string>>(new Set())
  const epochRef = useRef(runEpoch)
  // A repeated stage id in another run must neither inherit ownership nor receive old request tails.
  if (epochRef.current !== runEpoch) {
    epochRef.current = runEpoch
    opsRef.current.clear()
    pendingRef.current.clear()
    resumingRef.current.clear()
    sawPausedRef.current.clear()
    genRef.current.clear()
    resumedRef.current.clear()
  }
  const isCurrentRun = useCallback(() => epochRef.current === runEpoch, [runEpoch])

  const [, setVersion] = useState(0)
  const bump = useCallback((): void => setVersion((v) => v + 1), [])

  const curGen = useCallback((stageId: string): number => genRef.current.get(stageId) ?? 0, [])
  // nextGen — открыть новую интенцию: инкремент gen лочит все прежние in-flight
  // развязки этой стадии (их curGen-проверки перестанут совпадать).
  const nextGen = useCallback((stageId: string): number => {
    const g = (genRef.current.get(stageId) ?? 0) + 1
    genRef.current.set(stageId, g)
    return g
  }, [])

  const setOp = useCallback(
    (stageId: string, op: OpState): void => {
      opsRef.current.set(stageId, op)
      bump()
    },
    [bump],
  )

  // clearOp — снять владение И ИНВАЛИДИРОВАТЬ gen: любая ещё висящая развязка
  // (напр. просроченный revise.catch) увидит несовпадение gen и ничего не тронет.
  const clearOp = useCallback(
    (stageId: string): void => {
      sawPausedRef.current.delete(stageId)
      resumedRef.current.delete(stageId)
      genRef.current.set(stageId, (genRef.current.get(stageId) ?? 0) + 1)
      if (opsRef.current.delete(stageId)) bump()
    },
    [bump],
  )

  const isOwned = useCallback((stageId: string): boolean => opsRef.current.get(stageId) === 'owned', [])
  const hasActiveOp = useCallback((stageId: string): boolean => opsRef.current.has(stageId), [])
  const shouldSuppressAttention = hasActiveOp

  const awaitPending = useCallback(async (stageId: string): Promise<void> => {
    const p = pendingRef.current.get(stageId)
    if (p === undefined) return
    try {
      await p
    } catch {
      /* pause отклонился — обработано в onFocus.catch */
    }
  }, [])

  // continueOnce — single-flight сам HTTP-continue: параллельные вызовы (blur +
  // click «Resume now») делят один запрос. Op'ом управляют вызывающие.
  const continueOnce = useCallback(
    (stageId: string): Promise<void> => {
      const existing = resumingRef.current.get(stageId)
      if (existing !== undefined) return existing
      const p = continueStage(stageId)
        .then(() => {
          if (!isCurrentRun()) return
          resumedRef.current.add(stageId) // эпизод снят с паузы — второй continue не нужен
        })
        .finally(() => {
          if (!isCurrentRun()) return
          resumingRef.current.delete(stageId)
        })
      resumingRef.current.set(stageId, p)
      return p
    },
    [continueStage, isCurrentRun],
  )

  // resume — снять НАШУ паузу (отмена без отправки). Новая интенция (nextGen) лочит любой конкурентный
  // in-flight Send этой стадии, чтобы его хвост не тронул op. Снятие/откат драйвит промис.
  const resume = useCallback(
    async (stageId: string): Promise<void> => {
      await awaitPending(stageId)
      if (!isCurrentRun()) return
      if (!opsRef.current.has(stageId)) return
      const g = nextGen(stageId)
      setOp(stageId, 'resuming')
      // Уже сняли с паузы в этом эпизоде (напр. Send-continue прошёл, а теперь toggle-off) — второй
      // continue не шлём (он бы 400-ил «уже running»), просто снимаем владение.
      if (resumedRef.current.has(stageId)) {
        clearOp(stageId)
        return
      }
      try {
        await continueOnce(stageId)
        if (!isCurrentRun()) return
        if (curGen(stageId) === g) clearOp(stageId)
      } catch (err: unknown) {
        if (!isCurrentRun()) return
        // Continue не удался (стадия всё ещё paused) — откат в 'owned' для повтора,
        // только если интенция всё ещё наша.
        if (curGen(stageId) === g && opsRef.current.get(stageId) === 'resuming') setOp(stageId, 'owned')
        console.error('Failed to resume stage on pause-on-focus:', err)
      }
    },
    [awaitPending, nextGen, continueOnce, curGen, setOp, clearOp, isCurrentRun],
  )

  const setEnabled = useCallback(
    (v: boolean): void => {
      setEnabledState(v)
      try {
        localStorage.setItem(STORAGE_KEY, v ? 'true' : 'false')
      } catch {
        /* приватный режим/недоступный storage — просто не персистим */
      }
      // Выключение toggle снимает паузу со всех «наших» стадий. Запросы шлём ВНЕ
      // setState-апдейтера (StrictMode-safe); дедуп в continueOnce страхует.
      if (!v) {
        for (const id of [...opsRef.current.keys()]) {
          void resume(id)
        }
      }
    },
    [resume],
  )

  const onFocus = useCallback(
    (stageId: string, status: string): void => {
      if (!isCurrentRun()) return
      if (!enabled || status !== 'running') return
      if (opsRef.current.has(stageId)) return // уже есть операция — не дублируем
      sawPausedRef.current.delete(stageId)
      resumedRef.current.delete(stageId) // новый эпизод владения
      const g = nextGen(stageId)
      setOp(stageId, 'pausing')
      const p = pauseStage(stageId)
      pendingRef.current.set(stageId, p)
      p.then(() => {
        if (!isCurrentRun()) return
        pendingRef.current.delete(stageId)
        // Промоутим в owned только если это ВСЁ ЕЩЁ тот же эпизод и он всё ещё
        // pausing (Send/blur мог увести в resuming, новый onFocus — сменить gen).
        if (curGen(stageId) === g && opsRef.current.get(stageId) === 'pausing') setOp(stageId, 'owned')
      }).catch((err: unknown) => {
        if (!isCurrentRun()) return
        pendingRef.current.delete(stageId)
        if (curGen(stageId) === g && opsRef.current.get(stageId) === 'pausing') clearOp(stageId)
        console.error('Failed to pause stage on focus:', err)
      })
    },
    [enabled, pauseStage, nextGen, curGen, setOp, clearOp, isCurrentRun],
  )

  const onBlur = useCallback(
    (stageId: string, hasDraft: boolean): void => {
      // Уход из пустого поля = отмена: возобновляем. С черновиком — оставляем паузу
      // (пользователь ещё пишет, Send потом сам возобновит).
      if (hasDraft) return
      if (!opsRef.current.has(stageId)) return
      void resume(stageId)
    },
    [resume],
  )

  const resumeNow = useCallback((stageId: string): void => void resume(stageId), [resume])

  const reconcile = useCallback(
    (stages: { id: string; status: string }[]): void => {
      for (const { id, status } of stages) {
        // Только ИДЛОВОЕ 'owned'.
        if (opsRef.current.get(id) !== 'owned') continue

        if (status === 'done' || status === 'failed') {
          clearOp(id)
          continue
        }
        // Наблюдённая пауза → только после неё 'running' трактуется как внешний
        // Continue, а не как устаревший до-паузный снапшот /api/status.
        if (status === 'paused' || status === 'revising') {
          if (sawPausedRef.current.get(id) !== true) {
            sawPausedRef.current.set(id, true)
            bump()
          }
          continue
        }
        if (status === 'running' && sawPausedRef.current.get(id) === true) {
          clearOp(id)
        }
      }
    },
    [bump, clearOp],
  )

  const handleSend = useCallback(
    async (stageId: string, text: string): Promise<void> => {
      // High #1: дождаться ещё не завершившейся паузы, иначе continue/revise обгонят её.
      await awaitPending(stageId)
      if (!isCurrentRun()) return
      if (!opsRef.current.has(stageId)) {
        // Владения нет — обычная доставка заметки живому агенту.
        await reviseStage(stageId, text)
        return
      }
      const g = nextGen(stageId)
      setOp(stageId, 'resuming') // держим композер на весь in-flight Send (High #1) continued: стадия снята с паузы. Если эпизод уже
      // резюмирован (blur/resume-now успели) — второй continue не шлём.
      let continued = resumedRef.current.has(stageId)
      if (!continued) {
        // Single-flight continue (может уже идти из blur/resume-now) — терпим отказ:
        // устаревшее владение (стадия уже running → 400) не мешает доставке заметки.
        try {
          await continueOnce(stageId)
          continued = true
        } catch (err: unknown) {
          if (!isCurrentRun()) return
          console.error('Failed to resume stage on send:', err)
        }
      }
      if (!isCurrentRun()) return
      try {
        await reviseStage(stageId, text)
      } catch (err: unknown) {
        if (!isCurrentRun()) return
        // Доставка не удалась.
        if (curGen(stageId) === g) {
          if (continued) clearOp(stageId)
          else setOp(stageId, 'owned')
        }
        throw err
      }
      // Успех: заметка доставлена, стадия снята с паузы. Снимаем владение СРАЗУ
      // (драйвится промисом, не polling) — если интенция ещё наша.
      if (isCurrentRun() && curGen(stageId) === g) clearOp(stageId)
    },
    [awaitPending, nextGen, continueOnce, reviseStage, curGen, setOp, clearOp, isCurrentRun],
  )

  return {
    enabled,
    setEnabled,
    isOwned,
    hasActiveOp,
    shouldSuppressAttention,
    onFocus,
    onBlur,
    resumeNow,
    reconcile,
    handleSend,
  }
}
