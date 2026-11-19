'use strict';

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

  return { connectSplitPane: window.connectOmniRelaySplitPane, composer, mountComposer };
})();
