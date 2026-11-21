import { assignmentAPI } from './chat-assignment.js';

export function fileSources(row) {
  const { escape } = window.OmniRelayUI;
  const seen = new Set();
  return row.sources.flatMap(source => {
    if (![source.chat_id, source.message_id].every(id => typeof id === 'string' && /^[A-Za-z0-9_-]{1,160}$/.test(id))) return [];
    const route = '#/feishu/bot-chats/' + source.chat_id + '/' + source.message_id;
    if (seen.has(route)) return [];
    seen.add(route);
    return '<a class="drive-source" data-source-file="' + escape(row.id) + '" href="' + route + '">' + escape(source.chat_name || source.chat_id) +
      ' · ' + escape(source.sender_name || '未知分享人') + '</a>';
  }).join('<br>');
}

export function fileProjects(row, names) {
  const { escape } = window.OmniRelayUI;
  const chips = row.project_ids.map(id => '<span class="kind">' + escape(names.get(id) || id) + (row.inherited_project_ids?.includes(id) ? ' · 来自群聊' : '') + '</span>');
  return '<button class="row-action drive-projects" type="button" data-drive-projects="' + escape(row.id) +
    '" aria-label="修改 ' + escape(row.name) + ' 的所属项目">' + (chips.join('') || '添加所属项目') + '</button>';
}

export async function editFileProjects({ row, trigger, request, signal, refresh, updated, restoreFocus }) {
  const { combo, mountCombo, openDialog, toast } = window.OmniRelayUI;
  const path = '/api/v1/feishu/files/' + encodeURIComponent(row.id);
  let snapshot, choices;
  trigger.disabled = true;
  try {
    [snapshot, choices] = await Promise.all([request(path, {signal}), assignmentAPI(request).choices(signal)]);
  } catch (error) {
    if (signal.aborted) return;
    if ([403,404].includes(error.status)) { updated?.(null); refresh(); }
    toast('error', '暂时无法读取文件或项目，请重试。');
    return;
  } finally {
    trigger.disabled = false;
  }
  if (signal.aborted || !trigger.isConnected) return;
  const current = new Set(snapshot.manual_project_ids ?? snapshot.project_ids);
  const known = new Set(choices.map(item => item.id));
  for (const id of current) if (!known.has(id)) choices.push({id, label:id});
  const read = form => new Set(form.querySelector('[data-combo-result]').value.split(',').filter(Boolean));
  const dirty = form => {
    const next = read(form);
    return next.size !== current.size || [...next].some(id => !current.has(id));
  };
  let busy = false;
  const close = () => dialog.close();
  const dialog = openDialog({
    title:'修改所属项目', description:snapshot.name, variant:'form', confirm:'保存',
    body:(snapshot.inherited_project_ids?.length ? '<p>来自群聊：' + snapshot.inherited_project_ids.map(id => window.OmniRelayUI.escape(choices.find(choice => choice.id === id)?.label || id)).join('、') + '</p>' : '') + combo('drive-project-picker','人工关联项目','projects',true), isDirty:dirty,
    async onSubmit(form) {
      if (busy) return {keepOpen:true};
      if (!dirty(form)) return;
      const next = read(form);
      const submit = form.querySelector('[type="submit"]');
      busy = true;
      for (const control of form.elements) control.disabled = true;
      try {
        await request(path + '/projects', {method:'PATCH', signal, body:{
          project_version:snapshot.project_version, add_project_ids:[...next].filter(id => !current.has(id)),
          remove_project_ids:[...current].filter(id => !next.has(id))
        }});
      } catch (error) {
        if (signal.aborted) return;
        if (![403,404,409].includes(error.status)) {
          busy = false;
          for (const control of form.elements) control.disabled = false;
          return {field:submit, message:'保存失败，请重试。'};
        }
        toast('warning', error.status === 409 ? '归属已变化，请重新打开编辑并确认。' : '文件或项目权限已变化。');
        updated?.(null);
        close();
        await refresh();
        return;
      }
      if (signal.aborted) return;
      // 移除最后一个可读来源项目后，以重新授权的结果决定抽屉能否继续展示。
      let fresh;
      if (updated) {
        fresh = null;
        try { fresh = await request(path, {signal}); } catch { /* 无法重新核验时关闭抽屉。 */ }
        if (!fresh && !signal.aborted) { updated(null); close(); }
      }
      if (!signal.aborted) {
        await refresh();
        if (!signal.aborted) {
          if (fresh) updated(fresh);
          toast('success', '所属项目已保存');
        }
      }
    },
    restoreFocus:() => trigger.isConnected ? trigger : restoreFocus?.(),
    onClose:() => signal.removeEventListener('abort', close)
  });
  signal.addEventListener('abort', close, {once:true});
  dialog.addEventListener('cancel', event => {
    if (busy) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
  mountCombo(dialog.querySelector('[data-combo]'), {choices, initial:[...current]});
  const input = dialog.querySelector('[role="combobox"]');
  dialog.querySelector('.dialog-footer').addEventListener('pointerdown', event => {
    if (event.target.closest('button') && input.getAttribute('aria-expanded') === 'true') event.preventDefault();
  });
  input.focus();
}

export function mountFileMetadata(dialog, host, row, projects, edit, signal, category, size) {
  let current = row;
  function paint(record) {
    if (!record || record.version_id !== row.version_id) { dialog.close(); return; }
    current = record;
    host.innerHTML = '<dl class="facts drive-preview-facts"><div><dt>分类</dt><dd>' + category + '</dd></div><div><dt>来源</dt><dd>' + fileSources(record) +
      '</dd></div><div><dt>所属项目</dt><dd>' + projects(record) + '</dd></div><div><dt>大小</dt><dd>' + size + '</dd></div></dl>';
  }
  paint(current);
  host.addEventListener('click', event => {
    const opener = event.target.closest('[data-drive-projects]');
    if (opener) void edit(current, opener, paint);
    if (event.target.closest('.drive-source') && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) dialog.close();
  });
  const close = () => dialog.close();
  signal.addEventListener('abort', close, {once:true});
  dialog.addEventListener('close', () => signal.removeEventListener('abort', close), {once:true});
}
