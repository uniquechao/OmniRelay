import { isDirectoryConflict } from './system-api.js';

const system = '/api/v1/system';
const titles = { users:'用户管理', roles:'角色权限', audit:'审计日志' };
const statuses = { active:'已启用', disabled:'已停用', unprovisioned:'尚未开通' };
const directoryStatuses = { active:'在职', inactive:'未启用', resigned:'已离职', unknown:'未知' };
function syncFailure(reason, scopeHidden) {
  if (/paginat|page|分页/i.test(reason ?? '')) return '飞书目录分页不完整，请重试同步。';
  if (/credentials are missing/i.test(reason ?? '')) return '飞书应用凭据未配置，请联系管理员。';
  if (scopeHidden || /authorization scope changed|unsupported groups/i.test(reason ?? '')) {
    return '飞书通讯录授权已变化，请核对应用的可读范围后重试。';
  }
  if (/HTTP 429|request budget|deadline/i.test(reason ?? '')) return '飞书响应超时或请求受限，请稍后重试。';
  if (/transport unavailable|HTTP 5[0-9]{2}|retries exhausted/i.test(reason ?? '')) return '飞书服务暂不可用，请稍后重试。';
  if (/invalid|inconsistent|incomplete|orphaned|unexpected|duplicate/i.test(reason ?? '')) {
    return '飞书目录数据未通过校验，请重试同步。';
  }
  return '同步未完成，请检查飞书应用的通讯录授权和连接状态后重试。';
}
const activeRun = run => ['queued','running'].includes(run?.status);
const scopes = { system:'系统级', project:'项目级' };
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
  const canManageAccounts = user.permissions?.['account.manage'] === true;
  const canManageRoles = user.permissions?.['role.manage'] === true;
  const maySync = user.permissions?.['directory.sync'] === true;
  const ifUsers = html => usersPage ? html : '';
  const request = (path,options) => api(system + path, { signal, ...options });
  const userPath = id => '/users/' + encodeURIComponent(id);
  const element = document.createElement('template');
  element.innerHTML = '<header class="head admin-heading"><h1 id="page-heading" tabindex="-1">' + titles[section] + '</h1>' +
    ifUsers('<div class="system-heading-actions"><button class="button primary" type="button" data-directory-sync title="同步飞书组织与用户">同步</button>' +
    '<button class="button" type="button" data-directory-details>同步详情</button></div>') + '</header>' +
    ifUsers('<div class="list-toolbar-line" data-directory-notice hidden><span class="aux" role="status"></span>' +
    '<button class="button" type="button" data-directory-retry hidden>重试</button></div>') +
    ifUsers('<div class="directory-layout"><details class="directory-org" open><summary>组织结构</summary>' +
    '<div class="directory-tree" data-directory-tree></div></details>') +
    '<section id="system-list" class="or-loading-placeholder"></section>' + ifUsers('</div>');
  const heading = element.content.querySelector('#page-heading');
  page.replaceChildren(element.content);
  const list = page.querySelector('#system-list');
  const inPage = () => !signal?.aborted && list.isConnected;
  const focusHeading = () => inPage() ? heading : undefined;
  const syncButton = page.querySelector('[data-directory-sync]');
  const syncDetails = page.querySelector('[data-directory-details]');
  const treeHost = page.querySelector('[data-directory-tree]');
  const notice = page.querySelector('[data-directory-notice]');
  const retry = page.querySelector('[data-directory-retry]');
  const dialogs = new Set();
  let userDrawer;
  let navigationPrompt;
  if (usersPage) {
    page.querySelector('.directory-org').open = !matchMedia('(max-width: 720px)').matches;
    syncButton.hidden = !maySync;
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
  let directoryMeta;
  let displayedVersion;
  let readForbidden = false;
  let syncForbidden = false;
  let directoryError = false;
  let syncError = false;
  let syncRevision = 0;
  let syncRequest = 0;
  let summaryRequest;
  let listVersion = 0;
  let directoryRequest;
  let reloadingAfterConflict = false;
  let mounted;
  const refresh = () => mounted.refresh();
  const findUser = id => rows.find(record => String(record.id) === id);
  const roleName = id => catalog.get(id)?.name ?? id;
  const displayedRoles = record => [
    ...(record.systemRoles ?? []).filter(id => !record.roles.some(grant => grant.role === id))
      .map(id => ({ role:id, scope:'system', default:catalog.get(id)?.default === true })),
    ...record.roles
  ];
  const userRoles = record => [...new Set(displayedRoles(record).map(grant => roleName(grant.role)))];
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
    : grant.default ? '系统级 · 默认拥有' : record.status === 'disabled' ? '系统级 · 已停用' : '系统级';
  const focusAction = (key,id) => [...list.querySelectorAll('[data-admin-action]')].find(button =>
    button.dataset.adminAction === key && button.dataset.record === String(id)) ?? focusHeading();

  function configuration() {
    const shared = { key:'system-' + section, label:titles[section], selection:false, rows };
    if (section === 'users') return {
      ...shared, itemLabel:'用户或账号', rowLabel:record => record.name,
      includes:record => !directoryMeta?.visible || !selectedId || (record.departmentIds ?? []).some(id => selectedDepartments?.has(String(id))),
      search:record => [record.name,record.id,record.org,sourceOf(record),...userRoles(record)].join(' '),
      filters:[{ key:'status', label:'账号状态', options:statuses }],
      columns:[
        column('name','用户',record => {
          const summary = [record.status === 'unprovisioned' ? '' : record.id,sourceOf(record),record.org].filter(Boolean).join(' · ');
          return '<strong>' + escape(record.name) + '</strong><small class="admin-cell-sub" title="' + escape(summary) + '">' +
            escape(summary) + '</small>';
        },record => record.name),
        column('org','组织',record => escape(record.org || '—'),record => record.org || '',true),
        column('roles','角色',record => escape(userRoles(record).join('、') || '未授权'),null,true),
        column('status','账号状态',record => badge(statusTone(record.status),statuses[record.status] ?? '未知') +
          (record.directoryStatus ? '<small class="admin-cell-sub">目录 · ' + escape(directoryStatuses[record.directoryStatus] ?? '未知') + '</small>' : ''),record => record.status),
        column('actions','操作',record => '<div class="admin-actions">' + action('user',record.id,'查看') +
          (canManageAccounts && !isCurrent(record) && ['active','disabled'].includes(record.status) ?
            action('toggle',record.id,record.status === 'active' ? '停用' : '启用',record.status === 'active') : '') + '</div>')
      ]
    };
    if (section === 'roles') return {
      ...shared, itemLabel:'角色', rowLabel:role => role.name, search:role => [role.name,...role.permissions].join(' '),
      filters:[{ key:'scope', label:'授权范围', options:scopes }],
      columns:[
        column('name','角色名称',role => '<strong>' + escape(role.name) + '</strong>',role => role.name),
        column('scope','范围',role => badge(role.scope === 'system' ? 'progress' : 'important',scopes[role.scope]),role => scopes[role.scope]),
        column('permissions','权限摘要',role => escape(role.permissions[0] ?? '—'),null,true),
        column('grants','授权数',role => role.default ? '默认拥有 · ' + escape(role.count) + ' 人' : escape(role.count ?? '—'),role => role.count ?? -1),
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
    rows.length = 0;
    for (const row of next) rows.push(row);
    if (mounted) refresh();
    else mounted = mountList(list, configuration());
  }
  function replaceCatalog(roles) {
    catalog.clear();
    for (const role of roles) catalog.set(role.id,role);
  }
  function paintTree(departments) {
    if (!departments.some(item => String(item.id) === selectedId)) selectedId = '';
    const focused = treeHost.contains(document.activeElement);
    selectedDepartments = organizationTree(treeHost,departments,selectedId,(id,descendants) => {
      if (id === selectedId) return;
      selectedId = id;
      selectedDepartments = descendants;
      mounted?.resetPage();
    });
    if (focused) treeHost.querySelector('[aria-pressed="true"]')?.focus();
  }
  function clearDirectory() {
    displayedVersion = undefined;
    selectedDepartments = undefined;
    const focusTree = treeHost.contains(document.activeElement);
    const focusRow = list.querySelector('tbody')?.contains(document.activeElement);
    for (const dialog of dialogs) {
      if (dialog !== detailsTarget?.closest('dialog') && dialog.open) dialog.close('cancel');
    }
    treeHost.replaceChildren();
    if (mounted) replaceRows([]);
    else list.replaceChildren();
    if (focusTree || focusRow) heading.focus();
  }
  function observeDirectory(next) {
    const changed = directoryMeta?.snapshotVersion !== next.snapshotVersion;
    if (changed) {
      syncRevision++;
      summaryRequest = undefined;
      if (displayedVersion !== undefined) clearDirectory();
    }
    directoryMeta = next;
    return changed;
  }
  function denyDirectory() {
    readForbidden = true;
    ++listVersion;
    directoryRequest?.abort();
    clearTimeout(pollTimer);
    directoryMeta = undefined;
    sync = null;
    clearDirectory();
    detailsTarget?.closest('dialog')?.close('cancel');
    updateSyncControls();
  }
  async function refreshUsers() {
    directoryRequest?.abort();
    const controller = new AbortController();
    directoryRequest = controller;
    const requestSignal = signal ? AbortSignal.any([signal,controller.signal]) : controller.signal;
    const version = ++listVersion;
    const current = () => inPage() && !readForbidden && version === listVersion;
    for (let attempt = 0; current(); attempt++) {
      try {
        const view = await request('/directory/view',{
          signal:requestSignal,
          onSnapshot(next) {
            if (current()) {
              observeDirectory(next);
              updateSyncControls();
            }
          }
        });
        if (!current()) return;
        if (view.snapshot.visible && displayedVersion !== view.snapshot.snapshotVersion) paintTree(view.departments);
        displayedVersion = view.snapshot.snapshotVersion;
        replaceRows(view.users);
        directoryError = false;
        reloadingAfterConflict = false;
        updateSyncControls();
        return true;
      } catch (error) {
        if (!current()) return;
        if (error.status === 403 || error.status === 401) {
          denyDirectory();
          return;
        }
        if (isDirectoryConflict(error)) {
          clearDirectory();
          directoryMeta = undefined;
          summaryRequest = undefined;
          ++syncRevision;
          if (attempt < 2) {
            reloadingAfterConflict = true;
            updateSyncControls();
            continue;
          }
        }
        reloadingAfterConflict = false;
        directoryError = true;
        if (!mounted) list.replaceChildren();
        updateSyncControls();
        throw error;
      }
    }
  }
  function reloadDirectory() {
    void refreshUsers().catch(() => {});
  }

  function busyDialog(dialog, pending) {
    dialog.toggleAttribute('aria-busy', pending);
    const controls = [...dialog.querySelectorAll(':is(button,select):not([disabled])')];
    controls.forEach(control => { control.disabled = pending; });
    const feedback = document.createElement('span');
    feedback.className = 'or-loading-feedback';
    feedback.innerHTML = window.OmniRelayLoading.markup({size:'inline'});
    feedback.setAttribute('role','status');
    feedback.setAttribute('aria-label','正在提交更改…');
    dialog.querySelector('.dialog-footer').prepend(feedback);
    return () => {
      feedback.remove();
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
        await refreshUsers();
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
    const canProvision = canManageAccounts && unprovisioned && record.directoryStatus === 'active';
    const provision = canProvision ? '<div class="user-grant-tools">' +
      '<button class="button primary" type="button" data-admin-action="provision">开通账号</button></div>' : '';
    const content = () => displayedRoles(record).length ? '<ul class="admin-grants">' + displayedRoles(record).map(grant =>
      '<li><div class="user-grant-label"><strong>' + escape(roleName(grant.role)) + '</strong>' +
      '<small class="user-grant-scope" data-scope="' + (grant.scope === 'project' ? 'project' : 'system') + '">' + escape(grantScopeLabel(grant,record)) +
      '</small></div>' + (canManageRoles && grant.id && catalog.get(grant.role)?.grantable === true && (!isCurrent(record) || grant.role !== 'system_admin') ? action('revoke',grant.id,'撤销',true) : '') + '</li>').join('') +
      '</ul>' : '<p class="aux">' + (record.status === 'unprovisioned' ?
        '飞书目录人员尚未开通账号。开通后可在此授予角色。' : '当前无角色授权。') + '</p>';
    const roleOptions = options([...catalog.values()].filter(role => role.grantable === true && !role.default)
      .map(role => [role.id,role.name + ' · ' + scopes[role.scope]]),'选择角色');
    const editor = canManageRoles && record.status === 'active' ? '<div class="user-grant-tools">' +
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
        ['身份来源',sourceOf(record)],['账号状态',statuses[record.status] ?? '未知',statusTone(record.status)],
        ...(record.directoryStatus ? [['目录状态',directoryStatuses[record.directoryStatus] ?? '未知']] : [])
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
      if (!role || role.grantable !== true || role.default) return invalid(roleField,'请选择可授权的角色。');
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
      const indicator = document.createElement('div');
      dialog.querySelector('.dialog-footer').prepend(indicator);
      const loading = window.OmniRelayLoading.mount(indicator,{size:'inline'});
      indicator.setAttribute('aria-label','正在刷新角色授权…');
      try {
        await refreshUsers();
        if (!dialog.open) return;
        const updated = findUser(String(record.id));
        record.roles = updated?.roles ?? [];
        record.systemRoles = updated?.systemRoles ?? [];
        grantBody.innerHTML = content();
      } catch (error) {
        if (dialog.open && inPage()) {
          grantBody.querySelectorAll('button').forEach(button => { button.disabled = false; });
          toast('warning',messageOf(error,'变更已提交，刷新角色授权失败。'));
        }
      } finally {
        loading.finish();
        indicator.remove();
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
          if (!inPage() || !dialog.open) return;
          try {
            await refreshUsers();
            refreshed = created?.id ? findUser(String(created.id)) : undefined;
          } catch {
            if (inPage()) toast('warning','账号已开通，用户列表刷新失败。请重新打开用户管理。');
            return;
          }
          if (inPage() && dialog.open) toast('success',record.name + '的账号已开通。');
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
      body:group('授权范围',details([['范围',scopes[role.scope]],['授权方式',role.default ? '默认拥有' : role.grantable ? '手动授权' : '由项目成员关系确定']])) +
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

  function scopeLabel(scope) {
    if (!scope) return '—';
    if (scope.entire_org) return '整组织';
    return scope.department_roots + ' 个授权部门 · ' + scope.explicit_people + ' 位单独授权人员';
  }
  const count = (value, unit) => Number.isInteger(value) ? value + ' ' + unit : '—';
  function runCounts(run) {
    return [
      ['部门',count(run?.departments,'个')],['人员',count(run?.users,'人')],
      ['新增人员',count(run?.addedUsers,'人')],['变更人员',count(run?.changedUsers,'人')],
      ['目录缺失',count(run?.missingUsers,'人')],['授权范围外',count(run?.outsideScopeUsers,'人')]
    ];
  }
  function updateSyncControls() {
    if (!inPage() || !usersPage) return;
    const run = sync?.run;
    const active = activeRun(run);
    const waiting = run?.status === 'queued';
    const visible = directoryMeta?.visible;
    const lastTime = directoryMeta?.lastSuccessAt;
    const visibility = visible
      ? run?.status === 'failed' ? '本轮同步失败，仍可浏览上次同步的目录。'
        : active ? '同步期间可浏览上次同步的目录。' : ''
      : lastTime ? '飞书通讯录授权已变化，旧目录已隐藏。'
        : directoryMeta ? '尚无可见的飞书目录，首次同步成功后可浏览。' : '';
    const error = readForbidden ? '当前账号没有查看飞书目录的权限。' :
      directoryError ? '无法刷新组织与人员，请重试。' :
      reloadingAfterConflict ? '组织目录已更新，正在重新加载…' :
      syncError ? '暂时无法获取同步状态，请重试。' : '';
    const status = active ? waiting ? '同步排队中…' : '同步进行中…' :
      !visible && run?.status === 'failed' ? '本轮同步失败，请查看同步详情。' : '';
    const noticeText = [status,error,readForbidden ? '' : visibility].filter(Boolean).join(' ');
    notice.hidden = !noticeText;
    if (notice.firstElementChild.textContent !== noticeText) notice.firstElementChild.textContent = noticeText;
    const hideRetry = readForbidden || !(directoryError || syncError);
    if (hideRetry && document.activeElement === retry) heading.focus();
    retry.hidden = hideRetry;
    syncButton.hidden = !maySync || syncForbidden || readForbidden;
    syncButton.disabled = requestingSync || active || !directoryMeta;
    window.OmniRelayLoading.button(syncButton,requestingSync || active,requestingSync ? '提交中…' : waiting ? '等待同步…' : active ? '同步中…' : '同步');
    syncDetails.disabled = readForbidden;
    if (!detailsTarget) return;
    const labels = {
      queued:['pending','等待同步'], running:['progress','进行中'],
      succeeded:['success','已完成'], failed:['danger','同步失败']
    };
    const [tone,label] = labels[run?.status] ?? ['pending',syncError ? '无法获取同步状态' : loadingSync ? '正在查询…' : '暂无同步记录'];
    const phase = {
      running:run?.scope ? '读取飞书组织与人员' : '核对飞书通讯录授权',
      succeeded:'组织与人员已更新',
      failed:'飞书目录未更新'
    }[run?.status] ?? label;
    const statusTime = run?.finishedAt ? '完成于 ' + showTime(run.finishedAt) :
      run?.startedAt ? '开始于 ' + showTime(run.startedAt) :
      run?.createdAt ? '排队于 ' + showTime(run.createdAt) : '';
    const failure = run?.status === 'failed' ? syncFailure(run.failureReason,!visible && Boolean(lastTime)) : '';
    const last = visible && sync?.snapshotVersion === directoryMeta.snapshotVersion ? sync.lastSuccess : null;
    const markup = '<section class="directory-sync-status"><div><strong>' + escape(phase) + '</strong>' +
      badge(tone,label) + '</div>' + (active || run?.status === 'succeeded'
        ? window.OmniRelayLoading.markup({size:'compact',label:active ? phase + '…' : phase,progress:active ? null : 100,active}) : '') +
      (statusTime ? '<p class="aux">' + escape(statusTime) + '</p>' : '') +
      (failure ? '<p class="aux">失败原因：' + escape(failure) + '</p>' : '') +
      (visibility ? '<p class="aux">' + escape(visibility) + '</p>' : '') + '</section>' +
      group('同步安排',details([
        ['最近成功',showTime(lastTime)],['下次同步',showTime(directoryMeta?.nextRunAt)],
        ['可见目录授权范围',scopeLabel(directoryMeta?.scope)]
      ])) +
      (run ? group('本轮同步',details([['本轮授权范围',scopeLabel(run.scope)], ...runCounts(run)])) : '') +
      (last && last.id !== run?.id ? group('上次可见快照',details(runCounts(last))) : '');
    if (markup !== lastDetailsMarkup) {
      detailsTarget.innerHTML = markup;
      lastDetailsMarkup = markup;
    }
  }
  function schedulePoll(delay = activeRun(sync?.run) ? 1200 : 5000) {
    clearTimeout(pollTimer);
    if (inPage() && !readForbidden) pollTimer = setTimeout(readSync,delay);
  }
  async function readSync() {
    if (loadingSync || requestingSync || readForbidden || !inPage()) return;
    clearTimeout(pollTimer);
    const requestID = ++syncRequest;
    const revision = syncRevision;
    const current = () => inPage() && !readForbidden && requestID === syncRequest;
    loadingSync = true;
    try {
      const next = await request('/directory/sync');
      if (!current() || revision !== syncRevision) return;
      const previous = sync?.run;
      const changed = observeDirectory(next);
      if (!changed && next.visible && !next.lastSuccess && sync?.snapshotVersion === next.snapshotVersion) {
        next.lastSuccess = sync.lastSuccess;
      }
      sync = next;
      syncError = false;
      if (changed) reloadDirectory();
      if (activeRun(previous) && next.run?.id === previous.id && !activeRun(next.run)) {
        if (next.run.status === 'succeeded') toast('success','组织与人员同步完成。');
        if (next.run.status === 'failed') toast('error','组织与人员同步失败，请查看同步详情。');
      }
      updateSyncControls();
      readPreviousSummary();
    } catch (error) {
      if (!current()) return;
      if (error.status === 403 || error.status === 401) denyDirectory();
      else syncError = true;
    } finally {
      loadingSync = false;
      if (current()) {
        updateSyncControls();
        schedulePoll(syncError ? 3500 : undefined);
      }
    }
  }
  function readPreviousSummary() {
    if (!detailsTarget || !sync?.visible || sync.lastSuccess || sync.snapshotVersion !== directoryMeta?.snapshotVersion) return;
    const version = sync.snapshotVersion;
    if (summaryRequest === version) return;
    summaryRequest = version;
    void request('/directory/sync-summary',{ snapshotVersion:version }).then(result => {
      if (!inPage() || summaryRequest !== version || directoryMeta?.snapshotVersion !== version || sync?.snapshotVersion !== version) return;
      sync.lastSuccess = result;
      updateSyncControls();
    }).catch(error => {
      if (!inPage() || summaryRequest !== version) return;
      summaryRequest = undefined;
      if (error.status === 403 || error.status === 401) denyDirectory();
    });
  }
  async function startSync() {
    if (readForbidden || syncForbidden || !maySync || requestingSync) return;
    ++syncRequest;
    clearTimeout(pollTimer);
    requestingSync = true;
    updateSyncControls();
    syncDetails.focus();
    try {
      const run = await request('/directory/sync',{ method:'POST' });
      if (!inPage() || readForbidden) return;
      sync = { ...sync, run };
      toast(run.coalesced ? 'info' : 'success',run.coalesced ? '已有同步任务，已并入该任务。' : '已提交同步请求。');
    } catch (error) {
      if (!inPage() || readForbidden || error.status === 401) return;
      if (error.status === 403) syncForbidden = true;
      toast('error',error.status === 403 ? '当前账号无法启动同步，请重新登录后再试。' : '无法启动同步，请重试。');
    } finally {
      requestingSync = false;
      updateSyncControls();
      schedulePoll(0);
    }
  }
  function showSyncDetails() {
    const dialog = showDialog({
      title:'同步详情', description:'飞书组织与用户', drawer:'standard', tone:'info',
      body:'<div class="directory-sync-detail" data-sync-detail role="status"></div>',
      restoreFocus:() => inPage() && !syncDetails.disabled ? syncDetails : focusHeading(),
      onClose:() => { detailsTarget = undefined; }
    });
    dialog.classList.add('user-detail-drawer');
    detailsTarget = dialog.querySelector('[data-sync-detail]');
    lastDetailsMarkup = '';
    updateSyncControls();
    readPreviousSummary();
    void readSync();
  }

  async function load() {
    const loading = window.OmniRelayLoading.mount(list,{size:'compact',label:'正在读取' + titles[section] + '…'});
    try {
      if (usersPage) {
        replaceCatalog(await request('/role-catalog'));
        if (!inPage()) return;
        await refreshUsers();
      } else {
        const next = await request('/' + section);
        if (inPage()) replaceRows(next);
      }
    } catch (error) {
      if (!inPage() || (usersPage && directoryError)) return;
      list.innerHTML = '<div class="empty"><p role="alert"></p><button class="button" type="button">重试</button></div>';
      list.querySelector('p').textContent = messageOf(error,'加载失败，请重试。');
      list.querySelector('button').addEventListener('click',load);
    } finally {
      loading.finish();
      list.classList.remove('or-loading-placeholder');
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
      case 'toggle': if (canManageAccounts) toggleUser(record,trigger); break;
      case 'role': showRole(record,trigger); break;
      case 'event': showEvent(record,trigger); break;
    }
  });
  if (usersPage) {
    syncButton.addEventListener('click',startSync);
    syncDetails.addEventListener('click',showSyncDetails);
    retry.addEventListener('click',() => {
      if (directoryError) reloadDirectory();
      void readSync();
    });
  }
  await load();
  if (usersPage && inPage()) {
    updateSyncControls();
    void readSync();
  }
  return { confirmNavigation, hasDraft:() => Boolean(userDrawer?.dirty()) };
}
