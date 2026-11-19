import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { isDirectoryConflict, systemAPI } from '../../web/system-api.js';

const base = '/api/v1/system';
const directory = base + '/directory';
const fixtures = JSON.parse(await readFile(new URL('../fixtures/feishu/directory.json', import.meta.url), 'utf8'));
const byName = name => fixtures.find(item => item.name === name);

test('组织树、人员及后续分页共用同一快照，账号与目录状态独立', async () => {
  const { departments, people } = byName('visible');
  const signal = new AbortController().signal;
  const observed = [];
  const queryOrder = [];
  const request = systemAPI(async (path, options) => {
    assert.equal(options.signal, signal);
    const { pathname, searchParams } = new URL(path, 'https://relay.example.test');
    if (pathname === base + '/accounts') return { items: [{ id:'account-example-1', active:true, source:'feishu' }] };
    if (pathname === base + '/role-grants') return { items:[] };
    const resource = pathname.split('/').at(-1);
    const parent = searchParams.get('parent_id');
    const offset = searchParams.get('offset');
    queryOrder.push([resource, parent, offset, searchParams.get('snapshot_version')]);
    if (resource === 'departments') {
      if (parent) return { ...departments, items:[{ id:'od_ops', name:'运维', parent_id:'od_eng' }], total:1 };
      return departments;
    }
    assert.equal(resource, 'people');
    return { ...people, items:people.items.slice(Number(offset), Number(offset) + 1) };
  });
  const result = await request(directory + '/view', { signal, onSnapshot: value => observed.push(value) });
  assert.deepEqual(queryOrder, [
    ['departments',null,'0',null], ['departments','od_eng','0','17'],
    ...people.items.map((_, index) => ['people',null,String(index),'17'])
  ]);
  assert.equal(observed.length, 1);
  assert.equal(result.snapshot.snapshotVersion, 17);
  assert.equal(result.departments[1].parentId, 'od_eng');
  assert.deepEqual(result.users[0].departmentIds, ['od_eng','od_ops']);
  assert.equal(result.users[0].org, '研发；研发 / 运维');
  assert.equal(result.users[0].status, 'active');
  assert.equal(result.users[1].status, 'unprovisioned');
  assert.equal(result.users[1].directoryStatus, 'unknown');
  assert.equal(new Set(result.users.map(row => row.id)).size, result.users.length);
});

test('原子目录读取拒绝失效或混版结果', async () => {
  const visible = byName('visible');
  const hidden = byName('scope-hidden');
  for (const starting of [visible, hidden]) {
    const request = systemAPI(async path => {
      if (path.includes('/departments?')) return { ...starting.departments, total:0, items:[] };
      if (path.includes('/people?')) {
        if (starting === visible) throw Object.assign(new Error(), { status:409, data:{ error:'directory_snapshot_changed' } });
        return visible.people;
      }
      return { items:[] };
    });
    await assert.rejects(request(directory + '/view'), isDirectoryConflict);
  }
});

test('同步保留排队、复用标志、可见性、下次时间及当前任务计数', async () => {
  let fixture = byName('failed-visible');
  const request = systemAPI(async (path, options) => {
    if (options.method === 'POST') {
      assert.equal(path, directory + '/sync-runs');
      return { ...fixture.runs.latest_attempt, state:'queued', coalesced:true };
    }
    assert.equal(path, directory + '/sync-runs?limit=2');
    return fixture.runs;
  });
  const queued = await request(directory + '/sync', { method:'POST' });
  assert.equal(queued.status, 'queued');
  assert.equal(queued.coalesced, true);
  const failed = await request(directory + '/sync');
  assert.equal(failed.run.status, 'failed');
  assert.equal(failed.visible, true);
  assert.equal(failed.nextRunAt, fixture.runs.next_run_at);
  assert.equal(failed.scope.department_roots, 1);
  assert.equal(failed.run.changedUsers, 0);
  assert.equal(failed.run.failureReason, fixture.runs.latest_attempt.failure_reason);
  fixture = byName('scope-hidden');
  const revoked = await request(directory + '/sync');
  assert.equal(revoked.visible, false);
  assert.equal(revoked.snapshotVersion, null);
  assert.equal(revoked.scope, null);
  assert.equal(revoked.lastSuccess, null);
  assert.ok(revoked.lastSuccessAt);
});

test('开通目录人员必须调用 accounts，提交稳定人员标识', async () => {
  const request = systemAPI(async (path, { method, body }) => {
    assert.equal(path, base + '/accounts');
    assert.equal(method, 'POST');
    assert.deepEqual(body, { openId:'ou_example' });
    return { id:'account-example' };
  });
  assert.deepEqual(await request(base + '/users/directory%3Aou_example/provision', { method:'POST' }), { id:'account-example' });
  await assert.rejects(request(base + '/users/account-example/provision', { method:'POST' }), /尚未开通/);
});

test('角色目录沿用服务端标识和说明，默认人数取自启用账号的有效角色', async () => {
  const catalog = [
    { id:'system_user', name:'普通用户', scope:'system', default:true, grantable:false, permissions:['登录系统'] },
    { id:'system_operator', name:'系统运维者', scope:'system', default:false, grantable:true, permissions:['执行连接检测'] },
    { id:'project_admin', name:'项目管理员', scope:'project', default:false, grantable:true, permissions:[] }
  ];
  const accounts = [{ id:'a', active:true, systemRoles:['system_user'] }, { id:'b', active:false, systemRoles:[] }];
  const grants = [{ id:'g', accountId:'b', role:'system_operator' }];
  const writes = [];
  const request = systemAPI(async (path, options) => {
    if (options.method === 'POST') return writes.push(options.body);
    if (path === base + '/roles') return { items:catalog };
    if (path.includes('/accounts?')) return { items:accounts };
    if (path.includes('/role-grants?')) return { items:grants };
    return { visible:false, snapshot_version:null, total:0, items:[] };
  });
  const roles = await request(base + '/roles');
  assert.deepEqual(roles, catalog.map((role,i) => ({ ...role, count:[1,1,null][i] })));
  const {users} = await request(directory + '/view');
  assert.deepEqual(users[0].roles, []);
  assert.deepEqual(users[0].systemRoles, ['system_user']);
  assert.deepEqual(users[1].systemRoles, []);
  assert.equal(users[1].roles[0].id, 'g');
  const grant = role => request(base + '/users/a/roles', { method:'POST', body:{ role, project:'p' } });
  await assert.rejects(grant('system_user'));
  await grant('project_admin');
  assert.deepEqual(writes, [{ accountId:'a', role:'project_admin', projectId:'p' }]);
});
