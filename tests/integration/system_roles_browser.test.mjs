import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { test } from 'node:test';

const require = createRequire(import.meta.url);
const origin = process.env.OMNIRELAY_BROWSER_ORIGIN;
const system = '/api/v1/system';
const accounts = system + '/accounts';
const grants = system + '/role-grants';
const bot = system + '/feishu-bot';
const connections = system + '/connections';
const sync = system + '/directory/sync-runs';
const mePath = '/api/v1/me';
const permissionKeys = [
  'account.read', 'account.manage', 'role.read', 'role.manage', 'directory.read',
  'directory.sync', 'system.read', 'system.configure', 'system.operate', 'audit.read', 'chat.read_all'
];
const operatorPermissions = [
  'account.read', 'role.read', 'directory.read', 'directory.sync',
  'system.read', 'system.operate', 'audit.read'
];
const systemSections = ['users', 'roles', 'feishu-bot', 'settings', 'audit'];
const matrix = [
  ['admin', 'system_admin', 'workbench', permissionKeys, systemSections, true],
  ['operator', 'system_operator', 'system/users', operatorPermissions, systemSections, false],
  ['auditor', 'audit_viewer', 'system/audit', ['audit.read'], ['audit'], true],
  ['historical', null, 'workbench', [], [], true],
  ['project_operator', 'system_operator', 'system/users', operatorPermissions, systemSections, true]
];

async function request(context, method, path, status, { data, csrf, headers } = {}) {
  const response = await context.request.fetch(origin + path, {
    method, data, headers: { Origin: origin, ...(csrf && { 'X-CSRF-Token': csrf }), ...headers }
  });
  assert.equal(response.status(), status, method + ' ' + path);
  return response;
}

async function login(page, username, expected) {
  await page.locator('#login-username').fill(username);
  await page.locator('#login-password').fill('fixture-password-only');
  await page.locator('#password-login button[type=submit]').click();
  await page.waitForURL('**/#/' + expected);
  await page.locator('#page-heading').waitFor();
}

async function navigate(page, route) {
  await page.goto(origin + '/#/' + route);
  await page.waitForURL('**/#/' + route);
  await page.locator('#page-heading').waitFor();
}

test('真实 Go、PostgreSQL 与浏览器使用同一套系统角色能力', { skip: !origin }, async t => {
  const { chromium } = require(process.env.OMNIRELAY_PLAYWRIGHT_MODULE || 'playwright-core');
  const browser = await chromium.launch({
    executablePath: process.env.OMNIRELAY_CHROME, args: ['--ignore-certificate-errors']
  });
  t.after(() => browser.close());
  const errors = [];
  const actors = new Map();
  for (const [name, role, route, permissions, sections, workbench] of matrix) {
    const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1366, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(origin);
    await login(page, name, route);
    const session = await (await request(context, 'GET', mePath, 200)).json();
    const access = session.access;
    assert.ok(session.principal.account_id && session.csrf_token);
    assert.match(access.permission_version, /^[0-9]+$/);
    assert.deepEqual(access.roles, role ? [role, 'system_user'] : ['system_user']);
    assert.deepEqual(access.permissions, Object.fromEntries(permissionKeys.map(key => [key, permissions.includes(key)])));
    assert.deepEqual(access.navigation, { workbench, system: sections.length > 0, bot_chats: workbench });
    assert.deepEqual(access.admin, Object.fromEntries(systemSections.map(section => [
      section.replace('-', '_'), sections.includes(section)
    ])));
    assert.equal(access.default_route, '/#/' + route);
    assert.deepEqual(await page.locator('#system-nav [data-system]:not([hidden])').evaluateAll(nodes => nodes.map(n => n.dataset.system)), sections);
    assert.equal(await page.locator('[data-nav=workbench]').isVisible(), workbench);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator('#mobile-nav').waitFor();
    assert.deepEqual(await page.locator('#mobile-nav option:not([hidden])').evaluateAll(nodes => nodes.map(n => n.value)),
      [...(workbench ? ['workbench', 'projects', 'feishu/bot-chats', 'feishu/drive'] : []),
        ...sections.map(section => 'system/' + section)]);
    if (sections.includes('roles')) {
      await page.locator('#mobile-nav').selectOption('system/roles');
      await page.waitForURL('**/#/system/roles');
      await page.locator('tr[data-row=system_user]').waitFor();
      await page.goBack();
      await page.waitForURL('**/#/' + route);
      await page.goForward();
      await page.locator('tr[data-row=system_user]').waitFor();
    }
    await page.setViewportSize({ width: 1366, height: 900 });

    for (const [path, action] of [
      [accounts, 'account.read'], [system + '/roles', 'role.read'], [grants, 'role.read'],
      [system + '/audit-events', 'audit.read'], [system + '/directory/departments', 'directory.read'],
      [sync, 'directory.read'], [bot, 'system.read'], [connections, 'system.read']
    ]) {
      await request(context, 'GET', path, permissions.includes(action) ? 200 : 403);
    }
    if (!sections.includes('users')) {
      await page.goto(origin + '/#/system/users');
      await page.waitForURL('**/#/' + route);
    }
    if (!sections.includes('settings')) {
      for (const alias of ['settings', 'system/bot']) {
        await page.goto(origin + '/#/' + alias);
        await page.waitForURL('**/#/' + route);
      }
    }
    const write = (method, path, status, data, headers) =>
      request(context, method, path, status, { data, headers, csrf: session.csrf_token });
    actors.set(name, { context, page, session, write });
    if (name !== 'admin') {
      const { session: root } = actors.get('admin');
      await write('POST', accounts, 403, { openId: 'ou_member' });
      await write('PATCH', accounts + '/' + root.principal.account_id, 403, { active: false });
      await write('POST', grants, 403, { accountId: root.principal.account_id, role: 'audit_viewer' });
      await write('PATCH', bot, 403, { enabled: true }, { 'If-Match': '"bot-0"' });
      if (!permissions.includes('system.operate')) {
        await write('POST', bot + '/checks', 403, {}, { 'Idempotency-Key': 'rejected-check' });
        await write('POST', sync, 403);
      }
    }
  }

  const admin = actors.get('admin');
  const operator = actors.get('operator');
  const historical = actors.get('historical');
  const projectOperator = actors.get('project_operator');
  const target = historical.session.principal.account_id;
  const read = (path, actor = admin) => request(actor.context, 'GET', path, 200).then(response => response.json());
  const accountId = actor => actor.session.principal.account_id;

  const entry = await admin.context.newPage();
  await entry.goto(origin + '/#/settings');
  await entry.waitForURL('**/#/system/settings');
  await entry.getByText('未接入', { exact: true }).first().waitFor();
  assert.equal(await entry.locator('#system-nav').getAttribute('open'), '');
  assert.equal(await entry.locator('#system-nav [aria-current=page]').getAttribute('data-system'), 'settings');
  await entry.getByRole('link', { name: '飞书 Bot 配置', exact: true }).click();
  await entry.waitForURL('**/#/system/feishu-bot');
  await entry.locator('#bot-form').waitFor();
  await entry.goBack();
  await entry.waitForURL('**/#/system/settings');
  await entry.getByRole('heading', { name: '连接设置' }).waitFor();
  await entry.goForward();
  await entry.waitForURL('**/#/system/feishu-bot');
  await entry.locator('#bot-form').waitFor();
  const current = admin.page;
  await navigate(current, 'system/feishu-bot');
  await current.locator('#bot-form').waitFor();
  await current.locator('#bot-enabled').check();
  const firstSave = current.waitForResponse(res => res.url().endsWith(bot) && res.request().method() === 'PATCH');
  await current.locator('#bot-save').click();
  assert.equal((await firstSave).status(), 200);
  await current.locator('[data-bot=enabled]').filter({ hasText: '已保存为启用' }).waitFor();

  await entry.locator('#bot-enabled').check();
  const staleSave = entry.waitForResponse(res => res.url().endsWith(bot) && res.request().method() === 'PATCH');
  await entry.locator('#bot-save').click();
  assert.equal((await staleSave).status(), 412);
  await entry.locator('#bot-save-feedback').filter({ hasText: /再次点击「保存」确认/ }).waitFor();
  assert.equal(await entry.locator('#bot-enabled').isChecked(), true);
  await entry.locator('#bot-enabled').uncheck();
  const confirmedSave = entry.waitForResponse(res => res.url().endsWith(bot) && res.request().method() === 'PATCH');
  await entry.locator('#bot-save').click();
  const saved = await confirmedSave;
  assert.equal(saved.status(), 200);
  assert.equal(saved.headers().etag, '"bot-2"');
  assert.equal((await saved.json()).enabled, false);
  await entry.close();

  const returning = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 390, height: 844 } });
  const mobile = await returning.newPage();
  await mobile.goto(origin + '/#/system/bot');
  await mobile.waitForURL('**/#/login');
  await login(mobile, 'admin', 'system/feishu-bot');
  await mobile.locator('#bot-form').waitFor();
  assert.equal(await mobile.locator('#mobile-nav').inputValue(), 'system/feishu-bot');
  await mobile.locator('#mobile-nav').selectOption('system/settings');
  await mobile.waitForURL('**/#/system/settings');
  await mobile.getByText('未接入', { exact: true }).first().waitFor();
  assert.equal(await mobile.locator('#mobile-nav').inputValue(), 'system/settings');
  await returning.close();
  assert.deepEqual(operator.session.access.permissions, projectOperator.session.access.permissions);

  const { items: catalog } = await read(system + '/roles');
  assert.deepEqual(catalog.map(({ id, scope, default: automatic, grantable }) => [id, scope, automatic, grantable]), [
    ['system_user', 'system', true, false], ['system_admin', 'system', false, true],
    ['system_operator', 'system', false, true], ['audit_viewer', 'system', false, true],
    ['project_admin', 'project', false, true], ['project_member', 'project', false, false]
  ]);
  assert.ok(catalog.every(role => role.name && role.permissions.length && role.permissions.every(text => typeof text === 'string')));
  const { items: initialAccounts } = await read(accounts);
  assert.equal(initialAccounts.length, matrix.length);
  for (const actor of actors.values()) {
    const account = initialAccounts.find(item => item.id === accountId(actor));
    assert.equal(account.active, true);
    assert.equal(typeof account.name, 'string');
    assert.equal(typeof account.avatarUrl, 'string');
    assert.ok(Number.isFinite(Date.parse(account.createdAt)));
    assert.ok(Number.isFinite(Date.parse(account.updatedAt)));
    assert.deepEqual(account.systemRoles, actor.session.access.roles);
  }
  assert.deepEqual((await read(grants + '?accountId=' + target)).items, []);
  const initialGrants = (await read(grants)).items;
  const adminGrant = initialGrants.find(grant => grant.role === 'system_admin');
  const operatorGrant = initialGrants.find(grant => grant.accountId === accountId(operator));
  await operator.write('DELETE', grants + '/' + adminGrant.id, 403);
  await actors.get('auditor').write('DELETE', grants + '/' + adminGrant.id, 404);
  await admin.write('POST', grants, 400, { accountId: target, role: 'system_user' });
  await admin.write('DELETE', grants + '/system_user', 404);
  await admin.write('DELETE', grants + '/' + adminGrant.id, 409);
  await admin.write('PATCH', accounts + '/' + accountId(admin), 409, { active: false });

  const projectID = 'acceptance-project';
  await request(admin.context, 'GET', grants + '?projectId=' + projectID, 403);
  await operator.write('POST', grants, 403, { accountId: target, role: 'project_admin', projectId: projectID });
  await projectOperator.write('POST', grants, 500,
    { accountId: target, role: 'project_admin', projectId: 'unavailable-project' });
  const delegated = await (await projectOperator.write('POST', grants, 201,
    { accountId: target, role: 'project_admin', projectId: projectID })).json();
  assert.equal(delegated.projectId, projectID);
  assert.equal(delegated.source, 'project_manager');
  assert.deepEqual((await read(grants + '?projectId=' + projectID, projectOperator)).items.map(item => item.id), [delegated.id]);
  assert.deepEqual((await read(grants + '?accountId=' + target)).items, []);
  await admin.write('DELETE', grants + '/' + delegated.id, 404);
  await projectOperator.write('DELETE', grants + '/' + delegated.id, 204);
  await request(historical.context, 'GET', mePath, 401);
  await historical.page.reload();
  await login(historical.page, 'historical', 'workbench');

  await navigate(operator.page, 'system/users');
  const userDrawer = operator.page.locator('[data-admin-action=user]').first();
  await userDrawer.click();
  await operator.page.getByText('系统级 · 默认拥有').first().waitFor();
  assert.equal(await operator.page.locator('[data-admin-action=toggle], [data-admin-action=add-role], [data-admin-action=revoke]').count(), 0);
  await operator.page.keyboard.press('Escape');
  const syncResponse = operator.page.waitForResponse(res => res.url().endsWith(sync) && res.request().method() === 'POST');
  await operator.page.locator('[data-directory-sync]').click();
  assert.equal((await syncResponse).status(), 202);
  await navigate(operator.page, 'system/feishu-bot');
  await operator.page.locator('#bot-content').waitFor();
  assert.equal(await operator.page.locator('#bot-form').isVisible(), false);
  const checkEndpoint = origin + bot + '/checks';
  let originalKey;
  let originalTask;
  await operator.page.route(checkEndpoint, async route => {
    originalKey = route.request().headers()['idempotency-key'];
    const response = await route.fetch();
    originalTask = await response.json();
    await route.abort();
  }, { times: 1 });

  const testButton = operator.page.locator('#bot-test');
  await testButton.click();
  await testButton.filter({ hasText: '重试检测' }).waitFor();
  const retry = operator.page.waitForResponse(res => res.url() === checkEndpoint && res.request().method() === 'POST');
  await testButton.click();
  const retried = await retry;
  assert.equal(retried.status(), 202);
  assert.ok(originalKey && originalTask.id);
  assert.equal(retried.request().headers()['idempotency-key'], originalKey);
  assert.equal((await retried.json()).id, originalTask.id);
  const tokenResult = operator.page.locator('[data-bot=tenant_token]');
  const botResult = operator.page.locator('[data-bot=bot_info]');
  await tokenResult.filter({ hasText: '通过' }).waitFor();
  await botResult.filter({ hasText: '通过' }).waitFor();
  assert.equal(await operator.page.locator('[data-bot=message]').textContent(), '尚未收到');

  await testButton.click();
  await botResult.filter({ hasText: '失败' }).waitFor();
  assert.equal(await tokenResult.textContent(), '通过');
  assert.match(await operator.page.locator('[data-bot=bot_info_detail]').textContent(), /无法读取 Bot 信息/);
  assert.match(await operator.page.locator('#bot-test-feedback').textContent(), /机器人能力/);

  await navigate(admin.page, 'system/roles');
  const baseRole = admin.page.locator('tr[data-row=system_user]');
  assert.match(await baseRole.textContent(), /默认拥有 · 5 人/);
  await admin.page.locator('[data-more]').click();
  const scope = admin.page.locator('[data-filter=scope]');
  await scope.selectOption('project');
  await admin.page.locator('tr[data-row=project_admin]').waitFor();
  assert.equal(await admin.page.locator('tr[data-row]').count(), 2);
  await scope.selectOption('system');
  await baseRole.waitFor();
  assert.equal(await admin.page.locator('tr[data-row]').count(), 4);

  await navigate(admin.page, 'system/users');
  const historicalRow = admin.page.locator('tr[data-row="' + target + '"]');
  await historicalRow.locator('[data-admin-action=toggle]').waitFor();
  await historicalRow.locator('[data-admin-action=user]').click();
  await admin.page.locator('[data-admin-action=add-role]').waitFor();
  const defaultRole = admin.page.locator('.admin-grants li').filter({ hasText: '系统级 · 默认拥有' });
  await defaultRole.waitFor();
  assert.equal(await defaultRole.locator('[data-admin-action=revoke]').count(), 0);
  await admin.page.keyboard.press('Escape');
  await navigate(admin.page, 'system/feishu-bot');
  await admin.page.locator('#bot-form').waitFor();
  await admin.page.locator('#bot-test').waitFor();
  await navigate(admin.page, 'system/roles');

  const oldVersion = BigInt((await read(mePath, historical)).access.permission_version);
  const granted = await (await admin.write('POST', grants, 201, { accountId: target, role: 'audit_viewer' })).json();
  const elevated = await read(mePath, historical);
  assert.deepEqual(elevated.access.roles, ['audit_viewer', 'system_user']);
  assert.equal(BigInt(elevated.access.permission_version), oldVersion + 1n);
  await request(historical.context, 'GET', system + '/audit-events', 200);
  await historical.page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await historical.page.locator('#system-nav [data-system=audit]:not([hidden])').waitFor({ state: 'attached' });
  await navigate(historical.page, 'system/audit');
  const disabled = await (await admin.write('PATCH', accounts + '/' + target, 200, { active: false })).json();
  assert.deepEqual(disabled.systemRoles, []);
  await request(historical.context, 'GET', mePath, 401);
  assert.equal((await read(grants + '?accountId=' + target)).items[0].id, granted.id);
  await admin.page.reload();
  await baseRole.getByText('默认拥有 · 4 人').waitFor();
  const enabled = await (await admin.write('PATCH', accounts + '/' + target, 200, { active: true })).json();
  assert.deepEqual(enabled.systemRoles, ['audit_viewer', 'system_user']);
  await admin.page.reload();
  await baseRole.getByText('默认拥有 · 5 人').waitFor();
  await historical.page.reload();
  await login(historical.page, 'historical', 'system/audit');
  assert.equal(BigInt((await read(mePath, historical)).access.permission_version), oldVersion + 3n);
  await admin.write('DELETE', grants + '/' + granted.id, 204);
  await request(historical.context, 'GET', mePath, 401);

  const secondTab = await operator.context.newPage();
  secondTab.setDefaultTimeout(8000);
  await navigate(secondTab, 'system/feishu-bot');
  await secondTab.locator('#bot-test').waitFor();
  await navigate(operator.page, 'system/settings');
  await operator.page.getByText('未接入', { exact: true }).first().waitFor();
  await admin.write('DELETE', grants + '/' + operatorGrant.id, 204);
  await operator.write('POST', bot + '/checks', 401, {}, { 'Idempotency-Key': 'stale-session' });
  for (const tab of [operator.page, secondTab]) {
    // 无头浏览器用事件触发恢复焦点，与应用注册的刷新路径相同。
    await tab.evaluate(() => window.dispatchEvent(new Event('focus')));
    await tab.waitForURL('**/#/login');
    await tab.locator('#login-username').waitFor();
    assert.equal(await tab.locator('#bot-test').isVisible(), false);
  }
  await login(secondTab, 'operator', 'workbench');
  await operator.page.reload();
  await operator.page.waitForURL('**/#/workbench');
  await operator.page.getByRole('heading', { name: '工作台总览' }).waitFor();
  assert.equal(await operator.page.locator('[data-system=settings]').isVisible(), false);
  await request(operator.context, 'GET', connections, 403);
  await request(operator.context, 'GET', bot, 403);
  assert.deepEqual((await read(mePath, operator)).access.roles, ['system_user']);
  await secondTab.locator('.rail .account-trigger').click();
  await secondTab.locator('[data-account=profile]').click();
  const profile = secondTab.getByRole('dialog', { name: '个人信息' });
  await profile.getByText('operator', { exact: true }).waitFor();
  await request(operator.context, 'POST', '/auth/login', 204, {
    data: { username: 'auditor', password: 'fixture-password-only' }
  });
  await secondTab.evaluate(() => window.dispatchEvent(new Event('focus')));
  await secondTab.locator('.rail .account-trigger-name').getByText('auditor', { exact: true }).waitFor();
  await profile.waitFor({ state: 'hidden' });
  assert.deepEqual(errors, []);
});

test('后台轮询持续成功时，会话仍按空闲期限失效', { skip: !process.env.OMNIRELAY_IDLE_BROWSER }, async t => {
  const { chromium } = require(process.env.OMNIRELAY_PLAYWRIGHT_MODULE);
  const browser = await chromium.launch({
    executablePath: process.env.OMNIRELAY_CHROME, args: ['--ignore-certificate-errors']
  });
  t.after(() => browser.close());
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const users = await context.newPage();
  const detector = await context.newPage();
  users.setDefaultTimeout(22000);
  detector.setDefaultTimeout(22000);
  const results = { directory: [], checks: [], probes: [] };
  const errors = [];
  for (const page of [users, detector]) page.on('pageerror', error => errors.push(error.message));
  users.on('response', response => {
    const path = new URL(response.url()).pathname;
    if (path === sync) results.directory.push(response.status());
    if (path === mePath) results.probes.push(response.status());
  });
  detector.on('response', response => {
    if (new URL(response.url()).pathname.startsWith(bot + '/checks/')) results.checks.push(response.status());
  });
  await users.goto(origin + '/#/system/users');
  await login(users, 'admin', 'system/users');
  await users.locator('[data-admin-action=user]').first().click();
  await users.locator('dialog[open]').waitFor();
  await navigate(detector, 'system/feishu-bot');
  await detector.locator('#bot-test').click();

  // 等到首次后台状态查询成功，再触发应用实际使用的焦点探活。
  await users.waitForResponse(response => new URL(response.url()).pathname === sync && response.status() === 200);
  const probe = users.waitForResponse(response => new URL(response.url()).pathname === mePath);
  await users.evaluate(() => window.dispatchEvent(new Event('focus')));
  assert.equal((await probe).status(), 200);
  await Promise.all([users, detector].map(page => page.waitForURL('**/#/login')));
  for (const name of ['directory', 'checks']) {
    assert.ok(results[name].filter(status => status === 200).length >= 2, JSON.stringify(results));
    assert.equal(results[name].at(-1), 401, JSON.stringify(results));
  }
  assert.equal(await users.locator('#page').count(), 0);
  assert.equal(await users.locator('dialog[open]').count(), 0);
  assert.equal(await detector.locator('#page').count(), 0);

  await login(users, 'auditor', 'system/audit');
  assert.deepEqual(await users.locator('#system-nav [data-system]:not([hidden])')
    .evaluateAll(nodes => nodes.map(node => node.dataset.system)), ['audit']);
  assert.equal(await users.locator('[data-admin-action=user]').count(), 0);
  assert.deepEqual(errors, []);
});
