import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { isDirectoryConflict, systemAPI } from '../../frontend/src/api/system-api.ts';

const base = '/api/v1/system';
const directory = base + '/directory';
const fixtures = JSON.parse(await readFile(new URL('../fixtures/feishu/directory.json', import.meta.url), 'utf8'));
const byName = name => fixtures.find(item => item.name === name);

test('子节点分页固定快照，单次人员读取采用界面页大小', async () => {
  const calls = [];
  const request = systemAPI(async path => {
    const url = new URL(path,'https://relay.example.test');
    calls.push(url);
    const offset = Number(url.searchParams.get('offset'));
    if (url.pathname === base + '/user-page') return {...byName('visible').people,items:[],total:1001};
    return {...byName('visible').departments,items:[{id:'child-' + offset,parent_id:'root',name:'分部',has_children:false}],total:2};
  });
  const nodes = await request.children({parent_id:'root',snapshot_version:17});
  assert.deepEqual(nodes.map(item=>item.id),['child-0','child-1']);
  await request.users({offset:50,limit:50,q:'研发',snapshot_version:17});
  assert.deepEqual(calls.map(url=>url.searchParams.get('snapshot_version')),['17','17','17']);
  assert.deepEqual(calls.map(url=>url.searchParams.get('limit')),['200','200','50']);
  assert.equal(calls[2].searchParams.get('offset'),'50');
});

test('人员页和组织树均拒绝混版响应', async () => {
  const request = systemAPI(async () => byName('visible').departments);
  for (const read of [request.children,request.users]) {
    await assert.rejects(read({snapshot_version:16}),isDirectoryConflict);
    await assert.rejects(read({snapshot_version:0}),isDirectoryConflict);
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
  const queued = await request.startSync();
  assert.equal(queued.status, 'queued');
  assert.equal(queued.coalesced, true);
  const failed = await request.sync();
  assert.equal(failed.run.status, 'failed');
  assert.equal(failed.visible, true);
  assert.equal(failed.nextRunAt, fixture.runs.next_run_at);
  assert.equal(failed.scope.department_roots, 1);
  assert.equal(failed.run.changedUsers, 0);
  assert.equal(failed.run.failureReason, fixture.runs.latest_attempt.failure_reason);
  fixture = byName('scope-hidden');
  const revoked = await request.sync();
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
  assert.deepEqual(await request.provision('directory:ou_example'), { id:'account-example' });
  await assert.rejects(request.provision('account-example'), /尚未开通/);
});

test('角色目录沿用服务端标识和说明，默认人数取自启用账号的有效角色', async () => {
  const catalog = [
    { id:'system_user', name:'普通用户', scope:'system', default:true, grantable:false, permissions:['登录系统'] },
    { id:'system_operator', name:'系统运维者', scope:'system', default:false, grantable:true, permissions:['执行连接检测'] },
    { id:'project_admin', name:'项目管理员', scope:'project', default:false, grantable:true, permissions:[] }
  ];
  const grants = [{ id:'g', accountId:'b', role:'system_operator' }];
  const writes = [];
  const request = systemAPI(async (path, options) => {
    if (options.method === 'POST') return writes.push(options.body);
    if (path === base + '/roles') return { items:catalog };
    if (path === base + '/role-counts') return {system_user:1,system_operator:1};
    if (path.includes('/role-grants?')) return { items:grants };
    return { visible:false, snapshot_version:null, total:0, items:[] };
  });
  const roles = await request.roles();
  assert.deepEqual(roles, catalog.map((role,i) => ({ ...role, count:[1,1,null][i] })));
  const accountGrants = await request.grants('b');
  assert.equal(accountGrants[0].id,'g');
  const grant = id => request.grant('a',{role:catalog.find(role=>role.id===id),project:'p',generation:'membership-uuid'});
  await assert.rejects(grant('system_user'));
  await grant('project_admin');
  assert.deepEqual(writes, [{ accountId:'a', role:'project_admin', projectId:'p',generation:'membership-uuid' }]);
});
