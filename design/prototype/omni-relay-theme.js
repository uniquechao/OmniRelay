/* Shared theme and local preview handoff. */
window.OmniRelayTheme = (() => {
  const root = document.documentElement;
  const key = 'omni-relay.theme';
  const eventType = 'omni-relay:theme';
  const valid = theme => theme === 'light' || theme === 'dark';
  const targetOrigin = location.protocol === 'file:' ? '*' : location.origin;
  const url = new URL(location.href);
  let saved;
  try { saved = localStorage.getItem(key); } catch {}
  root.dataset.theme = [url.searchParams.get('theme'), saved].find(valid) ?? 'light';

  function decorate(href) {
    const destination = new URL(href, location.href);
    destination.searchParams.set('theme', root.dataset.theme);
    return destination.href;
  }

  function syncControls() {
    const dark = root.dataset.theme === 'dark';
    for (const button of document.querySelectorAll('[data-theme-choice]')) {
      button.setAttribute('aria-pressed',String(button.dataset.themeChoice === root.dataset.theme));
    }
    for (const button of document.querySelectorAll('[data-theme-toggle]')) {
      button.textContent = dark ? '浅色' : '深色';
      button.setAttribute('aria-label',dark ? '切换浅色主题' : '切换深色主题');
      button.setAttribute('aria-pressed',String(dark));
    }
    for (const link of document.querySelectorAll('a[data-theme-link]')) link.href = decorate(link.href);
  }

  function send(target, theme = root.dataset.theme) {
    target?.postMessage({ type:eventType, theme }, targetOrigin);
  }

  function apply(theme) {
    if (!valid(theme) || root.dataset.theme === theme) return;
    root.dataset.theme = theme;
    try { localStorage.setItem(key, theme); } catch {}
    if (url.searchParams.has('theme')) {
      const current = new URL(location.href);
      current.searchParams.set('theme',theme);
      try { history.replaceState(history.state,'',current); } catch {}
    }
    syncControls();
    document.dispatchEvent(new Event(eventType));
    document.querySelectorAll('iframe[data-theme-frame]').forEach(frame => send(frame.contentWindow));
  }

  document.addEventListener('click',event => {
    const control = event.target.closest('[data-theme-toggle],[data-theme-choice]');
    if (control) {
      const theme = control.dataset.themeChoice ?? (root.dataset.theme === 'dark' ? 'light' : 'dark');
      apply(theme);
      if (window.parent !== window) send(window.parent,theme);
    }
    const link = event.target.closest('a[data-theme-link]');
    if (link) link.href = decorate(link.href);
  });

  document.addEventListener('load',event => {
    if (event.target instanceof HTMLIFrameElement && event.target.hasAttribute('data-theme-frame')) send(event.target.contentWindow);
  },true);

  addEventListener('message',event => {
    if (event.data?.type !== eventType || !valid(event.data.theme)) return;
    if (location.protocol !== 'file:' && event.origin !== location.origin) return;
    const fromParent = window.parent !== window && event.source === window.parent;
    const fromPreview = [...document.querySelectorAll('iframe[data-theme-frame]')].some(frame => frame.contentWindow === event.source);
    if (fromParent || fromPreview) apply(event.data.theme);
  });

  document.addEventListener('DOMContentLoaded',syncControls);
  return { href:decorate, eventType };
})();
