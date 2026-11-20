/* Shared theme and local preview handoff. */
window.OmniRelayTheme = (() => {
  const root = document.documentElement;
  const key = 'omni-relay.theme';
  const eventType = 'omni-relay:theme';
  const icons = {
    light: '<svg class="icon theme-moon" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20.7 14.2A9 9 0 0 1 9.8 3.3a9 9 0 1 0 10.9 10.9Z"/></svg>',
    dark: '<svg class="icon theme-sun" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4m0-14.2-1.4 1.4M6.3 17.7l-1.4 1.4"/></svg>'
  };
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
      const label = dark ? '切换浅色主题' : '切换深色主题';
      if (button.hasAttribute('data-theme-icon')) {
        button.innerHTML = icons[root.dataset.theme];
        button.title = label;
      } else {
        button.textContent = dark ? '浅色' : '深色';
      }
      button.setAttribute('aria-label',label);
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

  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  let transition;
  let pendingTheme;

  function clearReveal() {
    delete root.dataset.themeReveal;
    for (const name of ['--theme-reveal-x','--theme-reveal-y','--theme-reveal-end']) {
      root.style.removeProperty(name);
    }
  }

  function prepareReveal(control) {
    if (!control.hasAttribute('data-theme-icon') || !window.CSS?.registerProperty) return;
    const { left, top, width, height } = control.getBoundingClientRect();
    const x = left + width / 2;
    const y = top + height / 2;
    const reach = Math.hypot(Math.max(x,innerWidth - x),Math.max(y,innerHeight - y));
    root.style.setProperty('--theme-reveal-x',x + 'px');
    root.style.setProperty('--theme-reveal-y',y + 'px');
    root.style.setProperty('--theme-reveal-end',Math.ceil(reach + 40) + 'px');
    root.dataset.themeReveal = '';
  }

  function choose(theme, control) {
    transition?.skipTransition();
    transition = undefined;
    clearReveal();
    const update = () => {
      apply(theme);
      if (window.parent !== window) send(window.parent,theme);
    };
    if (!control.hasAttribute('data-theme-toggle') || reducedMotion.matches || !document.startViewTransition || theme === root.dataset.theme) {
      pendingTheme = undefined;
      update();
      return;
    }
    pendingTheme = theme;
    prepareReveal(control);
    const next = document.startViewTransition(() => {
      if (transition === next) update();
    });
    transition = next;
    const finish = () => {
      if (transition !== next) return;
      transition = undefined;
      pendingTheme = undefined;
      clearReveal();
    };
    next.finished.then(finish,finish);
  }

  document.addEventListener('click',event => {
    const control = event.target.closest('[data-theme-toggle],[data-theme-choice]');
    if (control) {
      const current = pendingTheme ?? root.dataset.theme;
      const theme = control.dataset.themeChoice ?? (current === 'dark' ? 'light' : 'dark');
      choose(theme,control);
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
  return { href:decorate, eventType, syncControls };
})();
