import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { extname } from 'node:path';
import { test } from 'node:test';

const require = createRequire(import.meta.url);
const moduleName = process.env.OMNIRELAY_PLAYWRIGHT_MODULE;
const origin = 'https://project.example.test';
const project = { id: 'p-21', name: '联调项目', project_type: '交付', status: 'active' };
const task = { id: 't-99', project_id: project.id, title: '联调验收', status: 'done' };
const root = '/api/v1/projects/' + project.id;
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png' };
const list = (items) => ({ items, total: items.length });

async function fixture(page) {
  const errors = [];
  page.on('pageerror', (err) => errors.push(err.message));
  let deny = false;
  let held;
  let release;
  const group = { id: 'oc_21', kind: 'group', name: '联调群' };
  const run = {
    id: 'r-2',
    business_date: '2026-11-19',
    generated_at: '2026-11-19T01:00:00Z',
    delivery_state: 'not_sent',
  };
  const history = {
    ...run,
    id: 'r-1',
    business_date: '2026-11-18',
    delivery_state: 'success',
    delivery_confirmed_at: '2026-11-18T03:00:00Z',
  };
  await page.route(origin + '/**', async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const respond = (json) => route.fulfill({ json });
    if (path === '/api/v1/me')
      return respond({
        principal: { account_id: 'reader' },
        csrf_token: 'token',
        access: { navigation: { workbench: true }, default_route: '/#/workbench' },
      });
    if (path === '/api/v1/projects')
      return respond({
        items: [project],
        total: 26,
        page: Number(url.searchParams.get('page')),
        page_size: 25,
      });
    if (path.startsWith(root + '/')) {
      const section = path.slice(root.length + 1);
      if (deny) return route.fulfill({ status: 404, json: { error: 'project_access_denied' } });
      const data = {
        overview: {
          workspace: { project },
          milestones: { items: [{ kind: 'milestone', title: '验收节点', task_id: task.id }] },
        },
        members: { members: [{ display_name: '项目成员甲' }] },
        docs: list([{ title: '交付文档', ref: { kind: 'document', source_id: 'd-1' } }]),
        meetings: list([{ title: '交付纪要', resource_type: 'minutes', resource_token: 'm-1' }]),
        tasks: { columns: [{ name: '进行中', items: [], total: 1, next_cursor: 'more' }] },
        reports: {
          ...list([{ person_name: '成员甲', work_date: '2026-11-19', submission_state: 'submitted' }]),
          coverage: { state: 'partial' },
        },
      };
      if (section === 'members' && held) {
        release();
        await held;
      }
      if (section in data) return respond({ project_id: project.id, panel: section, data: data[section] });
      if (section === 'tasks/' + task.id) return respond(task);
      if (section.startsWith('sources/')) return respond(list([task]));
      if (section === 'reports/briefings') return respond(list([run]));
      if (section === 'reports/briefings/r-1') return respond(list([history]));
      if (section === 'feishu/check') {
        assert.equal(route.request().method(), 'POST');
        return respond({
          bot: 'available',
          group: 'member',
          account: 'active',
          action: 'allowed',
          public_scope: 'project_only',
        });
      }
      if (section === 'feishu')
        return respond({
          state: 'available',
          can_check: true,
          target_name: group.name,
          plan: {
            target_chat_id: group.id,
            timezone: 'Asia/Shanghai',
            local_time: '18:00',
            plan_enabled: true,
          },
          briefings: list([history]),
        });
      if (section === 'chat-reader' || section === 'chat-reader/' + group.id) return respond(list([group]));
      if (section === 'chat-reader/' + group.id + '/send-capabilities')
        return respond({ can_send: false, reason: 'send_disabled' });
      if (section === 'chat-reader/sync') return respond(list([]));
      if (section === 'chat-reader/' + group.id + '/messages')
        return respond(
          list([
            {
              id: 'msg1',
              chat_id: group.id,
              sent_at: '2026-11-19T00:00:00Z',
              sender: { name: '甲' },
              type: 'text',
              body: '提交任务',
            },
          ]),
        );
      throw new Error('意外的项目请求 ' + section);
    }
    const asset = path.slice(1) || 'index.html';
    for (const dir of ['web', 'design/prototype']) {
      try {
        return await route.fulfill({
          body: await readFile(new URL('../../' + dir + '/' + asset, import.meta.url)),
          contentType: mime[extname(asset)],
        });
      } catch (err) {
        if (err.code !== 'ENOENT') throw err;
      }
    }
    return route.fulfill({ status: 404 });
  });
  return {
    errors,
    revoke() {
      deny = true;
    },
    holdMembers() {
      let done;
      held = new Promise((resolve) => {
        done = resolve;
      });
      const started = new Promise((resolve) => {
        release = resolve;
      });
      return { started, finish: done };
    },
  };
}
test('项目导航、来源深链、返回恢复与权限状态', { skip: !moduleName }, async (t) => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const width of [1366, 390])
    for (const theme of ['light', 'dark']) {
      const page = await browser.newPage({
        viewport: { width, height: width === 390 ? 844 : 900 },
        hasTouch: width === 390,
        colorScheme: theme,
      });
      const prototype = new URL('../../design/prototype/omni-relay-workbench.html', import.meta.url);
      prototype.searchParams.set('theme', theme);
      prototype.hash = '/projects/beichen/overview';
      await page.goto(prototype.href);
      await page.getByRole('tab', { name: '项目概览' }).waitFor();
      const expectedTabs = await page.getByRole('tab').allTextContents();
      await page.addInitScript(() => {
        const fetchOriginal = window.fetch;
        window.fetch = (path, options) => {
          if (String(path).endsWith('/members'))
            options?.signal?.addEventListener(
              'abort',
              () => {
                window.memberReadAborted = true;
              },
              { once: true },
            );
          return fetchOriginal(path, options);
        };
      });
      const f = await fixture(page);
      const path = '#/projects/p-21/';
      const go = (hash) =>
        page.evaluate((value) => {
          location.hash = value;
        }, hash);
      const visible = (text) => page.getByText(text, { exact: true }).first().waitFor();
      await page.goto(origin + '/?theme=' + theme + '#/projects?q=联调&page=2');
      assert.equal(await page.locator('html').getAttribute('data-theme'), theme);
      await page.getByRole('link', { name: '联调项目' }).click();
      await visible('验收节点');
      assert.deepEqual(await page.getByRole('tab').allTextContents(), expectedTabs);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
      await page.getByText('验收节点').click();
      await visible('联调验收');
      await page.reload();
      await visible('联调验收');
      await page.getByRole('link', { name: '返回项目列表' }).click();
      await visible('联调项目');
      assert.equal(await page.locator('input[name=q]').inputValue(), '联调');
      assert.equal(await page.evaluate(() => document.activeElement.textContent), '联调项目');
      assert.match(page.url(), /page=2/);
      for (const [section, title] of [
        ['docs', '交付文档'],
        ['meetings', '交付纪要'],
      ]) {
        await go(path + section);
        await visible(title);
        await page.getByRole('link', { name: '查看关联任务' }).click();
        await page.getByRole('link', { name: '联调验收' }).click();
        await visible('任务说明');
      }
      await go(path + 'chats/oc_21');
      await visible('提交任务');
      await page.getByRole('link', { name: '查看关联任务' }).click();
      await visible('联调验收');
      await go(path + 'brief');
      await visible('尚未发送');
      assert.equal(new URL(page.url()).hash, path + 'reports/brief');
      await visible('已生成');
      await go(path + 'reports/brief/r-1');
      await visible('已送达');
      await go(path + 'reports');
      await visible('部分数据暂不可用。');
      await visible('已提交');
      await go(path + 'tasks');
      await page.getByRole('link', { name: '列表', exact: true }).click();
      await visible('暂无任务。');
      await go(path + 'members');
      await visible('项目成员甲');
      const delayed = f.holdMembers();
      await go(path + 'members?q=甲');
      await delayed.started;
      await visible('正在加载…');
      await go(path + 'feishu');
      await visible('已送达');
      assert.equal(await page.evaluate(() => window.memberReadAborted), true);
      delayed.finish();
      await visible('联调群');
      await visible('18:00 · Asia/Shanghai');
      await page.getByRole('button', { name: '接入验证' }).click();
      await visible('账号与机器人均已入群');
      f.revoke();
      await go(path + 'overview');
      await visible('当前项目或来源已不可访问。');
      assert.equal(await page.getByText('联调群', { exact: true }).count(), 0);
      assert.deepEqual(f.errors, []);
      await page.close();
    }
});
