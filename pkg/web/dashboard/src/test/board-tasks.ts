// Test fixtures for typed board tasks: the server's task view with only the fields a test cares about set.
import type { MeshBoardTask, MeshBoardTaskAssignment } from '../api/mesh-client'

export function typedAssignment(overrides: Partial<MeshBoardTaskAssignment> & Pick<MeshBoardTaskAssignment, 'id' | 'participant_id'>): MeshBoardTaskAssignment {
  return { state: 'assigned', assigned_by: 'user', assigned_seq: 1, ...overrides }
}

export function typedTaskView(overrides: Partial<MeshBoardTask> & Pick<MeshBoardTask, 'key' | 'title'>): MeshBoardTask {
  return {
    author_id: 'user',
    version: 1,
    generation: 1,
    assignments: [],
    created_seq: 1,
    updated_seq: 1,
    created_at: '2026-10-22T00:00:00Z',
    updated_at: '2026-10-22T00:00:00Z',
    status: 'todo',
    ...overrides,
  }
}
