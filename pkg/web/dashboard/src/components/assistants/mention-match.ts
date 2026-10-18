// mention-match — pure text helpers for the `@mention` composer menu (dev-plan.md T4.2). Deliberately NOT a
// hook/component: these are easy to unit-test in isolation, and the menu/composer just call them.

export type MentionTrigger = {
  // start — index of the '@' that opened the in-progress mention.
  start: number
  // query — text typed after '@' so far (never contains whitespace — a space ends the attempt).
  query: string
}

// detectMentionTrigger looks backward from the caret for an in-progress "@word" immediately before it. The
// '@' must sit at the very start of the text or be preceded by whitespace — this is what keeps
// "user@example.com" from opening the menu (that '@' is preceded by "r", not whitespace/start).
export function detectMentionTrigger(value: string, caret: number): MentionTrigger | null {
  const upTo = value.slice(0, caret)
  const at = upTo.lastIndexOf('@')
  if (at === -1) return null
  const query = upTo.slice(at + 1)
  if (/\s/.test(query)) return null // a space already ended this mention attempt
  const before = at === 0 ? '' : upTo.charAt(at - 1)
  if (before !== '' && !/\s/.test(before)) return null
  return { start: at, query }
}

// filterMentionProfiles matches a typed query against a profile's id or title, case-insensitively. An empty
// query (just typed '@') matches everything, so the menu opens showing every configured role.
export function filterMentionProfiles<T extends { id: string; title: string; handle?: string }>(profiles: T[], query: string): T[] {
  if (query === '') return profiles
  const q = query.toLowerCase()
  return profiles.filter(
    (p) => p.id.toLowerCase().includes(q) || p.title.toLowerCase().includes(q) || (p.handle?.toLowerCase().includes(q) ?? false),
  )
}

// MentionGroup — a labeled section of mention candidates. The feed composer's `@` menu (dev-plan.md T7.4)
// needs several sections ("This run" / "This stage" / "Attach to stage"); the side-agent composer keeps using
// one unlabeled group (AssistantMentionMenu wraps filterMentionProfiles directly, unchanged).
export type MentionGroup<T extends { id: string; title: string }> = {
  label: string
  items: T[]
}

// filterMentionGroups applies filterMentionProfiles to each group independently and drops any group the query
// leaves empty, so a narrowing query can make a whole section (e.g. "Attach to stage") disappear instead of
// rendering an empty heading.
export function filterMentionGroups<T extends { id: string; title: string }>(
  groups: MentionGroup<T>[],
  query: string,
): MentionGroup<T>[] {
  return groups
    .map((g) => ({ label: g.label, items: filterMentionProfiles(g.items, query) }))
    .filter((g) => g.items.length > 0)
}

// draftMentions reports whether the draft still contains `@handle` as a whole token. Recipients picked with
// `@` are derived from the text at send time, so deleting the mention from the draft also un-addresses it.
export function draftMentions(draft: string, handle: string): boolean {
  if (handle === '') return false
  const escaped = handle.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp(`(^|\\s)@${escaped}(?=$|[\\s.,;:!?)])`, 'i').test(draft)
}
