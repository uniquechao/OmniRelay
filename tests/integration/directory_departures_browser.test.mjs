import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { test } from 'node:test';

const require = createRequire(import.meta.url);
const origin = process.env.OMNIRELAY_BROWSER_ORIGIN;
const system = '/api/v1/system';
const runs = system + '/directory/sync-runs';
const credentials = username => ({ username, password: 'fixture-password-only' });

async function request(context, method, path, status, { data, csrf, headers, ...options } = {}) {
  const response = await context.request.fetch(origin + path, {
    method, data, headers: { Origin: origin, ...(csrf && { 'X-CSRF-Token': csrf }), ...headers },
    ...options
  });
  assert.equal(response.status(), status, method + ' ' + path);
  return response;
}

async function expectSync(context, id) {
  for (let attempt = 0; attempt < 80; attempt++) {
    const response = await request(context, 'GET', runs + (id ? '/' + id : '?limit=1'), 200);
    const body = await response.json();
    const run = id ? body : body.items[0];
    if (run?.state === 'succeeded') return run.id;
    assert.notEqual(run?.state, 'failed', run?.failure_reason);
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.fail('目录同步未完成');
}

async function sync(context, csrf) {
  const response = await request(context, 'POST', runs, 202, { data: {}, csrf });
  return expectSync(context, (await response.json()).id);
}

async function inspectDeparture(page, name, status) {
  const row = page.getByRole('row').filter({ hasText: name });
  await row.getByText(status, { exact: true }).waitFor();
  await row.getByText('目录 · 已离职', { exact: true }).waitFor();
  assert.ok(!(await row.innerText()).includes('Example team'));
  await row.getByRole('button', { name: '查看', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '用户详情' });
  await dialog.getByText('离职确认时间', { exact: true }).waitFor();
  assert.match(await dialog.innerText(), /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/);
  if (status === '已启用') await dialog.getByText('停用保护', { exact: true }).waitFor();
  await dialog.getByRole('button', { name: '关闭', exact: true }).last().click();
}

test('浏览器显示飞书离职、账号停用、管理员保护和同步批次', { skip: !origin }, async t => {
  const { chromium } = require(process.env.OMNIRELAY_PLAYWRIGHT_MODULE || 'playwright-core');
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME, args: ['--ignore-certificate-errors'] });
  t.after(() => browser.close());
  const admin = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1366, height: 900 } });
  const member = await browser.newContext({ ignoreHTTPSErrors: true });
  const oauth = await browser.newContext({ ignoreHTTPSErrors: true });
  const oauthLogin = async () => {
    const start = await request(oauth, 'GET', '/auth/feishu/start', 302, { maxRedirects: 0 });
    const state = new URL(start.headers().location).searchParams.get('state');
    return request(oauth, 'GET', '/auth/feishu/callback?' + new URLSearchParams({ state, code: 'ou_member' }),
      303, { maxRedirects: 0 });
  };
  await request(admin, 'POST', '/auth/login', 204, { data: credentials('admin') });
  await request(member, 'POST', '/auth/login', 204, { data: credentials('member') });
  await request(member, 'GET', '/api/v1/me', 200);
  assert.ok(!(await oauthLogin()).headers().location.includes('denied'));
  await request(oauth, 'GET', '/api/v1/me', 200);
  const csrf = (await (await request(admin, 'GET', '/api/v1/me', 200)).json()).csrf_token;
  await expectSync(admin);
  await request(admin, 'POST', '/test-fixture/depart', 204);
  const batch = await sync(admin, csrf);
  await request(member, 'GET', '/api/v1/me', 401);
  await request(oauth, 'GET', '/api/v1/me', 401);
  await request(member, 'POST', '/auth/login', 401, { data: credentials('member') });
  assert.match((await oauthLogin()).headers().location, /denied/);

  const page = await admin.newPage();
  page.setDefaultTimeout(8000);
  await page.goto(origin + '/?theme=light#/system/users');
  await inspectDeparture(page, '样例成员', '已停用');
  await inspectDeparture(page, '同步管理员', '已启用');

  await page.goto(origin + '/#/system/audit');
  const event = page.getByRole('row').filter({ hasText: '停用账号' }).filter({ hasText: '系统（飞书通讯录同步）' });
  await event.getByRole('button', { name: '查看详情' }).click();
  const audit = page.getByRole('dialog', { name: '审计详情' });
  await audit.getByText('同步批次', { exact: true }).waitFor();
  assert.equal(await audit.getByText('同步批次', { exact: true }).locator('xpath=following-sibling::dd').innerText(), String(batch));

  await request(admin, 'POST', '/test-fixture/narrow', 204);
  await sync(admin, csrf);
  const narrow = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
  await request(narrow, 'POST', '/auth/login', 204, { data: credentials('admin') });
  const narrowPage = await narrow.newPage();
  narrowPage.setDefaultTimeout(8000);
  await narrowPage.goto(origin + '/?theme=dark#/system/users');
  assert.equal(await narrowPage.locator('html').getAttribute('data-theme'), 'dark');
  await inspectDeparture(narrowPage, '样例成员', '已停用');
  assert.equal(await narrowPage.getByRole('button', { name: 'Example team', exact: true }).count(), 0);

  const accountsPath = system + '/accounts';
  const listAccounts = async () => (await (await request(admin, 'GET', accountsPath, 200)).json()).items;
  const disabled = (await listAccounts()).find(account => account.name === '样例成员');
  assert.deepEqual(disabled.systemRoles, []);
  await request(admin, 'PATCH', accountsPath + '/' + disabled.id, 409, { data: { active: true }, csrf });
  await request(admin, 'POST', '/test-fixture/return', 204);
  await sync(admin, csrf);
  const returned = (await listAccounts()).find(account => account.id === disabled.id);
  assert.equal(returned.active, false);
  assert.equal(returned.resignedAt, undefined);
  await page.goto(origin + '/#/system/users');
  const returnedRow = page.getByRole('row').filter({ hasText: '样例成员' });
  await returnedRow.getByText('目录 · 在职', { exact: true }).waitFor();
  await returnedRow.getByRole('button', { name: '启用', exact: true }).click();
  await page.getByRole('button', { name: '确认启用', exact: true }).click();
  await returnedRow.getByText('已启用', { exact: true }).waitFor();
  await request(member, 'POST', '/auth/login', 204, { data: credentials('member') });
});
