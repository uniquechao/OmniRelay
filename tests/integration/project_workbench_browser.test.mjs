import { browserAsset } from './browser-assets.mjs';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { test } from 'node:test';

const require = createRequire(import.meta.url);
const moduleName = process.env.OMNIRELAY_PLAYWRIGHT_MODULE;
const origin = 'https://project.example.test';
const project = { id: 'p-21', name: '联调项目', project_type: '交付', status: 'active' };
const task = { id: 't-99', project_id: project.id, title: '联调验收', status: 'done' };
const root = '/api/v1/projects/' + project.id;
const list = (items) => ({ items, total: items.length });

function localFiles(id, title, partial = false) {
  const row = { id:'document:d-1', local_source_id:'d-1', name:title, format:'项目文档', format_key:'generic',
    category:'files', sync_state:'local', project_ids:[id], sources:[] };
  return {...list([row]), projects:{[id]:id}, partial};
}

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
    if (path === '/api/v1/projects' && url.searchParams.get('page_size') === '100')
      return respond(list([project]));
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
      if (section === 'files') return respond(localFiles(project.id, '交付文档'));
      if (section === 'files/documents/d-1') return respond({title:'交付文档', text:'交付记录正文'});
      const data = {
        overview: {
          workspace: { project },
          milestones: { items: [{ kind: 'milestone', id: 'm1', title: '验收节点', task_id: task.id }] },
        },
        members: { members: [{ display_name: '项目成员甲' }] },
        meetings: list([{ title: '交付纪要', resource_type: 'minutes', resource_token: 'm-1' }]),
        tasks: { owner_member_ids: ['成员甲'], columns: [{ name: '进行中', items: [], total: 1, next_cursor: 'more' }] },
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
      if (section === 'tasks/' + task.id + '/actions') return respond({});
      if (['task-tags','milestones',...['activity','update-suggestions','evidence'].map(name=>'tasks/'+task.id+'/'+name)].includes(section)) return respond(list([]));
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
    return route.fulfill(await browserAsset(path));
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
test('项目档案创建、冲突保留草稿与归档', { skip: !moduleName }, async (t) => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const width of [1366, 390]) for (const theme of ['light', 'dark']) {
    const page = await browser.newPage({ viewport: { width, height: 844 }, colorScheme: theme, hasTouch: width === 390 });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    const id = 'p-created';
    let item;
    let canCreate = false;
    let canManage = true;
    let writes = 0;
    await page.route(origin + '/**', async (route) => {
      const req = route.request();
      const url = new URL(req.url());
      const reply = (json, status = 200) => route.fulfill({ json, status });
      if (url.pathname === '/api/v1/me') return reply({
        principal: { account_id: 'manager' }, csrf_token: 'token',
        access: { navigation: { workbench: true }, default_route: '/#/workbench' },
      });
      if (url.pathname === '/api/v1/projects') {
        if (req.method() === 'GET') return reply({ ...list(item ? [item] : []), can_create: canCreate });
        const values = req.postDataJSON();
        assert.match(req.headers()['idempotency-key'], /^[a-f\d-]{36}$/);
        assert.equal(req.headers()['x-csrf-token'], 'token');
        assert.equal(values.status, 'planned');
        assert.equal(values.business_timezone, 'Asia/Shanghai');
        assert.equal(values.planned_start, '2026-11-22');
        item = { ...values, id, version: 1, board_version: 1 };
        return reply({ project_id: id, version: 1 });
      }
      const prefix = '/api/v1/projects/' + id + '/';
      if (url.pathname === prefix + 'overview') return reply({ data: {
        workspace: { project: item, can_manage_profile: canManage && item.status !== 'archived' }, milestones: { items: [] },
      }});
      if (url.pathname === prefix + 'members') return reply({ data: { members: [] } });
      if (url.pathname === prefix + 'profile') {
        const values = req.postDataJSON();
        assert.equal(values.name, '已编辑项目');
        if (++writes === 1) {
          assert.equal(values.expected_version, 1);
          item = { ...item, version: 2, background: '\n同事更新的背景' };
          return reply({ error: 'project_state_changed' }, 409);
        }
        assert.equal(values.expected_version, 2);
        assert.equal(values.background, '\n同事更新的背景');
        item = { ...item, ...values, version: 3 };
        return reply({ project_id: id, version: 3 });
      }
      if (url.pathname === prefix + 'archive') {
        assert.deepEqual(req.postDataJSON(), { expected_version: 3 });
        item = { ...item, status: 'archived', version: 4 };
        return reply({ project_id: id, version: 4 });
      }
      return route.fulfill(await browserAsset(url.pathname));
    });
    try {
      const prototype = new URL('../../design/prototype/omni-relay-workbench.html', import.meta.url);
      prototype.searchParams.set('theme', theme);
      prototype.hash = '/projects/beichen/overview';
      await page.goto(prototype.href);
      await page.getByRole('button', { name: '项目设置' }).click();
      const referenceWidth = (await page.getByRole('dialog').boundingBox()).width;

      const directory = origin + '/?theme=' + theme + '#/projects';
      await page.goto(directory);
      const create = page.getByRole('button', { name: '新建项目' });
      await page.getByText('没有符合条件的项目。').waitFor();
      assert.equal(await create.count(), 0);
      canCreate = true;
      await page.reload();
      await create.click();
      const drawer = page.getByRole('dialog');
      assert.equal(await page.locator('html').getAttribute('data-theme'), theme);
      assert.ok(Math.abs((await drawer.boundingBox()).width - referenceWidth) <= 1);
      assert.ok(await drawer.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
      const name = drawer.getByRole('textbox', { name: '项目名称' });
      await name.fill('新建项目试点');
      await drawer.getByLabel('计划开始日期').fill('2026-11-22');
      const end = drawer.getByLabel('计划结束日期');
      await end.fill('2026-11-21');
      await drawer.getByRole('button', { name: '新建项目' }).click();
      await drawer.getByText('计划结束日期应晚于或等于开始日期。').waitFor();
      await end.fill('2026-12-01');
      await drawer.getByRole('button', { name: '新建项目' }).click();
      await page.getByRole('heading', { name: '新建项目试点' }).waitFor();
      const settings = page.getByRole('button', { name: '项目设置' });
      canManage = false;
      await page.reload();
      await page.getByRole('heading', { name: '新建项目试点' }).waitFor();
      assert.equal(await settings.count(), 0);
      canManage = true;
      await page.reload();
      await settings.click();
      await name.fill('已编辑项目');
      await page.keyboard.press('Escape');
      await page.getByRole('button', { name: '继续编辑' }).click();
      assert.equal(await name.inputValue(), '已编辑项目');
      await page.evaluate(() => { location.hash = '#/projects/p-created/members'; });
      await page.getByRole('button', { name: '继续编辑' }).click();
      await page.waitForURL(/#\/projects\/p-created\/overview$/);
      assert.equal(await name.inputValue(), '已编辑项目');
      await drawer.getByRole('button', { name: '保存设置' }).click();
      await drawer.getByRole('button', { name: '读取最新档案' }).click();
      await drawer.getByRole('heading', { name: '最新档案' }).waitFor();
      assert.equal(await name.inputValue(), '已编辑项目');
      assert.equal(await drawer.getByRole('textbox', { name: '项目背景' }).inputValue(), '\n同事更新的背景');
      await drawer.getByRole('button', { name: '保存设置' }).click();
      await page.getByRole('heading', { name: '已编辑项目' }).waitFor();
      await settings.click();
      const archive = page.getByRole('button', { name: '归档项目', exact: true });
      await archive.click();
      const confirm = page.getByRole('dialog', { name: '归档项目？' });
      await confirm.getByRole('button', { name: '取消', exact: true }).click();
      assert.equal(item.status, 'planned');
      await archive.click();
      await confirm.getByRole('button', { name: '确认归档' }).click();
      await page.getByText('已归档', { exact: true }).waitFor();
      assert.equal(await settings.count(), 0);
      await page.getByRole('link', { name: '返回项目列表' }).click();
      await page.getByRole('link', { name: '已编辑项目' }).waitFor();
      await page.getByRole('cell', { name: '已归档', exact: true }).waitFor();
      assert.deepEqual(errors, []);
    } finally {
      await page.close();
    }
  }
});

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
      prototype.hash = '/projects/beichen/tasks/1';
      await page.goto(prototype.href);
      const inspector=page.locator('.or-task-inspector');
      await inspector.waitFor();
      const referenceLayout=await inspector.locator('.task-edit-grid').evaluate(el=>getComputedStyle(el).display);
      const referenceBody=await inspector.locator('.task-edit-copy').evaluate(el=>getComputedStyle(el).padding);
      if(process.env.OMNIRELAY_TASK_EVIDENCE)await inspector.screenshot({path:process.env.OMNIRELAY_TASK_EVIDENCE+'/原型-'+width+'-'+theme+'.png'});
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
      await page.getByLabel('标题', { exact:true }).waitFor();
      await page.reload();
      await page.getByLabel('标题', { exact:true }).waitFor();
      assert.equal(await page.getByLabel('标题', { exact:true }).inputValue(), task.title);
      assert.equal(await inspector.locator('.task-edit-grid').evaluate(el=>getComputedStyle(el).display),referenceLayout);
      assert.equal(await inspector.locator('.task-edit-copy').evaluate(el=>getComputedStyle(el).padding),referenceBody);
      assert.ok(await inspector.evaluate(el=>el.scrollWidth<=el.clientWidth+1));
      if(process.env.OMNIRELAY_TASK_EVIDENCE)await inspector.screenshot({path:process.env.OMNIRELAY_TASK_EVIDENCE+'/正式-'+width+'-'+theme+'.png'});
      await page.getByRole('link', { name: '返回项目列表' }).click();
      await page.getByRole('link', {name:'联调项目',exact:true}).waitFor();
      assert.equal(await page.locator('input[name=q]').inputValue(), '联调');
      assert.equal(await page.evaluate(() => document.activeElement.textContent), '联调项目');
      assert.match(page.url(), /page=2/);
      for (const [section, title] of [
        ['docs', '交付文档'],
        ['meetings', '交付纪要'],
      ]) {
        await go(path + section);
        await visible(title);
        if (section === 'docs') {
          assert.deepEqual(await page.locator('[data-drive-category]').allTextContents(),
            ['全部','会议纪要','飞书文档','任务表格（多维表格）','图片','视频','文件']);
          await page.getByRole('button', { name:'预览', exact:true }).click();
          await page.getByRole('dialog').getByText('交付记录正文').waitFor();
          await page.keyboard.press('Escape');
          const download = page.waitForEvent('download');
          await page.getByRole('button',{name:'下载',exact:true}).click();
          assert.equal((await download).suggestedFilename(),'交付文档.txt');
          assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
        }
        await page.getByRole('link', { name: '查看关联任务' }).click();
        await page.getByRole('link', { name: '联调验收' }).click();
        await page.getByLabel('正文', { exact:true }).waitFor();
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
      await page.getByRole('searchbox').fill('验收');
      await page.getByLabel('筛选负责人').selectOption('成员甲');
      await page.getByLabel('筛选里程碑').selectOption('m1');
      const filtered = page.waitForRequest((r) => {
        const u = new URL(r.url());
        return u.pathname === root + '/tasks' && u.searchParams.get('q') === '验收' &&
          u.searchParams.get('owner_member_id') === '成员甲' && u.searchParams.get('milestone_id') === 'm1';
      });
      await page.getByRole('button', {name:'查询'}).click();
      await filtered;
      await page.getByRole('link', {name:'看板', exact:true}).click();
      await page.getByLabel('筛选里程碑').waitFor();
      assert.equal(await page.getByLabel('筛选里程碑').inputValue(), 'm1');
      await page.getByLabel('筛选里程碑').selectOption('@none');
      const unassigned = page.waitForRequest((r) => {
        const u = new URL(r.url());
        return u.pathname === root + '/tasks' && u.searchParams.get('milestone_unassigned') === 'true' &&
          !u.searchParams.has('milestone_id') && u.searchParams.get('owner_member_id') === '成员甲';
      });
      await page.getByRole('button', {name:'查询'}).click();
      await unassigned;
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

test('任务四列样式、移动回执与恢复', { skip: !moduleName }, async (t) => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const stages = ['todo', 'in_progress', 'blocked', 'done'];
  const names = ['待开始', '进行中', '受阻', '已完成'];
  for (const width of [1366, 390]) for (const theme of ['light', 'dark']) {
    const page = await browser.newPage({ viewport: { width, height: 844 }, colorScheme: theme });
    await fixture(page);
    const rows = stages.map((column_id, i) => ({
      id: 'task-' + i, title: '交付验收'.repeat(28), column_id, status: column_id, version: 1,
      owner_member_id: 'reader', priority: 'P' + i, priority_label: ['紧急', '重要', '普通', '低'][i],
      milestone_id: 'm1', due_at: ['2026-11-18','2026-11-19','2026-11-22','2026-11-19'][i],
      due_label: ['11/18','11/19','11/22','11/19'][i], due_alert: i < 3,
      due_alert_label: ['已逾期','今日到期','临期',''][i],
      blocked_reason: i === 2 ? '等待评审' : '', tag_ids: ['评审', '交付'],
    }));
    let version = 1, canMove = true, failureStatus;
    await page.route(origin + root + '/overview', route => route.fulfill({ json: { data: {
      workspace: { project },
      milestones: { items: rows.map(row => ({ kind: 'milestone', id: 'm-' + row.id,
        task_id: row.id, title: '节点' + row.id, task: row })) },
    } } }));
    await page.route(origin + root + '/members', route => route.fulfill({
      json: { data: { members: [{ member_id: 'reader', display_name: '李明' }] } },
    }));
    await page.route(origin + root + '/tasks', route => route.fulfill({ json: { data: {
      version, can_move: canMove, owner_member_ids: ['reader'],
      columns: stages.map((id, i) => {
        const items = rows.filter(row => row.column_id === id);
        return { id, name: names[i], items, total: items.length };
      }),
    } } }));
    await page.route(origin + root + '/tasks/task-0/move', route => {
      const command = route.request().postDataJSON();
      assert.equal(command.expected_task_version, rows[0].version);
      assert.equal(command.expected_board_version, version);
      assert.ok(command.request_key);
      if (failureStatus) return route.fulfill({ status: failureStatus, json: {} });
      version++;
      Object.assign(rows[0], { column_id: command.target_column_id, status: command.target_column_id,
        version, blocked_reason: command.blocked_reason });
      return route.fulfill({ json: rows[0] });
    });
    const prototype = new URL('../../design/prototype/omni-relay-workbench.html', import.meta.url);
    prototype.searchParams.set('theme', theme);
    prototype.hash = '/projects/beichen/tasks';
    await page.goto(prototype.href);
    const colors = cards => cards.evaluateAll(els => Object.fromEntries(els.map(el => {
      const style = getComputedStyle(el);
      return [el.dataset.priority, [style.backgroundColor, style.borderColor]];
    })));
    await page.locator('.pr-task').first().waitFor();
    const expectedColors = await colors(page.locator('.pr-task'));
    const warningColor = await page.locator('.pr-task [data-deadline-alert]').first().evaluate(el => getComputedStyle(el).color);
    await page.goto(origin + '/?theme=' + theme + '#/projects/p-21/tasks');
    const card = page.locator('[data-task-id="task-0"] a');
    await card.waitFor();
    assert.equal(await page.locator('.pr-lane').count(), 4);
    const actualColors = await colors(page.locator('.pr-task'));
    assert.equal(new Set(Object.values(actualColors).map(String)).size, 4);
    for (const [priority, pair] of Object.entries(expectedColors)) assert.deepEqual(actualColors[priority], pair);
    assert.ok(await page.locator('.pr-task-title').evaluateAll(titles => titles.every(el =>
      el.scrollWidth <= el.clientWidth && el.offsetHeight <= 2 * parseFloat(getComputedStyle(el).lineHeight) + 1)));
    assert.equal(await card.locator('.pr-task-person[title="李明"] .person-avatar').count(), 1);
    assert.equal(await page.locator('[data-column="done"] [data-deadline-alert]').count(), 0);
    assert.equal(await page.locator('.pr-task [data-deadline-alert]').count(), 3);
    for (const row of rows) {
      const date = page.locator('[data-task-id="' + row.id + '"] time');
      assert.equal(await date.textContent(), (row.due_alert ? row.due_alert_label + ' · ' : '') + row.due_label);
      assert.equal(await date.locator('use[href="#status-warning"]').count(), Number(row.due_alert));
      if (row.due_alert) assert.equal(await date.evaluate(el => getComputedStyle(el).color), warningColor);
      assert.equal(await date.getAttribute('title'), [row.due_alert_label, row.due_at].filter(Boolean).join(' · '));
    }
    const href = await card.getAttribute('href');
    const dates = page.locator('[data-deadline-alert]');
    const boardWarnings = await dates.allTextContents();
    await page.getByRole('tab', { name: '项目概览' }).click();
    await page.getByRole('link', { name: '节点task-0' }).waitFor();
    assert.deepEqual(await dates.allTextContents(), boardWarnings);
    await page.getByRole('tab', { name: '任务', exact: true }).click();
    await page.getByRole('link', { name: '列表', exact: true }).click();
    await page.getByRole('table').waitFor();
    assert.deepEqual(await dates.allTextContents(), boardWarnings);
    await page.getByRole('link', { name: '看板', exact: true }).click();
    await card.waitFor();
    if (width === 1366) await card.dragTo(page.locator('[data-column="in_progress"] h3'));
    else await card.press('Alt+ArrowRight');
    await page.locator('[data-column="in_progress"] [data-task-id="task-0"]').waitFor();
    if (width === 1366) await card.dragTo(page.locator('[data-column="blocked"] h3'));
    else await card.press('Alt+ArrowRight');
    await page.getByLabel('受阻原因').waitFor();
    await page.getByRole('button', { name: '取消', exact: true }).click();
    assert.equal(rows[0].column_id, 'in_progress');
    await card.press('Alt+ArrowRight');
    await page.getByLabel('受阻原因').fill('等待复核');
    await page.getByRole('button', { name: '保存', exact: true }).click();
    await card.getByText('等待复核').waitFor();
    assert.equal(await card.getAttribute('href'), href);
    await page.getByRole('link', { name: '列表', exact: true }).click();
    const select = page.locator('[data-move-task="task-0"]');
    await select.waitFor();
    assert.equal(await select.inputValue(), 'blocked');
    for (const status of [409, 404]) {
      failureStatus = status;
      await select.selectOption('done');
      const retry = page.getByRole('button', { name: '重新加载' });
      await retry.waitFor();
      assert.equal(await page.locator('[data-move-task]').count(), 0);
      canMove = status === 409;
      await retry.click();
      await page.getByRole('table').waitFor();
      const restored = page.locator('tbody tr').filter({ has: page.locator('a[href="' + href + '"]') });
      assert.equal(await restored.count(), 1);
      assert.equal(await page.locator('tbody tr').count(), 4);
      assert.equal(canMove ? await restored.locator('select').inputValue() : await restored.locator('td').nth(1).innerText(),
        canMove ? 'blocked' : '受阻');
    }
    assert.equal(await page.locator('[data-move-task]').count(), 0);
    await page.close();
  }
});

test('任务字段、条件校验、草稿、冲突及活动', {skip: !moduleName}, async t=>{
  const browser=await require(moduleName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(()=>browser.close());
  const page=await browser.newPage();
  page.setDefaultTimeout(5000);
  const {errors}=await fixture(page);
  let current={...task,status:'in_progress',version:1,description:'初始正文',participant_ids:[],tag_ids:[]};
  let readonly=false;
  const events=[],suggestions=[];
  const endpoint=origin+root+'/tasks/'+task.id;
  const detail='#/projects/'+project.id+'/tasks/'+task.id;
  await page.route(origin+root+'/members',r=>r.fulfill({json:{data:{members:[{id:'m1',display_name:'王宁'}]}}}));
  await page.route(endpoint+'**',async r=>{
    const {pathname,searchParams}=new URL(r.request().url());
    const path=pathname.slice(new URL(endpoint).pathname.length);
    const body=r.request().postDataJSON();
    let result;
    switch(path) {
      case '':
        if(body){
          if(body.expected_task_version!==current.version)return r.fulfill({status:409,json:{error:'task_version_conflict'}});
          current={...current,...body,version:current.version+1};
        }
        result=current;break;
      case '/actions':result={edit_fields:!readonly,update_progress:!readonly,comment:!readonly,review_feedback:!readonly};break;
      case '/activity':result={items:searchParams.has('cursor')?[{id:8,kind:'change',actor_name:'审核人',before:{标题:'旧'},after:{标题:'新'}}]:events,next_cursor:searchParams.has('cursor')?null:'续页'};break;
      case '/evidence':result={items:[{accessible:false,reason:'当前账号无权读取原始依据。'}]};break;
      case '/comments':events.unshift({id:1,kind:'comment',body:body.body,actor_name:'王宁'});result=events[0];break;
      case '/update-suggestions':
        if(body)suggestions.push({id:'s1',version:1,status:'pending',actor_id:'m1',...body});
        result={items:suggestions,can_submit:!readonly,can_review:!readonly};break;
      default:throw Error(path);
    }
    await r.fulfill({json:result});
  });
  await page.route(origin+root+'/task-update-suggestions/s1/review',async r=>{
    const body=r.request().postDataJSON();
    assert.equal(body.expected_version,1);
    suggestions[0].status='rejected';suggestions[0].reason=body.reason;
    await r.fulfill({json:suggestions[0]});
  });
  const chunk = '**/assets/task-detail-*.js';
  await page.route(chunk, route => route.fulfill({status:404}));
  await page.goto(origin+'/'+detail);
  const reload = page.getByRole('button',{name:'刷新页面'});
  await reload.waitFor();
  await page.unroute(chunk);
  await reload.click();
  const byName=k=>page.locator('[name="'+k+'"]'), save=page.getByRole('button',{name:'保存任务'});
  await save.waitFor();
  assert.ok(await save.isDisabled());
  await byName('title').fill('  ');await save.click();
  assert.equal(await page.evaluate(()=>document.activeElement.name),'title');
  await byName('title').fill('本地草稿');
  await byName('owner_member_id').fill('wangning');await save.click();
  assert.equal(await page.evaluate(()=>document.activeElement.name),'owner_member_id');
  await byName('owner_member_id').fill('wn');await page.getByRole('button',{name:'王宁 · m1'}).click();
  await byName('start_at').fill('2026-11-19');await byName('due_at').fill('2026-11-18');await save.click();
  assert.equal(await page.evaluate(()=>document.activeElement.name),'due_at');
  await byName('due_at').fill('2026-11-20');await byName('status').selectOption('blocked');await save.click();
  assert.equal(await page.evaluate(()=>document.activeElement.name),'blocked_reason');
  await byName('blocked_reason').fill('等待审核');await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'继续编辑'}).click();
  assert.equal(await byName('title').inputValue(),'本地草稿');
  current={...current,description:'另一位成员补充的正文',version:2};
  await save.click();await page.getByRole('button',{name:'应用比对结果'}).click();
  assert.equal(await byName('description').inputValue(),current.description);
  assert.equal(await byName('title').inputValue(),'本地草稿');await save.click();
  await page.getByText('任务已保存。',{exact:true}).waitFor();
  assert.equal(current.version,3);assert.equal(current.owner_member_id,'m1');assert.ok(page.url().endsWith(detail));
  await page.getByText('当前账号无权读取原始依据。').waitFor();
  await page.getByRole('button',{name:'加载更多'}).click();await page.getByText('旧 → 新').waitFor();
  await page.getByRole('button',{name:'提交进展反馈'}).click();await byName('note').fill('提请核验');
  await page.getByRole('button',{name:'确认提交'}).click();
  await page.getByRole('button',{name:'拒绝',exact:true}).click();await byName('note').fill('请补充依据');
  await page.getByRole('button',{name:'确认提交'}).click();await page.getByText('审核说明　请补充依据').waitFor();
  await byName('status').selectOption('cancelled');await save.click();
  assert.equal(await page.evaluate(()=>document.activeElement.name),'change_reason');
  await byName('change_reason').fill('计划取消');
  await page.locator('.head').click({position:{x:1,y:1}});
  await page.getByRole('button',{name:'不保存',exact:true}).click();await save.waitFor({state:'detached'});
  assert.equal(current.status,'blocked');
  await page.goto(origin+'/'+detail);await page.getByPlaceholder('留下评论…').fill('保留此条评论');
  await page.getByRole('tab',{name:'成员',exact:true}).click();
  await page.getByRole('button',{name:'保存并关闭'}).click();
  await save.waitFor({state:'detached'});assert.equal(events[0].body,'保留此条评论');
  await page.goto(origin+'/'+detail);
  await byName('status').selectOption('done');
  await byName('completion_note').fill('已完成核验');
  await save.click();await page.getByText('任务已保存。',{exact:true}).waitFor();
  assert.equal(current.status,'done');
  await byName('status').selectOption('in_progress');await save.click();
  assert.equal(await page.evaluate(()=>document.activeElement.name),'change_reason');
  await byName('change_reason').fill('补充复验');
  await page.getByRole('link',{name:'收起任务详情'}).click();
  await page.getByRole('button',{name:'保存并关闭'}).click();await save.waitFor({state:'detached'});
  assert.equal(current.status,'in_progress');assert.equal(current.change_reason,'补充复验');
  readonly=true;await page.goto(origin+'/'+detail);await save.waitFor();
  assert.ok(await byName('title').isDisabled());assert.ok(await save.isDisabled());
  assert.deepEqual(errors,[]);
});

test('两项目分别保留搜索、负责人、里程碑和任务视图', { skip: !moduleName }, async t => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  await fixture(page);
  const samples = [
    { id: project.id, q: '甲', owner: 'u1', milestone: 'm1', view: '列表' },
    { id: 'p-22', q: '乙', owner: 'u2', milestone: '@none', view: '看板' },
  ];
  for (const sample of samples) {
    const api = origin + '/api/v1/projects/' + sample.id;
    await page.route(api + '/overview', r => r.fulfill({ json: { data: {
      workspace: { project: { id: sample.id, name: sample.q } },
      milestones: { items: [{ id: 'm1', kind: 'milestone', title: '交付节点' }] },
    } } }));
    await page.route(api + '/members', r => r.fulfill({ json: { data: { members: [] } } }));
    await page.route(api + '/tasks*', r => {
      const params = new URL(r.request().url()).searchParams;
      const matches = params.get('owner_member_id') === sample.owner && params.get('q') === sample.q &&
        (sample.milestone === '@none' ? params.get('milestone_unassigned') === 'true' : params.get('milestone_id') === sample.milestone);
      const items = matches ? [{ id: 't1', title: sample.q + '验收', column_id: 'todo' }] : [];
      return r.fulfill({ json: { data: { owner_member_ids: [sample.owner],
        columns: [{ id: 'todo', name: '待开始', items, total: items.length }] } } });
    });
  }
  async function enter(sample) {
    await page.evaluate(id => { location.hash = '#/projects/' + id + '/overview'; }, sample.id);
    await page.getByRole('heading', { name: sample.q, exact: true }).waitFor();
    await page.getByRole('tab', { name: '任务', exact: true }).click();
    await page.getByLabel('筛选里程碑').waitFor();
  }
  await page.goto(origin + '/#/projects');
  for (const sample of samples) {
    await enter(sample);
    assert.equal(await page.getByRole('searchbox').inputValue(), '');
    await page.getByRole('searchbox').fill(sample.q);
    await page.getByLabel('筛选负责人').selectOption(sample.owner);
    await page.getByLabel('筛选里程碑').selectOption(sample.milestone);
    await page.getByRole('button', { name: '查询' }).click();
    await page.getByRole('link', { name: sample.q + '验收' }).waitFor();
    await page.getByRole('link', { name: sample.view, exact: true }).click();
  }
  for (const sample of samples) {
    await enter(sample);
    await page.getByRole('link', { name: sample.q + '验收' }).waitFor();
    assert.equal(await page.getByRole('searchbox').inputValue(), sample.q);
    assert.equal(await page.getByLabel('筛选负责人').inputValue(), sample.owner);
    assert.equal(await page.getByLabel('筛选里程碑').inputValue(), sample.milestone);
    assert.equal(await page.getByRole('link', { name: sample.view, exact: true }).getAttribute('aria-current'), 'page');
  }
});

test('切换项目隔离延迟响应、栏目状态和简报回执', { skip: !moduleName }, async (t) => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  // 模拟取消时已在途的响应，让旧项目数据确实晚于新项目到达。
  await page.addInitScript((membersPath) => {
    const fetchOriginal = window.fetch;
    window.fetch = async (input, options) => {
      if (!window.delayFirstMembers || new URL(input, location.origin).pathname !== membersPath)
        return fetchOriginal(input, options);
      const response = await fetchOriginal(input, { ...options, signal: undefined });
      const read = response.json.bind(response);
      response.json = async () => {
        const data = await read();
        setTimeout(() => { window.lateMembers = data; }, 0);
        return data;
      };
      return response;
    };
  }, root + '/members');
  const first = await fixture(page);
  const other = { id: 'p-22', name: '启明项目' };
  const base = '#/projects/' + other.id;
  const api = '/api/v1/projects/' + other.id + '/';
  const run = {
    id: 'r-22', business_date: '2026-11-18', generated_at: '2026-11-18T09:00:00Z',
    delivery_state: 'failed',
  };
  const writes = [];
  page.on('request', (r) => {
    if (r.method() !== 'GET' && new URL(r.url()).pathname.startsWith(api)) writes.push(r.url());
  });
  await page.route(origin + '/api/v1/projects?**', (r) =>
    r.fulfill({ json: { ...list([project, other]), page: 1, page_size: 25 } }));
  await page.route(origin + api + '**', async (route) => {
    const section = new URL(route.request().url()).pathname.slice(api.length);
    const send = (json, status = 200) => route.fulfill({ status, json });
    switch (section) {
      case 'overview':
        return send({ project_id: other.id, data: { workspace: { project: other }, milestones: { items: [] } } });
      case 'members':
        return send({ project_id: other.id, data: { members: [{ display_name: '启明成员' }] } });
      case 'feishu':
        return send({ project_id: other.id, state: 'available', can_check: false, target_name: '启明群',
          plan: { plan_enabled: false }, briefings: list([run]) });
      case 'reports/briefings/r-22':
        return send(list([run]));
      case 'reports/briefings/r-1':
        return send({ error: 'project_access_denied' }, 404);
      case 'files':
        return send(localFiles(other.id, '启明项目档案', true));
      default:
        throw Error('意外的项目请求 ' + section);
    }
  });

  const visible = (name) => page.getByText(name, { exact: true }).first().waitFor();
  const go = (hash) => page.evaluate((target) => { location.hash = target; }, hash);
  await page.goto(origin + '/#/projects');
  await page.getByRole('link', { name: project.name }).click();
  await page.getByRole('tab', { name: '飞书 Bot' }).click();
  await visible('联调群');
  await visible('已送达');
  await page.getByRole('button', { name: '接入验证' }).waitFor();
  await page.evaluate(() => { window.delayFirstMembers = true; });
  const delayed = first.holdMembers();
  await page.getByRole('tab', { name: '成员' }).click();
  await delayed.started;
  try {
    await page.getByRole('link', { name: '返回项目列表' }).click();
    await page.getByRole('link', { name: other.name }).click();
    await page.getByRole('tab', { name: '成员' }).click();
    await visible('启明成员');
  } finally {
    delayed.finish();
  }
  await page.waitForFunction(() => window.lateMembers?.data.members[0].display_name === '项目成员甲');
  await visible('启明成员');
  assert.equal(await page.getByText('项目成员甲', { exact: true }).count(), 0);
  assert.equal(await page.locator('#page-heading').innerText(), other.name);

  await page.getByRole('tab', { name: '飞书 Bot' }).click();
  await visible('启明群');
  await visible('已生成');
  await visible('投递失败');
  assert.equal(await page.getByRole('button', { name: '接入验证' }).count(), 0);
  assert.doesNotMatch(await page.locator('#page').innerText(), /联调群|项目成员甲|已送达/);
  await go(base + '/brief/r-22');
  await page.waitForURL(url => url.hash === base + '/reports/brief/r-22');
  await visible('投递失败');
  run.delivery_state = 'success';
  await page.reload();
  await visible('送达待确认');
  await go(base + '/brief/r-1');
  await visible('当前项目或来源已不可访问。');
  await page.getByRole('tab', { name: '文件' }).click();
  await visible('部分数据暂不可用。');
  await visible('启明项目档案');
  assert.doesNotMatch(await page.locator('#page').innerText(), /暂无记录|交付文档|启明群|已送达/);
  assert.deepEqual(writes, []);
  assert.deepEqual(first.errors, []);
});

test('项目文件分类、筛选、来源和版本操作保留项目范围', {skip:!moduleName}, async t => {
  const browser = await require(moduleName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage({viewport:{width:390,height:844}});
  page.setDefaultTimeout(5000);
  await fixture(page);
  const row = {id:17, name:'现场照片.png', format_key:'image', format:'图片', category:'files', version_id:2,
    shared_at:'2026-11-21T12:00:00Z', sync_state:'complete', project_ids:[project.id],
    sources:[{project_id:project.id, chat_id:'oc_21', message_id:'msg1', chat_name:'联调群', sender_name:'成员甲'}]};
  let search;
  await page.route(origin + root + '/files?**', route => {
    search = new URL(route.request().url()).searchParams;
    const items = ['all','image'].includes(search.get('category')) && row.name.includes(search.get('q') || '') ? [row] : [];
    return route.fulfill({json:{...list(items), projects:{[project.id]:project.name}}});
  });
  const file = '/api/v1/feishu/files/17/versions';
  await page.route(origin + file + '**', route => {
    const url = new URL(route.request().url());
    assert.equal(url.searchParams.get('source_project'),project.id);
    return url.pathname === file ? route.fulfill({json:{items:[{id:2}]}}) :
      route.fulfill({path:'design/prototype/omni-relay-logo.png',contentType:'image/png'});
  });
  await page.goto(origin + '/#/projects/' + project.id + '/docs');
  await page.getByText(row.name,{exact:true}).waitFor();
  assert.equal(search.get('sort'),'shared_at');
  assert.equal(await page.locator('th[aria-sort="descending"] [data-sort]').getAttribute('data-sort'),'shared_at');
  const source = page.getByRole('link',{name:'定位原消息：联调群（来源 1）'});
  assert.equal(await source.getAttribute('href'),'#/projects/p-21/chats/oc_21/msg1');
  assert.equal(await source.locator('xpath=ancestor::td').innerText(),'联调群');
  await page.getByRole('button',{name:'视频',exact:true}).click();
  await page.getByText('暂无文件。').waitFor();
  await page.getByRole('button',{name:'图片',exact:true}).click();
  await page.getByRole('searchbox',{name:'搜索文件'}).fill('现场');
  await page.getByRole('button',{name:'筛选',exact:true}).click();
  await page.getByLabel('同步状态',{exact:true}).selectOption('complete');
  await page.keyboard.press('Escape');
  await page.getByText(row.name,{exact:true}).waitFor();
  assert.equal(search.get('category'),'image');
  assert.equal(search.get('q'),'现场');
  assert.equal(search.get('sync_state'),'complete');
  assert.equal(await page.locator('[data-filter="project_id"]').count(),0);
  await page.getByRole('button',{name:'预览',exact:true}).click();
  await page.getByRole('dialog').getByRole('img',{name:row.name,exact:true}).evaluate(img => img.decode());
  await page.keyboard.press('Escape');
  const download = page.waitForEvent('download');
  await page.getByRole('link',{name:'下载',exact:true}).click();
  assert.equal(new URL((await download).url()).searchParams.get('source_project'),project.id);
});
