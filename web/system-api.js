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

    if (!byAccount.has(grant.accountId)) byAccount.set(grant.accountId, []);
    byAccount.get(grant.accountId).push({
      id: grant.id, role: grant.role, scope: grant.projectId ? 'project' : 'system', project: grant.projectId ?? ''
    });
  }
  return byAccount;
}

function directoryFields(person, account) {
  const departments = person?.departments ?? [];
  return {
    personKey: person?.person_key ?? account?.personKey,
    departmentIds: [...new Set(departments.map(item => item.id))],
    org: departments.map(item => item.path.join(' / ')).join('；'),
    directoryStatus: person?.directory_status ?? account?.directoryStatus,
    resignedAt: account?.resignedAt,
    disableProtected: account?.disableProtected === true
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
      systemRoles: account.active ? account.systemRoles ?? [] : [],
      roles: accountGrants.get(account.id) ?? [],
      projects: [],
      ...directoryFields(person, account)
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

async function roleCatalog(api, options) {
  const response = await api(system + '/roles', options);
  if (!Array.isArray(response?.items)) throw new Error('角色目录响应无效。');
  return response.items;
}

async function listRoles(api, options) {
  const [catalog, grants, accounts] = await Promise.all([
    roleCatalog(api, options),
    collectPages(api, grantsPath, options),
    collectPages(api, accountPath, options)
  ]);
  const counts = new Map();
  for (const grant of grants) counts.set(grant.role, (counts.get(grant.role) ?? 0) + 1);
  return catalog.map(role => ({
    ...role,
    count: role.default ? accounts.filter(account => account.active && account.systemRoles?.includes(role.id)).length
      : role.scope === 'system' ? counts.get(role.id) ?? 0 : null
  }));
}

const auditActions = new Map([
  ['identity.login', '登录'], ['identity.logout', '退出'], ['identity.password_change', '修改密码'],
  ['account.preauthorize', '开通飞书账号'], ['account.enable', '启用账号'], ['account.disable', '停用账号'],
  ['role.bootstrap', '初始化管理员'], ['role.grant', '授予角色'], ['role.revoke', '撤销角色'],
  ['directory.sync', '同步组织和用户'], ['directory.resign', '确认人员离职'],
  ['account.disable_protected', '保留最后一位管理员账号']
]);
const eventKinds = new Map([
  ['identity', 'login'], ['account', 'account'], ['role', 'role'], ['directory', 'directory']
]);

async function listAudit(api, options) {
  const events = await collectPages(api, system + '/audit-events', options);
  return events.map(event => ({
    id: String(event.id),
    time: event.createdAt,
    actor: event.directorySyncRunId ? '系统（飞书通讯录同步）' : event.actorAccountId,
    directorySyncRunId: event.directorySyncRunId,
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
  ['account_resigned', '飞书目录仍显示已离职。人员重新同步为在册后可启用账号。'],
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
        case system + '/role-catalog': return roleCatalog(api, { signal });
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
      const roles = await roleCatalog(api, { signal });
      const role = roles.find(item => item.id === body?.role);
      if (role?.grantable !== true || role.default) throw new Error('该角色无法手动授予。');
      return write(api, grantsPath, {
        method, signal, body: { accountId: id, role: role.id, ...(role.scope === 'project' && { projectId:body.project }) }
      });
    }
    if (method === 'DELETE' && grant) {
      return write(api, grantsPath + '/' + encodeURIComponent(decodeURIComponent(grant)), { method, signal });
    }
    throw new Error('工作台请求无效。');
  };
}
