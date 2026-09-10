import type { ReactElement } from 'react'
import type { AttentionKind } from '../../hooks/use-workspace-view'

type AttentionBannerProps = {
  kind: AttentionKind
}

// Текст баннера по виду attention: заголовок «что происходит» + подзаголовок.
const COPY: Record<AttentionKind, { title: string; subtitle: string; glyph: string }> = {
  approval: { title: 'Plan needs your approval', subtitle: 'Review the plan below. The agent will proceed after approval.', glyph: '?' },
  question: { title: 'Agent needs your input', subtitle: 'Your answer will help the agent decide how to proceed.', glyph: '?' },
  paused: { title: 'Stage is paused', subtitle: 'Continue when you are ready to resume this stage.', glyph: '❙❙' },
  failed: { title: 'Stage failed', subtitle: 'Review the details and retry when the problem is resolved.', glyph: '!' },
  hook_failed: { title: 'Hook failed', subtitle: 'A stage hook failed. Retry it or skip to continue.', glyph: '!' },
}

// AttentionBanner — шапка внутри контекстной вкладки (Approval/Question/…): большая
// сияющая иконка + заголовок «что происходит» + подзаголовок. Тон определяется
// видом attention (attention-жёлтый для approval/question/paused, danger для
// failed/hook_failed). Данные не тянет — чистый презентационный компонент.
export function AttentionBanner({ kind }: AttentionBannerProps): ReactElement {
  const copy = COPY[kind]
  const tone = kind === 'failed' || kind === 'hook_failed' ? 'danger' : 'attention'
  return (
    <div className="attention-banner" data-kind={kind} data-tone={tone}>
      <span className="attention-banner-icon" aria-hidden="true">{copy.glyph}</span>
      <span className="attention-banner-text">
        <span className="attention-banner-title">{copy.title}</span>
        <span className="attention-banner-subtitle">{copy.subtitle}</span>
      </span>
    </div>
  )
}
