import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';

// 使用 final-candidate.sql 的本地专用库。通过同一入口运行新旧候选。
const origin = process.env.H3_ORIGIN;
const password = process.env.H3_PASSWORD;
assert.ok(origin && /^https:\/\/127\.0\.0\.1:\d+$/.test(origin) && password, '需要隔离候选 HTTPS 地址及测试账号口令');
const browser = await chromium.launch({executablePath: process.env.OMNIRELAY_CHROME});
const external = new Set();
const pageErrors = [];
async function login(username) {
  const context = await browser.newContext({ignoreHTTPSErrors: true});
  const page = await context.newPage();
  page.setDefaultTimeout(7000);
  page.on('request', request => {
    const url = new URL(request.url());
    if (/^https?:$/.test(url.protocol) && url.origin !== origin) external.add(url.origin);
  });
  page.on('pageerror', error => pageErrors.push(error.message));
  await page.goto(origin);
  assert.equal(await page.locator('script[src^="/assets/index-"]').count(), 1);
  await page.locator('#login-username').fill(username);
  await page.locator('#login-password').fill(password);
  await page.locator('#password-login button[type=submit]').click();
  await page.waitForURL('**/#/workbench');
  const me = await (await page.request.get(origin + '/api/v1/me')).json();
  assert.equal(me.principal.account_id, username);
  async function api(method, path, data, expected = 200) {
    const response = await page.request.fetch(origin + '/api/v1/' + path, {
      method, data, headers: {Origin: origin, 'X-CSRF-Token': me.csrf_token},
    });
    assert.equal(response.status(), expected, method + ' ' + path);
    return response.json();
  }
  return {page, api};
}

async function check(name, action) {
  try {
    const result = await action();
    console.log('通过', name, result ?? '');
  } catch (error) {
    process.exitCode = 1;
    console.error('未通过', name, error.message);
  }
}
const panels = [
  ['overview', '暂无关键节点。'], ['members', '项目负责人'], ['chats', '当前账号已无法访问此会话。'],
  ['meetings', '暂无记录。'], ['docs', '暂无文件。'], ['reports', '本地日报'],
  ['tasks', '暂无任务'], ['feishu', '待绑定'],
];
async function projects(manager, second) {
  const {page, api} = manager;
  for (const [id, name] of [['h3', '隔离项目甲'], ['joint-b', '隔离项目乙']]) {
    const base = 'projects/' + id + '/';
    await api('PUT', base + 'members/manager/access', {member_id: 'u', role: 'manager'});
    const samples = new Map(panels);
    if (id === 'h3') {
      await api('PUT', base + 'members/second/access', {member_id: 'v', role: 'reader'});
      for (const [tab, kind, title] of [['docs', 'document', '甲项目文档'], ['meetings', 'minutes', '甲项目纪要']]) {
        await api('PUT', base + 'sources/' + kind + '/panel-sample', {title, body: title, reader_account_ids: ['manager']});
        samples.set(tab, title);
        assert.deepEqual((await second.api('GET', base + tab)).data.items, []);
      }
    }
    const existing = (await api('GET', base + 'reports')).data.items.find(item => item.work_date === '2026-11-20');
    await api('PUT', base + 'reports/mine/2026-11-20', {note: '本地日报' + name, expected_version: existing?.version ?? 0});
    for (const [tab, sample] of samples) {
      await check(name + ' ' + tab, async () => {
        await page.goto(origin + '/#/' + base + tab);
        await page.getByRole('heading', {name, exact: true}).waitFor();
        const expected = tab === 'reports' ? sample + name : sample;
        await page.locator('#page').getByText(expected, {exact: true}).first().waitFor();
        const response = await api('GET', base + tab);
        const data = response.data ?? response;
        switch (tab) {
          case 'overview': assert.equal(data.workspace.project.name, name); break;
          case 'members': assert.equal(data.members.length, id === 'h3' ? 2 : 1); break;
          case 'tasks': assert.equal(data.columns.length, 4); break;
          case 'docs':
          case 'meetings': assert.deepEqual(data.items.map(row => row.title), id === 'h3' ? [expected] : []); break;
          case 'reports': assert.deepEqual(data.items.map(item => item.note), [expected]); break;
          case 'feishu': assert.equal(data.state, 'not_bound'); break;
          default: assert.deepEqual(data.items, []);
        }
        if (tab === 'chats') assert.equal((await api('GET', base + 'chat-reader', undefined, 404)).error, 'chat_access_lost');
        return expected;
      });
    }
  }
  await check('跨项目八栏直读、深链与旧正文', async () => {
    const list = await second.api('GET', 'projects');
    assert.deepEqual(list.items.map(p => p.id), ['h3']);
    for (const tab of [...panels.map(([tab]) => tab), 'chat-reader']) {
      await second.api('GET', 'projects/joint-b/' + tab, undefined, 404);
    }
    await second.page.goto(origin + '/#/projects/h3/overview');
    await second.page.getByRole('heading', {name: '隔离项目甲'}).waitFor();
    await second.page.goto(origin + '/#/projects/joint-b/overview');
    await second.page.getByText('当前项目或来源已不可访问。').waitFor();
    assert.doesNotMatch(await second.page.locator('#page').innerText(), /隔离项目[甲乙]/);
  });
  await check('本地会籍恢复再撤销', async () => {
    const member = 'projects/joint-b/members/second/access';
    try {
      await api('PUT', member, {member_id: 'v', role: 'reader'});
      await second.page.reload();
      await second.page.getByRole('heading', {name: '隔离项目乙'}).waitFor();
    } finally {
      await api('PUT', member, {member_id: 'v', role: 'revoked'});
    }
    await second.page.reload();
    await second.page.getByText('当前项目或来源已不可访问。').waitFor();
    assert.doesNotMatch(await second.page.locator('#page').innerText(), /隔离项目乙/);
  });
}

async function bot({page, api}, width) {
  await page.setViewportSize({width, height: 900});
  const status = origin + '/api/v1/system/feishu-bot';
  const target = origin + '/#/system/feishu-bot';
  await page.goto(origin + '/#/workbench');
  await page.getByRole('heading', {name: '工作台总览'}).waitFor();
  const first = Promise.withResolvers();
  const arrived = Promise.withResolvers();
  await page.route(status, async route => {
    arrived.resolve();
    await first.promise;
    await route.continue();
  });
  try {
    await page.goto(target);
    await Promise.race([arrived.promise, page.waitForSelector('#bot-refresh[aria-busy=true]')]);
    await check(width + ' Bot 首读', async () => {
      await page.locator('#page .or-loading').first().waitFor();
      const sizes = await page.locator('#page .or-loading').evaluateAll(nodes => nodes.map(n => n.dataset.size));
      assert.ok(sizes.includes('page'), '实际 Loading ' + JSON.stringify(sizes));
    });
  } finally {
    first.resolve();
  }
  await page.locator('#bot-content').waitFor();
  await page.unroute(status);
  // 固定异步任务的等待窗口，使浏览器能逐档采样。配置、会话仍读真实服务。
  let state = 'queued';
  await page.route(status + '/checks', route => route.fulfill({json: {id: 'acceptance', state}}));
  await page.route(status + '/checks/acceptance', route => route.fulfill({json: {id: 'acceptance', state, results: []}}));
  await page.locator('#bot-test').click();
  for (const [phase, label] of [['queued', '等待开始检测…'], ['running', '正在检测飞书应用…']]) {
    state = phase;
    await page.locator('#bot-test-feedback').filter({hasText: label}).waitFor();
    await check(width + ' Bot ' + phase, async () => {
      const sizes = await page.locator('section').filter({has: page.locator('#bot-test')}).locator('.or-loading')
        .evaluateAll(nodes => nodes.map(n => n.dataset.size));
      assert.ok(sizes.includes('compact'), '实际 Loading ' + JSON.stringify(sizes));
    });
  }
  state = 'succeeded';
  await page.getByText('连接检测完成，请查看各项结果。').waitFor();
  await check(width + ' 诊断结束清理', async () => {
    await page.waitForFunction(() => !document.querySelector('#page .or-loading'));
  });
  await page.unroute(status + '/checks');
  await page.unroute(status + '/checks/acceptance');
  const toggle = page.locator('#bot-enabled');
  const saved = await toggle.isChecked();
  await toggle.click();
  await page.route(status, route => route.fulfill({status: 403, json: {error: 'forbidden'}}));
  await page.locator('#bot-refresh').click();
  await page.getByText('当前账号没有访问飞书 Bot 配置的权限。').waitFor();
  await check(width + ' 403 清除旧页面', async () => {
    assert.equal(await page.locator('#bot-content, #bot-save, #bot-test-feedback').count(), 0);
    assert.equal((await api('GET', 'me')).principal.account_id, 'manager');
  });
  await check(width + ' 403 返回工作台', async () => {
    if (width < 600) {
      await page.locator('.mobile-nav select').selectOption({label: '工作台总览'});
    } else {
      await page.locator('.rail a[data-nav=workbench]').click();
    }
    try {
      await page.getByRole('heading', {name: '工作台总览'}).waitFor({timeout: 3000});
    } catch {
      assert.fail('操作后停留在 ' + new URL(page.url()).hash);
    }
    assert.equal(new URL(page.url()).hash, '#/workbench');
  });
  if (new URL(page.url()).hash !== '#/system/feishu-bot') {
    await page.goto(target);
    await page.getByText('当前账号没有访问飞书 Bot 配置的权限。').waitFor();
  }
  await page.unroute(status);
  await check(width + ' 恢复权限后原页聚焦复核', async () => {
    const verified = page.waitForResponse(r => r.url() === origin + '/api/v1/me');
    await page.evaluate(() => dispatchEvent(new Event('focus')));
    assert.equal((await verified).status(), 200);
    await toggle.waitFor({timeout: 3000});
    assert.equal(await toggle.isChecked(), saved);
    assert.equal(await page.locator('#bot-save').isDisabled(), true);
  });
  await check(width + ' 同会话恢复后重新进入配置', async () => {
    await page.goto(origin);
    await page.goto(target);
    await toggle.waitFor();
    assert.equal(await toggle.isChecked(), saved);
    assert.equal(await page.locator('#bot-save').isDisabled(), true);
    assert.equal((await page.locator('#bot-test-feedback').textContent()).trim(), '');
  });
}

try {
  const manager = await login('manager');
  const second = await login('second');
  const connections = await manager.api('GET', 'system/connections');
  assert.ok(connections.items.some(i => i.id === 'omnirelay' && i.state === 'local'));
  assert.ok(!connections.items.some(i => /grid/i.test(i.id)));
  await projects(manager, second);
  for (const width of [1366, 390]) await bot(manager, width);
  await check('浏览器外域请求与页面错误', () => {
    assert.deepEqual([...external], []);
    assert.deepEqual(pageErrors, []);
  });
} finally {
  await browser.close();
}
