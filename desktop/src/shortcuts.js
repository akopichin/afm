const ALT_ACTIONS = { KeyA: 'assistant', KeyL: 'live' };
const PRIMARY_ACTIONS = { KeyH: 'home', KeyJ: 'next', KeyK: 'previous', KeyM: 'model', KeyD: 'changes' };
const SHIFT_ACTIONS = { KeyK: 'picker', KeyC: 'connect-run' };

// Window-wide shortcuts that native views forward to the Desktop shell.
// Option/Alt is matched by physical key because Option changes the typed character on a Mac.
// Command (Mac) or Control (elsewhere) is the only primary modifier, so Control-K stays an editing key on a Mac.
export function shortcutAction(input, platform = process.platform) {
  if (input.type !== 'keyDown' || input.isAutoRepeat || input.isComposing) return null;
  if (input.alt && !input.control && !input.meta && !input.shift) return ALT_ACTIONS[input.code] ?? null;
  const primary = platform === 'darwin' ? input.meta && !input.control : input.control && !input.meta;
  if (!primary || input.alt) return null;
  const key = String(input.key).toLowerCase();
  if (input.shift) return key === 'enter' ? 'launch' : SHIFT_ACTIONS[input.code] ?? null;
  if (key === ',') return 'settings';
  if (/^[1-9]$/.test(key)) return Number(key);
  return PRIMARY_ACTIONS[input.code] ?? null;
}

// These bindings belong to Desktop even when the AFM page owns keyboard focus.
export function dashboardShortcutMessage(input, platform = process.platform) {
  const action = shortcutAction(input, platform);
  if (action === 'picker') return 'switch-project';
  if (action !== null && action !== 'launch') return { navigate: action };
  return null;
}
