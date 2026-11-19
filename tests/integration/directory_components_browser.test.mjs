import assert from 'node:assert/strict';
import { mkdir, readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { extname, join } from 'node:path';
import { test } from 'node:test';

const require = createRequire(import.meta.url);
const origin = 'https://relay.example.test';
const system = '/api/v1/system';
const directory = system + '/directory/';
const fixtures = JSON.parse(await readFile(new URL('../fixtures/feishu/directory.json', import.meta.url), 'utf8'));
const visible = fixtures.find(item => item.name === 'visible');
const hidden = fixtures.find(item => item.name === 'scope-hidden');
const prototype = '/prototype/omni-relay-workbench.html';
const team = '平台研发事业群用于检查长组织路径'.repeat(4);
const name = '同名成员用于检查缺省头像与长姓名省略'.repeat(4);
const paths = [[team, '运维'], ['交付中心', '交付一组']];
const fullPath = paths.map(path => path.join(' / ')).join('；');
const [pending, registered] = visible.people.items;
const roster = [
  { ...registered, name, person_key: 'wang-ning', departments: [
    { id: 'od_ops', path: paths[0] }, { id: 'od_delivery_1', path: paths[1] }
  ] },
  { ...pending, name, person_key: 'chen-yue', departments: [{ id: 'od_delivery_1', path: paths[1] }] }
];
const account = { id: registered.account_id, name, source: 'feishu', active: true, personKey: roster[0].person_key };
const departments = {
  '': [{ ...visible.departments.items[0], name: team }, { id: 'od_delivery', name: '交付中心', has_children: true }],
  od_eng: [{ id: 'od_ops', parent_id: 'od_eng', name: '运维' }],
  od_delivery: [{ id: 'od_delivery_1', parent_id: 'od_delivery', name: '交付一组' }]
};
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png' };
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'";

async function mountFixture(page) {
  let state = visible;
  const writes = [];
  const { account_id } = registered;
  await page.route(origin + '/**', async route => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path.startsWith('/api/')) {
      if (route.request().method() !== 'GET') {
        writes.push([route.request().method(), path]);
        return route.fulfill({ status: 405 });
      }
      if (path === '/api/v1/me') return route.fulfill({ json: {
        principal: { account_id }, csrf_token: 'fixture',
        access: { navigation: { system: true, workbench: true }, admin: { users: true },
          permissions: { 'directory.read': true }, default_route: '/#/system/users' }
      } });
      if (path === system + '/accounts') return route.fulfill({ json: { items: [account] } });
      if ([system + '/roles', system + '/role-grants'].includes(path)) return route.fulfill({ json: { items: [] } });
      if (state === null && path.startsWith(directory)) return route.fulfill({ status: 403, json: { error: 'forbidden' } });
      if (path === directory + 'sync-runs') return route.fulfill({ json: state.runs });
      if (path === directory + 'departments' || path === directory + 'people') {
        const kind = path.slice(directory.length);
        const records = kind === 'people' ? roster : departments[url.searchParams.get('parent_id') ?? ''];
        const items = state === visible ? records : [];
        return route.fulfill({ json: { ...state[kind], items, total: items.length } });
      }
      return route.fulfill({ status: 404 });
    }

    const isPrototype = path.startsWith('/prototype/');
    const asset = isPrototype ? path.slice('/prototype/'.length) : path.slice(1) || 'index.html';
    for (const folder of isPrototype ? ['design/prototype'] : ['web', 'design/prototype']) {
      try {
        const body = await readFile(new URL('../../' + folder + '/' + asset, import.meta.url));
        return route.fulfill({
          body, contentType: mime[extname(asset)] ?? 'application/octet-stream',
          headers: isPrototype ? {} : { 'Content-Security-Policy': csp }
        });
      } catch (error) {
        if (error.code !== 'ENOENT') throw error;
      }
    }
    return route.fulfill({ status: 404 });
  });
  return { writes, revoke: () => { state = hidden; }, deny: () => { state = null; } };
}

const avatarTone = locator => locator.locator('.person-avatar').getAttribute('data-avatar-tone');
const openCard = page => page.locator('.person-popover:popover-open');
const treeButton = (tree, label) => tree.getByRole('button', { name: label, exact: true });
const active = (locator, selector) => locator.evaluate((node, target) => node.ownerDocument.activeElement === node && node.matches(target), selector);

async function checkTree(page, tree, root, first, second, leaf, touch) {
  const expand = treeButton(tree, '展开全部');
  const collapse = treeButton(tree, '收起全部');
  await (touch ? expand.tap() : expand.click());
  assert.equal(await tree.locator('[data-tree-toggle][aria-expanded="false"]').count(), 0);
  await expand.focus();
  await page.keyboard.press('Tab');
  assert.ok(await active(collapse, ':focus-visible'));
  await page.keyboard.press('Space');
  assert.equal(await tree.locator('[data-tree-toggle][aria-expanded="true"]').count(), 0);
  assert.ok(await active(collapse, ':focus-visible'));
  if (touch) {
    await expand.tap();
    await collapse.tap();
    assert.equal(await tree.locator('[data-tree-toggle][aria-expanded="true"]').count(), 0);
  }
  await treeButton(tree, '展开' + root).click();
  await treeButton(tree, '展开' + first).click();
  const switchBranch = treeButton(tree, '展开' + second);
  await (touch ? switchBranch.tap() : switchBranch.click());
  assert.equal(await treeButton(tree, '展开' + first).count(), 1);
  await treeButton(tree, '收起' + second).focus();
  await page.keyboard.press('ArrowRight');
  const chosen = treeButton(tree, leaf);
  assert.ok(await active(chosen, ':focus-visible'));
  await page.keyboard.press('ArrowLeft');
  await page.keyboard.press('ArrowRight');
  assert.ok(await active(chosen, ':focus-visible'));
  await page.keyboard.press('Enter');
  assert.equal(await chosen.getAttribute('aria-pressed'), 'true');
  assert.equal(await tree.locator('[data-org-selection]').textContent(), '已选择 ' + second + ' / ' + leaf);
  assert.ok(await tree.evaluate(node => node.scrollWidth <= node.clientWidth));
}

async function checkCard(card, tone, path) {
  await card.waitFor();
  assert.equal(await avatarTone(card), tone);
  assert.equal(await card.locator('.person-org').count(), path ? 1 : 0);
  if (path) assert.equal(await card.locator('.person-org').getAttribute('title'), path);
  assert.ok(await card.evaluate(node => {
    const box = node.getBoundingClientRect();
    return box.left >= 7 && box.top >= 7 && box.right <= innerWidth - 7 && box.bottom <= innerHeight - 7;
  }));
}

test('UI01 与 UI02 共享组件通过目录身份和路径回归', { skip: !process.env.OMNIRELAY_PLAYWRIGHT_MODULE }, async t => {
  const { chromium } = require(process.env.OMNIRELAY_PLAYWRIGHT_MODULE);
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const evidence = process.env.OMNIRELAY_DIRECTORY_EVIDENCE;
  if (evidence) await mkdir(evidence, { recursive: true });

  for (const [width, height] of [[1366, 900], [390, 844]]) {
    for (const theme of ['light', 'dark']) {
      await t.test(width + '×' + height + ' ' + (theme === 'light' ? '浅色' : '深色'), async () => {
        const touch = width === 390;
        const context = await browser.newContext({ viewport: { width, height }, hasTouch: touch });
        try {
          const page = await context.newPage();
          page.setDefaultTimeout(8000);
          await page.clock.install();
          const errors = [];
          page.on('pageerror', error => errors.push(error.message));
          const fixture = await mountFixture(page);
          const go = (path, route) => page.goto(origin + path + '?theme=' + theme + '#/' + route);
          const capture = stage => evidence && page.screenshot({ path: join(evidence, width + '-' + theme + '-' + stage + '.png') });
          await go('/', 'system/users');
          if (touch) await page.locator('.directory-org summary').tap();
          assert.equal(await page.locator('html').getAttribute('data-theme'), theme);
          const tree = page.locator('[data-directory-tree] .dev-tree');
          const ready = page.locator('tbody tr[data-row]');
          await page.locator('tbody .person-name').first().waitFor();
          await checkTree(page, tree, '全部组织', team, '交付中心', '交付一组', touch);
          assert.equal(await ready.count(), 2);
          const employee = page.locator('[data-row="' + account.id + '"]');
          const other = page.locator('[data-row="directory:' + pending.open_id + '"]');
          await treeButton(tree, '展开' + team).click();
          await treeButton(tree, '运维').click();
          assert.equal(await ready.count(), 1);
          assert.equal(await employee.count(), 1);
          await treeButton(tree, '全部组织').click();
          const portrait = employee.locator('.person-portrait');
          const personName = employee.locator('.person-name');
          const card = openCard(page);
          const tone = await avatarTone(portrait);
          assert.equal(await other.locator('.person-name').textContent(), await personName.textContent());
          assert.notEqual(await other.locator('.person-portrait').getAttribute('data-person-key'), roster[0].person_key);
          assert.equal(await portrait.locator('.person-avatar').textContent(), [...name].slice(-2).join(''));
          assert.equal(await portrait.locator('img').count(), 0);
          assert.ok(await personName.evaluate(node => node.scrollWidth > node.clientWidth));
          await other.locator('.person-name').click();
          await checkCard(card, await avatarTone(other.locator('.person-portrait')), paths[1].join(' / '));
          await page.keyboard.press('Escape');
          await card.waitFor({ state: 'detached' });
          await (touch ? portrait.tap() : portrait.click());
          await checkCard(card, tone, fullPath);
          await page.keyboard.press('Escape');
          await card.waitFor({ state: 'detached' });
          assert.ok(await active(portrait, 'button'));
          await personName.focus();
          await page.keyboard.press('Enter');
          await checkCard(card, tone, fullPath);
          await capture('directory');
          await page.mouse.click(4, 4);
          await card.waitFor({ state: 'detached' });
          assert.ok(await active(personName, 'button'));
          await personName.click();
          await card.waitFor();

          fixture.revoke();
          await page.clock.fastForward(5000);
          await page.getByText('飞书通讯录授权已变化，旧目录已隐藏。').waitFor();
          await card.waitFor({ state: 'detached' });
          await personName.waitFor();
          assert.equal(await tree.count(), 0);
          assert.equal(await other.count(), 0);
          assert.equal(await employee.locator('.person-org').count(), 0);
          await personName.click();
          await checkCard(card, tone, '');
          assert.equal(await card.getAttribute('aria-label'), name + '的基本信息');
          await page.keyboard.press('Escape');
          fixture.deny();
          await page.reload();
          await page.getByText('当前账号没有查看飞书目录的权限。').waitFor();
          assert.equal(await tree.count(), 0);
          assert.equal(await ready.count(), 0);
          assert.equal(await page.locator('body').getByText(paths[1][1], { exact: true }).count(), 0);
          assert.deepEqual(fixture.writes, []);

          await go(prototype, 'developer/components');
          const demoTree = page.locator('[data-demo-tree]');
          await checkTree(page, demoTree, 'OmniRelay', '研发中心', '交付中心', '交付一组', touch);
          const pickerTrigger = page.locator('[data-org-open]');
          await (touch ? pickerTrigger.tap() : pickerTrigger.click());
          const picker = page.getByRole('dialog', { name: '选择组织' });
          await checkTree(page, picker, 'OmniRelay', '研发中心', '交付中心', '交付一组', touch);
          await capture('picker');
          await treeButton(picker, '确认选择').click();
          await picker.waitFor({ state: 'hidden' });
          assert.equal(await page.locator('[data-org-value]').textContent(), paths[1].join(' / '));
          assert.ok(await active(pickerTrigger, 'button'));
          const demo = page.locator('.person-examples .person-portrait[data-person-key="wang-ning"]');
          assert.equal(await avatarTone(demo), tone);

          await go(prototype, 'feishu/bot-chats');
          const direct = page.locator('.chat-direct-row').filter({ hasText: '王宁' });
          await direct.locator('.person-name').click();
          await checkCard(card, tone, paths[1].join(' / '));
          await page.keyboard.press('Escape');
          await direct.getByRole('button', { name: '打开与王宁的会话' }).click();
          await page.locator('.chat-reading-title .person-portrait').click();
          await checkCard(card, tone, paths[1].join(' / '));
          await page.keyboard.press('Escape');
          const message = page.locator('.chat-message[data-author="person"]').filter({
            has: page.locator('.person-name[data-person-key="wang-ning"]')
          }).first();
          assert.equal(await avatarTone(message), tone);
          await message.locator('.person-name').click();
          await checkCard(card, tone, paths[1].join(' / '));
          await page.keyboard.press('Escape');
          await page.locator('[data-chat-action="members"]').click();
          const members = page.getByRole('dialog', { name: '会话成员' });
          await members.locator('.person-name').first().click();
          assert.ok(await card.isVisible());
          await page.keyboard.press('Escape');
          assert.ok(await members.isVisible());

          await go(prototype, 'projects/beichen/tasks');
          const taskOwner = page.locator('.pr-task-person[title="王宁"]').first();
          await taskOwner.waitFor();
          assert.equal(await taskOwner.locator('.person-avatar').count(), 1);
          assert.equal(await avatarTone(taskOwner), tone);
          assert.deepEqual(errors, []);
        } finally {
          await context.close();
        }
      });
    }
  }
});
