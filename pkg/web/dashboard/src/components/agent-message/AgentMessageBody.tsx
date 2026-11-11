import { useState, type ReactElement } from 'react'
import { MarkdownRenderer } from '../plan-panel'
import { parseAgentResultBody, verdictLabel } from '../../lib/agent-result'
import { renderPlainMarkdownCached } from '../plan-panel/markdown'
import { highlightMentions } from './render-mentions'

type AgentMessageBodyProps = {
  body: string
  // fromAgent — the message was written by an agent. Only an agent's body is ever read as a result object: a
  // human pasting JSON into a conversation sees exactly what they wrote.
  fromAgent: boolean
  // markdown — render the text through the shared safe Markdown renderer (the stage feed); plain text otherwise.
  markdown?: boolean
  // autoResult — afm posted this message as the agent's turn result (auto_result); it woke nobody.
  autoResult?: boolean
  mentionHandles?: readonly string[]
  // raw — the "Raw" section opened and closed by the caller, so it survives the row being unmounted (the
  // virtualized feed). Without it the section keeps its own state.
  raw?: { open: boolean; onToggle: (open: boolean) => void }
}

export const TURN_RESULT_TOOLTIP = "afm posted the agent's turn result as a reply; it does not wake the recipient"


// AgentMessageBody — the ONE renderer of a mesh/discussion message text for Team and the stage feed's
// conversations. An agent's body that is its pasted v1 JSON result object shows as its
// summary with a small verdict chip; the original text stays one click away ("Raw"). A turn result afm posted
// for the agent carries a small "turn result" tag.
export function AgentMessageBody({ body, fromAgent, markdown = false, autoResult = false, mentionHandles, raw }: AgentMessageBodyProps): ReactElement {
  const [ownRawOpen, setOwnRawOpen] = useState(false)
  const rawOpen = raw?.open ?? ownRawOpen
  const toggleRaw = raw?.onToggle ?? setOwnRawOpen
  const result = fromAgent ? parseAgentResultBody(body) : null
  const text = result?.summary ?? body
  return (
    <>
      {autoResult && (
        <span className="agent-turn-result-tag" title={TURN_RESULT_TOOLTIP}>
          turn result
        </span>
      )}
      {result?.verdict !== undefined && (
        <span className="agent-result-verdict" data-verdict={result.verdict}>
          {verdictLabel(result.verdict)}
        </span>
      )}
      {markdown ? mentionHandles !== undefined ? (
        <div className="md" dangerouslySetInnerHTML={{ __html: highlightMentions(renderPlainMarkdownCached(text), mentionHandles) }} />
      ) : <MarkdownRenderer source={text} /> : text}
      {result !== null && (
        <details className="agent-result-raw" open={rawOpen} onToggle={(event) => { if (event.currentTarget.open !== rawOpen) toggleRaw(event.currentTarget.open) }}>
          <summary>Raw</summary>
          {rawOpen && <pre>{body}</pre>}
        </details>
      )}
    </>
  )
}
