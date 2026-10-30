import type { ReactElement } from 'react'
import type { AttentionKind } from '../../hooks/use-workspace-view'

type AttentionBannerProps = {
  kind: AttentionKind
}

// Тонкие линейные иконки: круг + знак, цвет берут у тона баннера.
const ICON_ALERT = <path d="M12 7v6m0 3v.1" />
const ICON_HELP = <path d="M9.6 9.4a2.5 2.5 0 1 1 3.6 2.2c-.8.4-1.2.9-1.2 1.8M12 16.5v.1" />
const ICON_PAUSE = <path d="M10 9v6M14 9v6" />

// Текст баннера по виду attention: заголовок «что происходит» + подзаголовок.
const COPY: Record<AttentionKind, { title: string; subtitle: string; icon: ReactElement }> = {
  approval: { title: 'Plan needs your approval', subtitle: 'Review the plan below. The agent will proceed after approval.', icon: ICON_HELP },
  question: { title: 'Agent needs your input', subtitle: 'Your answer will help the agent decide how to proceed.', icon: ICON_HELP },
  paused: { title: 'Stage is paused', subtitle: 'Continue when you are ready to resume this stage.', icon: ICON_PAUSE },
  failed: { title: 'Stage failed', subtitle: 'Review the details and retry when the problem is resolved.', icon: ICON_ALERT },
  hook_failed: { title: 'Hook failed', subtitle: 'A stage hook failed. Retry it or skip to continue.', icon: ICON_ALERT },
  assistants: {
    title: 'A helper task needs your decision',
    subtitle: 'A required helper did not pass cleanly — review it, then revise, retry, or waive.',
    icon: ICON_ALERT,
  },
}

// AttentionBanner — шапка внутри контекстной вкладки: тихая линейная иконка + заголовок «что происходит»
// + подзаголовок. Данные не тянет — чистый презентационный компонент.
export function AttentionBanner({ kind }: AttentionBannerProps): ReactElement {
  const copy = COPY[kind]
  const tone = kind === 'failed' || kind === 'hook_failed' ? 'danger' : 'attention'
  return (
    <div className="attention-banner" data-kind={kind} data-tone={tone}>
      <svg className="attention-banner-icon" viewBox="0 0 24 24" width="21" height="21" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <circle cx="12" cy="12" r="9" />
        {copy.icon}
      </svg>
      <span className="attention-banner-text">
        <span className="attention-banner-title">{copy.title}</span>
        <span className="attention-banner-subtitle">{copy.subtitle}</span>
      </span>
    </div>
  )
}
