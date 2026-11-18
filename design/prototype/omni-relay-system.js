/* In-memory data for the OmniRelay identity and system management prototype. */
window.OmniRelaySystem = (() => {
  const { escape, mountList, openDialog, toast } = OmniRelayUI;
  const pages = { users:'用户管理', roles:'角色权限', audit:'审计日志' };
  const users = [
    { id:'U-001', name:'苏涵', org:'平台运营', status:'active', projects:['north'] },
    { id:'U-002', name:'王宁', org:'交付中心 / 交付一组', status:'active', projects:['north','pilot'] },
    { id:'U-003', name:'陈悦', org:'研发中心 / 平台组', status:'active', projects:['north'] },
    { id:'U-004', name:'林晓', org:'研发中心 / 产品组', status:'active', projects:['pilot'] },
    { id:'U-005', name:'赵可', org:'交付中心 / 测试组', status:'disabled', projects:['north'] }
  ];
  const current = users[0];
  const projects = { north:'北辰交付项目', pilot:'启明试点项目' };
  const roles = [
    { id:'admin', name:'系统管理员', scope:'org', permissions:['管理平台账号与角色授权','管理连接状态和系统配置'], boundary:'查看项目内容须具备相应项目授权。' },
    { id:'operator', name:'系统运维', scope:'org', permissions:['查看连接、队列与回执摘要','暂停或恢复入口与调度','取消运行并核对异常回执'], boundary:'运行状态和回执以脱敏摘要展示。' },
    { id:'auditor', name:'审计查看者', scope:'org', permissions:['查询本组织审计日志','查看登录、授权、配置与运维变更'], boundary:'正文可见范围由项目与会话权限决定。' },
    { id:'project-admin', name:'项目管理员', scope:'project', permissions:['查看本项目数据和运行摘要','管理项目群绑定与简报计划','预览、启停项目简报'], boundary:'适用范围为指定项目，授权对象须是该项目成员。' },
    { id:'member', name:'项目成员', scope:'project', permissions:['查询项目共享任务','查看自己的运行记录','在飞书向同项目成员明确分派任务'], boundary:'成员资格由正式业务系统确认。' }
  ];
  const grants = [
    { id:'G-01', user:'U-001', role:'admin', scope:'' },
    { id:'G-02', user:'U-001', role:'auditor', scope:'' },
    { id:'G-03', user:'U-001', role:'project-admin', scope:'north' },
    { id:'G-04', user:'U-002', role:'project-admin', scope:'north' },
    { id:'G-05', user:'U-002', role:'member', scope:'north' },
    { id:'G-06', user:'U-003', role:'operator', scope:'' },
    { id:'G-07', user:'U-004', role:'member', scope:'pilot' },
    { id:'G-08', user:'U-005', role:'member', scope:'north' }
  ];
  const eventKinds = { login:'身份认证', account:'用户管理', role:'角色授权', config:'系统配置' };
  const results = { success:'成功', denied:'已拒绝', cancelled:'已取消' };
  const statuses = { active:'已启用', disabled:'已停用' };
  const scopes = { org:'组织级', project:'项目级' };
  const dateFormat = new Intl.DateTimeFormat('sv-SE', {
    timeZone:'Asia/Singapore', year:'numeric', month:'2-digit', day:'2-digit',
    hour:'2-digit', minute:'2-digit', second:'2-digit'
  });
  const sampleEvents = [
    { id:'AUD-00104', actor:'苏涵', kind:'role', action:'授予项目角色', target:'王宁', result:'success', before:'未授权', after:'项目管理员 · 北辰交付项目' },
    { id:'AUD-00103', actor:'苏涵', kind:'account', action:'停用账号', target:'赵可', result:'success', before:'已启用', after:'已停用' },
    { id:'AUD-00102', actor:'陈悦', kind:'config', action:'更新简报计划', target:'北辰交付项目', result:'denied', before:'每日 18:00', after:'权限不足，保留原计划' },
    { id:'AUD-00101', actor:'苏涵', kind:'login', action:'飞书登录', target:'OmniRelay 工作台', result:'success', before:'未登录', after:'已登录' }
  ].map((entry,index) => ({ ...entry, time:dateFormat.format(Date.now() - (index + 1) * 15 * 60 * 1000) }));
  let grantNumber = grants.length;
  let auditNumber = 104;
  let organization = '';
  let showDirectoryUpdate;
  const syncUpdates = new EventTarget();
  const syncSteps = [
    [12,'连接飞书目录'],[35,'拉取组织架构'],[62,'拉取用户数据'],[88,'更新本地目录']
  ];
  let syncRun;
  let lastSync = {
    started:dateFormat.format(Date.now() - 32 * 60 * 1000),
    finished:dateFormat.format(Date.now() - 30 * 60 * 1000),
    organizations:organizationCount(), users:users.length, addedOrganizations:0, addedUsers:0
  };

  const findUser = id => users.find(user => user.id === id);
  const findRole = id => roles.find(role => role.id === id);
  const scopeLabel = grant => projects[grant.scope] ?? '本组织';
  const userGrants = user => grants.filter(grant => grant.user === user.id);
  const grantLabel = grant => findRole(grant.role).name + ' · ' + scopeLabel(grant);
  const roleNames = user => [...new Set(userGrants(user).map(grant => findRole(grant.role).name))];
  const badge = (tone, label) => '<span class="chip" data-tone="' + tone + '">' + escape(label) + '</span>';
  const action = (key, id, label, danger = false) =>
    '<button class="row-action' + (danger ? ' admin-danger' : '') + '" type="button" data-admin-action="' + key +
    '" data-record="' + id + '">' + label + '</button>';
  const column = (key, label, render, sort, secondary = false) => ({ key, label, render, sort, secondary });
  const group = (title, body) => '<section class="drawer-section"><h3>' + escape(title) + '</h3>' + body + '</section>';
  const details = entries => '<dl class="admin-details">' + entries.map(([label, value, tone]) =>
    '<div><dt>' + escape(label) + '</dt><dd>' + (tone ? badge(tone,value) : escape(value)) + '</dd></div>').join('') + '</dl>';

  function record(kind, action, target, before, after, result = 'success') {
    sampleEvents.unshift({
      id:'AUD-' + String(++auditNumber).padStart(5,'0'), time:dateFormat.format(new Date()),
      actor:current.name, kind, action, target, result, before, after
    });
  }

  function render(page) {
    const controls = page === 'users' ?
      '<div class="system-heading-actions"><button class="button primary" type="button" data-directory-sync title="同步飞书组织与用户">同步</button>' +
      '<button class="button" type="button" data-directory-details>同步详情</button></div>' : '';
    const list = page === 'users' ? '<div class="directory-layout"><details class="directory-org" open>' +
      '<summary>组织结构</summary><div class="directory-tree" data-directory-tree></div></details>' +
      '<section id="system-list"></section></div>' : '<section id="system-list"></section>';
    return '<header class="head admin-heading"><h1 id="page-heading" tabindex="-1">' + pages[page] + '</h1>' +
      controls + '</header>' + list;
  }

  function configuration(page) {
    const shared = { key:'system-' + page, label:pages[page], selection:false };
    if (page === 'users') {
      return {
        ...shared, itemLabel:'用户或账号', rows:users, rowLabel:user => user.name,
        includes:user => !organization || user.org === organization || user.org.startsWith(organization + ' / '),
        search:user => [user.name,user.id,user.org,...roleNames(user)].join(' '),
        filters:[{ key:'status', label:'账号状态', options:statuses }],
        columns:[
          column('name','用户',user => '<strong>' + escape(user.name) + '</strong><small class="admin-cell-sub">' + user.id + ' · 飞书</small>',user => user.name),
          column('org','组织',user => escape(user.org),user => user.org,true),
          column('roles','角色',user => escape(roleNames(user).join('、') || '未授权'),null,true),
          column('status','状态',user => badge(user.status === 'active' ? 'success' : 'pending',statuses[user.status]),user => user.status),
          column('actions','操作',user => '<div class="admin-actions">' + action('user',user.id,'查看') +
            (user !== current ? action('toggle',user.id,user.status === 'active' ? '停用' : '启用',user.status === 'active') : '') + '</div>')
        ]
      };
    }
    if (page === 'roles') {
      return {
        ...shared, itemLabel:'角色', rows:roles, rowLabel:role => role.name, search:role => role.name + role.permissions.join(' '),
        filters:[{ key:'scope', label:'授权范围', options:scopes }],
        columns:[
          column('name','角色名称',role => '<strong>' + role.name + '</strong>',role => role.name),
          column('scope','范围',role => badge(role.scope === 'org' ? 'progress' : 'important',scopes[role.scope]),role => scopes[role.scope]),
          column('permissions','权限摘要',role => role.permissions[0],null,true),
          column('grants','授权数',role => grants.filter(grant => grant.role === role.id).length,
            role => grants.filter(grant => grant.role === role.id).length),
          column('actions','操作',role => action('role',role.id,'查看权限'))
        ]
      };
    }
    return {
      ...shared, itemLabel:'操作、操作者或编号', rows:sampleEvents, rowLabel:event => event.action,
      search:event => [event.actor,event.action,event.target,event.id].join(' '),
      filters:[{ key:'kind', label:'事件类型', options:eventKinds },{ key:'result', label:'结果', options:results }],
      defaultSort:{ key:'time', direction:'desc' },
      columns:[
          column('time','时间 (UTC+08:00)',event => '<span class="date">' + event.time + '</span>',event => event.time),
        column('action','事件',event => '<strong>' + event.action + '</strong><small class="admin-cell-sub">' +
          eventKinds[event.kind] + '<span class="audit-compact-actor"> · ' + escape(event.actor) + '</span></small>',event => event.action),
        column('actor','操作者',event => escape(event.actor),event => event.actor,true),
        column('target','对象',event => escape(event.target),event => event.target,true),
        column('result','结果',event => badge({ success:'success', denied:'danger', cancelled:'pending' }[event.result],results[event.result]),event => event.result),
        column('actions','详情',event => action('event',event.id,'查看详情'))
      ]
    };
  }

  function confirmChange(title, description, message, confirm, mutate, trigger, tone = 'warning') {
    const parentDialog = trigger.closest('dialog');
    openDialog({
      title, description, body:'<p class="dialog-note">' + escape(message) + '</p>', confirm, tone, variant:'confirm',
      onSubmit:mutate, restoreFocus:() => trigger.isConnected ? trigger :
        parentDialog?.querySelector('.dialog-close') ??
        document.querySelector('#system-list [data-record="' + trigger.dataset.record + '"]') ?? document.getElementById('page-heading')
    });
  }

  function toggleUser(user, trigger, refresh) {
    const active = user.status === 'active';
    const verb = active ? '停用账号' : '启用账号';
    confirmChange(verb,user.name,active ? '停用后，该用户将无法使用工作台和飞书 Bot。' : '启用后，该用户可按已有授权继续访问。',
      active ? '确认停用' : '确认启用', () => {
        const before = statuses[user.status];
        user.status = active ? 'disabled' : 'active';
        record('account',verb,user.name,before,statuses[user.status]);
        refresh();
        toast('success',user.name + '的账号' + statuses[user.status] + '。');
      }, trigger, active ? 'danger' : 'default');
  }

  function showUser(user, refresh) {
    const content = () => {
      const list = userGrants(user);
      return list.length ? '<ul class="admin-grants">' + list.map(grant => {
        const role = findRole(grant.role);
        return '<li><div class="user-grant-label"><strong>' + role.name + '</strong>' +
          '<small class="user-grant-scope" data-scope="' + role.scope + '">' + scopeLabel(grant) + '</small></div>' +
          (user !== current || grant.role !== 'admin' ? action('revoke',grant.id,'撤销',true) : '') + '</li>';
      }).join('') + '</ul>' :
        '<p class="aux">当前无角色授权。</p>';
    };
    const editor = user.status === 'active' ? '<div class="user-grant-tools">' +
      '<button class="button" type="button" data-admin-action="add-role" aria-expanded="false" aria-controls="user-grant-editor">' +
      '<span aria-hidden="true">＋</span> 添加角色</button></div>' +
      '<div class="user-grant-editor" id="user-grant-editor" hidden>' + grantFields() +
      '<div class="user-grant-buttons"><button class="button neutral" type="button" data-admin-action="cancel-role">取消</button>' +
      '<button class="button primary" type="submit" data-save-role>保存</button></div></div>' : '';
    const dialog = openDialog({
      title:'用户详情', description:user.name, drawer:'standard', tone:'info',
      restoreFocus:() => document.querySelector('#system-list [data-record="' + user.id + '"][data-admin-action="user"]') ??
        document.getElementById('page-heading'),
      body:group('账号信息',details([
        ['账号编号',user.id],['所属组织',user.org],['身份来源','飞书'],
        ['状态',statuses[user.status],user.status === 'active' ? 'success' : 'pending']
      ])) + group('角色授权','<div data-user-grants>' + content() + '</div>' +
        editor + '<span class="sr-only" data-grant-status role="status"></span>'),
      isDirty:form => Boolean(form.elements.role?.value),
      onSubmit(form,submitter) {
        if (!row || row.hidden) return { keepOpen:true };
        const failure = saveGrant(form,user);
        if (failure) return failure;
        update();
        setEditor(false);
        dialog.querySelector('[data-grant-status]').textContent = '角色已保存。';
        if (submitter?.matches('[data-save-role]')) return { keepOpen:true };
      }
    });
    dialog.classList.add('user-detail-drawer');
    const grantBody = dialog.querySelector('[data-user-grants]');
    const update = () => { grantBody.innerHTML = content(); refresh(); };
    const row = dialog.querySelector('.user-grant-editor');
    const add = dialog.querySelector('[data-admin-action="add-role"]');
    const form = dialog.querySelector('form');
    const syncScope = row ? mountGrantFields(form,user) : null;
    function setEditor(open) {
      row.hidden = !open;
      add.hidden = open;
      add.setAttribute('aria-expanded',String(open));
      form.elements.role.disabled = !open;
      if (!open) form.reset();
      syncScope();
      (open ? form.elements.role : add).focus();
    }
    if (row) form.elements.role.disabled = true;
    dialog.addEventListener('click', event => {
      const button = event.target.closest('[data-admin-action]');
      if (button?.dataset.adminAction === 'add-role') setEditor(true);
      if (button?.dataset.adminAction === 'cancel-role') setEditor(false);
      if (button?.dataset.adminAction !== 'revoke') return;
      const grant = grants.find(item => item.id === button.dataset.record);
      confirmChange('撤销授权',user.name, '撤销后，该账号将失去「' + grantLabel(grant) + '」对应的访问权限。',
        '确认撤销', () => {
          grants.splice(grants.indexOf(grant),1);
          record('role','撤销角色授权',user.name,grantLabel(grant),'已撤销');
          update();
          toast('success','角色授权已撤销。');
        }, button, 'danger');
    });
  }

  function showRole(role, trigger) {
    openDialog({
      title:'角色权限', description:role.name, drawer:'standard', tone:'info', restoreFocus:() => trigger,
      body:group('授权范围',details([['范围',scopes[role.scope]],['适用条件',role.boundary]])) +
        group('可执行操作','<ul class="admin-permissions">' + role.permissions.map(item => '<li>' + item + '</li>').join('') + '</ul>')
    });
  }

  const grantOptions = (items, placeholder) => '<option value="">' + placeholder + '</option>' +
    items.map(([id,label]) => '<option value="' + id + '">' + escape(label) + '</option>').join('');
  const grantField = (key, label, items, placeholder, hidden = false) =>
    '<label class="field admin-field"' + (hidden ? ' hidden' : '') + '><span class="field-label">' + label +
    '</span><select class="select" name="' + key + '"' + (hidden ? ' disabled' : ' required') + '>' +
    grantOptions(items,placeholder) + '</select></label>';
  const grantFields = () => grantField('role','角色',roles.map(role => [role.id,role.name + ' · ' + scopes[role.scope]]),'选择角色') +
    grantField('scope','适用项目',[],'选择项目',true);

  function saveGrant(form,user) {
    const role = findRole(form.elements.role.value);
    const scope = role.scope === 'project' ? form.elements.scope.value : '';
    if (userGrants(user).some(grant => grant.role === role.id && grant.scope === scope)) {
      return { field:form.elements[scope ? 'scope' : 'role'], message:'该用户已有相同范围的角色授权。' };
    }
    const grant = { id:'G-' + ++grantNumber, user:user.id, role:role.id, scope };
    grants.push(grant);
    record('role','授予角色',user.name,'未授权',grantLabel(grant));
    toast('success','已为' + user.name + '授予' + role.name + '。');
  }

  function mountGrantFields(form,user) {
    const project = form.elements.scope;
    function syncScope() {
      const role = findRole(form.elements.role.value);
      const projectRole = role?.scope === 'project';
      const previous = project.value;
      const available = user.projects;
      project.closest('label').hidden = !projectRole;
      project.required = Boolean(projectRole);
      project.disabled = !projectRole;
      project.innerHTML = grantOptions(available.map(id => [id,projects[id]]),'选择项目');
      if (available.includes(previous)) project.value = previous;
    }
    form.elements.role.addEventListener('change',syncScope);
    syncScope();
    return syncScope;
  }

  function showEvent(entry, trigger) {
    openDialog({
      title:'审计详情', description:entry.id, drawer:'wide', tone:'info', restoreFocus:() => trigger,
      body:group('事件信息',details([
        ['操作时间',entry.time + ' (UTC+08:00)'],['操作者',entry.actor],['事件类型',eventKinds[entry.kind]],
        ['操作',entry.action],['对象',entry.target],['结果',results[entry.result]]
      ])) + group('变更摘要','<div class="admin-diff">' +
        details([['变更前',entry.before],['变更后',entry.after]]) + '</div>')
    });
  }

  function directoryGroups() {
    const byName = new Map();
    for (const { org } of users) {
      const [department,team] = org.split(' / ');
      if (!byName.has(department)) {
        byName.set(department,{ id:'dept-' + department, label:department, path:department, teams:[] });
      }
      const group = byName.get(department);
      if (team && !group.teams.some(item => item.path === org)) {
        group.teams.push({ id:group.id + '-' + team, label:team, path:org });
      }
    }
    return [...byName.values()];
  }

  function organizationCount() {
    return directoryGroups().reduce((count,group) => count + 1 + group.teams.length,0);
  }

  function publishSync(directoryChanged = false) {
    showDirectoryUpdate?.(directoryChanged);
    syncUpdates.dispatchEvent(new Event('change'));
  }

  function startDirectorySync() {
    if (syncRun) return;
    syncRun = { started:dateFormat.format(new Date()), step:0 };
    publishSync();
    let step = 0;
    const timer = setInterval(() => {
      step += 1;
      if (step < syncSteps.length) {
        syncRun.step = step;
        publishSync();
        return;
      }
      clearInterval(timer);
      const added = !findUser('U-006');
      if (added) users.push({ id:'U-006', name:'许诺', org:'平台运营 / 运营支持组', status:'active', projects:[] });
      lastSync = {
        started:syncRun.started,
        finished:dateFormat.format(new Date()),
        organizations:organizationCount(),
        users:users.length, addedOrganizations:Number(added), addedUsers:Number(added)
      };
      syncRun = undefined;
      record('config','同步飞书目录','组织与用户','开始同步',
        '组织 ' + lastSync.organizations + ' 个，用户 ' + lastSync.users + ' 人');
      publishSync(added);
      toast('success','组织与用户同步完成。');
    },850);
  }

  function showSyncDetails(trigger) {
    const dialog = openDialog({
      title:'同步详情', description:'飞书组织与用户', drawer:'standard', tone:'info',
      body:'<div class="directory-sync-detail" data-sync-detail aria-live="polite"></div>',
      restoreFocus:() => trigger,
      onClose:() => syncUpdates.removeEventListener('change',update)
    });
    dialog.classList.add('user-detail-drawer');
    const target = dialog.querySelector('[data-sync-detail]');
    function update() {
      const [percent,phase] = syncRun ? syncSteps[syncRun.step] : [100,'同步完成'];
      target.innerHTML = '<section class="directory-sync-status"><div><strong>' + phase + '</strong>' +
        badge(syncRun ? 'progress' : 'success',syncRun ? '进行中' : '已完成') + '</div>' +
        '<progress value="' + percent + '" max="100" aria-label="同步进度"></progress>' +
        '<span class="aux">' + percent + '% · ' + (syncRun ? '开始于 ' + syncRun.started : '完成于 ' + lastSync.finished) +
        '</span></section>' +
        group('上次同步结果',details([
          ['开始时间',lastSync.started],['完成时间',lastSync.finished],['组织',lastSync.organizations + ' 个'],
          ['用户',lastSync.users + ' 人'],['新增组织',lastSync.addedOrganizations + ' 个'],['新增用户',lastSync.addedUsers + ' 人']
        ]));
    }
    syncUpdates.addEventListener('change',update);
    update();
  }

  function mountDirectory(root, { refresh, resetPage }) {
    const panel = root.querySelector('.directory-org');
    const tree = panel.querySelector('[data-directory-tree]');
    const syncButton = root.querySelector('[data-directory-sync]');
    panel.open = !matchMedia('(max-width: 720px)').matches;
    function paintTree() {
      const groups = directoryGroups();
      const entries = new Map(groups.flatMap(group => [group,...group.teams]).map(item => [item.id,item]));
      entries.set('all',{ id:'all', path:'全部组织' });
      const initial = [...entries.values()].find(item => item.path === organization)?.id ?? 'all';
      tree.innerHTML = OmniRelayDeveloper.tree('directory-org',true,groups,true);
      OmniRelayDeveloper.mountTree(tree.firstElementChild, {
        entries, initialId:initial,
        onChange(selection) {
          const next = selection.id === 'all' ? '' : selection.path;
          if (next === organization) return;
          organization = next;
          resetPage();
        }
      });
    }
    showDirectoryUpdate = changed => {
      if (!panel.isConnected) return;
      syncButton.disabled = Boolean(syncRun);
      syncButton.textContent = syncRun ? '同步中…' : '同步';
      if (changed) { paintTree(); refresh(); }
    };
    paintTree();
    showDirectoryUpdate(false);
    const detailsButton = root.querySelector('[data-directory-details]');
    syncButton.addEventListener('click',() => {
      startDirectorySync();
      detailsButton.focus();
    });
    detailsButton.addEventListener('click',() => showSyncDetails(detailsButton));
  }

  function mount(root, page) {
    const list = root.querySelector('#system-list');
    const mounted = mountList(list,configuration(page));
    const { refresh } = mounted;
    if (page === 'users') mountDirectory(root,mounted);
    list.addEventListener('click',event => {
      const trigger = event.target.closest('[data-admin-action]');
      if (!trigger) return;
      const { adminAction, record:id } = trigger.dataset;
      if (adminAction === 'user') showUser(findUser(id),refresh);
      if (adminAction === 'toggle') toggleUser(findUser(id),trigger,refresh);
      if (adminAction === 'role') showRole(findRole(id),trigger);
      if (adminAction === 'event') showEvent(sampleEvents.find(entry => entry.id === id),trigger);
    });
  }

  function mountLogin(page, enter) {
    const trigger = page.querySelector('#feishu-login');
    const form = page.querySelector('#password-login');
    const feedback = page.querySelector('#login-feedback');
    const {username,password} = form.elements;

    function clearFeedback() {
      feedback.hidden = true;
      password.removeAttribute('aria-invalid');
      password.removeAttribute('aria-describedby');
    }

    function completeLogin(method) {
      record('login',method,'OmniRelay 工作台','未登录','已登录');
      form.reset();
      clearFeedback();
      enter();
    }

    form.addEventListener('input',clearFeedback);
    form.addEventListener('submit',event => {
      event.preventDefault();
      if (username.value.trim() === 'demo' && password.value === 'omni-demo') {
        completeLogin('账号密码登录');
        return;
      }
      feedback.dataset.tone = 'error';
      feedback.textContent = '账号或密码不匹配，请使用页面上的演示账号。';
      feedback.hidden = false;
      password.setAttribute('aria-invalid','true');
      password.setAttribute('aria-describedby',feedback.id);
      password.focus();
    });

    trigger.addEventListener('click', () => {
      clearFeedback();
      openDialog({
        title:'飞书登录授权', description:'模拟飞书授权确认', variant:'confirm', tone:'default',
        body:details([['登录身份',current.name + ' · 飞书示例账号'],['应用','OmniRelay'],['授权信息','基本身份资料']]),
        confirm:'同意并登录', cancelLabel:'取消授权',
        onClose(confirmed) {
          if (confirmed) {
            completeLogin('飞书登录');
          } else {
            record('login','飞书登录','OmniRelay 工作台','未登录','取消授权','cancelled');
            feedback.dataset.tone = 'warning';
            feedback.textContent = '已取消授权。你可以重新发起登录。';
            feedback.hidden = false;
          }
        },
        restoreFocus:confirmed => confirmed ? null : trigger
      });
    });
  }

  return { pages, render, mount, mountLogin,
    recordLogout:() => record('login','退出登录','OmniRelay 工作台','已登录','已退出') };
})();
