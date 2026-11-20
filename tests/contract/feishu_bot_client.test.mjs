import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { api, ApiError, setSession } from '../../web/login.js';
import { feishuBotAPI } from '../../web/system-api.js';

const origin = 'https://relay.example.test';
const basePath = '/api/v1/system/feishu-bot';
const fixtures = JSON.parse(await readFile(new URL('../fixtures/feishu/bot-config.json', import.meta.url), 'utf8'));
globalThis.location = new URL(origin);

test('飞书 Bot 客户端传递条件头、幂等键和 CSRF，并读取原始 ETag', async context => {
  const signal = new AbortController().signal;
  const initial = fixtures[0];
  const updated = fixtures[1];
  const task = { id: 'check-queued', state: 'queued', reused: false };
  const expected = [
    { method: 'GET', path: basePath, response: initial },
    { method: 'PATCH', path: basePath, ifMatch: initial.headers.ETag, body: { enabled: true }, response: updated },
    { method: 'POST', path: basePath + '/checks', key: 'check-intent', body: {}, response: { body: task, status: 202 } },
    { method: 'GET', path: basePath + '/checks/check-queued', response: { body: task, status: 200 } }
  ];
  setSession({ csrf_token: 'fixture-csrf' });
  context.after(() => setSession(null));
  context.mock.method(globalThis, 'fetch', async (url, request) => {
    const step = expected.shift();
    assert.equal(url.href, origin + step.path);
    assert.equal(request.method, step.method);
    assert.equal(request.signal, signal);
    assert.equal(request.credentials, 'same-origin');
    assert.equal(request.headers.get('X-CSRF-Token'), step.method === 'GET' ? null : 'fixture-csrf');
    assert.equal(request.headers.get('X-OmniRelay-Activity'), step.path.endsWith('/check-queued') ? 'poll' : null);
    assert.equal(request.headers.get('If-Match'), step.ifMatch ?? null);
    assert.equal(request.headers.get('Idempotency-Key'), step.key ?? null);
    assert.equal(request.body, step.body === undefined ? undefined : JSON.stringify(step.body));
    return Response.json(step.response.body, { status: step.response.status, headers: step.response.headers });
  });

  const bot = feishuBotAPI(api);
  assert.deepEqual(await bot.status(signal), { data: initial.body, etag: initial.headers.ETag });
  assert.deepEqual(await bot.save(true, initial.headers.ETag, signal), { data: updated.body, etag: updated.headers.ETag });
  assert.deepEqual(await bot.test('check-intent', signal), task);
  assert.deepEqual(await bot.check(task.id, signal), task);
  assert.equal(expected.length, 0);
});

test('请求包装保留旧调用返回值、错误与同源路径限制', async context => {
  const fetch = context.mock.method(globalThis, 'fetch', async (url, request) => {
    if (url.pathname === '/auth/logout') return new Response(null, { status: 204 });
    if (request.method === 'PATCH') return Response.json({ error: 'config_version_conflict' }, { status: 412 });
    return Response.json({ items: [] }, { headers: { ETag: '"existing"' } });
  });
  assert.deepEqual(await api('/api/v1/system/accounts'), { items: [] });
  assert.equal(await api('/auth/logout', { method: 'POST' }), null);
  await assert.rejects(feishuBotAPI(api).save(true, '"bot-0"'), error =>
    error instanceof ApiError && error.status === 412 && error.data.error === 'config_version_conflict');
  for (const path of ['https://external.example/api/v1/me', '//external.example/api/v1/me', '/api/v1/../../other']) {
    await assert.rejects(api(path), TypeError);
  }
  assert.equal(fetch.mock.callCount(), 3);
});
