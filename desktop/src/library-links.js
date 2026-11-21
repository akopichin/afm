import { randomUUID } from 'node:crypto';

const LINK_LIMIT = 8192;
export const isLibraryLink = value => typeof value === 'string' && /^afm:/i.test(value);

export function parseLibraryLink(value) {
  const invalid = () => Error('Use an AFM Library link with one Git source.');
  if (!isLibraryLink(value) || value.length > LINK_LIMIT || !/^afm:\/\/library\/install\?/i.test(value) || /[\r\n#]/.test(value)) throw invalid();
  const url = new URL(value);
  const params = [...url.searchParams];
  if (params.length !== 1 || params[0][0] !== 'source') throw invalid();
  return gitSource(params[0][1]);
}

function gitSource(input) {
  const invalid = () => Error('Use an HTTPS or SSH Git source without embedded credentials. Local paths are unavailable in AFM links.');
  if (!input || /[\s\p{Cc}\\?#]|%(?![a-f0-9]{2})|%(?:00|0a|0d)/iu.test(input)) throw invalid();
  const source = /^[a-z0-9][a-z0-9.-]*\.[a-z0-9-]+\//i.test(input) ? 'https://' + input : input;
  // Split after the authority, where AFM recognizes //subdirectory and @ref.
  const transport = /^(https|ssh):\/\/([^/]+)(\/.+)$/.exec(source);
  const scp = /^(git@[a-zA-Z0-9][a-zA-Z0-9.-]*:)(.+)$/.exec(source);
  if (!transport && !scp) throw invalid();
  const base = transport ? transport[1] + '://' + transport[2] : scp[1];
  let suffix = transport ? transport[3] : scp[2];
  const refStart = suffix.lastIndexOf('@');
  const ref = refStart < 0 ? '' : suffix.slice(refStart + 1);
  if (refStart >= 0 && (!ref || ref.startsWith('-'))) throw invalid();
  if (refStart >= 0) suffix = suffix.slice(0, refStart);
  const dirStart = suffix.indexOf('//');
  const subdir = dirStart < 0 ? '' : suffix.slice(dirStart + 2);
  if (dirStart >= 0 && (!subdir || subdir.startsWith('/') || subdir.split('/').includes('..'))) throw invalid();
  const repoPath = dirStart < 0 ? suffix : suffix.slice(0, dirStart);
  if (!repoPath.replace(/^\/+|\/+$/g, '') || repoPath.startsWith('-')) throw invalid();
  const repository = base + repoPath;
  if (transport) {
    const authority = transport[2];
    if (authority.includes('@') && (transport[1] === 'https' || !/^[\w.-]+@[^@]+$/.test(authority))) throw invalid();
    try { if (!new URL(repository).hostname) throw invalid(); }
    catch { throw invalid(); }
  }
  return { source, repository, subdir, ref };
}

export class LibraryLinks {
  #current = null;

  constructor(addSource) { this.addSource = addSource; }

  receive(value) {
    if (!isLibraryLink(value)) return false;
    let review;
    try { review = parseLibraryLink(value); }
    catch (error) { review = { error: error.message }; }
    if (!review.source || review.source !== this.#current?.review.source) this.#current = { review: { id: randomUUID(), ...review } };
    return true;
  }

  current() { return this.#current?.review ?? null; }

  dismiss(id) {
    const link = this.#current;
    if (link && link.review.id === id && !link.install) this.#current = null;
  }

  async confirm(id) {
    const link = this.#current;
    if (!link || link.review.id !== id || !link.review.source) throw Error('This Library link is no longer available.');
    link.install ??= Promise.resolve().then(() => this.addSource({ location: link.review.source }));
    try {
      const source = await link.install;
      if (!source.error && this.#current === link) this.#current = null;
      return source;
    } finally {
      link.install = null;
    }
  }
}
