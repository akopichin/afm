import type { ReactElement, ReactNode } from 'react'
import type { MeshBoard } from '../../hooks/use-mesh'

type BoardLoadStateProps = {
  board: MeshBoard
  children: ReactNode
}

// BoardLoadState — what a board view shows around its content, so "no tasks" is never confused with "not
// loaded" or "unavailable": loading and a first-load error replace the content (there is no snapshot to show),
// a failed refresh keeps the last snapshot visible under a warning. Retry goes through the board's reload().
export function BoardLoadState({ board, children }: BoardLoadStateProps): ReactElement {
  switch (board.status) {
    case 'loading':
      return (
        <div className="team-board-empty empty-hint" role="status">
          Loading board…
        </div>
      )
    case 'error':
      return <BoardFailure board={board} message="Board unavailable — could not load the tasks." />
    case 'stale':
      return (
        <>
          <BoardFailure board={board} message="The board may be out of date — the last refresh failed." />
          {children}
        </>
      )
    case 'ready':
      return <>{children}</>
  }
}

function BoardFailure({ board, message }: { board: MeshBoard; message: string }): ReactElement {
  return (
    <div className="team-board-error" role="alert">
      {message}{' '}
      <button type="button" className="btn btn-cancel" onClick={() => void board.reload()}>
        Retry
      </button>
    </div>
  )
}
