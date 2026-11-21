const reasons = {
  send_disabled: '此部署尚未开放机器人发送。',
  wrong_target_receipt: '发送失败：飞书回执的收件会话与目标不符。',
  invalid_target: '当前会话无法接收消息。',
  access_lost: '当前账号或机器人已失去会话权限。',
  mention_not_allowed: '提及的人员已不在群内。',
  attachment_failed: '附件上传失败或引用已失效。',
  invalid_delivery: '内容超出发送限制，请调整后重试。',
  operation_conflict: '操作键已绑定其他内容，请核对回执。'
};
const emptyDraft = () => ({ text: '', mentions: [], files: [], operation: null, notice: '' });

// 仅成员选择器插入的区间能生成飞书提及。手动输入的同名文本保持原样。
function payload(draft) {
  const attachments = draft.files.map(item => item.id);
  if (!draft.text.trim()) return { attachments };
  const paragraphs = [[]];
  let rich = false;
  function append(value) {
    const lines = value.split('\n');
    lines.forEach((line, index) => {
      if (index) paragraphs.push([]);
      const row = paragraphs.at(-1);
      let start = 0;
      for (const hit of line.matchAll(/(\*\*|\*)([^*]+?)\1/g)) {
        if (hit.index > start) row.push({ text: line.slice(start, hit.index) });
        row.push({ text: hit[2], styles: [hit[1] === '**' ? 'bold' : 'italic'] });
        rich = true;
        start = hit.index + hit[0].length;
      }
      if (start < line.length) row.push({ text: line.slice(start) });
    });
  }
  let start = 0;
  for (const mention of draft.mentions) {
    append(draft.text.slice(start, mention.start));
    paragraphs.at(-1).push({ mention_open_id: mention.id });
    rich = true;
    start = mention.end;
  }
  append(draft.text.slice(start));
  return rich ? { paragraphs, attachments } : { text: draft.text, attachments };
}

export function mountChatComposer(host, api, { signal, draftScope, onConfirmed = () => {} } = {}) {
  const lifetime = new AbortController();
  signal = signal ? AbortSignal.any([signal, lifetime.signal]) : lifetime.signal;
  const { icon, escape: esc } = window.OmniRelayUI;
  const tools = [['format', '文本格式'], ['emoji', '表情'], ['mention', '提及成员'], ['file', '添加附件'], ['expand', '展开输入框']];
  host.innerHTML = `<form class="chat-composer" aria-label="发送消息" hidden>
    <div class="chat-composer-box">
      <ul class="chat-composer-files" aria-label="待发送附件" hidden></ul>
      <textarea rows="1" aria-label="消息内容"></textarea>
      <div class="chat-composer-tools">${tools.map(([key, label]) =>
        `<button type="button" class="chat-icon-button" data-tool="${key}" aria-label="${label}" title="${label}" ${key === 'file' ? '' : 'aria-expanded="false"'}>${key === 'format' ? '<span class="chat-format-icon">Aa</span>' : icon('compose-' + key)}</button>`).join('')}
        <button type="submit" class="chat-icon-button chat-send" aria-label="发送消息" disabled>${icon('compose-send')}</button>
      </div>
      <div class="chat-composer-choice" role="group" hidden></div>
      <input type="file" multiple hidden aria-label="选择附件">
    </div>
    <div class="chat-delivery-info"><span data-identity></span> <span role="status"></span>
      <button type="button" class="button" data-recover hidden></button></div>
  </form>`;
  const form = host.firstElementChild;
  const get = selector => form.querySelector(selector);
  const editor = get('textarea'), send = get('[type=submit]'), chooser = get('[type=file]');
  const list = get('ul'), panel = get('[role=group]'), recover = get('[data-recover]');
  const mentionButton = get('[data-tool=mention]');
  const drafts = draftScope?.drafts ?? new Map();
  let active, menu, mentionPicker, beforeEdit;
  let composing = false;
  // 编辑器共享会话内存，整页刷新时从已核验的会话恢复。
  const editorKey = id => 'chat.' + id;
  function save(d) {
    if (!draftScope || signal?.aborted) return;
    try {
      if (!d.operation && !d.text && !d.files.length) draftScope.store(editorKey(d.id), null);
      else draftScope.store(editorKey(d.id), JSON.stringify(d, (key, value) =>
        ['blob', 'busy', 'uploading'].includes(key) ? undefined : value));
    } catch {}
  }
  function refresh(d) {
    if (d !== active?.draft || signal?.aborted) return;
    const locked = Boolean(d.operation);
    editor.readOnly = locked;
    editor.rows = Math.min(4, d.text.split('\n').length);
    if (editor.value !== d.text) editor.value = d.text;
    const cap = active.cap;
    const reason = !cap?.can_send ? active.reason : locked ? '请先核对本次发送结果。' :
      d.files.some(f => !usable(f)) ? '请等待上传完成或处理异常附件。' :
      !d.text.trim() && !d.files.length ? '请输入消息或添加附件。' : '';
    send.disabled = Boolean(reason);
    send.title = reason || '发送消息';
    get('[data-identity]').textContent = cap?.sender ? '发送身份：机器人 ' + (cap.sender.name || cap.sender.open_id) : '';
    get('.chat-delivery-info [role=status]').textContent = [d.notice, reason].filter(Boolean).join(' ') || '可发送到当前会话。';
    get('[data-tool=file]').disabled = locked || !cap?.can_send;
    mentionButton.disabled = locked || !cap?.can_send || !cap.mentions;
    for (const key of ['format','emoji']) get('[data-tool=' + key + ']').disabled = locked;
    recover.hidden = d.busy || !(locked || !cap?.can_send && active.loaded);
    recover.textContent = locked ? (d.operation.failedParts ? '编辑未发送部分' : d.operation.missing ? '按原操作重试' : '查询发送结果') : '重新检查';
    list.hidden = !d.files.length;
    list.innerHTML = d.files.map((f,i) => {
      const state = f.uploading ? '正在上传' : usable(f) ? '已上传' : f.error || (f.id ? '已过期' : '请重新选择文件');
      return `<li><span>${esc(f.name)} · ${esc(state)}</span>
        ${!locked && !f.uploading && !usable(f) && f.blob ? '<button type="button" data-retry="' + i + '">重试上传</button>' : ''}
        <button type="button" data-remove="${i}" aria-label="移除附件 ${esc(f.name)}" ${locked ? 'disabled' : ''}>${icon('action-close')}</button></li>`;
    }).join('');
  }
  const usable = f => !f.invalid && f.id && Date.parse(f.expires_at) > Date.now();
  function closeMenu() {
    menu?.setAttribute('aria-expanded', 'false');
    menu = null;
    mentionPicker = null;
    panel.hidden = true;
    panel.replaceChildren();
  }
  async function capabilities(view) {
    if (active === view && menu === mentionButton) closeMenu();
    view.loaded = false;
    view.cap = null;
    view.reason = '正在检查发送权限…';
    refresh(view.draft);
    try {
      view.cap = await api.capabilities(view.draft.id, signal);
      view.reason = reasons[view.cap.reason] || '当前会话暂不可发。';
    } catch (e) {
      view.reason = reasons[e.data?.error] || '无法检查发送权限。';
    }
    view.loaded = true;
    if (active === view) refresh(view.draft);
  }
  function show(record) {
    closeMenu();
    beforeEdit = null;
    composing = false;
    active = null;
    form.hidden = !record;
    if (!record) return;
    let d = drafts.get(record.id);
    if (!d) {
      d = emptyDraft();
      if (draftScope) {
        try {
          const saved = draftScope.read(editorKey(record.id));
          if (saved) Object.assign(d, saved);
        } catch {}
      }
      d.id = record.id;
      drafts.set(d.id, d);
    }
    active = { draft: d };
    editor.placeholder = '发送给 ' + record.title;
    void capabilities(active);
  }
  function changed(d) { save(d); refresh(d); }
  function edit(d, start, end, value) {
    const delta = value.length - (end - start);
    d.mentions = d.mentions.filter(m => m.end <= start || m.start >= end);
    for (const m of d.mentions) if (m.start >= end) { m.start += delta; m.end += delta; }
    d.text = d.text.slice(0, start) + value + d.text.slice(end);
    d.notice = '';
  }
  editor.addEventListener('beforeinput', () => { beforeEdit = [editor.selectionStart, editor.selectionEnd]; });
  editor.addEventListener('input', event => {
    const d = active.draft, next = editor.value;
    const [selectionStart, selectionEnd] = beforeEdit || [0, d.text.length];
    beforeEdit = null;
    // 保留选择区外未变的前后缀，兼容退格、粘贴、撤销和输入法替换。
    let start = 0, tail = 0;
    while (start < selectionStart && d.text[start] === next[start]) start++;
    while (tail < d.text.length - selectionEnd && tail < next.length - start &&
        d.text[d.text.length - tail - 1] === next[next.length - tail - 1]) tail++;
    const end = d.text.length - tail;
    edit(d, start, end, next.slice(start, next.length - (d.text.length - end)));
    changed(d);
    if (!composing && !event.isComposing) updateMentionInput(event.data);
  });
  function insert(value, mention, marker, start = editor.selectionStart) {
    const d = active.draft, end = editor.selectionEnd;
    if (marker) value = (editor.value.slice(start, end) || '文字').split('\n')
      .map(line => line ? marker + line + marker : '').join('\n');
    edit(d, start, end, value);
    if (mention) {
      d.mentions.push({ start, end: start + value.length - 1, id: mention });
      d.mentions.sort((a,b) => a.start - b.start);
    }
    changed(d);
    editor.focus();
    const caret = start + value.length;
    editor.setSelectionRange(marker && start === end ? start + marker.length : caret,
      marker && start === end ? caret - marker.length : caret);
    closeMenu();
  }
  function choice(label, fn) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'button';
    button.textContent = label;
    button.onclick = fn;
    panel.append(button);
    return button;
  }
  function mentionQuery(start) {
    if (start === null) return '';
    const end = editor.selectionEnd;
    if (editor.selectionStart !== end || end <= start) return;
    return /^[@＠]([^\s@＠]*)$/u.exec(editor.value.slice(start, end))?.[1].toLocaleLowerCase();
  }
  function updateMentionInput(data) {
    if ((data === '@' || data === '＠') && !mentionButton.disabled && editor.selectionEnd > 0 &&
        editor.value[editor.selectionEnd - 1] === data) {
      const start = editor.selectionEnd - 1;
      if (mentionPicker?.start !== start) void openMenu(mentionButton, 'mention', start);
    } else if (mentionPicker && mentionPicker.start !== null) {
      if (mentionQuery(mentionPicker.start) === undefined) closeMenu();
      else mentionPicker.filter();
    }
  }
  async function openMenu(button, kind, mentionStart = null) {
    const previous = menu;
    closeMenu();
    if (previous === button && mentionStart === null) return;
    menu = button;
    panel.hidden = false;
    button.setAttribute('aria-expanded','true');
    panel.setAttribute('aria-label', button.title);
    panel.dataset.composeKind = kind;
    if (kind === 'mention') {
      const view = active;
      const notice = document.createElement('span');
      notice.setAttribute('role', 'status');
      panel.append(notice);
      const picker = { start: mentionStart, loading: false, hasMore: false, error: false, filter };
      mentionPicker = picker;
      function filter() {
        const query = mentionQuery(mentionStart);
        let matches = 0;
        for (const option of panel.querySelectorAll('[data-mention-name]')) {
          option.hidden = query === undefined || !option.dataset.mentionName.includes(query);
          if (!option.hidden) matches++;
        }
        notice.textContent = picker.loading ? '正在读取群成员…' : picker.error ? '无法读取群成员。' :
          matches ? '' : picker.hasMore ? '已加载成员中没有匹配项。' :
          query ? '没有匹配的群成员。' : '暂无可提及成员。';
        notice.hidden = !notice.textContent;
      }
      async function page(offset) {
        picker.loading = true;
        picker.error = false;
        filter();
        try {
          const data = await api.mentionMembers(view.draft.id, offset, signal);
          if (mentionPicker !== picker || signal.aborted) return;
          for (const member of data.items) {
            const option = choice(member.name, () => {
              if (mentionQuery(mentionStart) !== undefined) {
                insert('@' + member.name + ' ', member.mention_open_id, null, mentionStart ?? editor.selectionStart);
              } else closeMenu();
            });
            option.dataset.mentionName = member.name.toLocaleLowerCase();
          }
          const next = offset + data.items.length;
          picker.hasMore = next > offset && next < data.total;
          if (picker.hasMore) {
            const more = choice('更多成员', () => { editor.focus(); more.remove(); void page(next); });
          }
        } catch {
          if (mentionPicker !== picker || signal.aborted) return;
          picker.error = true;
          const retry = choice('重试读取成员', () => { editor.focus(); retry.remove(); void page(offset); });
        } finally {
          if (mentionPicker === picker) {
            picker.loading = false;
            filter();
          }
        }
      }
      await page(0);
      if (mentionPicker !== picker || mentionStart !== null) return;
    } else if (kind === 'format') {
      choice('加粗', () => insert('', null, '**'));
      choice('斜体', () => insert('', null, '*'));
    } else for (const emoji of ['😀','👍','🎉','❤️','🙏','✅']) choice(emoji, () => insert(emoji));
    panel.querySelector('button')?.focus({ preventScroll: true });
  }
  async function upload(d, f, cap) {
    if (!cap?.can_send || !f.blob || f.uploading) return;
    const kind = f.blob.type.startsWith('image/') ? 'image' : 'file';
    f.id = null;
    const limit = kind === 'image' ? cap.max_image_bytes : cap.max_file_bytes;
    if (!f.blob.size || f.blob.size > limit) {
      f.error = '文件为空或超过大小限制';
      changed(d);
      return;
    }
    f.uploading = true;
    changed(d);
    try {
      const result = await api.upload(d.id, kind, f.blob, signal);
      if (!usable(result)) throw new Error();
      Object.assign(f, { id: result.id, expires_at: result.expires_at, invalid: false, error: '' });
    } catch { f.error = '上传失败'; }
    finally { f.uploading = false; changed(d); }
  }
  chooser.onchange = () => {
    if (!active) return;
    const { draft: d, cap } = active;
    if (!d.operation && cap?.can_send) for (const blob of chooser.files) {
      if (d.files.length >= cap.max_attachments) {
        d.notice = '每次最多添加 ' + cap.max_attachments + ' 个附件。';
        break;
      }
      const f = { name: blob.name, blob };
      d.files.push(f);
      void upload(d, f, cap);
    }
    chooser.value = '';
    changed(d);
  };
  async function deliver(d, query) {
    const op = d.operation;
    if (!op || d.busy) return;
    d.busy = true;
    d.notice = query ? '正在查询原操作…' : '正在发送…';
    changed(d);
    try {
      const r = query ? await api.delivery(d.id, op.key, signal) :
        await api.send(d.id, { operation_key: op.key, ...op.body }, signal);
      const expected = op.body.attachments.length + Number(Boolean(op.body.text || op.body.paragraphs));
      if (r.operation_key !== op.key || r.chat_id !== d.id || r.parts?.length !== expected) throw new Error();
      const bodyParts = expected - op.body.attachments.length;
      if (r.parts.some((part, i) => part.position !== i ||
          (part.attachment_id || null) !== (op.body.attachments[i - bodyParts] || null))) throw new Error();
      const confirmed = r.parts.filter(p => p.state === 'success' && p.message_id);
      if (r.state === 'success' && confirmed.length === expected) {
        Object.assign(d, emptyDraft(), { notice: '飞书已确认接收。' });
        if (active?.draft === d && !signal?.aborted) onConfirmed();
      } else if (r.state === 'failed' && r.parts.every(p => p.state === 'failed' || p.state === 'success' && p.message_id)) {
        const failed = r.parts.filter(p => p.state === 'failed');
        const message = reasons[failed[0]?.reason] || '发送失败。';
        for (const part of failed) {
          const file = d.files.find(f => f.id === part.attachment_id);
          if (file && part.reason === 'attachment_failed') {
            file.error = '请重新上传';
            file.invalid = true;
          }
        }
        if (confirmed.length) op.failedParts = failed;
        else d.operation = null;
        d.notice = (confirmed.length ? '部分已接收。' : '') + message;
      } else d.notice = '尚未取得完整回执，可查询原操作。';
    } catch (e) {
      if (query && e.data?.error === 'operation_not_found') {
        op.missing = true;
        d.notice = '未查到此操作，可使用原操作键重试。';
      } else if (!query && ['invalid_delivery','send_disabled','invalid_target','access_lost'].includes(e.data?.error)) {
        d.operation = null;
        d.notice = reasons[e.data.error];
      } else d.notice = '结果待核对，草稿已保留。';
    } finally { d.busy = false; changed(d); }
  }
  form.onsubmit = event => {
    event.preventDefault();
    if (!active) return;
    const d = active.draft;
    refresh(d);
    if (send.disabled || d.operation) return;
    closeMenu();
    d.operation = { key: Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, '0')).join(''), body: payload(d) };
    void deliver(d, false);
  };
  form.onclick = event => {
    if (!active) return;
    const { draft: d, cap } = active;
    const button = event.target.closest('button');
    if (!button || button.disabled) return;
    const kind = button.dataset.tool;
    if (button === recover) {
      const op = d.operation;
      if (!op) void capabilities(active);
      else if (op.failedParts) {
        if (op.failedParts.every(p => p.attachment_id)) { d.text = ''; d.mentions = []; }
        d.files = d.files.filter(f => op.failedParts.some(p => p.attachment_id === f.id));
        d.operation = null;
        d.notice = '已保留未发送部分。';
        changed(d);
      } else { const resend = op.missing; op.missing = false; void deliver(d, !resend); }
    } else if (button.hasAttribute('data-remove')) {
      d.files.splice(Number(button.dataset.remove), 1);
      changed(d);
      editor.focus();
    } else if (button.hasAttribute('data-retry')) void upload(d, d.files[Number(button.dataset.retry)], cap);
    else if (kind === 'file') { closeMenu(); chooser.click(); }
    else if (kind === 'expand') {
      closeMenu();
      const expanded = form.toggleAttribute('data-expanded');
      button.setAttribute('aria-expanded', String(expanded));
      button.title = expanded ? '收起输入框' : '展开输入框';
      button.setAttribute('aria-label', button.title);
      editor.focus();
    } else if (kind) void openMenu(button, kind);
  };
  editor.addEventListener('compositionstart', () => { composing = true; });
  editor.addEventListener('compositionend', event => {
    composing = false;
    updateMentionInput(event.data);
  });
  editor.addEventListener('click', () => updateMentionInput());
  form.onkeydown = event => {
    if (composing || event.isComposing || event.keyCode === 229) return;
    if (event.key === 'Escape' && menu) {
      event.preventDefault();
      (mentionPicker?.start != null ? editor : menu).focus();
      closeMenu();
    }
    if (event.target === editor && event.key === 'ArrowDown' && mentionPicker) {
      const first = panel.querySelector('button:not([hidden])');
      if (first) {
        event.preventDefault();
        first.focus();
      }
    }
    if (event.target === editor && event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      if (mentionPicker) {
        const option = panel.querySelector('[data-mention-name]:not([hidden])');
        if (option) option.click();
        else closeMenu();
      } else if (!send.disabled) form.requestSubmit();
    }
  };
  document.addEventListener('click', event => {
    // 翻页或重试按钮可能已在点击处理过程中移除，沿用事件原有的传播路径。
    if (!event.composedPath().includes(form)) closeMenu();
  }, { signal });
  signal.addEventListener('abort', () => { active = null; form.remove(); }, { once: true });
  return { show, dispose: () => lifetime.abort() };
}
