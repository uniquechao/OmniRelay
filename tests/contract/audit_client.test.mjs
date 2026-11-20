import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { systemAPI } from '../../web/system-api.js';

const sample = JSON.parse(await readFile(new URL('../fixtures/audit-query.json', import.meta.url)));

test('审计适配器每次只请求一页，保留显示值、精确编号和续页游标', async () => {
  const { query, response, last, empty } = sample;
  const signal = new AbortController().signal;
  const calls = [];
  const request = systemAPI(async (path, options) => {
    assert.equal(options.signal, signal);
    const url = new URL(path, 'https://relay.example.test');
    assert.equal(url.pathname, '/api/v1/system/audit-events');
    calls.push(Object.fromEntries(url.searchParams));
    return [response, last, empty][calls.length - 1];
  });
  const first = await request('/api/v1/system/audit', { query, signal });
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0], { ...query, limit: '1' });
  assert.equal(first.total, 2);
  assert.equal(first.nextCursor, response.nextCursor);
  assert.deepEqual(first.items[0], {
    id: '9007199254740993', time: '2026-11-19T01:00:00Z', actor: 'user-2',
    directorySyncRunId: undefined, kind: 'login', action: '登录', target: 'user-2',
    result: 'success', before: '', after: '', source: 'example-2'
  });
  const second = await request('/api/v1/system/audit', { query: { ...query, cursor: first.nextCursor }, signal });
  assert.equal(second.items[0].id, '9007199254740992');
  assert.equal(second.nextCursor, undefined);
  assert.equal(second.total, 2);
  assert.deepEqual(await request('/api/v1/system/audit', { query: { q: '无匹配' }, signal }), empty);
  assert.deepEqual(calls[1], { ...calls[0], cursor: first.nextCursor });
});
