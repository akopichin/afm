import type { ReactElement } from 'react'
import { MarkdownRenderer } from '../plan-panel'
import { parseAgentResultBody, verdictLabel } from '../../lib/agent-result'

type AgentMessageBodyProps = {
  body: string
  // fromAgent — the message was written by an agent. Only an agent's body is ever read as a result object: a
  // human pasting JSON into a conversation sees exactly what they wrote.
  fromAgent: boolean
  // markdown — render the text through the shared safe Markdown renderer (Discussion); plain text otherwise.
  markdown?: boolean
}

// AgentMessageBody — the ONE renderer of a mesh/discussion message text for Team, the feed's conversation
// preview and the stage Discussion. An agent's body that is its pasted v1 JSON result object shows as its
// summary with a small verdict chip; the original text stays one click away ("Raw").
export function AgentMessageBody({ body, fromAgent, markdown = false }: AgentMessageBodyProps): ReactElement {
  const result = fromAgent ? parseAgentResultBody(body) : null
  const text = result?.summary ?? body
  return (
    <>
      {result !== null && (
        <span className="agent-result-verdict" data-verdict={result.verdict}>
          {verdictLabel(result.verdict)}
        </span>
      )}
      {markdown ? <MarkdownRenderer source={text} /> : text}
      {result !== null && (
        <details className="agent-result-raw">
          <summary>Raw</summary>
          <pre>{body}</pre>
        </details>
      )}
    </>
  )
}
