import type { ReactElement, ReactNode } from 'react'
import type { MeshBoard } from '../../hooks/use-mesh'

type BoardLoadStateProps = {
  board: MeshBoard
  children: ReactNode
}

// BoardLoadState — what a board view shows around its content, so "no tasks" is never confused with "not
// loaded" or "unavailable": loading and a first-load error replace the content (there is no snapshot to show),
// a failed refresh keeps the last snapshot visible under a warning. Retry goes through the board's reload().
// The content keeps one position in both ready and stale, so a failed refresh never remounts it (a typed task,
// filters and open dialogs survive).
export function BoardLoadState({ board, children }: BoardLoadStateProps): ReactElement {
  if (board.status === 'loading') {
    return (
      <div className="team-board-empty empty-hint" role="status">
        Loading board…
      </div>
    )
  }
  if (board.status === 'error') return <BoardFailure board={board} message="Board unavailable — could not load the tasks." />
  return (
    <>
      {board.status === 'stale' && <BoardFailure board={board} message="The board may be out of date — the last refresh failed." />}
      {children}
    </>
  )
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
