const system = '/api/v1/system';
const directory = system + '/directory';
const feishuBot = system + '/feishu-bot';

export function feishuBotAPI(api) {
  const checks = feishuBot + '/checks';
  return {
    status: signal => api(feishuBot, { includeETag: true, signal }),
    save: (enabled, ifMatch, signal) => api(feishuBot, {
      method: 'PATCH', body: { enabled }, ifMatch, includeETag: true, signal
    }),
    test: (idempotencyKey, signal) => api(checks, {
      method: 'POST', body: {}, idempotencyKey, signal
    }),
    check: (id, signal) => api(checks + '/' + encodeURIComponent(id), { signal })
  };
}

// Keep the design's labels here. The server separately authorizes every action.
const roles = [
  {
    id: 'admin', wireID: 'system_admin', name: '系统管理员', scope: 'org',
    condition: '查看项目内容须具备相应项目授权。',
    permissions: ['管理平台账号与角色授权', '管理连接状态和系统配置']
  },
  {
    id: 'operator', wireID: 'system_operator', name: '系统运维', scope: 'org',
    condition: '运行状态和回执以脱敏摘要展示。',
    permissions: ['查看连接、队列与回执摘要', '暂停或恢复入口与调度', '取消运行并核对异常回执']
  },
  {
    id: 'auditor', wireID: 'audit_viewer', name: '审计查看者', scope: 'org',
    condition: '正文可见范围由项目与会话权限决定。',
    permissions: ['查询本组织审计日志', '查看登录、授权、配置与运维变更']
  },
  {
    id: 'project-admin', wireID: 'project_admin', name: '项目管理员', scope: 'project',
    condition: '由正式项目管理员授权给当前项目成员。',
    permissions: ['查看本项目数据和运行摘要', '管理项目群绑定与简报计划', '预览、启停项目简报']
  },
  {
    id: 'member', wireID: 'project_member', name: '项目成员', scope: 'project', grantable: false,
    condition: '成员资格由正式业务系统确认。',
    permissions: ['查询项目共享任务', '查看自己的运行记录', '在飞书向同项目成员明确分派任务']
  }
];
const displayRoles = new Map(roles.map(role => [role.wireID, role]));
const wireRoles = new Map(roles.map(role => [role.id, role]));
const accountPath = system + '/accounts';
const grantsPath = system + '/role-grants';
const syncPath = directory + '/sync-runs';
const directoryPrefix = 'directory:';

async function collectPages(api, path, { signal } = {}) {
  const records = [];
  const cursors = new Set();
  let cursor;
  do {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    const page = await api(path + '?' + query, { signal });
    if (!Array.isArray(page?.items)) throw new Error('服务返回了无效的列表。');
    records.push(...page.items);
    cursor = page.nextCursor;
    if (cursor) {
      if (cursors.has(cursor) || cursors.size >= 99) throw new Error('无法读取完整列表。');
      cursors.add(cursor);
    }
  } while (cursor);
  return records;
}

function groupGrants(grants) {
  const byAccount = new Map();
  for (const grant of grants) {
    const role = displayRoles.get(grant.role);
    if (!role) continue;
    if (!byAccount.has(grant.accountId)) byAccount.set(grant.accountId, []);
    byAccount.get(grant.accountId).push({
      id: grant.id, role: role.id, scope: role.scope, project: grant.projectId ?? ''
    });
  }
  return byAccount;
}

function directoryFields(person) {
  const departments = person?.departments ?? [];
  return {
    departmentIds: [...new Set(departments.map(item => item.id))],
    org: departments.map(item => item.path.join(' / ')).join('；'),
    directoryStatus: person?.directory_status
  };
}

function directoryUsers(accounts, grants, people) {
  const directoryByAccount = new Map(people.filter(person => person.account_id).map(person => [person.account_id, person]));
  const accountGrants = groupGrants(grants);

  const registered = accounts.map(account => {
    const person = directoryByAccount.get(account.id);
    return {
      id: account.id,
      name: person?.name || account.name || account.id,
      source: account.source === 'local' ? '本地账号' : '飞书',
      status: account.active ? 'active' : 'disabled',
      roles: accountGrants.get(account.id) ?? [],
      projects: [],
      ...directoryFields(person)
    };
  });
  const unprovisioned = people.filter(person => person.account_status === 'unprovisioned')
    .map(person => ({
      id: directoryPrefix + person.open_id,
      name: person.name,
      source: '飞书',
      status: 'unprovisioned',
      roles: [],
      projects: [],
      ...directoryFields(person)
    }));
  return [...registered, ...unprovisioned];
}

async function listRoles(api, options) {
  const grants = await collectPages(api, grantsPath, options);
  const counts = new Map();
  for (const grant of grants) counts.set(grant.role, (counts.get(grant.role) ?? 0) + 1);
  return roles.map(({ wireID, ...role }) => ({
    ...role,
    count: role.scope === 'org' ? counts.get(wireID) ?? 0 : null
  }));
}

const auditActions = new Map([
  ['identity.login', '登录'], ['identity.logout', '退出'], ['identity.password_change', '修改密码'],
  ['account.preauthorize', '开通飞书账号'], ['account.enable', '启用账号'], ['account.disable', '停用账号'],
  ['role.bootstrap', '初始化管理员'], ['role.grant', '授予角色'], ['role.revoke', '撤销角色'],
  ['directory.sync', '同步组织和用户']
]);
const eventKinds = new Map([
  ['identity', 'login'], ['account', 'account'], ['role', 'role'], ['directory', 'directory']
]);

async function listAudit(api, options) {
  const events = await collectPages(api, system + '/audit-events', options);
  return events.map(event => ({
    id: String(event.id),
    time: event.createdAt,
    actor: event.actorAccountId,
    kind: eventKinds.get(event.action.split('.')[0]) ?? 'config',
    action: auditActions.get(event.action) ?? event.action,
    target: event.resourceId,
    result: event.result,
    before: '',
    after: '',
    source: event.correlationId
  }));
}

function directoryStatus(response) {
  return {
    snapshotVersion: response.snapshot_version,
    visible: response.visible,
    scope: response.scope,
    lastSuccessAt: response.last_success_at,
    nextRunAt: response.next_run_at
  };
}

const snapshotChanged = () => Object.assign(new Error('组织目录已更新，请重试。'), {
  status: 409, data: { error: 'directory_snapshot_changed' }
});

export const isDirectoryConflict = error => error.status === 409 && error.data?.error === 'directory_snapshot_changed';

async function readDirectory(api, { signal, onSnapshot } = {}) {
  let snapshot;
  async function items(resource, params = {}) {
    const records = [];
    const limit = 200;
    let offset = 0;
    for (;;) {
      const query = new URLSearchParams({ ...params, limit, offset });
      if (snapshot?.visible) query.set('snapshot_version', snapshot.snapshotVersion);
      const page = await api(directory + '/' + resource + '?' + query, { signal });
      if (signal?.aborted) throw signal.reason;
      if (!Array.isArray(page?.items) || !Number.isSafeInteger(page.total) ||
          typeof page.visible !== 'boolean' ||
          (page.visible ? !Number.isSafeInteger(page.snapshot_version) || page.snapshot_version <= 0 : page.snapshot_version !== null)) {
        throw new Error('飞书目录响应无效。');
      }
      const status = directoryStatus(page);
      if (!snapshot) {
        snapshot = status;
        onSnapshot?.(status);
      }
      if (snapshot.snapshotVersion !== status.snapshotVersion) throw snapshotChanged();
      records.push(...page.items);
      offset += page.items.length;
      if (offset >= page.total) return records;
      if (!page.items.length || offset > 1000000) throw new Error('无法读取完整的飞书目录。');
    }
  }
  const queue = await items('departments');
  const departments = [];
  const visited = new Set();
  for (const item of queue) {
    if (visited.has(item.id)) throw new Error('飞书组织结构数据无效。');
    visited.add(item.id);
    departments.push({ id: item.id, name: item.name, parentId: item.parent_id });
    if (item.has_children) {
      for (const child of await items('departments', { parent_id: item.id })) queue.push(child);
    }
  }
  const people = await items('people');
  return { snapshot, departments, people };
}

async function directoryView(api, options) {
  // Collect the tree and people together before publishing either to the list.
  const [view, accounts, grants] = await Promise.all([
    readDirectory(api, options),
    collectPages(api, accountPath, options),
    collectPages(api, grantsPath, options)
  ]);
  return {
    snapshot: view.snapshot, departments: view.departments, users: directoryUsers(accounts, grants, view.people)
  };
}

function syncRun(run) {
  if (!run) return null;
  return {
    id: run.id,
    status: run.state,
    createdAt: run.created_at,
    startedAt: run.started_at,
    finishedAt: run.finished_at,
    scope: run.scope,
    departments: run.department_count,
    users: run.people_count,
    addedUsers: run.new_people_count,
    changedUsers: run.changed_people_count,
    missingUsers: run.missing_people_count,
    outsideScopeUsers: run.outside_scope_count,
    failureReason: run.failure_reason,
    coalesced: run.coalesced === true
  };
}

async function getSync(api, { signal } = {}) {
  const response = await api(syncPath + '?limit=2', { signal });
  const snapshot = directoryStatus(response);
  const lastSuccess = snapshot.visible ? response.items.find(run => run.id === snapshot.snapshotVersion) : null;
  return { ...snapshot, run: syncRun(response.current ?? response.latest_attempt), lastSuccess: syncRun(lastSuccess) };
}

async function getSyncSummary(api, snapshotVersion, signal) {
  return syncRun(await api(syncPath + '/' + encodeURIComponent(snapshotVersion), { signal }));
}

async function startSync(api, { signal } = {}) {
  return syncRun(await write(api, syncPath, { method: 'POST', signal }));
}

const writeErrors = new Map([
  ['permission denied', '当前账号没有执行此操作的权限。'],
  ['resource not found', '记录不可用，请刷新后重试。'],
  ['at least one active system administrator is required', '系统必须保留至少一位已启用的管理员。'],
  ['account already exists', '该用户的账号已开通，请刷新列表。'],
  ['role grant already exists', '该用户已有相同范围的角色授权。'],
  ['directory_unavailable', '飞书目录暂不可用，请稍后重试。']
]);

async function write(api, path, options) {
  try {
    return await api(path, options);
  } catch (error) {
    const message = writeErrors.get(error.data?.error);
    if (!message) throw error;
    throw Object.assign(new Error(message, { cause: error }), { status: error.status });
  }
}

function accountID(segment) {
  const id = decodeURIComponent(segment);
  if (!id || id.startsWith(directoryPrefix)) throw new Error('该目录人员尚未开通账号。');
  return id;
}

function provisionAccount(api, segment, signal) {
  const record = decodeURIComponent(segment);
  const openId = record.startsWith(directoryPrefix) && record.slice(directoryPrefix.length);
  if (!openId) throw new Error('请选择尚未开通账号的目录人员。');
  return write(api, accountPath, { method: 'POST', signal, body: { openId } });
}

// Normalizes the admin view's data at the HTTP boundary. Navigation and client
// filtering never grant capabilities, which remain controlled by the API.
export function systemAPI(api) {
  return async (path, { method = 'GET', body, signal, onSnapshot, snapshotVersion } = {}) => {
    if (method === 'GET') {
      switch (path) {
        case system + '/roles': return listRoles(api, { signal });
        case system + '/audit': return listAudit(api, { signal });
        case directory + '/view': return directoryView(api, { signal, onSnapshot });
        case directory + '/sync-summary': return getSyncSummary(api, snapshotVersion, signal);
        case directory + '/sync': return getSync(api, { signal });
      }
    }
    if (method === 'POST' && path === directory + '/sync') return startSync(api, { signal });
    const provisionRoute = path.match(/^\/api\/v1\/system\/users\/([^/]+)\/provision$/);
    if (method === 'POST' && provisionRoute) return provisionAccount(api, provisionRoute[1], signal);

    const accountRoute = path.match(/^\/api\/v1\/system\/users\/([^/]+)(?:\/(roles)(?:\/([^/]+))?)?$/);
    if (!accountRoute) throw new Error('工作台请求地址无效。');

    const [, account, grantRoute, grant] = accountRoute;
    const id = accountID(account);
    if (method === 'PATCH' && !grantRoute && ['active', 'disabled'].includes(body?.status)) {
      return write(api, accountPath + '/' + encodeURIComponent(id), {
        method, signal, body: { active: body.status === 'active' }
      });
    }
    if (method === 'POST' && grantRoute && !grant) {
      const role = wireRoles.get(body?.role);
      if (!role || role.grantable === false) throw new Error('该角色由正式业务系统确定。');
      return write(api, grantsPath, {
        method, signal, body: { accountId: id, role: role.wireID, projectId: body.project || '' }
      });
    }
    if (method === 'DELETE' && grant) {
      return write(api, grantsPath + '/' + encodeURIComponent(decodeURIComponent(grant)), { method, signal });
    }
    throw new Error('工作台请求无效。');
  };
}

// Authentication and authorization errors are server decisions. Probe each
// protected entry with one record so no permissions are inferred from a name.
export async function readAdminCapabilities(api, signal) {
  async function canRead(path) {
    try {
      await api(path + '?limit=1', { signal });
      return true;
    } catch (error) {
      if (error.status === 403) return false;
      throw error;
    }
  }
  const [accounts, roles, directoryPeople, audit] = await Promise.all([
    canRead(accountPath),
    canRead(grantsPath),
    canRead(directory + '/people'),
    canRead(system + '/audit-events')
  ]);
  return { users: accounts && roles && directoryPeople, roles, audit };
}
