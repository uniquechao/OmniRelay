import {closestControl,requiredElement} from './dom.ts'
import type {DialogOptions,Person,Tone} from './components/runtime'
const tipLevels={info:'信息',success:'成功',warning:'警告',error:'错误'}
const tipTimers=new WeakMap<Element,number>()
let dialogNumber=0
export function escape(value:unknown) {
  return String(value).replace(/[&<>"']/g, char => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  })[char] ?? char);
}

const avatarTones = ['indigo','teal','plum','amber','blue'];

export function fileSourceLocator(fileId:string, group:string, route:string, ordinal:number) {
  const name = escape(group);
  const label = '定位原消息：' + name + '（来源 ' + ordinal + '）';
  return '<div class="drive-source-row"><strong>' + name + '</strong>' +
    '<a class="drive-source drive-source-locate" data-source-file="' + escape(fileId) + '" href="' + escape(route) +
    '" aria-label="' + label + '" title="' + label + '">' +
    '<svg aria-hidden="true" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">' +
    '<circle cx="12" cy="12" r="7"/><circle cx="12" cy="12" r="2"/><path d="M12 2v3m0 14v3M2 12h3m14 0h3"/></svg></a></div>';
}

export function personAvatar(label:string, id = label) {
  const identity = id.normalize('NFKC').trim();
  let hash = 2166136261;
  for (const character of identity) hash = Math.imul(hash ^ (character.codePointAt(0) ?? 0),16777619);
  const tone = avatarTones[(hash >>> 0) % avatarTones.length];
  return '<span class="person-avatar" data-avatar-tone="' + tone + '" aria-hidden="true">' +
    escape([...label].slice(-2).join('')) + '</span>';
}

function personDetails(person:Person) {
  return {
    name: person.name?.trim() || '未知人员',
    key: person.person_key ?? person.personKey,
    org: person.organization_paths?.map(path => path.join(' / ')).join('；') ?? person.org ?? '',
    source: person.source === 'feishu' ? '飞书' : person.source ?? ''
  };
}

export function personButton(person:Person, content:string, className = '') {
  const { name, key, org, source } = personDetails(person);
  const resigned = person.resigned === true;
  if (resigned && className === 'person-name') content += '<small class="person-resigned">离职</small>';
  return '<button type="button" class="person-button ' + escape(className) +
    '" data-person-name="' + escape(name) + '" data-person-key="' + escape(key ?? '') +
    '" data-person-org="' + escape(org) + '" data-person-source="' + escape(source) + '"' +
    (resigned ? ' data-person-resigned="true"' : '') +
    ' aria-haspopup="dialog" aria-expanded="false" aria-label="查看' + escape(name) + (resigned ? '（离职）' : '') +
    '的基本信息" title="' + escape(name) + '">' + content + '</button>';
}

function personOrganization(org?:string) {
  return org ? '<span class="person-org" title="' + escape(org) + '">' + escape(org) + '</span>' : '';
}

export function personView(person:Person) {
  const { name, key, org } = personDetails(person);
  const label = escape(name);
  return '<span class="person-view person-compact">' +
    personButton(person,personAvatar(name,key || name),'person-portrait') +
    '<span class="person-copy">' + personButton(person,label,'person-name') +
    personOrganization(org) + '</span></span>';
}

export function mountPersonCards(root:HTMLElement, {signal}:{signal?:AbortSignal} = {}) {
  let active:{card:HTMLDivElement;trigger:HTMLElement;close:()=>void}|undefined;
  function dismiss() {
    active?.close();
  }
  signal?.addEventListener('abort',dismiss,{ once:true });
  root.addEventListener('click',event => {
    const trigger = closestControl(event,'[data-person-name]');
    if (!trigger) return;
    const alreadyOpen = active?.card.matches(':popover-open') && active.trigger === trigger;
    dismiss();
    if (alreadyOpen) return;

    const { personName:name = "", personKey:key, personOrg:org, personSource:source } = trigger.dataset;
    const card = document.createElement('div');
    card.className = 'person-popover';
    card.popover = 'auto';
    card.tabIndex = -1;
    card.setAttribute('role','dialog');
    card.setAttribute('aria-label',name + '的基本信息');
    card.innerHTML = '<div class="person-view">' + personAvatar(name,key || name) +
      '<div class="person-copy"><strong class="person-name" title="' + escape(name) + '">' + escape(name) +
      '</strong>' + (trigger.dataset.personResigned === 'true' ?
        '<small class="person-resigned">' + icon('status-danger') + '离职</small>' : '') +
      personOrganization(org) + '</div></div>' +
      (source ? '<p class="aux">身份来源　' + escape(source) + '</p>' : '');
    const ownerDialog = trigger.closest('dialog');
    (ownerDialog ?? root).append(card);

    const tracking = new AbortController();
    active = {
      card, trigger,
      close() {
        if (card.matches(':popover-open')) card.hidePopover();
        else if (!card.isConnected) {
          tracking.abort();
          observer.disconnect();
          active = undefined;
        }
      }
    };
    const viewport = window.visualViewport;
    const position = () => {
      if (!trigger.getClientRects().length) { dismiss(); return; }
      const anchor = trigger.getBoundingClientRect();
      const leftEdge = viewport?.offsetLeft ?? 0;
      const topEdge = viewport?.offsetTop ?? 0;
      const rightEdge = leftEdge + (viewport?.width ?? document.documentElement.clientWidth);
      const bottomEdge = topEdge + (viewport?.height ?? document.documentElement.clientHeight);
      const inset = 8;
      card.style.maxWidth = Math.max(0,rightEdge - leftEdge - inset * 2) + 'px';
      card.style.maxHeight = Math.max(0,bottomEdge - topEdge - inset * 2) + 'px';
      const width = card.offsetWidth;
      const height = card.offsetHeight;
      const below = anchor.bottom + 6;
      const above = anchor.top - height - 6;
      const top = below + height <= bottomEdge - inset ? below : above;
      card.style.left = Math.max(leftEdge + inset, Math.min(anchor.left,rightEdge - width - inset)) + 'px';
      card.style.top = Math.max(topEdge + inset, Math.min(top,bottomEdge - height - inset)) + 'px';
    }

    const observer = new MutationObserver(() => {
      if (!root.contains(trigger)) dismiss();
    });
    observer.observe(root,{ childList:true, subtree:true });
    window.addEventListener('resize',position,{ signal:tracking.signal });
    window.addEventListener('scroll',position,{ capture:true, signal:tracking.signal });
    window.addEventListener('hashchange',dismiss,{ signal:tracking.signal });
    ownerDialog?.addEventListener('close',dismiss,{ signal:tracking.signal });
    viewport?.addEventListener('resize',position,{ signal:tracking.signal });
    viewport?.addEventListener('scroll',position,{ signal:tracking.signal });
    card.addEventListener('beforetoggle',event => {
      if (event.newState !== 'closed') return;
      tracking.abort();
      observer.disconnect();
      trigger.setAttribute('aria-expanded','false');
    });
    card.addEventListener('toggle',event => {
      if (event.newState !== 'closed') return;
      card.remove();
      if (active?.card !== card) return;
      active = undefined;
      const focusedOverlay = document.activeElement?.closest(':popover-open, dialog[open]');
      if (!signal?.aborted && trigger.getClientRects().length && (!focusedOverlay || focusedOverlay === ownerDialog)) {
        trigger.focus({ preventScroll:true });
      }
    });

    card.showPopover({ source:trigger });
    position();
    trigger.setAttribute('aria-expanded','true');
    card.focus({ preventScroll:true });
  },{ signal });
  return dismiss;
}

export const icon = (name:string, className = 'icon') =>
  '<svg class="' + className + '" aria-hidden="true"><use href="#' + name + '"></use></svg>';

export function openDialog({title, description, body, variant = 'detail', tone = 'default',
  confirm, cancelLabel, discard, onSubmit, onClose, restoreFocus, drawer,isDirty}:DialogOptions):HTMLDialogElement {
  const opener = document.activeElement;
  const base = 'workspace-dialog-' + ++dialogNumber;
  const dialog = document.createElement('dialog');
  const symbol = tone === 'danger' ? 'status-danger' : tone === 'warning' ? 'status-warning' : 'nav-logs';
  const actionColor = tone === 'default' ? 'primary' : 'dialog-confirm-' + tone;
  dialog.className = 'or-dialog' + (drawer ? ' or-drawer' : '');
  if (drawer) dialog.dataset.drawerWidth = drawer;
  dialog.dataset.variant = variant;
  dialog.dataset.tone = tone;
  dialog.setAttribute('aria-labelledby',base + '-title');
  if (description) dialog.setAttribute('aria-describedby',base + '-description');
  dialog.innerHTML = `<form class="dialog-form" method="dialog">
    <header class="dialog-head">
      <span class="dialog-symbol">${icon(symbol)}</span>
      <div class="dialog-heading"><h2 id="${base}-title">${escape(title)}</h2>
        ${description ? '<p id="' + base + '-description">' + escape(description) + '</p>' : ''}
      </div>
      <button class="dialog-close" type="button" data-dismiss aria-label="关闭对话框">${icon('action-close')}</button>
    </header>
    <div class="dialog-body">${body}
      <p class="field-error" id="${base}-error" role="alert" hidden></p>
    </div>
    <footer class="dialog-footer">
      <button class="button dialog-secondary" type="button" data-dismiss${variant === 'form' ? '' : ' autofocus'}>${escape(cancelLabel || (confirm ? '取消' : '关闭'))}</button>
      ${discard ? '<button class="button dialog-secondary" type="button" data-discard>' + escape(discard) + '</button>' : ''}
      ${confirm ? '<button class="button ' + actionColor + '" type="submit" value="confirm">' + escape(confirm) + '</button>' : ''}
    </footer>
  </form>`;
  const form = requiredElement(dialog,'form',HTMLFormElement);
  const message = requiredElement(dialog,'.field-error',HTMLElement);
  let invalidField:HTMLElement|undefined;
  let guard:HTMLDialogElement|undefined;
  let closeAfterSubmit = false;
  let lastField:HTMLElement|undefined;
  requiredElement(dialog,'.dialog-body',HTMLElement).addEventListener('focusin',event => {
    if (event.target instanceof HTMLElement && event.target.matches('input, select, textarea')) lastField = event.target;
  });
  function requestClose() {
    if (!isDirty?.(form)) { dialog.close('cancel'); return; }
    if (guard?.open) return;
    const nextGuard = openDialog({
      title:'保存修改？', variant:'confirm', tone:'warning',
      body:'<p class="dialog-note">有未保存的修改，直接关闭会丢失本次更改。</p>',
      cancelLabel:'继续编辑', discard:'不保存', confirm:'保存并关闭',
      onClose(confirmed, result) {
        if (guard !== nextGuard) return;
        guard = undefined;
        if (!dialog.open) return;
        if (confirmed) {
          closeAfterSubmit = true;
          form.requestSubmit();
          closeAfterSubmit = false;
        }
        else if (result === 'discard') dialog.close('cancel');
        else {
          dialog.dispatchEvent(new Event('or-dialog-guard-cancel'));
          lastField?.focus();
        }
      }
    });
    guard = nextGuard;
  }
  const clearError = () => {
    if (!invalidField) return;
    invalidField.removeAttribute('aria-invalid');
    invalidField.removeAttribute('aria-describedby');
    message.hidden = true;
    invalidField = undefined;
  };
  form.addEventListener('input',clearError);
  form.addEventListener('reset',clearError);
  form.addEventListener('submit',async event => {
    event.preventDefault();
    if (!confirm && !onSubmit) return;
    clearError();
    const submission = onSubmit?.(form,event.submitter);
    const result = submission instanceof Promise ? await submission : submission;
    if (!dialog.open) return;
    if (result && 'keepOpen' in result) {
      if (closeAfterSubmit) dialog.close('confirm');
      return;
    }
    if (result) {
      invalidField = result.field;
      message.hidden = false;
      message.textContent = result.message;
      invalidField.setAttribute('aria-invalid','true');
      invalidField.setAttribute('aria-describedby',message.id);
      invalidField.focus();
      return;
    }
    dialog.close('confirm');
  });
  dialog.querySelectorAll('[data-dismiss]').forEach(button => {
    button.addEventListener('click',requestClose);
  });
  dialog.querySelector('[data-discard]')?.addEventListener('click',() => dialog.close('discard'));
  dialog.addEventListener('cancel',event => { event.preventDefault(); requestClose(); });
  if (drawer) {
    let startedOutside = false;
    const outside = (event:MouseEvent) => {
      const box = dialog.getBoundingClientRect();
      return event.target === dialog && (event.clientX < box.left || event.clientX >= box.right ||
        event.clientY < box.top || event.clientY >= box.bottom);
    };
    dialog.addEventListener('pointerdown',event => { startedOutside = outside(event); });
    dialog.addEventListener('pointercancel',() => { startedOutside = false; });
    dialog.addEventListener('click',event => { if (startedOutside && outside(event)) requestClose(); });
  }
  dialog.addEventListener('close',() => {
    const focused = document.activeElement;
    const ownsFocus = !focused || focused === document.body || focused === opener ||
      dialog.contains(focused) || Boolean(guard?.contains(focused));
    guard?.close();
    const confirmed = dialog.returnValue === 'confirm';
    dialog.remove();
    const beforeCallback = document.activeElement;
    onClose?.(confirmed,dialog.returnValue);
    if (ownsFocus && (document.activeElement === beforeCallback ||
        !document.activeElement || document.activeElement === document.body)) {
      restoreFocus?.(confirmed)?.focus();
    }
  },{once:true});
  document.body.append(dialog);
  dialog.showModal();
  return dialog;
}

export function toast(level:Tone, message:string) {
  const region = document.getElementById('toast-region');
  const item = document.createElement('aside');
  item.className = 'toast';
  item.dataset.level = level;
  item.innerHTML = '<strong aria-hidden="true">' + tipLevels[level] + '</strong>' +
    '<span>' + escape(message) + '</span><button type="button" aria-label="关闭提示">×</button>';
  const dismiss = () => { clearTimeout(tipTimers.get(item)); item.remove(); };
  const schedule = () => {
    clearTimeout(tipTimers.get(item));
    tipTimers.set(item, setTimeout(dismiss, 6000));
  };
  requiredElement(item,'button',HTMLButtonElement).addEventListener('click', dismiss);
  item.addEventListener('pointerenter', () => clearTimeout(tipTimers.get(item)));
  item.addEventListener('pointerleave', () => { if (!item.contains(document.activeElement)) schedule(); });
  item.addEventListener('focusin', () => clearTimeout(tipTimers.get(item)));
  item.addEventListener('focusout', () => { if (!item.matches(':hover')) schedule(); });
  if(!region)return;
  if (region.children.length >= 3) {
    const oldest = region.firstElementChild!;
    clearTimeout(tipTimers.get(oldest));
    oldest.remove();
  }
  region.append(item);
  const live = document.getElementById(level === 'error' ? 'toast-urgent' : 'toast-status');
  if(!live)return;
  live.textContent = '';
  requestAnimationFrame(() => { live.textContent = tipLevels[level] + '，' + message; });
  schedule();
}
