export function assignmentAPI(request) {
  const projects = '/api/v1/projects';
  const path = id => projects + '/' + encodeURIComponent(id);
  return {
    async choices(signal) {
      const choices = [];
      for (let page = 1; ; page++) {
        const result = await request(projects + '?page_size=100&page=' + page, { signal });
        choices.push(...result.items.map(item => ({ id: item.id, label: item.name })));
        if (!result.items.length || choices.length >= result.total) return choices;
      }
    },
    async projectName(id, signal) {
      const result = await request(path(id) + '/overview', { signal });
      return result.data.workspace.project.name;
    },
    groups: (project, signal) => request(path(project) + '/chats', { signal }),
    assign: (id, project_id, version, signal) => request('/api/v1/feishu/chats/' + encodeURIComponent(id) + '/project',
      { method: 'PUT', body: { project_id, version }, signal }),
    primary: (project, chat_id, assignment_version, primary_version, signal) => request(path(project) + '/chats/primary',
      { method: 'PUT', body: { chat_id, assignment_version, primary_version }, signal })
  };
}

export async function editAssignment({ api, primary, projectId, record, trigger, signal, dialogs, changed, reload }) {
  const { combo, mountCombo, escape, openDialog, toast } = window.OmniRelayUI;
  const assignment = record.project_assignment;
  const current = assignment.project_id;
  let snapshot, choices;
  const label = primary ? '确认设置' : '保存';
  try {
    if (primary) snapshot = await api.groups(projectId, signal);
    else choices = await api.choices(signal);
  } catch {
    if (!signal.aborted) toast('error', '暂时无法读取项目信息，请重试。');
    return;
  }
  if (signal.aborted) return;
  const candidate = snapshot?.items.find(item => item.id === record.id);
  if (primary && (!candidate || candidate.primary)) {
    reload();
    return;
  }
  const before = snapshot?.items.find(item => item.primary);
  let submitting = false;
  let conflict = false;
  const dialog = openDialog({
    title: primary ? '设为项目主群' : '修改项目归属', description: record.name,
    variant: primary ? 'confirm' : 'form', confirm: label,
    body: primary ? '<p class="dialog-note">' + (before ? '主群将从「' + escape(before.name) + '」更换为当前群。' :
      '将当前群设为项目主群。') + '</p>' : combo('chat-project-picker', '所属项目', 'projects'),
    isDirty: form => !primary && !submitting && form.querySelector('[data-combo-result]').value !== current,
    async onSubmit(form) {
      if (submitting) return { keepOpen: true };
      if (conflict) {
        reload();
        return;
      }
      const next = primary ? projectId : form.querySelector('[data-combo-result]').value;
      if (!primary && next === current) return;
      const submit = form.querySelector('[type="submit"]');
      submitting = true;
      for (const control of form.elements) control.disabled = true;
      try {
        const result = primary
          ? await api.primary(projectId, record.id, candidate.version, snapshot.primary_version, signal)
          : await api.assign(record.id, next, assignment.version, signal);
        if (!signal.aborted) toast('success', primary ? '项目主群已更新' : next ? '群聊项目归属已保存' : '群聊项目归属已清除');
        changed(record.id, primary ? { project_id: projectId, primary: true } : result);
      } catch (error) {
        if (signal.aborted) return;
        conflict = error.status === 409 || error.status === 404;
        submit.textContent = conflict ? '重新读取' : '重试';
        return { field: submit, message: conflict ? '归属或权限已变化，请重新读取后确认。' : '保存失败，请重试。' };
      } finally {
        submitting = false;
        for (const control of form.elements) control.disabled = false;
      }
    },
    onClose() { dialogs.delete(dialog); },
    restoreFocus: () => trigger.isConnected && !trigger.closest('[hidden]') ? trigger : undefined
  });
  dialogs.add(dialog);
  dialog.addEventListener('cancel', event => {
    if (submitting) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
  if (!primary) {
    mountCombo(dialog.querySelector('[data-combo]'), { choices, initial: current });
    const input = dialog.querySelector('[role="combobox"]');
    // 保留按下时的布局，使收起选项前的点击仍落在保存按钮上。
    dialog.querySelector('[type="submit"]').addEventListener('pointerdown', event => {
      if (input.getAttribute('aria-expanded') === 'true') event.preventDefault();
    });
    input.focus();
  }
}
