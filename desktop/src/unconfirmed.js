// A question whose dialog could not be read has no request identity yet. Seen at the start of an observation,
// it is an already-open question, so its first identity is seeded silently instead of announced as new.
export const stageKey = item => [item.runKey, item.stageId, item.kind].join('|');

export const unconfirmedKeys = items => new Set(items.filter(item => !item.requestAt).map(stageKey));

// Keys that are still without identity; a stage that got one, or went away, stops being special.
export const stillUnconfirmed = (keys, items) => {
  const current = unconfirmedKeys(items);
  return new Set([...keys].filter(key => current.has(key)));
};
