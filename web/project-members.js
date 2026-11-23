import {memberPicker, mountMemberPicker} from './project-member-picker.js';

export function canRemoveMember(data, member) {
  return data.can_manage && (data.can_delegate || (member.role !== 'manager' && member.project_role !== 'project_admin'));
}

export function wireProjectMembers(host, {data, projectId, request, signal, navigation, refresh}) {
  const {escape: esc, openDialog, toast} = window.OmniRelayUI;
  const root = '/api/v1/projects/' + projectId;
  const projectRoles = (data.project_roles || []).filter(role => role.scope === 'project');
  const members = new Map(data.members.map(member => [member.account_id, member]));
  const updateMember = (member, changes) => request(root + '/members/' + encodeURIComponent(member.account_id), {
    method: 'PUT', signal, body: {...changes, expected_version: member.version, expected_project_role: member.project_role},
  });
  host.querySelector('[data-member-add]')?.addEventListener('click', e => open(null, e.currentTarget));
  host.querySelectorAll('[data-member-edit]').forEach(button => {
    button.onclick = () => open(members.get(button.dataset.memberEdit), button);
  });
  host.querySelectorAll('[data-member-remove]').forEach(button => {
    button.onclick = () => confirmRemoval(members.get(button.dataset.memberRemove), button, () => refresh('[data-member-add]'));
  });
  host.querySelectorAll('[data-role-edit]').forEach(button => {
    button.onclick = () => editRole(members.get(button.dataset.roleEdit), button);
  });

  function editRole(member, button) {
    const container = button.parentElement, label = button.previousElementSibling;
    const select = document.createElement('select');
    select.className = 'select';
    select.setAttribute('aria-label', button.getAttribute('aria-label'));
    for (const role of projectRoles) select.add(new Option(role.name, role.id, false, role.id === member.project_role));
    let saving = false;
    function close(restoreFocus) {
      select.remove();
      label.hidden = button.hidden = false;
      if (restoreFocus) button.focus();
    }
    select.onblur = () => { if (!saving && select.isConnected) close(false); };
    select.onkeydown = event => {
      if (event.key === 'Escape' && !saving) { event.preventDefault(); close(true); }
    };
    select.onchange = async () => {
      if (saving) return;
      if (select.value === member.project_role) { close(true); return; }
      saving = true;
      select.disabled = true;
      host.inert = true;
      try {
        await updateMember(member, {project_role: select.value});
        if (!signal.aborted) toast('success', '项目角色已更新。');
      } catch (error) {
        if (signal.aborted) return;
        toast('error', error.status === 409 ? '无法更改角色，请核对最新角色，并保留至少一位项目负责人。'
          : [403,404].includes(error.status) ? '当前账号无权修改该角色。' : '角色保存失败，请重试。');
      } finally { host.inert = false; }
      if (!signal.aborted && container.isConnected) await refresh(`[data-role-edit="${CSS.escape(member.account_id)}"]`);
    };
    label.hidden = button.hidden = true;
    container.append(select);
    select.focus();
    try { select.showPicker?.(); } catch { /* The focused select still supports native keyboard and pointer input. */ }
  }

  function confirmRemoval(member, trigger, removed) {
    let saving = false;
    const abort = () => prompt.close();
    const prompt = openDialog({
      title: '移除项目成员？', variant: 'confirm', tone: 'danger', confirm: '移除成员',
      body: `<p>将移除 ${esc(member.display_name)} 的项目访问权限。</p><p role="status" data-remove-error></p>`,
      async onSubmit(form) {
        if (saving) return {keepOpen:true};
        saving = true;
        const buttons = form.querySelectorAll('button');
        buttons.forEach(button => { button.disabled = true; });
        try {
          await updateMember(member, {role:'revoked'});
          if (!signal.aborted) toast('success', '成员已移除。');
        } catch (error) {
          form.querySelector('[data-remove-error]').textContent = error.status === 409
            ? '无法移除。请检查成员状态、交接未完成的任务，并保留至少一位项目负责人。'
            : [403,404].includes(error.status) ? '当前账号无权移除该成员。' : '移除失败，请重试。';
          return {keepOpen:true};
        } finally {
          saving = false;
          buttons.forEach(button => { button.disabled = false; });
        }
      },
      restoreFocus: ok => !ok && trigger.isConnected ? trigger : undefined,
      onClose(ok) {
        signal.removeEventListener('abort', abort);
        if (ok && !signal.aborted) removed();
      },
    });
    signal.addEventListener('abort', abort, {once:true});
    prompt.addEventListener('cancel', e => { if (saving) { e.preventDefault(); e.stopImmediatePropagation(); } }, true);
    return prompt;
  }

  function open(member, trigger) {
    let selected = [], changed = false, busy = false, prompt;
    const baseline = {project_role: member?.project_role || 'project_member', responsibility: member?.responsibility || ''};
    const values = form => ({project_role: form.elements.project_role.value, responsibility: form.elements.responsibility.value});
    const dirty = form => JSON.stringify(values(form)) !== JSON.stringify(baseline) || (!member && Boolean(selected.length || form.elements.user_query.value));
    const dialog = openDialog({
      title: member ? '编辑项目成员' : '添加项目成员', description: member?.display_name,
      drawer: 'standard', variant: 'form', confirm: member ? '保存修改' : '添加成员',
      body: `<fieldset class="project-profile-fields project-member-fields">
        ${member ? '' : memberPicker()}
        <label class="field pr-form"><span>项目角色</span><select class="select" name="project_role" aria-label="项目角色" ${!projectRoles.length || member?.project_role === 'project_admin' && !data.can_delegate ? 'disabled' : ''}>
          ${projectRoles.map(role => `<option value="${esc(role.id)}" ${role.id === baseline.project_role ? 'selected' : ''} ${role.id === 'project_admin' && !data.can_delegate && baseline.project_role !== role.id ? 'disabled' : ''}>${esc(role.name)}</option>`).join('')}
        </select></label>
        <label class="field pr-form"><span>项目职责</span><textarea name="responsibility" rows="4" maxlength="4000" placeholder="${member ? '填写此人在本项目中的职责…' : '填写所选成员在本项目中的共同职责…'}">${esc(baseline.responsibility)}</textarea></label>
      </fieldset><p data-error role="status"></p>
      ${member && canRemoveMember(data, member) ? '<button type="button" class="button" data-remove>移除成员</button>' : ''}`,
      isDirty: dirty,
      async onSubmit(form) {
        if (busy) return {keepOpen: true};
        if (!member && !selected.length) return {field: form.elements.user_query, message: '请从候选列表选择人员。'};
        if (!projectRoles.length) {message.textContent = '项目角色暂不可用，请关闭后重新加载。'; return {keepOpen: true};}
        const body = member
          ? values(form)
          : {...values(form), members: selected.map(user => ({account_id: user.account_id, expected_version: user.version}))};
        return await mutate(body) ? undefined : {keepOpen: true};
      },
      onClose() {
        signal.removeEventListener('abort', abort); window.removeEventListener('beforeunload', warn);
        delete navigation.hasDraft; delete navigation.confirmNavigation; prompt?.close();
        if (changed && !signal.aborted) refresh();
      },
      restoreFocus: () => signal.aborted ? undefined : trigger,
    });
    const form = dialog.querySelector('form'), message = dialog.querySelector('[data-error]');
    const fieldset = dialog.querySelector('fieldset'), submit = form.querySelector('[type=submit]');
    const abort = () => dialog.close();
    const warn = e => {if (dialog.open && dirty(form)) {e.preventDefault(); e.returnValue = '';}};
    signal.addEventListener('abort', abort, {once: true}); window.addEventListener('beforeunload', warn, {signal});
    navigation.hasDraft = () => dialog.open && dirty(form);
    navigation.confirmNavigation = () => {
      if (busy) return false;
      if (!dirty(form)) {dialog.close(); return true;}
      return new Promise(resolve => {prompt = openDialog({title: '放弃修改？', variant: 'confirm', tone: 'warning', body: '<p>本次修改尚未保存。</p>', cancelLabel: '继续编辑', confirm: '放弃修改', onClose(ok) {prompt = null; if (ok) dialog.close(); resolve(ok);}});});
    };

    if (!member) {
      mountMemberPicker(dialog, async (query, page) => {
        const params = new URLSearchParams({q: query, page: String(page)});
        return (await request(root + '/member-candidates?' + params, {signal})).items;
      }, users => {selected = users;});
    }
    async function mutate(body) {
      if (busy) return false;
      busy = true; fieldset.disabled = true; submit.disabled = true; message.textContent = '正在保存…';
      try {
        if (member) await updateMember(member, body);
        else await request(root + '/members', {method:'POST', body, signal});
        changed = true;
        toast('success', member ? '项目成员已保存。' : `已添加 ${selected.length} 名项目成员。`);
        return true;
      } catch (error) {
        if (!signal.aborted) message.textContent = error.status === 409 ? (member ? '人员或角色已更新，或仍有未完成任务。请先交接任务，关闭后重新加载成员再操作。' : '所选人员的成员状态已变更，本次未添加。请重新核对名单。') : [403, 404].includes(error.status) ? '当前账号无权修改该成员。' : '保存失败，已保留输入，请重试。';
        return false;
      } finally {busy = false; fieldset.disabled = false; submit.disabled = false;}
    }
    dialog.querySelector('[data-remove]')?.addEventListener('click', e => {
      if (busy) return;
      prompt = confirmRemoval(member, e.currentTarget, () => { changed = true; dialog.close(); });
    });
  }
}
