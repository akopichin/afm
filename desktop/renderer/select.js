// Older WebKit draws an OS menu even when the closed select uses the theme.
(() => {
  if (CSS.supports('appearance: base-select') && CSS.supports('selector(::picker(select))')) return;
  const selector = 'select:not([multiple], [size], :disabled)';
  let picker;
  function open(select) {
    if (picker?.matches(':popover-open')) picker.hidePopover();
    const menu = document.createElement('div');
    menu.popover = 'auto';
    menu.className = 'graphite-picker';
    menu.setAttribute('role', 'listbox');
    const label = select.labels[0]?.cloneNode(true);
    label?.querySelector('select')?.remove();
    menu.setAttribute('aria-label', select.getAttribute('aria-label') || label?.textContent.trim() || 'Options');
    const choices = [];
    for (const option of select.options) {
      if (option.hidden) continue;
      const button = document.createElement('button');
      button.type = 'button';
      button.tabIndex = -1;
      button.textContent = option.label;
      button.disabled = option.disabled || option.parentElement.matches('optgroup:disabled');
      button.setAttribute('role', 'option');
      button.setAttribute('aria-selected', String(option.selected));
      button.onclick = () => {
        const changed = select.selectedIndex !== option.index;
        select.selectedIndex = option.index;
        menu.hidePopover();
        select.focus();
        if (changed) {
          select.dispatchEvent(new Event('input', { bubbles: true }));
          select.dispatchEvent(new Event('change', { bubbles: true }));
        }
      };
      menu.append(button);
      if (!button.disabled) choices.push(button);
    }
    if (!choices.length) return;
    const rect = select.getBoundingClientRect();
    const below = innerHeight - rect.bottom - 8;
    const above = rect.top - 8;
    const upwards = below < Math.min(280, menu.children.length * 32 + 10) && above > below;
    menu.style.width = rect.width + 'px';
    menu.style.maxHeight = Math.min(280, upwards ? above : below) + 'px';
    menu.style.left = rect.left + 'px';
    if (upwards) menu.style.bottom = innerHeight - rect.top + 4 + 'px';
    else menu.style.top = rect.bottom + 4 + 'px';
    (select.closest('dialog') || document.body).append(menu);
    picker = menu;
    let prefix = '', typedAt = 0;
    menu.onkeydown = event => {
      const index = choices.indexOf(document.activeElement);
      let next;
      if (event.key === 'ArrowDown') next = choices[Math.min(index + 1, choices.length - 1)];
      else if (event.key === 'ArrowUp') next = choices[Math.max(0, index - 1)];
      else if (event.key === 'Home') next = choices[0];
      else if (event.key === 'End') next = choices.at(-1);
      else if (event.key === 'Escape' || event.key === 'Tab') {
        menu.hidePopover();
        select.focus();
        if (event.key === 'Tab') return;
      } else if (event.key.length === 1 && event.key !== ' ' && !event.ctrlKey && !event.metaKey && !event.altKey) {
        prefix = (Date.now() - typedAt < 650 ? prefix : '') + event.key.toLowerCase();
        typedAt = Date.now();
        next = choices.find(b => b.textContent.toLowerCase().startsWith(prefix));
      } else return;
      event.preventDefault();
      event.stopPropagation();
      next?.focus();
    };
    menu.addEventListener('toggle', event => {
      if (event.newState === 'closed') {
        menu.remove();
        if (picker === menu) {
          picker = null;
          select.removeAttribute('aria-expanded');
        }
      }
    });
    menu.showPopover();
    select.setAttribute('aria-expanded', 'true');
    (choices.find(b => b.getAttribute('aria-selected') === 'true') || choices[0]).focus();
  }
  document.addEventListener('pointerdown', event => {
    if (event.button !== 0 || !event.target.matches(selector)) return;
    event.preventDefault();
    event.target.focus();
  });
  document.addEventListener('click', event => {
    if (!event.target.matches(selector)) return;
    event.preventDefault();
    open(event.target);
  });
  document.addEventListener('keydown', event => {
    if (!event.target.matches(selector) || ![' ', 'Enter', 'ArrowDown', 'ArrowUp'].includes(event.key)) return;
    event.preventDefault();
    open(event.target);
  });
})();
