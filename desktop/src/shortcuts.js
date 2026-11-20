const ALT_ACTIONS = { KeyA: 'assistant', KeyM: 'model', KeyH: 'home', KeyJ: 'next', KeyK: 'previous', KeyL: 'live' };

// The window-wide shortcuts for native views Desktop does not own the DOM of (the Live tab).
// Option/Alt is matched by physical key because Option changes the typed character on a Mac.
// Command (Mac) or Control (elsewhere) is the only primary modifier, so Control-K stays an editing key on a Mac.
export function shortcutAction(input, platform = process.platform) {
  if (input.type !== 'keyDown' || input.isAutoRepeat || input.isComposing) return null;
  if (input.alt && !input.control && !input.meta && !input.shift) return ALT_ACTIONS[input.code] ?? null;
  const primary = platform === 'darwin' ? input.meta && !input.control : input.control && !input.meta;
  if (!primary || input.alt) return null;
  const key = String(input.key).toLowerCase();
  if (input.shift) return key === 'enter' ? 'launch' : null;
  if (key === ',') return 'settings';
  return key === 'k' ? 'picker' : null;
}
