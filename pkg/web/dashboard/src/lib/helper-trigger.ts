import type { AttachHelperRequest } from '../api/run-client'

type HelperTrigger = AttachHelperRequest['trigger']

// Порядок опций диалога Attach; подписи общие для диалога и tooltip орбиты.
export const HELPER_TRIGGERS: readonly HelperTrigger[] = ['one_shot', 'on_completion', 'checkpoints', 'on_message']

export const HELPER_TRIGGER_LABELS: Record<HelperTrigger, string> = {
  one_shot: 'One shot',
  on_completion: 'On stage completion',
  checkpoints: 'At checkpoints',
  on_message: 'On message',
}

// helperTriggerLabel: неизвестный trigger остаётся текстом сервера; loopEvery добавляет период цикла.
export function helperTriggerLabel(trigger: string, loopEvery?: string): string {
  const label = Object.hasOwn(HELPER_TRIGGER_LABELS, trigger) ? HELPER_TRIGGER_LABELS[trigger as HelperTrigger] : trigger
  return loopEvery !== undefined && loopEvery !== '' ? `${label} · every ${loopEvery}` : label
}
