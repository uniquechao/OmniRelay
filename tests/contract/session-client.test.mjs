import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SessionClient } from '../../frontend/src/api/session-client.ts';

const session = (account_id, allowed = true) => ({
  principal:{account_id}, csrf_token:'fixture', access:{navigation:{workbench:allowed}}
});
const json = (data, status = 200) => Response.json(data, {status});
function fixture(t, respond) {
  t.mock.method(globalThis, 'fetch', respond);
  Object.defineProperty(globalThis, 'location', {value:new URL('https://session.test'), configurable:true});
  t.after(() => delete globalThis.location);
  return new SessionClient();
}

test('账号切换后迟到的查询和写入均不能执行完成回调', async t => {
  const pending = [];
  let id = 'A';
  const client = fixture(t, (url, options) => url.pathname.endsWith('/me') ? json(session(id)) :
    new Promise(resolve => pending.push({resolve, signal:options.signal})));
  await client.verify();
  const request = client.bind();
  const commits = [];
  const onSaved = client.guard(value => commits.push(value));
  const outcomes = Promise.allSettled(['GET','POST'].map(method =>
    request('/api/v1/projects', {method}).then(data => commits.push(data))));
  id = 'B';
  await client.verify();
  for (const {resolve, signal} of pending) {
    assert.equal(signal.aborted, true);
    resolve(json({owner:'A'}));
  }
  assert.ok((await outcomes).every(result => result.reason.name === 'AbortError'));
  onSaved('A');
  assert.deepEqual(commits, []);
  await assert.rejects(request('/api/v1/projects'), {name:'AbortError'});
});

test('并发 403 共用一次复核，503 锁定后仅原账号和权限能恢复草稿', async t => {
  let probe = () => json(session('A'));
  let checks = 0;
  const client = fixture(t, url => {
    if (url.pathname !== '/api/v1/me') return json({}, 403);
    checks++;
    return probe();
  });
  await client.verify();
  const scope = client.scope;
  scope.drafts.set('task', '草稿');
  scope.projects.filter = '待办';
  const deferred = Promise.withResolvers();
  probe = () => deferred.promise;
  const request = client.bind();
  const failures = Promise.allSettled([request('/api/v1/tasks'), request('/api/v1/projects')]);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(checks, 2);
  deferred.resolve(json({}, 503));
  await failures;
  assert.equal(client.phase, 'unverified');
  await assert.rejects(request('/api/v1/tasks'), {name:'AbortError'});
  probe = () => json(session('A'));
  await client.verify();
  assert.equal(client.scope, scope);
  assert.equal(client.scope.drafts.get('task'), '草稿');
  assert.equal(client.scope.projects.filter, '待办');
  for (const next of [session('A', false), session('B')]) {
    const before = client.scope;
    before.drafts.set('task', '未保存');
    client.suspend();
    probe = () => json(next);
    await client.verify();
    assert.notEqual(client.scope, before);
    assert.equal(before.drafts.size, 0);
    assert.equal(before.projects.filter, undefined);
    assert.equal(client.scope.projects.filter, undefined);
  }
});

test('权限版本变化或身份接口明确撤权均清除旧状态', async t => {
  const data = session('A');
  let reply = () => json(data);
  const client = fixture(t, () => reply());
  await client.verify();
  const scope = client.scope;
  scope.drafts.set('task', '草稿');
  data.access.permission_version = '2';
  await client.verify();
  assert.notEqual(client.scope, scope);
  assert.equal(scope.drafts.size, 0);
  const verified = client.scope;
  reply = () => json({}, 403);
  await client.verify();
  assert.equal(client.phase, 'revoked');
  assert.equal(client.session, null);
  assert.notEqual(client.scope, verified);
});

test('401 和退出均取消进行中的请求并清除受保护状态', async t => {
  const held = [];
  const client = fixture(t, (url, options) => {
    if (url.pathname.endsWith('/me')) return json(session('A'));
    if (url.pathname === '/auth/logout') return new Response(null, {status:204});
    if (url.pathname.endsWith('/expired')) return json({}, 401);
    held.push(options.signal);
    return new Promise((_, reject) => options.signal.addEventListener('abort', () => reject(options.signal.reason)));
  });
  for (const exit of ['401', 'logout']) {
    await client.verify();
    const old = client.scope;
    old.drafts.set('task', '草稿');
    const request = client.bind();
    const stopped = assert.rejects(request('/api/v1/tasks'), {name:'AbortError'});
    if (exit === 'logout') await client.logout();
    else await assert.rejects(request('/api/v1/expired'), {status:401});
    await stopped;
    assert.ok(held.at(-1).aborted);
    assert.equal(old.drafts.size, 0);
    assert.equal(client.phase, 'anonymous');
  }
});
