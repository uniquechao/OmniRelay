const system = '/api/v1/system';
const titles = { users:'用户管理', roles:'角色权限', audit:'审计日志' };
const statuses = { active:'已启用', disabled:'已停用', unprovisioned:'尚未开通' };
const scopes = { org:'组织级', project:'项目级' };
const eventKinds = { login:'身份认证', account:'用户管理', role:'角色授权', config:'系统配置', directory:'组织同步' };
const eventResults = { success:'成功', denied:'已拒绝', cancelled:'已取消', failed:'失败', requested:'已请求', coalesced:'已合并' };
const statusTone = status => status === 'active' ? 'success' : 'pending';
const dateFormat = new Intl.DateTimeFormat('sv-SE', {
  timeZone:'Asia/Singapore', year:'numeric', month:'2-digit', day:'2-digit',
  hour:'2-digit', minute:'2-digit', second:'2-digit'
});
const showTime = value => value && Number.isFinite(Date.parse(value)) ? dateFormat.format(new Date(value)) : '—';
const messageOf = (error, fallback) => error instanceof Error && error.message ? error.message : fallback;

function organizationTree(host, departments, selectedId, onSelect) {
  const { escape, icon } = window.OmniRelayUI;
  const byParent = new Map();
  const departmentIds = new Set(departments.map(item => String(item.id)));
  const rendered = new Set();
  const included = new Map();
  let sequence = 0;
  for (const item of departments) {
    const parent = departmentIds.has(String(item.parentId)) ? String(item.parentId) : '';
    if (!byParent.has(parent)) byParent.set(parent, []);
    byParent.get(parent).push(item);
  }

  const pick = (id, label, path, symbol) =>
    '<button class="tree-choice" type="button" data-org="' + escape(id) + '" aria-pressed="' + (id === selectedId) +
    '" title="' + escape(path) + '">' + icon(symbol) + '<span>' + escape(label) + '</span>' +
    '<span data-chosen aria-hidden="true">✓</span></button>';
  const toggle = (key, label, open) =>
    '<button class="tree-toggle" type="button" data-tree-toggle aria-label="' + escape(label) +
    '分支" aria-controls="' + key + '" aria-expanded="' + open + '">' + icon('action-expand','icon tree-expander') + '</button>';
  function branch(item, ancestors, open) {
    const id = String(item.id);
    if (rendered.has(id)) return '';
    rendered.add(id);
    const path = [...ancestors, item.name];
    const descendants = new Set([id]);
    included.set(id, { path:path.join(' / '), descendants });
    const key = 'directory-node-' + ++sequence;
    const children = (byParent.get(id) ?? []).map((child,index) => {
      const markup = branch(child, path, index === 0);
      if (markup) for (const descendant of included.get(String(child.id)).descendants) descendants.add(descendant);
      return markup;
    }).join('');
    const expanded = open || descendants.has(selectedId);
    const choice = pick(id, item.name, path.join(' / '), ancestors.length && !children ? 'nav-team' : 'nav-projects');
    return children ?
      '<li class="tree-branch"><div class="tree-row">' + toggle(key,item.name,expanded) + choice + '</div>' +
        '<ul class="tree-children" id="' + key + '"' + (expanded ? '' : ' hidden') + '>' + children + '</ul></li>' :
      '<li class="tree-row tree-leaf">' + choice + '</li>';
  }
  let branches = (byParent.get('') ?? []).map((item,index) => branch(item, [], index === 0)).join('');
  for (const item of departments) branches += branch(item, [], false);
  host.innerHTML = '<div class="dev-tree" role="group" aria-label="组织结构"><div class="tree-surface tree-branch tree-root">' +
    '<div class="tree-row tree-root-row">' + toggle('directory-root','全部组织',true) +
    pick('','全部组织','全部组织','nav-components') + '<div class="tree-tools">' +
    '<button class="tree-tool" type="button" data-tree-expand="true" aria-label="全部展开" title="全部展开">+</button>' +
    '<button class="tree-tool" type="button" data-tree-expand="false" aria-label="全部收起" title="全部收起">−</button></div></div>' +
    '<ul class="tree-children" id="directory-root">' + branches + '</ul></div>' +
    '<p class="sr-only" data-org-selection role="status"></p></div>';
  const tree = host.firstElementChild;
  function expand(button, value) {
    button.setAttribute('aria-expanded',String(value));
    tree.querySelector('#' + button.getAttribute('aria-controls')).hidden = !value;
  }
  tree.addEventListener('click',event => {
    const button = event.target.closest('button');
    if (!button) return;
    if (button.hasAttribute('data-tree-expand')) {
      tree.querySelectorAll('[data-tree-toggle]').forEach(toggleButton => expand(toggleButton,button.dataset.treeExpand === 'true'));
    }
    if (button.hasAttribute('data-tree-toggle')) {
      const open = button.getAttribute('aria-expanded') !== 'true';
      const node = button.closest('.tree-branch');
      if (open && !node.classList.contains('tree-root')) {
        for (const sibling of node.parentElement.children) {
          if (sibling === node) continue;
          const toggleButton = sibling.querySelector(':scope > .tree-row > [data-tree-toggle]');
          if (toggleButton) expand(toggleButton,false);
        }
      }
      expand(button,open);
    }
    if (button.hasAttribute('data-org')) {
      const selection = included.get(button.dataset.org);
      tree.querySelectorAll('[data-org]').forEach(choice => choice.setAttribute('aria-pressed',String(choice === button)));
      tree.querySelector('[data-org-selection]').textContent = '已选择 ' + (selection?.path ?? '全部组织');
      onSelect(button.dataset.org,selection?.descendants);
    }
  });
  return included.get(selectedId)?.descendants;
}

export async function mountAdmin(page, section, api, { user, signal } = {}) {
  if (!Object.hasOwn(titles,section)) throw new Error('未知的系统管理页面');
  const { escape, mountList, openDialog, toast } = window.OmniRelayUI;
  const usersPage = section === 'users';
  const ifUsers = html => usersPage ? html : '';
  const request = (path,options) => api(system + path, { signal, ...options });
  const userPath = id => '/users/' + encodeURIComponent(id);
  const element = document.createElement('template');
  element.innerHTML = '<header class="head admin-heading"><h1 id="page-heading" tabindex="-1">' + titles[section] + '</h1>' +
    ifUsers('<div class="system-heading-actions"><button class="button primary" type="button" data-directory-sync title="同步飞书组织与用户">同步</button>' +
    '<button class="button" type="button" data-directory-details>同步详情</button></div>') + '</header>' +
    ifUsers('<div class="directory-layout"><details class="directory-org" open><summary>组织结构</summary>' +
    '<div class="directory-tree" data-directory-tree></div></details>') +
    '<section id="system-list"><p class="empty" role="status">加载中…</p></section>' + ifUsers('</div>');
  const heading = element.content.querySelector('#page-heading');
  page.replaceChildren(element.content);
  const list = page.querySelector('#system-list');
  const inPage = () => !signal?.aborted && list.isConnected;
  const focusHeading = () => inPage() ? heading : undefined;
  const syncButton = page.querySelector('[data-directory-sync]');
  const syncDetails = page.querySelector('[data-directory-details]');
  const treeHost = page.querySelector('[data-directory-tree]');
  const dialogs = new Set();
  let userDrawer;
  let navigationPrompt;
  if (usersPage) {
    page.querySelector('.directory-org').open = !matchMedia('(max-width: 720px)').matches;
    syncButton.disabled = true;
    syncDetails.disabled = true;
  }
  let pollTimer;
  signal?.addEventListener('abort',() => {
    clearTimeout(pollTimer);
    for (const dialog of dialogs) dialog.close('cancel');
  }, { once:true });
  function showDialog(options) {
    const dialog = openDialog({
      ...options,
      onClose(...args) {
        dialogs.delete(dialog);
        options.onClose?.(...args);
      }
    });
    dialogs.add(dialog);
    return dialog;
  }
  function confirmNavigation() {
    const draft = userDrawer;
    if (navigationPrompt) return navigationPrompt;
    if ([...dialogs].some(dialog => dialog.open && dialog.hasAttribute('aria-busy'))) return Promise.resolve(false);
    if (!draft?.dialog.open) return Promise.resolve(true);
    if (!draft.dirty()) return Promise.resolve(true);
    if ([...document.querySelectorAll('dialog[open]')].at(-1) !== draft.dialog) return Promise.resolve(false);
    navigationPrompt = new Promise(resolve => {
      showDialog({
        title:'保存修改？', variant:'confirm', tone:'warning',
        body:'<p class="dialog-note">有未保存的修改，直接关闭会丢失本次更改。</p>',
        cancelLabel:'继续编辑', discard:'不保存', confirm:'保存并关闭',
        async onClose(confirmed,result) {
          try {
            if (result === 'discard' && draft.dialog.open) draft.dialog.close('cancel');
            const proceed = result === 'discard' || (confirmed && draft.dialog.open && await draft.save());
            if (!proceed && draft.dialog.open && !draft.dialog.contains(document.activeElement)) draft.focus();
            resolve(proceed);
          } catch (error) {
            if (draft.dialog.open) fieldError(draft.dialog,messageOf(error,'无法保存修改，请重试。'));
            resolve(false);
          } finally {
            navigationPrompt = undefined;
          }
        }
      });
    });
    return navigationPrompt;
  }

  const rows = [];
  const catalog = new Map();
  let selectedId = '';
  let selectedDepartments;
  let sync = null;
  let detailsTarget;
  let lastDetailsMarkup = '';
  let loadingSync = false;
  let requestingSync = false;
  let refreshingDirectory = false;
  let listVersion = 0;
  let mounted;
  const refresh = () => mounted.refresh();
  const findUser = id => rows.find(record => String(record.id) === id);
  const roleName = id => catalog.get(id)?.name ?? id;
  const userRoles = record => [...new Set((record.roles ?? []).map(grant => roleName(grant.role)))];
  const sourceOf = record => record.source || '—';
  const isCurrent = record => user?.id != null && String(user.id) === String(record.id);
  const badge = (tone,label) => '<span class="chip" data-tone="' + tone + '">' + escape(label) + '</span>';
  const action = (key,id,label,danger = false) =>
    '<button class="row-action' + (danger ? ' admin-danger' : '') + '" type="button" data-admin-action="' +
    key + '" data-record="' + escape(id) + '">' + label + '</button>';
  const column = (key,label,render,sort,secondary = false) => ({ key,label,render,sort,secondary });
  const group = (label,body) => '<section class="drawer-section"><h3>' + escape(label) + '</h3>' + body + '</section>';
  const details = entries => '<dl class="admin-details">' + entries.map(([label,value,tone]) =>
    '<div><dt>' + escape(label) + '</dt><dd>' + (tone ? badge(tone,value) : escape(value ?? '—')) + '</dd></div>').join('') + '</dl>';
  const grantScopeLabel = (grant,record) => grant.scope === 'project'
    ? record.projects?.find(project => project.id === grant.project)?.name || grant.project || '项目'
    : '本组织';
  const focusAction = (key,id) => [...list.querySelectorAll('[data-admin-action]')].find(button =>
    button.dataset.adminAction === key && button.dataset.record === String(id)) ?? focusHeading();

  function configuration() {
    const shared = { key:'system-' + section, label:titles[section], selection:false, rows };
    if (section === 'users') return {
      ...shared, itemLabel:'用户或账号', rowLabel:record => record.name,
      includes:record => !selectedId || (record.departmentIds ?? []).some(id => selectedDepartments?.has(String(id))),
      search:record => [record.name,record.id,record.org,sourceOf(record),...userRoles(record)].join(' '),
      filters:[{ key:'status', label:'账号状态', options:statuses }],
      columns:[
        column('name','用户',record => '<strong>' + escape(record.name) + '</strong><small class="admin-cell-sub">' +
          escape(record.status === 'unprovisioned' ? sourceOf(record) : record.id + ' · ' + sourceOf(record)) + '</small>',record => record.name),
        column('org','组织',record => escape(record.org || '—'),record => record.org || '',true),
        column('roles','角色',record => escape(userRoles(record).join('、') || '未授权'),null,true),
        column('status','状态',record => badge(statusTone(record.status),statuses[record.status] ?? '未知'),record => record.status),
        column('actions','操作',record => '<div class="admin-actions">' + action('user',record.id,'查看') +
          (!isCurrent(record) && ['active','disabled'].includes(record.status) ?
            action('toggle',record.id,record.status === 'active' ? '停用' : '启用',record.status === 'active') : '') + '</div>')
      ]
    };
    if (section === 'roles') return {
      ...shared, itemLabel:'角色', rowLabel:role => role.name, search:role => [role.name,...role.permissions].join(' '),
      filters:[{ key:'scope', label:'授权范围', options:scopes }],
      columns:[
        column('name','角色名称',role => '<strong>' + escape(role.name) + '</strong>',role => role.name),
        column('scope','范围',role => badge(role.scope === 'org' ? 'progress' : 'important',scopes[role.scope]),role => scopes[role.scope]),
        column('permissions','权限摘要',role => escape(role.permissions[0] ?? '—'),null,true),
        column('grants','授权数',role => escape(role.count ?? '—'),role => role.count ?? -1),
        column('actions','操作',role => action('role',role.id,'查看权限'))
      ]
    };
    return {
      ...shared, itemLabel:'操作、操作者或编号', rowLabel:entry => entry.action,
      search:entry => [entry.actor,entry.action,entry.target,entry.id].join(' '),
      filters:[{ key:'kind', label:'事件类型', options:eventKinds },{ key:'result', label:'结果', options:eventResults }],
      defaultSort:{ key:'time', direction:'desc' },
      columns:[
        column('time','时间 (UTC+08:00)',entry => '<span class="date">' + showTime(entry.time) + '</span>',entry => Date.parse(entry.time)),
        column('action','事件',entry => '<strong>' + escape(entry.action) + '</strong><small class="admin-cell-sub">' +
          escape(eventKinds[entry.kind] ?? entry.kind) + '<span class="audit-compact-actor"> · ' + escape(entry.actor) + '</span></small>',entry => entry.action),
        column('actor','操作者',entry => escape(entry.actor),entry => entry.actor,true),
        column('target','对象',entry => escape(entry.target),entry => entry.target,true),
        column('result','结果',entry => badge({ success:'success', denied:'danger', cancelled:'pending', failed:'danger' }[entry.result] ?? 'pending',
          eventResults[entry.result] ?? entry.result),entry => entry.result),
        column('actions','详情',entry => action('event',entry.id,'查看详情'))
      ]
    };
  }

  function replaceRows(next) {
    rows.splice(0,rows.length,...next);
    if (mounted) refresh();
    else mounted = mountList(list, configuration());
  }
  function replaceCatalog(roles) {
    catalog.clear();
    for (const role of roles) catalog.set(role.id,role);
  }
  function paintTree(departments) {
    if (!departments.some(item => String(item.id) === selectedId)) selectedId = '';
    selectedDepartments = organizationTree(treeHost,departments,selectedId,(id,descendants) => {
      if (id === selectedId) return;
      selectedId = id;
      selectedDepartments = descendants;
      mounted.resetPage();
    });
  }
  async function refreshUsers() {
    const version = ++listVersion;
    const next = await request('/users');
    if (inPage() && version === listVersion) replaceRows(next);
  }
  async function refreshDirectory() {
    if (refreshingDirectory) return;
    refreshingDirectory = true;
    try {
      const departments = await request('/directory/departments');
      if (!inPage()) return;
      paintTree(departments);
      await refreshUsers();
    } catch (error) {
      if (inPage()) toast('error',messageOf(error,'无法刷新组织与用户，请重试。'));
    } finally {
      refreshingDirectory = false;
    }
  }

  function busyDialog(dialog, pending) {
    dialog.toggleAttribute('aria-busy', pending);
    const controls = [...dialog.querySelectorAll(':is(button,select):not([disabled])')];
    controls.forEach(control => { control.disabled = pending; });
    return () => {
      dialog.removeAttribute('aria-busy');
      controls.forEach(control => { control.disabled = false; });
    };
  }
  function blockBusyDismiss(dialog) {
    function block(event) {
      if (!dialog.hasAttribute('aria-busy')) return;
      event.preventDefault();
      event.stopImmediatePropagation();
    }
    dialog.addEventListener('cancel',block,true);
    dialog.addEventListener('click',event => { if (event.target === dialog) block(event); },true);
  }
  function fieldError(dialog,message,field) {
    const error = dialog.querySelector('.field-error');
    error.hidden = !message;
    error.textContent = message;
    if (!message) return;
    if (field) {
      field.setAttribute('aria-invalid','true');
      field.setAttribute('aria-describedby',error.id);
      field.focus();
    } else {
      error.tabIndex = -1;
      error.focus();
    }
  }
  function clearRoleError(dialog) {
    dialog.querySelectorAll('[aria-invalid="true"]').forEach(field => {
      field.removeAttribute('aria-invalid');
      field.removeAttribute('aria-describedby');
    });
    fieldError(dialog,'');
  }
  function confirmChange({ title, description, message, confirm, change, trigger, restoreFocus, onConfirmed, tone = 'danger' }) {
    const dialog = showDialog({
      title, description, body:'<p class="dialog-note">' + escape(message) + '</p>',
      confirm, tone, variant:'confirm', restoreFocus:() => trigger.isConnected ? trigger : restoreFocus?.() ?? focusHeading(),
      onClose(confirmed) { if (confirmed) onConfirmed?.(); },
      onSubmit() {
        if (dialog.hasAttribute('aria-busy')) return { keepOpen:true };
        fieldError(dialog,'');
        const unlock = busyDialog(dialog,true);
        void (async () => {
          try {
            await change();
            if (dialog.open) dialog.close('confirm');
          } catch (error) {
            if (dialog.open) fieldError(dialog,messageOf(error,'操作失败，请重试。'));
          } finally {
            unlock();
          }
        })();
        return { keepOpen:true };
      }
    });
    blockBusyDismiss(dialog);
  }

  function toggleUser(record, trigger) {
    const active = record.status === 'active';
    const status = active ? 'disabled' : 'active';
    confirmChange({
      title:active ? '停用账号' : '启用账号', description:record.name,
      message:active ? '停用后，该用户将无法使用工作台和飞书 Bot。' : '启用后，该用户可按已有授权继续访问。',
      confirm:active ? '确认停用' : '确认启用', tone:active ? 'danger' : 'default',
      trigger, restoreFocus:() => focusAction('toggle',record.id),
      async change() {
        await request(userPath(record.id), { method:'PATCH', body:{ status } });
        if (!inPage()) return;
        record.status = status;
        const current = findUser(String(record.id));
        if (current) current.status = status;
        refresh();
        toast('success',record.name + '的账号' + statuses[status] + '。');
      }
    });
  }

  function options(items,placeholder) {
    return '<option value="">' + escape(placeholder) + '</option>' +
      items.map(([id,label]) => '<option value="' + escape(id) + '">' + escape(label) + '</option>').join('');
  }
  function showUser(record) {
    const unprovisioned = record.status === 'unprovisioned';
    let replacementDrawer = false;
    const canProvision = unprovisioned && record.directoryStatus === 'active';
    const provision = canProvision ? '<div class="user-grant-tools">' +
      '<button class="button primary" type="button" data-admin-action="provision">开通账号</button></div>' : '';
    const content = () => (record.roles ?? []).length ? '<ul class="admin-grants">' + record.roles.map(grant =>
      '<li><div class="user-grant-label"><strong>' + escape(roleName(grant.role)) + '</strong>' +
      '<small class="user-grant-scope" data-scope="' + (grant.scope === 'project' ? 'project' : 'org') + '">' + escape(grantScopeLabel(grant,record)) +
      '</small></div>' + (!isCurrent(record) || grant.role !== 'admin' ? action('revoke',grant.id,'撤销',true) : '') + '</li>').join('') +
      '</ul>' : '<p class="aux">' + (record.status === 'unprovisioned' ?
        '飞书目录人员尚未开通账号。开通后可在此授予角色。' : '当前无角色授权。') + '</p>';
    const roleOptions = options([...catalog.values()].filter(role => role.grantable !== false)
      .map(role => [role.id,role.name + ' · ' + scopes[role.scope]]),'选择角色');
    const editor = record.status === 'active' ? '<div class="user-grant-tools">' +
      '<button class="button" type="button" data-admin-action="add-role" aria-expanded="false" aria-controls="user-grant-editor">' +
      '<span aria-hidden="true">＋</span> 添加角色</button></div>' +
      '<div class="user-grant-editor" id="user-grant-editor" hidden>' +
      '<label class="field admin-field"><span class="field-label">角色</span><select class="select" name="role" disabled required>' +
      roleOptions + '</select></label><label class="field admin-field" hidden><span class="field-label">适用项目</span>' +
      '<select class="select" name="scope" disabled></select></label>' +
      '<p class="aux full-field" data-project-empty hidden>该用户没有可授权的项目。</p>' +
      '<div class="user-grant-buttons"><button class="button neutral" type="button" data-admin-action="cancel-role">取消</button>' +
      '<button class="button primary" type="submit" data-save-role>保存</button></div></div>' : '';
    const dialog = showDialog({
      title:'用户详情', description:record.name, drawer:'standard', tone:'info',
      restoreFocus:() => inPage() && !replacementDrawer ? focusAction('user',record.id) : undefined,
      body:group('账号信息',details([
        ['账号编号',record.status === 'unprovisioned' ? '尚未开通' : record.id],['所属组织',record.org || '—'],
        ['身份来源',sourceOf(record)],['状态',statuses[record.status] ?? '未知',statusTone(record.status)]
      ]) + provision) + group('角色授权','<div data-user-grants>' + content() + '</div>' + editor + '<span class="sr-only" data-grant-status role="status"></span>'),
      isDirty:() => dirty(),
      onClose() {
        window.removeEventListener('beforeunload',warnBeforeUnload);
        if (userDrawer?.dialog === dialog) userDrawer = undefined;
      },
      onSubmit(_,submitter) {
        if (!row || row.hidden || dialog.hasAttribute('aria-busy')) return { keepOpen:true };
        clearRoleError(dialog);
        const grant = readGrant();
        if (grant.error) return grant.error;
        void saveGrant(grant,!submitter?.matches('[data-save-role]'));
        return { keepOpen:true };
      }
    });
    dialog.classList.add('user-detail-drawer');
    blockBusyDismiss(dialog);
    const row = dialog.querySelector('.user-grant-editor');
    const add = dialog.querySelector('[data-admin-action="add-role"]');
    const form = dialog.querySelector('form');
    const grantBody = dialog.querySelector('[data-user-grants]');
    const save = dialog.querySelector('[data-save-role]');
    let fetchingGrants = false;
    const dirty = () => Boolean(form.elements.role?.value);
    function warnBeforeUnload(event) {
      event.preventDefault();
      event.returnValue = true;
    }
    function updateUnloadGuard() {
      if (dirty()) window.addEventListener('beforeunload',warnBeforeUnload);
      else window.removeEventListener('beforeunload',warnBeforeUnload);
    }
    function readGrant() {
      const roleField = form.elements.role;
      const scopeField = form.elements.scope;
      const role = catalog.get(roleField?.value);
      const project = role?.scope === 'project' ? scopeField.value : '';
      const invalid = (field,message) => ({ error:{ field,message } });
      if (!role || role.grantable === false) return invalid(roleField,'请选择可授权的角色。');
      if (role.scope === 'project' && !project) {
        return scopeField.disabled ? invalid(roleField,'该用户没有可授权的项目。') : invalid(scopeField,'请选择项目。');
      }
      if ((record.roles ?? []).some(grant => grant.role === role.id && (grant.project || '') === project)) {
        return invalid(project ? scopeField : roleField,'该用户已有相同范围的角色授权。');
      }
      return { role,project };
    }
    async function saveGrant({ role, project },closeAfter) {
      const unlock = busyDialog(dialog,true);
      try {
        await request(userPath(record.id) + '/roles',{ method:'POST', body:{ role:role.id, project } });
      } catch (error) {
        unlock();
        if (dialog.open) fieldError(dialog,messageOf(error,'角色保存失败，请重试。'),form.elements[project ? 'scope' : 'role']);
        return false;
      }
      unlock();
      if (!inPage() || !dialog.open) return false;
      dialog.querySelector('[data-grant-status]').textContent = '角色已保存。';
      toast('success','已为' + record.name + '授予' + role.name + '。');
      setEditor(false);
      await updateGrants();
      if (closeAfter && dialog.open) dialog.close('confirm');
      return true;
    }
    userDrawer = {
      dialog,dirty,focus:() => (form.elements.role ?? dialog.querySelector('.dialog-close')).focus(),
      async save() {
        if (!form.reportValidity()) return false;
        const grant = readGrant();
        if (!grant.error) return saveGrant(grant,true);
        fieldError(dialog,grant.error.message,grant.error.field);
        return false;
      }
    };

    async function updateGrants() {
      fetchingGrants = true;
      if (add) add.disabled = true;
      grantBody.querySelectorAll('button').forEach(button => { button.disabled = true; });
      try {
        await refreshUsers();
        if (!dialog.open) return;
        record.roles = findUser(String(record.id))?.roles ?? [];
        grantBody.innerHTML = content();
      } catch (error) {
        if (dialog.open && inPage()) {
          grantBody.querySelectorAll('button').forEach(button => { button.disabled = false; });
          toast('warning',messageOf(error,'变更已提交，刷新角色授权失败。'));
        }
      } finally {
        fetchingGrants = false;
        if (add) add.disabled = false;
        if (dialog.open && !row?.hidden) form.elements.role?.focus();
        else if (dialog.open && add && document.activeElement === document.body) add.focus();
      }
    }
    function syncProjects() {
      const field = form.elements.scope;
      const isProject = catalog.get(form.elements.role.value)?.scope === 'project';
      const previous = field.value;
      const available = record.projects ?? [];
      field.closest('label').hidden = !isProject;
      const emptyProjects = Boolean(isProject && !available.length);
      field.required = Boolean(isProject);
      field.disabled = !isProject || emptyProjects;
      field.innerHTML = options(available.map(item => [item.id,item.name]),emptyProjects ? '暂无可选项目' : '选择项目');
      if (available.some(item => item.id === previous)) field.value = previous;
      row.querySelector('[data-project-empty]').hidden = !emptyProjects;
      save.disabled = emptyProjects;
    }
    function setEditor(open) {
      row.hidden = !open;
      add.hidden = open;
      add.setAttribute('aria-expanded',String(open));
      if (!open) form.reset();
      form.elements.role.disabled = !open;
      syncProjects();
      updateUnloadGuard();
      clearRoleError(dialog);
      (open ? form.elements.role : add).focus();
    }
    if (row) {
      form.addEventListener('input',() => clearRoleError(dialog));
      form.elements.role.addEventListener('change',() => {
        syncProjects();
        updateUnloadGuard();
      });
      syncProjects();
    }
    function provisionUser(trigger) {
      let refreshed;
      confirmChange({
        title:'开通账号', description:record.name, tone:'default',
        message:'开通后，该成员可以使用飞书登录 OmniRelay。可在用户详情中继续授予角色。',
        confirm:'确认开通', trigger,
        async change() {
          const created = await request(userPath(record.id) + '/provision',{ method:'POST' });
          if (!inPage()) return;
          try {
            await refreshUsers();
            refreshed = created?.id ? findUser(String(created.id)) : undefined;
          } catch {
            if (inPage()) toast('warning','账号已开通，用户列表刷新失败。请重新打开用户管理。');
            return;
          }
          if (inPage()) toast('success',record.name + '的账号已开通。');
        },
        onConfirmed() {
          if (!inPage() || !dialog.open) return;
          replacementDrawer = Boolean(refreshed);
          dialog.close('confirm');
          if (refreshed) showUser(refreshed);
        }
      });
    }
    dialog.addEventListener('click',event => {
      if (fetchingGrants || dialog.hasAttribute('aria-busy')) return;
      const button = event.target.closest('[data-admin-action]');
      if (!button) return;
      if (button.dataset.adminAction === 'provision' && canProvision) provisionUser(button);
      if (button.dataset.adminAction === 'add-role') setEditor(true);
      if (button.dataset.adminAction === 'cancel-role') setEditor(false);
      if (button.dataset.adminAction !== 'revoke') return;
      const grant = (record.roles ?? []).find(item => String(item.id) === button.dataset.record);
      if (!grant) return;
      confirmChange({
        title:'撤销授权', description:record.name, trigger:button, restoreFocus:() => add ?? dialog.querySelector('.dialog-close'),
        message:'撤销后，该账号将失去「' + roleName(grant.role) + ' · ' + grantScopeLabel(grant,record) + '」对应的访问权限。',
        confirm:'确认撤销',
        async change() {
          await request(userPath(record.id) + '/roles/' + encodeURIComponent(grant.id),{ method:'DELETE' });
          if (!inPage()) return;
          if (dialog.open) await updateGrants();
          toast('success','角色授权已撤销。');
        }
      });
    });
  }

  function showRole(role,trigger) {
    showDialog({
      title:'角色权限', description:role.name, drawer:'standard', tone:'info', restoreFocus:() => trigger.isConnected ? trigger : focusHeading(),
      body:group('授权范围',details([['范围',scopes[role.scope]],['适用条件',role.condition ?? role.boundary]])) +
        group('可执行操作','<ul class="admin-permissions">' + role.permissions.map(item => '<li>' + escape(item) + '</li>').join('') + '</ul>')
    });
  }
  function showEvent(entry,trigger) {
    showDialog({
      title:'审计详情', description:entry.id, drawer:'wide', tone:'info', restoreFocus:() => trigger.isConnected ? trigger : focusHeading(),
      body:group('事件信息',details([
        ['操作时间',showTime(entry.time) + ' (UTC+08:00)'],['操作者',entry.actor],['事件类型',eventKinds[entry.kind] ?? entry.kind],
        ['操作',entry.action],['对象',entry.target],['结果',eventResults[entry.result] ?? entry.result]
      ])) + group('变更摘要','<div class="admin-diff">' + details([['变更前',entry.before || '—'],['变更后',entry.after || '—']]) + '</div>')
    });
  }

  function updateSyncControls() {
    if (!inPage()) return;
    const running = sync?.status === 'running';
    syncButton.disabled = requestingSync || running || sync?.status === 'unconfigured';
    syncButton.textContent = requestingSync || running ? '同步中…' : '同步';
    syncDetails.disabled = false;
    if (!detailsTarget) return;
    const labels = {
      idle:['pending','暂无同步记录'], running:['progress','进行中'], success:['success','已完成'],
      failed:['danger','同步失败'], unconfigured:['pending','未配置']
    };
    const [tone,label] = labels[sync?.status] ?? ['pending',loadingSync ? '正在查询' : '无法获取同步状态'];
    const phase = sync?.phase || label;
    const raw = sync?.progress;
    const percent = Number.isFinite(raw) ? Math.max(0,Math.min(100,raw)) : null;
    const statusTime = running && sync.startedAt ? '开始于 ' + showTime(sync.startedAt) :
      sync?.finishedAt ? '完成于 ' + showTime(sync.finishedAt) : '';
    const subtitle = [percent !== null && (running || sync?.status === 'success') ? percent + '%' : '',statusTime].filter(Boolean).join(' · ');
    const last = sync?.lastSuccess ?? (sync?.status === 'success' ? sync : null);
    const lastTime = last?.finishedAt || sync?.lastSuccessAt;
    const markup = '<section class="directory-sync-status"><div><strong>' + escape(phase) + '</strong>' +
      badge(tone,label) + '</div>' + (running || sync?.status === 'success' ?
        '<progress ' + (percent !== null ? 'value="' + percent + '" ' : '') + 'max="100" aria-label="同步进度"></progress>' : '') +
      (subtitle ? '<span class="aux">' + subtitle + '</span>' : '') +
      (running && Number.isInteger(sync.departments) && Number.isInteger(sync.users) ?
        '<p class="aux">已处理组织 ' + sync.departments + ' 个 · 用户 ' + sync.users + ' 人</p>' : '') +
      (sync?.message ? '<p class="aux">' + escape(sync.message) + '</p>' : '') + '</section>' +
      group('上次同步结果',lastTime ? details([
        ['开始时间',showTime(last?.startedAt)],['完成时间',showTime(lastTime)],
        ['组织',Number.isInteger(last?.departments) ? last.departments + ' 个' : '—'],
        ['用户',Number.isInteger(last?.users) ? last.users + ' 人' : '—'],
        ...Number.isInteger(last?.addedDepartments) ? [['新增组织',last.addedDepartments + ' 个']] : [],
        ...Number.isInteger(last?.addedUsers) ? [['新增用户',last.addedUsers + ' 人']] : []
      ]) : '<p class="aux">暂无成功同步记录。</p>');
    if (markup !== lastDetailsMarkup) {
      detailsTarget.innerHTML = markup;
      lastDetailsMarkup = markup;
    }
  }
  function schedulePoll() {
    clearTimeout(pollTimer);
    if (inPage() && sync?.status === 'running') pollTimer = setTimeout(readSync,1200);
  }
  function applySync(next) {
    const previous = sync;
    sync = next;
    updateSyncControls();
    schedulePoll();
    if (next.status === 'success' && previous?.status === 'running') {
      toast('success','组织与用户同步完成。');
      void refreshDirectory();
    }
    if (next.status === 'failed' && previous?.status === 'running') {
      toast('error',next.message || '组织与用户同步失败，请查看同步详情。');
    }
  }
  async function readSync() {
    if (loadingSync || requestingSync || !inPage()) return;
    loadingSync = true;
    try {
      const data = await request('/directory/sync');
      if (inPage() && !requestingSync) applySync(data);
    } catch (error) {
      if (inPage() && sync?.status === 'running' && !requestingSync) pollTimer = setTimeout(readSync,3500);
    } finally {
      loadingSync = false;
      updateSyncControls();
    }
  }
  async function startSync() {
    requestingSync = true;
    updateSyncControls();
    syncDetails.focus();
    try {
      const data = await request('/directory/sync',{ method:'POST' });
      if (inPage()) applySync(data);
    } catch (error) {
      if (inPage()) toast('error',messageOf(error,'无法启动同步，请重试。'));
    } finally {
      requestingSync = false;
      updateSyncControls();
    }
  }
  function showSyncDetails() {
    const dialog = showDialog({
      title:'同步详情', description:'飞书组织与用户', drawer:'standard', tone:'info',
      body:'<div class="directory-sync-detail" data-sync-detail role="status"></div>',
      restoreFocus:() => syncDetails.isConnected ? syncDetails : focusHeading(),
      onClose:() => { detailsTarget = undefined; }
    });
    dialog.classList.add('user-detail-drawer');
    detailsTarget = dialog.querySelector('[data-sync-detail]');
    lastDetailsMarkup = '';
    updateSyncControls();
    if (!sync) void readSync();
  }

  async function load() {
    list.innerHTML = '<p class="empty" role="status">加载中…</p>';
    try {
      if (usersPage) {
        const [next,roles,departments] = await Promise.all([
          request('/users'),request('/roles'),request('/directory/departments')
        ]);
        if (!inPage()) return;
        replaceCatalog(roles);
        paintTree(departments);
        replaceRows(next);
        syncButton.addEventListener('click',startSync);
        syncDetails.addEventListener('click',showSyncDetails);
        void readSync();
      } else {
        const next = await request('/' + section);
        if (inPage()) replaceRows(next);
      }
    } catch (error) {
      if (!inPage()) return;
      list.innerHTML = '<div class="empty"><p role="alert"></p><button class="button" type="button">重试</button></div>';
      list.querySelector('p').textContent = messageOf(error,'加载失败，请重试。');
      list.querySelector('button').addEventListener('click',load);
    }
  }
  list.addEventListener('click',event => {
    const trigger = event.target.closest('[data-admin-action]');
    if (!trigger) return;
    const id = trigger.dataset.record;
    const record = findUser(id);
    if (!record) return;
    switch (trigger.dataset.adminAction) {
      case 'user': showUser(record); break;
      case 'toggle': toggleUser(record,trigger); break;
      case 'role': showRole(record,trigger); break;
      case 'event': showEvent(record,trigger); break;
    }
  });
  await load();
  return { confirmNavigation };
}
