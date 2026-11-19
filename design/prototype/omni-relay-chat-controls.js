'use strict';

// Adapted from the OmniGrid prototype split pane.
function connectSplitPane(container, {storageKey, defaultRatio, minDirectory}) {
  let width;
  const handle = container.querySelector('.split-handle');
  if (storageKey) {
    try {
      width = JSON.parse(localStorage.getItem(storageKey)) ?? width;
    } catch {
      // Use the default width when the browser cannot read the preference.
    }
  }
  let preferred = Number.isFinite(width) ? width : undefined;
  let current;
  let min;
  let max;
  let drag;

  function apply(value) {
    current = Math.round(Math.min(max, Math.max(min, value)));
    container.style.setProperty('--split-width', current + 'px');
    handle.setAttribute('aria-valuenow', current);
    handle.setAttribute('aria-valuetext', current + ' 像素');
  }

  function refresh() {
    const total = container.clientWidth;
    if (!total) return;
    const gap = handle.offsetWidth;
    const mainMinimum = Math.min(284, Math.ceil(total * .61));
    max = Math.max(0, Math.min(500, total - gap - mainMinimum));
    min = Math.min(150, max);
    handle.setAttribute('aria-valuemin', min);
    handle.setAttribute('aria-valuemax', max);
    const enabled = gap > 0 && min < max && !container.hasAttribute('data-collapsed');
    handle.setAttribute('aria-disabled', String(!enabled));
    handle.tabIndex = enabled ? 0 : -1;
    apply(drag ? current : preferred ?? Math.max(minDirectory, Math.min(350, (total - gap) * defaultRatio)));
  }

  function saveWidth() {
    preferred = current;
    if (storageKey) {
      try {
        localStorage.setItem(storageKey, String(preferred));
      } catch {
        // Keep the preference for the current page.
      }
    }
  }

  function endDrag(keepWidth) {
    const {id, width: initial} = drag;
    drag = undefined;
    container.removeAttribute('data-split-dragging');
    if (handle.hasPointerCapture(id)) handle.releasePointerCapture(id);
    if (keepWidth && current !== initial) saveWidth();
    else refresh();
  }

  handle.addEventListener('pointerdown', event => {
    if (event.button !== 0 || !event.isPrimary || handle.tabIndex < 0 || drag) return;
    event.preventDefault();
    const actualWidth = Math.round(container.firstElementChild.getBoundingClientRect().width);
    drag = {id:event.pointerId, x:event.clientX, width:actualWidth};
    container.setAttribute('data-split-dragging', '');
    apply(actualWidth);
    handle.focus({preventScroll:true});
    handle.setPointerCapture(event.pointerId);
  });

  handle.addEventListener('pointermove', event => {
    if (event.pointerId === drag?.id) apply(drag.width + event.clientX - drag.x);
  });

  handle.addEventListener('pointerup', event => {
    if (event.pointerId !== drag?.id) return;
    apply(drag.width + event.clientX - drag.x);
    endDrag(true);
  });

  for (const type of ['pointercancel', 'lostpointercapture']) {
    handle.addEventListener(type, event => {
      if (event.pointerId === drag?.id) endDrag(false);
    });
  }

  handle.addEventListener('keydown', event => {
    if (drag) {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        endDrag(false);
      }
      return;
    }
    if (handle.tabIndex < 0) return;
    const targets = {ArrowLeft:current - 16, ArrowRight:current + 16, Home:min, End:max};
    if (!Object.hasOwn(targets, event.key)) return;
    event.preventDefault();
    const previous = current;
    apply(targets[event.key]);
    if (current !== previous) saveWidth();
  });

  refresh();
  const observer = new ResizeObserver(() => {
    if (container.isConnected) refresh();
    else observer.disconnect();
  });
  observer.observe(container);
}

window.OmniRelayChatControls = (() => {
  const { escape, icon } = OmniRelayUI;
  const drafts = new Map();

  function composer() {
    const toggle = (action, label, graphic) => '<button type="button" class="chat-icon-button" data-compose-tool="' +
      action + '" aria-label="' + label + '" title="' + label + '"' +
      (action === 'file' ? '' : ' aria-expanded="false"') + '>' + graphic + '</button>';
    return `<form class="chat-composer" aria-label="发送演示消息" hidden>
      <div class="chat-composer-box">
        <ul class="chat-composer-files" aria-label="待发送附件" hidden></ul>
        <textarea rows="1" aria-label="消息内容"></textarea>
        <div class="chat-composer-tools">
          ${toggle('format','文本格式','<span class="chat-format-icon" aria-hidden="true">Aa</span>')}
          ${toggle('emoji','表情',icon('compose-emoji'))}
          ${toggle('mention','提及成员',icon('compose-mention'))}
          ${toggle('file','添加附件',icon('compose-file'))}
          ${toggle('expand','展开输入框',icon('compose-expand'))}
          <button type="submit" class="chat-icon-button chat-send" aria-label="发送消息" title="发送消息" disabled>${icon('compose-send')}</button>
        </div>
        <div class="chat-composer-choice" role="group" hidden></div>
        <input type="file" multiple hidden aria-label="选择附件">
      </div>
      <span class="sr-only" role="status" data-compose-status></span>
    </form>`;
  }

  function mountComposer(root, onSend) {
    const form = root.querySelector('.chat-composer');
    const text = form.querySelector('textarea');
    const send = form.querySelector('[type="submit"]');
    const fileInput = form.querySelector('[type="file"]');
    const fileList = form.querySelector('.chat-composer-files');
    const choices = form.querySelector('.chat-composer-choice');
    const status = form.querySelector('[data-compose-status]');
    const expand = form.querySelector('[data-compose-tool="expand"]');
    let active;
    let draft;
    let panelButton;

    function sync() {
      send.disabled = !text.value.trim() && !draft?.files.length;
      text.rows = Math.min(4,text.value.split('\n').length);
      if (!draft) return;
      draft.text = text.value;
      fileList.hidden = !draft.files.length;
      fileList.innerHTML = draft.files.map(({name},index) => '<li><span>' + escape(name) +
        '</span><button type="button" data-compose-remove="' + index + '" aria-label="移除附件 ' +
        escape(name) + '">' + icon('action-close') + '</button></li>').join('');
    }

    function closeChoices() {
      panelButton?.setAttribute('aria-expanded','false');
      panelButton = undefined;
      choices.hidden = true;
    }

    function show(record, names) {
      if (active?.id === record?.id) return;
      active = record;
      closeChoices();
      form.hidden = !record;
      if (!record) return;
      draft = drafts.get(record.id) || {text:'',files:[]};
      drafts.set(record.id,draft);
      text.value = draft.text;
      text.placeholder = '发送给 ' + record.title;
      form.participants = names;
      sync();
    }

    function insert(kind,value) {
      const start = text.selectionStart;
      const end = text.selectionEnd;
      const selected = text.value.slice(start,end);
      const replacement = kind === 'format' ? value + (selected || '文字') + value : value;
      text.setRangeText(replacement,start,end,'end');
      text.focus();
      if (kind === 'format' && !selected) text.setSelectionRange(start + value.length,start + value.length + 2);
      sync();
    }

    function showChoices(button,kind) {
      const wasOpen = panelButton === button;
      closeChoices();
      if (wasOpen) return;
      const options = kind === 'format' ? [['加粗','**'],['斜体','*']] :
        kind === 'emoji' ? [['😀','😀'],['👍','👍'],['🎉','🎉'],['❤️','❤️'],['🙏','🙏'],['✅','✅']] :
          form.participants.map(name => [name,'@' + name + ' ']);
      choices.setAttribute('aria-label',button.getAttribute('aria-label'));
      choices.dataset.composeKind = kind;
      choices.innerHTML = options.map(([label,value]) => '<button class="button" type="button" data-compose-insert="' +
        escape(value) + '">' + escape(label) + '</button>').join('');
      choices.hidden = false;
      panelButton = button;
      button.setAttribute('aria-expanded','true');
      choices.querySelector('button')?.focus({preventScroll:true});
    }

    form.addEventListener('click',event => {
      const choice = event.target.closest('[data-compose-insert]');
      if (choice) {
        insert(choices.dataset.composeKind,choice.dataset.composeInsert);
        closeChoices();
      }
      const remove = event.target.closest('[data-compose-remove]');
      if (remove) {
        draft.files.splice(Number(remove.dataset.composeRemove),1);
        sync();
        text.focus();
      }
      const button = event.target.closest('[data-compose-tool]');
      if (!button) return;
      const kind = button.dataset.composeTool;
      if (kind === 'file') {
        closeChoices();
        fileInput.click();
      } else if (kind === 'expand') {
        closeChoices();
        const expanded = form.toggleAttribute('data-expanded');
        expand.setAttribute('aria-expanded',String(expanded));
        const label = expanded ? '收起输入框' : '展开输入框';
        expand.setAttribute('aria-label',label);
        expand.title = label;
        text.focus();
      } else showChoices(button,kind);
    });
    root.addEventListener('click',event => {
      if (!form.contains(event.target)) closeChoices();
    });
    fileInput.addEventListener('change',() => {
      for (const file of fileInput.files) draft.files.push({name:file.name});
      fileInput.value = '';
      sync();
    });
    text.addEventListener('input',sync);
    text.addEventListener('keydown',event => {
      if (event.key !== 'Enter' || event.shiftKey || event.isComposing || event.keyCode === 229) return;
      event.preventDefault();
      if (!send.disabled) form.requestSubmit();
    });
    form.addEventListener('keydown',event => {
      if (event.key !== 'Escape' || !panelButton) return;
      event.preventDefault();
      panelButton.focus();
      closeChoices();
    });
    form.addEventListener('submit',event => {
      event.preventDefault();
      if (!active || send.disabled) return;
      const content = {text:text.value.trim(),files:[...draft.files]};
      text.value = '';
      draft.files = [];
      sync();
      closeChoices();
      onSend(active,content);
      text.focus();
      status.textContent = '演示消息已发送';
    });
    return {show};
  }

  return { connectSplitPane, composer, mountComposer };
})();
