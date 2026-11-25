type LoadingOptions={size?:'page'|'compact'|'inline';label?:string;progress?:number|null;active?:boolean}
let sequence = 0;
const buttonStates = new WeakMap<HTMLButtonElement,{html:string;busy:string|null;label?:string}>();
const mounts = new WeakMap<Element,HTMLElement>();
const escape = (value:unknown) => String(value).replace(/[&<>"']/g, character =>
  ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[character] ?? character));
const logo = (id:string) => `<svg aria-hidden="true" focusable="false" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 646 585"><defs>
        <linearGradient id="violetFacetrace__ID__" x1="0%" y1="0%" x2="84%" y2="78%">
          <stop stop-color="#bd64ff"/><stop offset=".38" stop-color="#7444fa"/><stop offset="1" stop-color="#452ee7"/>
        </linearGradient>
        <linearGradient id="tealFacetrace__ID__" x1="20%" y1="28%" x2="90%" y2="88%">
          <stop stop-color="#008e89"/><stop offset=".5" stop-color="#09b7b6"/><stop offset="1" stop-color="#00e2e9"/>
        </linearGradient>
      <mask id="violetInk__ID__" maskUnits="userSpaceOnUse" x="0" y="0" width="646" height="585"><path class="ink violet-ink" d="M 509 200 Q 458 150 419 150 H 222 Q 191 150 169 180 L 117 249 Q 83 294 112 333 L 143 367" fill="none" stroke="white" stroke-width="175" stroke-linecap="round" pathLength="1" stroke-dasharray="1 2"/></mask><mask id="tealInk__ID__" maskUnits="userSpaceOnUse" x="0" y="0" width="646" height="585"><path class="ink teal-ink" d="M 150 356 Q 188 434 231 434 H 389 Q 452 434 485 391 L 535 328 Q 561 293 543 260 L 497 209" fill="none" stroke="white" stroke-width="175" stroke-linecap="round" pathLength="1" stroke-dasharray="1 2"/></mask></defs>
      <g class="orbit"><path class="arc violet" mask="url(#violetInk__ID__)" fill="url(#violetFacetrace__ID__)" d="M 101 400 L 76 367 Q 20 296 76 220 L 139 139 Q 168 103 222 103 H 420 Q 477 103 512 148 L 531 172 L 469 264 Q 442 201 386 201 H 261 Q 226 201 202 231 Q 164 275 178 317 Z"/>
      <path class="arc teal" mask="url(#tealInk__ID__)" fill="url(#tealFacetrace__ID__)" d="M 530.5 171.5 L 572 224 Q 624 292 574 361 L 509 445 Q 480 482 422 482 H 230 Q 171 482 137 446 L 100.5 399.5 L 177.5 316.5 Q 211 384 266 384 H 383 Q 427 384 450 348 Q 482 306 468.5 263.5 Z"/></g></svg>`.replaceAll('__ID__', id);
export function brandMark() {
  return '<span class="or-loading or-loading-brand" aria-hidden="true"><span class="or-loading-mark">' +
    logo('or-loading-' + ++sequence) + '</span></span>';
}
export function markup({ size = 'compact', label = '正在加载…', progress = null, active = true }:LoadingOptions = {}) {
  if (!['page','compact','inline'].includes(size)) size = 'compact';
  const id = 'or-loading-' + ++sequence;
  const known = typeof progress === 'number' && Number.isFinite(progress);
  const value = known ? Math.max(0,Math.min(100,progress)) : null;
  const inline = size === 'inline';
  return '<' + (inline ? 'span' : 'div') + ' class="or-loading" data-size="' + size +
    '" data-active="' + active + '"' + (inline ? ' aria-hidden="true"' : ' role="status" aria-live="polite"') + '>' +
    '<span class="or-loading-mark">' + logo(id) + '</span>' +
    (size === 'page' ? '<strong class="or-loading-wordmark" translate="no"><span>Omni</span><span>Relay</span></strong>' : '') +
    '<span class="or-loading-meter"><svg class="or-loading-track" viewBox="0 0 280 3" preserveAspectRatio="none"' +
    (inline ? ' aria-hidden="true"' : ' role="progressbar" aria-label="' + escape(label) +
      '" aria-valuemin="0" aria-valuemax="100"' + (known ? ' aria-valuenow="' + value + '"' : ' aria-valuetext="处理中"')) +
    ' data-indeterminate="' + !known + '"><defs><linearGradient id="' + id + '-bar"><stop stop-color="var(--or-brand-indigo)"/>' +
    '<stop offset="1" stop-color="var(--or-brand-teal)"/></linearGradient></defs><rect class="or-loading-track-bg" width="280" height="3" rx="1.5"/>' +
    '<rect class="or-loading-fill" width="' + (value===null ? 84 : value * 2.8) + '" height="3" rx="1.5" fill="url(#' + id + '-bar)"/></svg>' +
    (inline ? '' : '<span class="or-loading-meta"><span class="or-loading-label">' + escape(label) + '</span>' +
      (known ? '<span class="or-loading-percent" aria-hidden="true">' + Math.round(value ?? 0) + '%</span>' : '') + '</span>') +
    '</span></' + (inline ? 'span' : 'div') + '>';
}
export function mount(host:HTMLElement, options:LoadingOptions = {}) {
  host.innerHTML = markup(options);
  host.setAttribute('aria-busy',String(options.active !== false));
  const root = host.firstElementChild;
  if(!(root instanceof HTMLElement)) throw new Error('加载组件缺失');
  mounts.set(host,root);
  return {
    update({ label, progress, active }:LoadingOptions = {}) {
      if (label !== undefined) {
        const text = root.querySelector('.or-loading-label');
        if (text) text.textContent = label;
        root.querySelector('[role="progressbar"]')?.setAttribute('aria-label',label);
      }
      if (progress !== undefined) {
        const known = typeof progress === 'number' && Number.isFinite(progress);
        const value = known ? Math.max(0,Math.min(100,progress)) : null;
        const bar = root.querySelector<SVGElement>('.or-loading-track');
        if(!bar) return;
        bar.dataset.indeterminate = String(!known);
        bar.querySelector('.or-loading-fill')?.setAttribute('width',String(value===null ? 84 : value * 2.8));
        if (value!==null) { bar.setAttribute('aria-valuenow',String(value)); bar.removeAttribute('aria-valuetext'); }
        else { bar.removeAttribute('aria-valuenow'); bar.setAttribute('aria-valuetext','处理中'); }
        let percent = root.querySelector('.or-loading-percent');
        if (!known) percent?.remove();
        else {
          if (!percent) {
            percent = document.createElement('span'); percent.className = 'or-loading-percent';
            percent.setAttribute('aria-hidden','true'); root.querySelector('.or-loading-meta')?.append(percent);
          }
          percent.textContent = Math.round(value ?? 0) + '%';
        }
      }
      if (active !== undefined) {
        root.dataset.active = String(active); host.setAttribute('aria-busy',String(active));
      }
    },
    finish() {
      root.remove();
      if (mounts.get(host) === root) { host.removeAttribute('aria-busy'); mounts.delete(host); }
    }
  };
}
export function button(button:HTMLButtonElement, pending:boolean, label?:string) {
  let state = buttonStates.get(button);
  if (pending) {
    if (!state) { state = { html:button.innerHTML, busy:button.getAttribute('aria-busy') }; buttonStates.set(button,state); }
    const text = label || button.textContent.trim();
    if (state.label !== text) {
      button.innerHTML = markup({size:'inline',label:text}) + '<span>' + escape(text) + '</span>';
      state.label = text;
    }
    button.setAttribute('aria-busy','true');
    button.classList.add('or-loading-button');
  } else {
    if (state) {
      button.innerHTML = state.html;
      if (state.busy === null) button.removeAttribute('aria-busy'); else button.setAttribute('aria-busy',state.busy);
      buttonStates.delete(button);
    }
    button.classList.remove('or-loading-button');
    if (label !== undefined) button.textContent = label;
  }
}
// Observe only loading roots; no perpetual frame callback or detached-node references.
if ('IntersectionObserver' in window) {
  const visibility = new IntersectionObserver(entries => entries.forEach(({target,isIntersecting}) => {
    if(target instanceof HTMLElement)target.dataset.loadingVisible = String(isIntersecting);
  }));
  const roots = (node:Node) => node instanceof Element
    ? [...(node.matches('.or-loading') ? [node] : []),...node.querySelectorAll('.or-loading')] : [];
  const mutations = new MutationObserver(records => records.forEach(record => {
    record.removedNodes.forEach(node => roots(node).forEach(root => visibility.unobserve(root)));
    record.addedNodes.forEach(node => roots(node).forEach(root => visibility.observe(root)));
  }));
  mutations.observe(document.documentElement,{childList:true,subtree:true});
}
const documentMotion = () => document.documentElement.toggleAttribute('data-loading-paused',document.hidden);
document.addEventListener('visibilitychange',documentMotion);
documentMotion();
