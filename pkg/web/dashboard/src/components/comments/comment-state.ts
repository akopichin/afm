// Чистое copy-on-write состояние комментариев одного рана: plan/question/file-черновики живут здесь,
// а не в локальном state компонентов, поэтому переживают переключение вкладок, стадий и размонтирование Files.

// CommentAnchor — к чему привязан комментарий. Plan/question-ключ строится из якоря (commentKey), у файловых
// комментариев стабильный ID: root/path/line редактируемы (re-anchor), а ETag — лишь поле якоря.
export type CommentAnchor =
  | { kind: 'plan'; stageId: string; document: string; version: string; line: number }
  | { kind: 'question'; stageId: string; phase: string; questionId: string; line: number | null }
  | { kind: 'file'; root: string; path: string; displayPath: string; reference: string; etag: string; line: number }

export type CommentEntry = {
  key: string
  anchor: CommentAnchor
  quote: string // исходная строка, к которой привязан комментарий
  text: string // текущий ввод — пишется на каждый onChange
  saved: string // текст последнего Save
  editorOpen: boolean
  recipientId: string
  revision: number // из общего счётчика состояния: уникальна и после удаления с повторным созданием ключа
}

export type CommentState = {
  epoch: number // эпоха рана: подтверждение от прошлого рана не должно трогать текст нового
  entries: ReadonlyMap<string, CommentEntry>
  lastRecipientId: string
  revisionClock: number // последняя выданная ревизия; не уменьшается, поэтому Discard и новый ввод не совпадут
}

export type CommentInit = { anchor: CommentAnchor; quote: string }
export type SentRevision = { key: string; revision: number }
export type Reanchor = { line: number; quote: string; etag: string }

export function emptyCommentState(epoch: number): CommentState {
  return { epoch, entries: new Map(), lastRecipientId: '', revisionClock: 0 }
}

// commentKey — ключ plan/question-комментария; JSON-кортеж исключает ложные коллизии между полями.
// Файловые комментарии получают ID от вызывающего (файл может уехать на другую строку).
export function commentKey(anchor: CommentAnchor): string {
  switch (anchor.kind) {
    case 'plan':
      return JSON.stringify(['plan', anchor.stageId, anchor.document, anchor.version, anchor.line])
    case 'question':
      return JSON.stringify(['question', anchor.stageId, anchor.phase, anchor.questionId, anchor.line])
    case 'file':
      throw new Error('file comments use a caller-provided id')
  }
}

export function isDraft(entry: CommentEntry): boolean {
  return entry.text !== entry.saved
}

export function listComments(state: CommentState): CommentEntry[] {
  return Array.from(state.entries.values())
}

function withEntry(state: CommentState, entry: CommentEntry): CommentState {
  return { ...state, entries: new Map(state.entries).set(entry.key, entry) }
}

// withNewRevision — записывает запись с новой ревизией из общего счётчика.
function withNewRevision(state: CommentState, entry: CommentEntry): CommentState {
  const revision = state.revisionClock + 1
  return { ...withEntry(state, { ...entry, revision }), revisionClock: revision }
}

// openComment — создаёт пустую форму или снова открывает свёрнутый комментарий с его текстом.
export function openComment(state: CommentState, key: string, init: CommentInit): CommentState {
  const existing = state.entries.get(key)
  if (existing !== undefined) return withEntry(state, { ...existing, editorOpen: true })
  return withNewRevision(state, {
    key,
    anchor: init.anchor,
    quote: init.quote,
    text: '',
    saved: '',
    editorOpen: true,
    recipientId: state.lastRecipientId,
    revision: 0,
  })
}

export function editComment(state: CommentState, key: string, text: string): CommentState {
  const entry = state.entries.get(key)
  if (entry === undefined || entry.text === text) return state
  return withNewRevision(state, { ...entry, text })
}

export function saveComment(state: CommentState, key: string): CommentState {
  const entry = state.entries.get(key)
  if (entry === undefined || entry.saved === entry.text) return state
  return withEntry(state, { ...entry, saved: entry.text })
}

// foldComment — крестик/Escape: форма закрывается. Пустой текст (в т.ч. из одних пробелов) не стирает
// сохранённый комментарий — возвращается saved; без saved пустая запись исчезает совсем, иначе невидимый
// черновик не попал бы ни в карточки, ни в счётчик, но блокировал бы строку.
export function foldComment(state: CommentState, key: string): CommentState {
  const entry = state.entries.get(key)
  if (entry === undefined) return state
  if (entry.text.trim() !== '') return withEntry(state, { ...entry, editorOpen: false })
  if (entry.saved === '') return discardComment(state, key)
  return withNewRevision(state, { ...entry, text: entry.saved, editorOpen: false })
}

// foldFileComments — уход из файла: открытые формы этого файла сворачиваются в карточки (несохранённый текст — Draft).
export function foldFileComments(state: CommentState, root: string, path: string): CommentState {
  let next = state
  for (const entry of state.entries.values()) {
    const { anchor } = entry
    if (entry.editorOpen && anchor.kind === 'file' && anchor.root === root && anchor.path === path) {
      next = foldComment(next, entry.key)
    }
  }
  return next
}

// saveAndFoldComment — кнопка Save/Add: сохраняет непустой текст и сворачивает форму; пустой Save ничего
// не сохраняет и не удаляет — удаление остаётся явным Delete/Discard.
export function saveAndFoldComment(state: CommentState, key: string): CommentState {
  const entry = state.entries.get(key)
  if (entry === undefined) return state
  return foldComment(entry.text.trim() === '' ? state : saveComment(state, key), key)
}

export function discardComment(state: CommentState, key: string): CommentState {
  if (!state.entries.has(key)) return state
  const entries = new Map(state.entries)
  entries.delete(key)
  return { ...state, entries }
}

// reanchorComment — перепривязка файлового комментария к другой строке/версии: ID и текст остаются.
// Занятую строку того же файла не перетираем: объединение комментариев — явный выбор пользователя.
export function reanchorComment(state: CommentState, key: string, target: Reanchor): CommentState {
  const entry = state.entries.get(key)
  if (entry === undefined || entry.anchor.kind !== 'file') return state
  const { root, path } = entry.anchor
  const occupied = listComments(state).some(
    (other) =>
      other.key !== key &&
      other.anchor.kind === 'file' &&
      other.anchor.root === root &&
      other.anchor.path === path &&
      other.anchor.line === target.line,
  )
  if (occupied) return state
  return withNewRevision(state, {
    ...entry,
    anchor: { ...entry.anchor, line: target.line, etag: target.etag },
    quote: target.quote,
  })
}

export function setCommentRecipient(state: CommentState, key: string, recipientId: string): CommentState {
  const entry = state.entries.get(key)
  if (entry === undefined) return state
  return { ...withEntry(state, { ...entry, recipientId }), lastRecipientId: recipientId }
}

// rememberRecipient — только умолчание для новых комментариев (Send all): записи и ревизии не трогаем.
export function rememberRecipient(state: CommentState, recipientId: string): CommentState {
  return state.lastRecipientId === recipientId ? state : { ...state, lastRecipientId: recipientId }
}

// acknowledgeSent — после принятия сервером удаляет только отправленные ревизии: если пользователь продолжил
// печатать во время запроса, ревизия выросла и свежий текст остаётся.
export function acknowledgeSent(state: CommentState, epoch: number, sent: readonly SentRevision[]): CommentState {
  if (state.epoch !== epoch) return state
  const entries = new Map(state.entries)
  for (const { key, revision } of sent) {
    if (entries.get(key)?.revision === revision) entries.delete(key)
  }
  return { ...state, entries }
}
