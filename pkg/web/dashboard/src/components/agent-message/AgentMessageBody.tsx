import type { ReactElement } from 'react'
import { MarkdownRenderer } from '../plan-panel'
import { parseAgentResultBody, verdictLabel } from '../../lib/agent-result'

type AgentMessageBodyProps = {
  body: string
  // markdown — render the text through the shared safe Markdown renderer (Discussion); plain text otherwise.
  markdown?: boolean
}

// AgentMessageBody — the ONE renderer of a mesh/discussion message text for Team, the feed's conversation
// preview and the stage Discussion. A body that is an agent's pasted JSON result object shows as its summary
// with a small verdict chip instead of raw JSON.
export function AgentMessageBody({ body, markdown = false }: AgentMessageBodyProps): ReactElement {
  const result = parseAgentResultBody(body)
  const text = result?.summary ?? body
  return (
    <>
      {result !== null && result.verdict !== '' && (
        <span className="agent-result-verdict" data-verdict={result.verdict}>
          {verdictLabel(result.verdict)}
        </span>
      )}
      {markdown ? <MarkdownRenderer source={text} /> : text}
    </>
  )
}
