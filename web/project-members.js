import {userChoice, navigateUserChoices} from './user-picker.js';

export function wireProjectMembers(host, {data, projectId, request, signal, navigation, refresh}) {
  const {escape: esc, openDialog, toast} = window.OmniRelayUI;
  const root = '/api/v1/projects/' + projectId;
  const projectRoles = (data.project_roles || []).filter(role => role.scope === 'project');
  host.querySelector('[data-member-add]')?.addEventListener('click', e => open(null, e.currentTarget));
  host.querySelectorAll('[data-member-edit]').forEach(button => {
    button.onclick = () => open(data.members.find(m => m.account_id === button.dataset.memberEdit), button);
  });

  function open(member, trigger) {
    let target = member, changed = false, busy = false, searchRun = 0, prompt, timer;
    let page = 1, query = '';
    const baseline = {project_role: member?.project_role || 'project_member', responsibility: member?.responsibility || ''};
    const values = form => ({project_role: form.elements.project_role.value, responsibility: form.elements.responsibility.value});
    const dirty = form => JSON.stringify(values(form)) !== JSON.stringify(baseline) || (!member && Boolean(target || form.elements.user_query.value));
    const dialog = openDialog({
      title: member ? '编辑项目成员' : '添加项目成员', description: member?.display_name,
      drawer: 'standard', variant: 'form', confirm: member ? '保存修改' : '添加成员',
      body: `<fieldset class="project-profile-fields">
        ${member ? '' : `<div data-user-picker>
          <label class="field pr-form"><span>选择人员</span><input type="search" name="user_query" aria-label="选择人员" autocomplete="off" maxlength="200" placeholder="搜索群成员姓名或账号…" aria-controls="project-user-options"></label>
          <div class="user-choice-list" id="project-user-options" data-user-results></div>
          <div class="user-choice-pages"><button class="button" type="button" data-previous hidden>上一页</button><button class="button" type="button" data-more hidden>下一页</button></div>
          <p data-selected role="status"></p></div>`}
        <label class="field pr-form"><span>项目角色</span><select class="select" name="project_role" aria-label="项目角色" ${!projectRoles.length || member?.project_role === 'project_admin' && !data.can_delegate ? 'disabled' : ''}>
          ${projectRoles.map(role => `<option value="${esc(role.id)}" ${role.id === baseline.project_role ? 'selected' : ''} ${role.id === 'project_admin' && !data.can_delegate && baseline.project_role !== role.id ? 'disabled' : ''}>${esc(role.name)}</option>`).join('')}
        </select></label>
        <label class="field pr-form"><span>项目职责</span><textarea name="responsibility" rows="4" maxlength="4000" placeholder="填写此人在本项目中的职责…">${esc(baseline.responsibility)}</textarea></label>
      </fieldset><p data-error role="status"></p>
      ${member ? '<button type="button" class="button" data-remove>移除成员</button>' : ''}`,
      isDirty: dirty,
      async onSubmit(form) {
        if (busy) return {keepOpen: true};
        if (!target) return {field: form.elements.user_query, message: '请从候选列表选择人员。'};
        if (!projectRoles.length) {message.textContent = '项目角色暂不可用，请关闭后重新加载。'; return {keepOpen: true};}
        return await mutate({...values(form), expected_version: target.version, expected_project_role: baseline.project_role}) ? undefined : {keepOpen: true};
      },
      onClose() {
        clearTimeout(timer); signal.removeEventListener('abort', abort); window.removeEventListener('beforeunload', warn);
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

    async function search(nextPage = 1) {
      const run = ++searchRun;
      query = form.elements.user_query.value.trim();
      const list = dialog.querySelector('[data-user-results]');
      list.hidden = false; list.setAttribute('aria-busy', 'true'); message.textContent = '正在加载候选人员…';
      try {
        const result = await request(root + '/member-candidates?q=' + encodeURIComponent(query) + '&page=' + nextPage, {signal});
        if (signal.aborted || !dialog.open || run !== searchRun) return;
        page = nextPage;
        list.innerHTML = result.items.map((user, i) => userChoice(user, i)).join('');
        list.querySelectorAll('[data-user-choice]').forEach(button => button.onclick = () => {
          searchRun++; clearTimeout(timer); list.removeAttribute('aria-busy');
          target = result.items[Number(button.dataset.userChoice)];
          form.elements.user_query.value = target.display_name;
          dialog.querySelector('[data-selected]').textContent = '已选择：' + target.display_name + ' · ' + (target.username || (target.account_id.startsWith('feishu:') ? '群成员' : target.account_id));
          message.textContent = ''; list.hidden = true; form.elements.project_role.focus();
        });
        message.textContent = result.items.length ? '' : '没有匹配的候选人员，已入项人员不会重复显示。';
        dialog.querySelector('[data-previous]').hidden = page <= 1;
        dialog.querySelector('[data-more]').hidden = result.items.length < 25;
      } catch {
        if (dialog.open && run === searchRun) message.innerHTML = '用户列表加载失败。<button class="button" type="button" data-retry>重试</button>';
        message.querySelector('[data-retry]')?.addEventListener('click', () => search(nextPage));
      } finally {if (run === searchRun) list.removeAttribute('aria-busy');}
    }
    if (!member) {
      const box = dialog.querySelector('[data-user-picker]'), input = form.elements.user_query;
      input.oninput = () => {searchRun++; target = null; dialog.querySelector('[data-selected]').textContent = ''; clearTimeout(timer); timer = setTimeout(() => search(), 180);};
      box.onkeydown = e => navigateUserChoices(e, box);
      input.onkeydown = e => {if (e.key === 'Enter') {e.preventDefault(); clearTimeout(timer); void search();}};
      dialog.querySelector('[data-more]').onclick = () => search(page + 1);
      dialog.querySelector('[data-previous]').onclick = () => search(page - 1);
      void search();
    }
    async function mutate(body) {
      if (busy) return false;
      busy = true; fieldset.disabled = true; submit.disabled = true; message.textContent = '正在保存…';
      try {
        await request(root + '/members/' + encodeURIComponent(target.account_id), {method: 'PUT', body, signal});
        changed = true; toast('success', body.role === 'revoked' ? '成员已移除。' : '项目成员已保存。'); return true;
      } catch (error) {
        if (!signal.aborted) message.textContent = error.status === 409 ? '人员或角色已更新，或仍有未完成任务。请先交接任务，关闭后重新加载成员再操作。' : [403, 404].includes(error.status) ? '当前账号无权修改该成员。' : '保存失败，已保留输入，请重试。';
        return false;
      } finally {busy = false; fieldset.disabled = false; submit.disabled = false;}
    }
    dialog.querySelector('[data-remove]')?.addEventListener('click', () => {
      if (busy) return;
      prompt = openDialog({title: '移除项目成员？', variant: 'confirm', tone: 'warning', confirm: '移除成员', body: `<p>将移除 ${esc(member.display_name)} 的项目访问权限。有未完成任务时，请先交接任务；最后一位负责人不能移除。</p><p role="status" data-remove-error></p>`, async onSubmit(form) {if (await mutate({role: 'revoked', expected_version: member.version})) return; form.querySelector('[data-remove-error]').textContent = message.textContent; return {keepOpen: true};}, onClose(ok) {prompt = null; if (ok) dialog.close();}});
    });
  }
}
