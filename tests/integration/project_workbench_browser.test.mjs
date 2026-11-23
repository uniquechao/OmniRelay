import { browserAsset } from './browser-assets.mjs';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { test } from 'node:test';

const require = createRequire(import.meta.url);
const moduleName = process.env.OMNIRELAY_PLAYWRIGHT_MODULE;
const origin = 'https://project.example.test';
const project = { version: 1, board_version: 1, business_timezone: 'Asia/Shanghai', id: 'p-21', short_code:'LDX', name: '联调项目', project_type: '交付', status: 'active', updated_at: '2026-11-21T09:15:00Z' };
const task = { task_code: 'LDX-T001', version: 1, description: '', acceptance: '', owner_member_id: null, participant_ids: [], priority: null, due_at: null, tag_ids: [], blocked_reason: '', completion_note: '', overdue: false, created_at: '2026-11-20T01:00:00Z', updated_at: '2026-11-20T01:00:00Z', created_by: 'reader', updated_by: 'reader', id: 't-99', project_id: project.id, title: '联调验收', status: 'done' };
const root = '/api/v1/projects/' + project.id;
const list = (items) => ({ items, total: items.length });
const projectTypes = [{ name: '研发', tone: 'blue' }, { name: '销售', tone: 'purple' }, { name: '售后', tone: 'amber' }];

const driveFileDefaults = {
  project_version:1, manual_project_ids:[], inherited_project_ids:[], version_id:null,
  updated_at:null, size_bytes:null, size_source:'unknown'
};

const directoryMember = {id:'成员甲', account_id:'reader', display_name:'项目成员甲', role:'reader', project_role:'project_member', version:1};
const directory = {members:[directoryMember], project_roles:[], can_manage:false, can_delegate:false};
function boardResponse(projectId, rows = []) {
  return {project_id:projectId, default_column_id:'todo', version:1, total:rows.length, tags:[],
    member_id:'reader', can_create:false, can_comment:false, can_configure:false, can_assign:false,
    business_timezone:'Asia/Shanghai', columns:['todo','in_progress','blocked','done'].map((stage, i) => {
      const items = rows.filter(item => item.status === stage);
      return {id:stage, stage, name:['待开始','进行中','受阻','已完成'][i], color:'#708090',
        total:items.length, items, next_cursor:null};
    })};
}

function localFiles(id, title, partial = false) {
  const row = { ...driveFileDefaults,id:'document:d-1', local_source_id:'d-1', name:title, format:'项目文档', format_key:'generic',
    category:'files', sync_state:'local', project_ids:[id], sources:[] };
  return {...list([row]), projects:{[id]:id}, partial};
}

async function fixture(page) {
  const errors = [];
  const botMessages = [];
  page.on('pageerror', (err) => errors.push(err.message));
  let deny = false;
  let followed = false;
  let held;
  let release;
  const group = { id: 'oc_21', kind: 'group', name: '联调群', last_message_at:'2026-11-19T00:00:00Z' };
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
    if (path === '/api/v1/me/followed-projects') return respond(list(followed && !deny ? [project] : []));
    if (path === '/api/v1/projects' && url.searchParams.get('page_size') === '100')
      return respond(list([project]));
    if (path === '/api/v1/projects')
      return respond({
        can_create:false, project_types:projectTypes, items: [{...project,followed}],
        total: 26,
        page: Number(url.searchParams.get('page')),
        page_size: 25,
      });
    if (path.startsWith(root + '/')) {
      const section = path.slice(root.length + 1);
      if (deny) return route.fulfill({ status: 404, json: { error: 'project_access_denied' } });
      if (section === 'follow') {
        assert.equal(route.request().headers()['x-csrf-token'], 'token');
        assert.ok(['PUT','DELETE'].includes(route.request().method()));
        followed = route.request().method() === 'PUT';
        return respond({followed});
      }
      if (section === 'files') return respond(localFiles(project.id, '交付文档'));
      if (section === 'files/documents/d-1') return respond({title:'交付文档', text:'交付记录正文'});
      const data = {
        overview: {
          workspace: { project: {...project,followed}, project_types:projectTypes, can_manage_profile:false },
          milestones: { items: [{ kind: 'milestone', id: 'm1', title: '验收节点', task_id: task.id }] },
        },
        members: directory,
        meetings: list([{title:'交付纪要',kind:'minutes',resource_token:'m-1',category:'minutes',created_at:'2026-11-19T00:00:00Z'}]),
        reports: {
          ...list([{ person_name: '成员甲', work_date: '2026-11-19', submission_state: 'submitted' }]),
          coverage: { state: 'partial' },
        },
      };
      if (section === 'task-candidates') return respond({items:[],next_cursor:null,total:0});
      if (section === 'board') return respond(boardResponse(project.id));
      if (section === 'task-candidates') return respond({items:[],total:0,next_cursor:null});
      if (section === 'tasks/query') return respond({items:[],total:0,next_cursor:null});
      if (section === 'tasks/'+task.id+'/activity') return respond({items:[], next_cursor:null});
      if (section === 'tasks/'+task.id+'/update-suggestions') return respond({items:[], can_submit:false});
      if (section === 'members' && held) {
        release();
        await held;
      }
      if (section in data) return respond({ project_id: project.id, panel: section, data: data[section] });
      if (section === 'tasks/' + task.id) return respond(task);
      if (section === 'tasks/' + task.id + '/evidence') return respond({items:[]});
      if (section.startsWith('sources/')) return respond(list([task]));
      if (section === 'reports/briefings') return respond(list([run]));
      if (section === 'reports/briefings/r-1') return respond(list([history]));
      if (section === 'feishu/check') {
        assert.equal(route.request().method(), 'POST');
        return respond({
          passed:true,
          bot: 'available',
          group: 'member',
          account: 'active',
          action: 'allowed',
          public_scope: 'project_only',
        });
      }
      if (section === 'bot/messages') {
        if (route.request().method() === 'POST') {
          assert.deepEqual(route.request().postDataJSON(), {text:'你好，项目 PM'});
          assert.equal(route.request().headers()['x-csrf-token'], 'token');
          assert.ok(route.request().headers()['idempotency-key']);
          botMessages.push({id:'one', question:'你好，项目 PM', answer:'项目 PM · 联调项目\n你好，已收到。', state:'sent'});
          return route.fulfill({status:202, json:{id:'one'}});
        }
        return respond({items:botMessages});
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
      if (section === 'chat-reader/sync') return respond({...list([]),scope:'current_memberships_and_own_private_chats'});
      if (section === 'chat-reader/' + group.id + '/messages')
        return respond(
          list([
            {
              id: 'msg1',
              chat_id: group.id,
              sent_at: '2026-11-19T00:00:00Z',
              sender: { name: '甲' },
              type: 'text',
              state:'available', display_text:'提交任务', body: JSON.stringify({text:'提交任务'}),
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
test('关注项目后可以从主导航打开详情，取消关注后入口消失', { skip: !moduleName }, async (t) => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const width of [1366,390]) for (const theme of ['light','dark']) {
    const page = await browser.newPage({viewport:{width,height:844},colorScheme:theme});
    try {
      const f = await fixture(page);
      // A directory action can finish while the first sidebar read is still in flight.
      let releaseInitial;
      const held = new Promise(resolve => {releaseInitial=resolve;});
      await page.route(origin+'/api/v1/me/followed-projects', async route => {
        await held;
        await route.fulfill({json:list([])});
      }, {times:1});
      await page.goto(origin + '/?theme='+theme+'#/projects');
      const toggle = page.getByRole('button',{name:'关注项目 '+project.name});
      const shortcut = page.locator('.rail').getByRole('link',{name:project.name});
      const mobile = page.locator('#mobile-nav');
      const option = mobile.locator('optgroup[label="已关注项目"] option');
      await toggle.click();
      await option.waitFor({state:'attached'});
      releaseInitial();
      await page.reload();
      await page.getByRole('button',{name:'关注项目 '+project.name,pressed:true}).waitFor();
      if (await mobile.isVisible()) await mobile.selectOption({label:project.name});
      else await shortcut.click();
      await page.getByRole('heading',{name:project.name,exact:true}).waitFor();
      assert.equal(new URL(page.url()).hash,'#/projects/'+project.id+'/overview');
      await toggle.click();
      await option.waitFor({state:'detached'});
      assert.equal(await shortcut.count(),0);
      assert.equal(await toggle.getAttribute('aria-pressed'),'false');
      await toggle.click();
      await option.waitFor({state:'attached'});
      f.revoke();
      await page.reload();
      await page.getByRole('button',{name:'重新加载'}).waitFor();
      assert.equal(await option.count(),0);
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
      assert.deepEqual(f.errors,[]);
    } finally {
      await page.close();
    }
  }
});

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
        if (req.method() === 'GET') {
          const status = url.searchParams.get('status');
          return reply({ ...list(item && (!status || item.status === status) ? [item] : []), can_create: canCreate, project_types: projectTypes });
        }
        const values = req.postDataJSON();
        assert.match(req.headers()['idempotency-key'], /^[a-f\d-]{36}$/);
        assert.equal(req.headers()['x-csrf-token'], 'token');
        assert.equal(values.status, 'planned');
        assert.equal(values.project_type, '研发');
        assert.equal(values.business_timezone, 'Asia/Shanghai');
        assert.equal(values.planned_start, '2026-11-22');
        item = { ...values, id, version: 1, board_version: 1 };
        return reply({ project_id: id, version: 1 });
      }
      const prefix = '/api/v1/projects/' + id + '/';
      if (url.pathname === prefix + 'overview') return reply({ data: {
        workspace: { project: item, project_types: projectTypes, can_manage_profile: canManage && item.status !== 'archived' }, milestones: { items: [] },
      }});
      if (url.pathname === prefix + 'members') return reply({ data: { members: [] } });
      if (url.pathname === prefix + 'profile') {
        const values = req.postDataJSON();
        assert.equal(values.name, '已编辑项目');
        assert.equal(values.project_type, '销售');
        if (++writes === 1) {
          assert.equal(values.expected_version, 1);
          item = { ...item, version: 2, project_type: '售后', background: '\n同事更新的背景' };
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
      await page.getByText('没有匹配的项目。').waitFor();
      assert.equal(await create.count(), 0);
      canCreate = true;
      await page.reload();
      await create.click();
      const drawer = page.getByRole('dialog');
      assert.equal(await page.locator('html').getAttribute('data-theme'), theme);
      assert.ok(Math.abs((await drawer.boundingBox()).width - referenceWidth) <= 1);
      assert.ok(await drawer.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
      const name = drawer.getByRole('textbox', { name: '项目名称' });
      const type = drawer.getByRole('combobox', { name: '项目类型' });
      assert.equal(await type.inputValue(), '');
      assert.equal(await type.getByRole('option').count(), projectTypes.length + 1);
      await type.focus();
      await type.press('ArrowDown');
      assert.equal(await type.inputValue(), '研发');
      assert.equal(await type.getAttribute('data-project-type-tone'), 'blue');
      await name.fill('新建项目试点');
      await drawer.getByLabel('计划开始日期').fill('2026-11-22');
      const end = drawer.getByLabel('计划结束日期');
      await end.fill('2026-11-21');
      await drawer.getByRole('button', { name: '新建项目' }).click();
      await drawer.getByText('计划结束日期应晚于或等于开始日期。').waitFor();
      await end.fill('2026-12-01');
      await drawer.getByRole('button', { name: '新建项目' }).click();
      await page.getByRole('heading', { name: '新建项目试点' }).waitFor();
      await page.locator('.project-title [data-project-type-tone="blue"]').getByText('研发').waitFor();
      const settings = page.getByRole('button', { name: '项目设置' });
      canManage = false;
      await page.reload();
      await page.getByRole('heading', { name: '新建项目试点' }).waitFor();
      assert.equal(await settings.count(), 0);
      canManage = true;
      await page.reload();
      await settings.click();
      assert.equal(await type.inputValue(), '研发');
      await type.selectOption('销售');
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
      assert.equal(await type.inputValue(), '销售');
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
      await page.getByText('没有匹配的项目。').waitFor();
      await page.getByRole('button',{name:'筛选与选择'}).click();
      await page.getByRole('combobox', {name:'项目阶段'}).selectOption('');
      await page.getByRole('button',{name:'收起',exact:true}).click();
      await page.getByRole('link', { name: '已编辑项目' }).waitFor();
      await page.getByRole('cell', { name: '已归档', exact: true }).waitFor();
      await page.getByRole('cell', { name: '销售', exact: true }).locator('[data-project-type-tone="purple"]').waitFor();
      assert.deepEqual(errors, []);
    } finally {
      await page.close();
    }
  }
});

test('总览只请求个人可见统计，项目列表默认进行中并可查看全部', { skip: !moduleName }, async (t) => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const planned = { ...project, updated_at:undefined, id: 'p-planned', short_code:'GHS', name: '规划示例', project_type: '研发', status: 'planned' };
  for (const admin of [false, true]) for (const { width, theme } of [
    { width: 1366, theme: 'light' }, { width: 390, theme: 'dark' },
  ]) {
    const page = await browser.newPage({ viewport: { width, height: 844 }, hasTouch: width === 390, colorScheme: theme });
    const requested = [];
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route(origin + '/**', async (route) => {
      const url = new URL(route.request().url());
      const reply = (json) => route.fulfill({ json });
      if (url.pathname.startsWith('/api/')) requested.push(url.pathname + url.search);
      if (url.pathname === '/api/v1/me') return reply({
        principal: { account_id: admin ? 'admin' : 'member' }, csrf_token: 'token',
        access: { navigation: { workbench: true, bot_chats: true, system: admin },
          admin: { users: admin, settings: admin }, default_route: '/#/workbench' },
      });
      if (url.pathname === '/api/v1/projects') {
        const status = url.searchParams.get('status');
        return reply({...list([project, planned].filter(row => !status || row.status === status)),can_create:admin,project_types:projectTypes});
      }
      if (url.pathname === '/api/v1/feishu/chats/messages/count/mine') return reply({ total: admin ? 3 : 2 });
      if (url.pathname === '/api/v1/feishu/files') return reply({ total: 1 });
      return route.fulfill(await browserAsset(url.pathname));
    });
    try {
      await page.goto(origin + '/?theme=' + theme + '#/workbench');
      const main = page.getByRole('main');
      await main.getByRole('link', { name: /消息数/ }).getByText(admin ? '3' : '2', { exact: true }).waitFor();
      await main.getByRole('link', { name: /文件数/ }).getByText('1', { exact: true }).waitFor();
      const projectMetric = main.getByRole('link', { name: /项目数/ });
      await projectMetric.getByText('1', { exact: true }).waitFor();
      assert.equal(await projectMetric.getAttribute('href'), '#/projects');
      await main.getByRole('table', { name: '项目速览' }).getByRole('link', { name: project.name }).waitFor();
      assert.equal(await main.getByRole('link', { name: planned.name }).count(), 0);
      assert.ok(requested.includes('/api/v1/projects?page=1&page_size=10&status=active'));
      assert.equal(await main.getByRole('heading', { name: '系统管理' }).count(), 0);
      assert.equal(await main.getByRole('link', { name: /连接设置|用户管理|启用用户/ }).count(), 0);
      assert.ok(!requested.some(path => path.startsWith('/api/v1/system/') || path === '/api/v1/feishu/chats/messages/count'));
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
      await main.getByRole('link', { name: '查看项目列表' }).click();
      await main.getByRole('link', { name: project.name }).waitFor();
      await main.getByRole('columnheader', { name: '更新时间' }).waitFor();
      assert.equal(await main.locator('tbody time').getAttribute('datetime'), project.updated_at);
      if(width===390) {
        assert.ok(await main.locator('.list-scroll').evaluate(el=>el.scrollWidth>el.clientWidth));
        assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
      }
      if (!admin && process.env.OMNIRELAY_PROJECT_EVIDENCE) await page.screenshot({
        path:process.env.OMNIRELAY_PROJECT_EVIDENCE+'/project-list-'+width+'-'+theme+'.png',
      });
      const more=main.getByRole('button',{name:'筛选与选择'});
      await more.click();
      const filter = main.getByRole('combobox', {name:'项目阶段'});
      assert.equal(await filter.inputValue(), 'active');
      assert.equal(await main.getByRole('link', { name: planned.name }).count(), 0);
      assert.ok(requested.includes('/api/v1/projects?page=1&page_size=10&status=active'));
      await filter.selectOption('');
      await main.getByRole('button',{name:'收起',exact:true}).click();
      await main.getByRole('link', { name: planned.name }).waitFor();
      await main.getByRole('row').filter({ hasText: planned.name }).getByRole('cell', { name: '—' }).waitFor();
      await page.waitForURL(/#\/projects\?status=$/);
      await page.reload();
      await main.getByRole('link', { name: planned.name }).waitFor();
      await more.click();
      assert.equal(await filter.inputValue(), '');
      await filter.selectOption('planned');
      await main.getByRole('button',{name:'收起',exact:true}).click();
      await page.waitForURL(/#\/projects\?status=planned$/);
      await main.getByText('共 1 条', { exact: false }).waitFor();
      assert.equal(await main.getByRole('link', { name: project.name }).count(), 0);
      assert.deepEqual(errors, []);
    } finally {
      await page.close();
    }
  }
});

test('项目标准列表按列排序、跨页并在刷新后恢复', {skip:!moduleName}, async t=>{
  const browser=await require(moduleName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(()=>browser.close());
  const page=await browser.newPage();
  await fixture(page);
  const rows=Array.from({length:24},(_,i)=>({...project,id:'p'+i,name:'项目 '+String(i+1).padStart(2,'0')}));
  await page.route(origin+'/api/v1/projects?**', route=>{
    const params=new URL(route.request().url()).searchParams;
    const offset=(Number(params.get('page'))-1)*Number(params.get('page_size'));
    const sorted=params.get('sort')==='name' && params.get('direction')==='desc'?[...rows].reverse():rows;
    return route.fulfill({json:{items:sorted.slice(offset,offset+Number(params.get('page_size'))),total:rows.length,can_create:false,project_types:projectTypes}});
  });
  await page.goto(origin+'/#/projects');
  const list=page.getByRole('table',{name:'项目列表'});
  const sort=list.getByRole('button',{name:'项目名称',exact:true});
  await sort.click();
  await page.waitForURL(url=>url.hash.includes('sort=name') && url.hash.includes('direction=asc'));
  await sort.click();
  await list.getByRole('link',{name:'项目 24'}).waitFor();
  await page.getByRole('button',{name:'下一页'}).click();
  await list.getByRole('link',{name:'项目 14'}).waitFor();
  assert.match(new URL(page.url()).hash,/page=2/);
  await page.reload();
  await list.getByRole('link',{name:'项目 14'}).waitFor();
  assert.equal(await list.getByRole('columnheader',{name:'项目名称'}).getAttribute('aria-sort'),'descending');
});

test('工作台与 Bot 共用居中宽度和左上对齐线', { skip: !moduleName }, async (t) => {
  const browser = await require(moduleName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const width of [2560, 1700, 390]) {
    const page = await browser.newPage({ viewport: { width, height: width === 390 ? 844 : 1080 }, hasTouch: width === 390 });
    await fixture(page);
    await page.goto(origin + '/#/projects/p-21/overview');
    await page.getByText('验收节点', { exact: true }).first().waitFor();
    const bounds = (selector) => page.locator(selector).boundingBox();
    const workspace = await bounds('.workspace');
    const frame = await bounds('#page');
    const heading = await bounds('#page > .head');
    const tabs = await bounds('#page > .tabs');
    const expectedWidth = Math.min(1753, workspace.width);
    assert.ok(Math.abs(frame.width - expectedWidth) <= 1);
    assert.ok(Math.abs(frame.x - workspace.x - (workspace.width - expectedWidth) / 2) <= 1);
    assert.ok(Math.abs(heading.x - tabs.x) <= 1);
    assert.ok(Math.abs(heading.x - (await bounds('#breadcrumb')).x) <= 1);
    assert.ok(heading.y >= frame.y && heading.y - frame.y <= 14);
    const proof = process.env.OMNIRELAY_PM_EVIDENCE;
    if (proof) await page.screenshot({path: proof + '/overview-' + width + '.png'});

    await page.getByRole('tab', { name: 'Bot', exact: true }).click();
    await page.getByRole('button', { name: '查看进行中任务' }).waitFor();
    assert.deepEqual(await bounds('#page'), frame);
    assert.deepEqual(await bounds('#page > .head'), heading);
    assert.deepEqual(await bounds('#page > .tabs'), tabs);
    const chat = await bounds('.dsh-project-chat');
    assert.ok(Math.abs(chat.x - tabs.x) <= 1 && Math.abs(chat.width - tabs.width) <= 1);
    assert.ok(Math.abs(chat.y + chat.height - (frame.y + frame.height)) <= 1);
    const body = await bounds('.dsh-project-body');
    const hero = await bounds('.dsh-hero');
    assert.ok(hero.y >= body.y && hero.y - body.y <= 24);
    assert.ok(hero.x >= body.x && hero.x - body.x <= 22);
    const card = await bounds('[data-composer-card]');
    assert.ok(card.x >= chat.x && card.x + card.width <= chat.x + chat.width + 1);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    if (proof) await page.screenshot({path: proof + '/bot-' + width + '.png'});
    await page.close();
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
      const expectedTabs = ['项目概览','成员','群聊','会议纪要','文件','日报','任务看板','Bot'];
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
      const inspector = page.locator('.task-inspector-dialog');
      await page.getByLabel('任务标题', {exact:true}).waitFor();
      await page.reload();
      await page.getByLabel('任务标题', {exact:true}).waitFor();
      assert.equal(await page.getByLabel('任务标题', {exact:true}).inputValue(), task.title);
      assert.ok(await inspector.evaluate(el => el.scrollWidth <= el.clientWidth+1));
      await page.getByRole('button',{name:'收起任务详情'}).click();
      await page.getByRole('link', { name: '返回项目列表' }).click();
      await page.getByRole('link', {name:'联调项目',exact:true}).waitFor();
      assert.equal(await page.getByRole('searchbox',{name:'搜索项目'}).inputValue(), '联调');
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
        await page.getByLabel('任务标题', {exact:true}).waitFor();
      }
      await go(path + 'chats/oc_21');
      await visible('提交任务');
      await page.getByRole('link', { name: '查看关联任务' }).click();
      await visible('联调验收');
      await go(path + 'reports/brief');
      await visible('尚未发送');
      assert.equal(new URL(page.url()).hash, path + 'reports/brief');
      await visible('已生成');
      await go(path + 'reports/brief/r-1');
      await visible('已送达');
      await go(path + 'reports');
      await visible('部分数据暂不可用。');
      await visible('已提交');
      await go(path + 'tasks?task_layout=list&task_q=验收&task_owner=成员甲&task_milestone=m1');
      await page.getByRole('searchbox',{name:'搜索任务'}).waitFor();
      assert.equal(await page.getByRole('searchbox',{name:'搜索任务'}).inputValue(),'验收');
      await page.reload();
      await page.locator('.task-list-layout').waitFor();
      assert.equal(await page.getByRole('searchbox',{name:'搜索任务'}).inputValue(),'验收');
      await go(path + 'members');
      await visible('项目成员甲');
      const delayed = f.holdMembers();
      await go(path + 'members?q=甲');
      await delayed.started;
      await visible('正在加载…');
      await go(path + 'bot');
      await page.getByRole('tab', {name:'Bot', exact:true}).waitFor();
      const input = page.getByRole('textbox', {name: '给项目 PM 发消息…'});
      const starter = page.getByRole('button', {name: '查看进行中任务'});
      const send = page.getByRole('button', {name: '发送消息', exact: true});
      await starter.waitFor();
      const tab = await page.getByRole('tab', {name: 'Bot', selected: true}).boundingBox();
      assert.ok(tab.x >= 0 && tab.x + tab.width <= width);
      assert.ok(await send.isDisabled());
      const chatBox = await page.locator('.dsh-project-chat').boundingBox();
      const workBox = await page.locator('#page').boundingBox();
      const tabsBox = await page.locator('#page > .tabs').boundingBox();
      assert.ok(Math.abs(chatBox.x - tabsBox.x) < 2 && Math.abs(chatBox.width - tabsBox.width) < 2);
      assert.ok(Math.abs(chatBox.y + chatBox.height - (workBox.y + workBox.height)) < 2);
      const proof = process.env.OMNIRELAY_PM_EVIDENCE;
      if (proof) await page.screenshot({path: proof + '/empty-' + width + '-' + theme + '.png'});
      await starter.click();
      assert.equal(await input.innerText(), '哪些任务还在进行中？');
      assert.ok(await input.evaluate(el => el === document.activeElement));
      await input.fill('你好，项目 PM');
      assert.ok(await starter.isDisabled());
      await send.click();
      await visible('你好，已收到。');
      assert.ok(await starter.isHidden());
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
      if (proof) await page.screenshot({path: proof + '/chat-' + width + '-' + theme + '.png'});
      await page.getByRole('link',{name:'飞书接入'}).click();
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

test('看板使用版本与操作键移动任务，失败后可重试', {skip:!moduleName}, async t => {
  const browser = await require(moduleName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(7000);
  await fixture(page);
  let current = {...task,status:'todo',column_id:'todo'};
  let boardVersion = 3;
  const commands = [];
  await page.route(origin+root+'/task-candidates**',r=>r.fulfill({json:{items:[],next_cursor:null,total:0}}));
  await page.route(origin+root+'/board?**', route => route.fulfill({json:{
    ...boardResponse(project.id,new URL(route.request().url()).searchParams.get('status')==='cancelled'?[]:[current]),version:boardVersion,can_assign:true,
  }}));
  await page.route(origin+root+'/tasks/'+task.id+'/move', route => {
    const body = route.request().postDataJSON();
    commands.push(body);
    if (commands.length===1) return route.fulfill({status:503,json:{error:'unavailable'}});
    current = {...current,column_id:body.target_column_id,status:body.target_column_id,version:current.version+1};
    boardVersion++;
    return route.fulfill({json:current});
  });
  await page.goto(origin+'/#/projects/'+project.id+'/tasks');
  const card = page.getByRole('button',{name:task.task_code+' '+task.title,exact:true});
  await card.press('Alt+ArrowRight');
  await page.getByRole('region',{name:'正式任务看板'}).getByRole('button',{name:'重新加载看板'}).waitFor();
  assert.equal(current.column_id,'todo');
  const moved=page.waitForResponse(res=>res.url().endsWith('/tasks/'+task.id+'/move') && res.ok());
  await card.press('Alt+ArrowRight');
  await moved;
  await page.getByRole('region',{name:'进行中',exact:true})
    .getByRole('button',{name:task.task_code+' '+task.title,exact:true}).waitFor();
  assert.equal(current.column_id,'in_progress');
  assert.equal(commands.length,2);
  assert.equal(commands[0].expected_task_version,1);
  assert.equal(commands[0].expected_board_version,3);
  assert.ok(commands[0].request_key);
  assert.deepEqual(commands[1],commands[0]);
});

test('独立任务链接通过详情抽屉核验取消和重开原因', {skip:!moduleName}, async t => {
  const browser = await require(moduleName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(7000);
  await fixture(page);
  let current = {...task,column_id:'done'};
  const commands = [];
  await page.route(origin+root+'/board?**',r=>r.fulfill({json:{...boardResponse(project.id,[current]),can_assign:true}}));
  await page.route(origin+root+'/tasks/'+task.id, r => {
    if (r.request().method()==='PUT') {
      const body = r.request().postDataJSON();
      commands.push(body);
      current={...current,...body,version:current.version+1};
    }
    return r.fulfill({json:current});
  });
  await page.goto(origin+'/#/projects/'+project.id+'/tasks/'+task.id);
  const dialog=page.getByRole('dialog');
  const save=dialog.getByRole('button',{name:'保存任务',exact:true});
  for(const [stage,label,reason] of [['cancelled','取消原因','验收计划取消'],['in_progress','变更原因','恢复联合核验']]){
    await dialog.getByLabel('任务状态').selectOption(stage);
    await save.click();
    const field=dialog.getByLabel(label,{exact:true});
    assert.equal(await field.evaluate(el=>el===document.activeElement),true);
    const count=commands.length;
    await field.fill(reason);
    await save.click();
    await page.waitForFunction(()=>document.querySelector('#task-inspector-form')&&
      document.querySelector('button[form="task-inspector-form"]')?.disabled);
    await dialog.getByText('任务已保存。',{exact:true}).waitFor();
    assert.equal(commands.length,count+1);
    assert.equal(commands.at(-1).change_reason,reason);
    assert.equal(current.status,stage);
  }
});

test('两项目分别保留搜索、负责人、里程碑和任务视图', {skip:!moduleName}, async t => {
  const browser = await require(moduleName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page=await browser.newPage();
  page.setDefaultTimeout(7000);
  await fixture(page);
  const samples=[
    {id:project.id,name:'项目甲',term:'交接甲',owner:'u1',person:'成员甲',milestone:'m1',layout:'列表'},
    {id:'p-22',name:'项目乙',term:'复核乙',owner:'u2',person:'成员乙',milestone:'@none',layout:'看板'}
  ];
  for(const sample of samples){
    const api=origin+'/api/v1/projects/'+sample.id;
    await page.route(api+'/task-candidates**',r=>r.fulfill({json:{items:[],next_cursor:null,total:0}}));
    const item={...task,project_id:sample.id,owner_member_id:sample.owner,title:sample.term+'确认',column_id:'todo',status:'todo'};
    await page.route(api+'/overview',r=>r.fulfill({json:{data:{workspace:{project:{...project,id:sample.id,name:sample.name},can_manage_profile:false,project_types:[]},
      milestones:{items:[{id:'m1',kind:'milestone',title:'交付节点'}]}}}}));
    await page.route(api+'/members**',r=>r.fulfill({json:{data:{...directory,members:[{...directoryMember,id:sample.owner,display_name:sample.person}]}}}));
    const result=(r,list)=>{
      const p=new URL(r.request().url()).searchParams;
      const filtered=p.get('owner_member_id')===sample.owner&&p.get('q')===sample.term&&
        (sample.milestone==='@none'?p.get('milestone_unassigned')==='true':p.get('milestone_id')===sample.milestone);
      const rows=filtered && p.get('status')!=='cancelled'?[item]:[];
      return r.fulfill({json:list?{items:rows,next_cursor:null,total:rows.length}:boardResponse(sample.id,rows)});
    };
    await page.route(api+'/board?**',r=>result(r,false));
    await page.route(api+'/tasks/query**',r=>result(r,true));
  }
  const filters=page.getByRole('button',{name:'筛选',exact:true});
  const search=page.getByRole('searchbox',{name:'搜索任务'});
  async function enter(sample){
    await page.evaluate(id=>{location.hash='#/projects/'+id+'/overview'},sample.id);
    await page.getByRole('heading',{name:sample.name,exact:true}).waitFor();
    await page.getByRole('tab',{name:'任务看板',exact:true}).click();
    await search.waitFor();
  }
  await page.goto(origin+'/#/projects');
  for(const sample of samples){
    await enter(sample);
    assert.equal(await search.inputValue(),'');
    await search.fill(sample.term);
    await filters.click();
    await page.getByRole('combobox',{name:'按负责人筛选'}).fill(sample.person);
    await page.getByRole('option',{name:sample.person}).click();
    await page.getByLabel('筛选里程碑').selectOption(sample.milestone);
    await page.keyboard.press('Escape');
    await page.getByRole('group',{name:'显示方式'}).getByRole('button',{name:sample.layout}).click();
    await page.locator('#board-task-'+task.id).waitFor();
  }
  for(const sample of samples){
    await enter(sample);
    await page.locator('#board-task-'+task.id).waitFor();
    assert.equal(await search.inputValue(),sample.term);
    assert.equal(await page.getByRole('group',{name:'显示方式'}).getByRole('button',{name:sample.layout}).getAttribute('aria-pressed'),'true');
    await filters.click();
    assert.equal(await page.getByRole('combobox',{name:'按负责人筛选'}).inputValue(),sample.person);
    assert.equal(await page.getByLabel('筛选里程碑').inputValue(),sample.milestone);
    await page.keyboard.press('Escape');
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
  const other = {...project, id: 'p-22', name: '启明项目'};
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
    r.fulfill({ json: { ...list([project, other]), can_create:false, project_types:projectTypes, page: 1, page_size: 25 } }));
  await page.route(origin + api + '**', async (route) => {
    const section = new URL(route.request().url()).pathname.slice(api.length);
    const send = (json, status = 200) => route.fulfill({ status, json });
    switch (section) {
      case 'overview':
        return send({ project_id: other.id, data: { workspace: { project: other, project_types:projectTypes, can_manage_profile:false }, milestones: { items: [] } } });
      case 'members':
        return send({ project_id: other.id, data: {...directory,members:[{...directoryMember,id:'member-22',display_name:'启明成员'}]} });
      case 'bot/messages': return send({items:[]});
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
  await page.getByRole('tab', { name: 'Bot', exact: true }).click();
  await page.getByRole('link', { name: '飞书接入' }).click();
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

  await page.getByRole('tab', { name: 'Bot', exact: true }).click();
  await page.getByRole('link', { name: '飞书接入' }).click();
  await visible('启明群');
  await visible('已生成');
  await visible('投递失败');
  assert.equal(await page.getByRole('button', { name: '接入验证' }).count(), 0);
  assert.doesNotMatch(await page.locator('#page').innerText(), /联调群|项目成员甲|已送达/);
  await go(base + '/reports/brief/r-22');
  await page.waitForURL((url) => url.hash === base + '/reports/brief/r-22');
  await visible('投递失败');
  run.delivery_state = 'success';
  await page.reload();
  await visible('送达待确认');
  await go(base + '/reports/brief/r-1');
  await visible('当前项目或来源已不可访问。');
  await page.getByRole('tab', { name: '文件' }).click();
  await visible('部分数据暂不可用。');
  await visible('启明项目档案');
  assert.doesNotMatch(await page.locator('#page').innerText(), /暂无记录|交付文档|启明群|已送达/);
  assert.deepEqual(writes, []);
  assert.deepEqual(first.errors, []);
});

test('DSH 项目对话长历史、键盘发送、重试与撤权', {skip: !moduleName}, async t => {
  const browser = await require(moduleName).chromium.launch({executablePath: process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage({viewport: {width: 390, height: 844}});
  const f = await fixture(page);
  const items = Array.from({length: 50}, (_, i) => ({
    id: String(i), state: 'sent', question: '任务 ' + i, answer: '项目 PM\n**已确认**',
  }));
  items[0] = {id: 'stale', question: '查询主群', state: 'stale'};
  items[1] = {id: 'primary', question: '设置主群', state: 'suppressed', primary_saved: true};
  items[2] = {id: 'restricted', question: '查看受限文件', state: 'suppressed'};
  const keys = [];
  let deny = false;
  await page.route(origin + root + '/bot/messages', route => {
    if (deny) return route.fulfill({status: 403, json: {error: 'project_access_denied'}});
    if (route.request().method() === 'POST') {
      const {text} = route.request().postDataJSON();
      keys.push(route.request().headers()['idempotency-key']);
      if (keys.length === 1) return route.fulfill({status: 503, json: {error: 'unavailable'}});
      items.push({id: 'next', question: text, state: 'running'});
      return route.fulfill({status: 202, json: {id: 'next'}});
    }
    return route.fulfill({json: {items}});
  });
  await page.goto(origin + '/#/projects/p-21/bot');
  const input = page.getByRole('textbox', {name: '给项目 PM 发消息…'});
  const history = page.getByRole('list', {name: '我的项目对话'});
  await history.getByText('任务 49', {exact: true}).waitFor();
  assert.equal(await history.locator('article strong').filter({hasText: '已确认'}).count(), 47);
  await history.getByText('查询时的来源数据已变化，请重新查询。').waitFor();
  await history.getByText('这次主群设置已保存。').waitFor();
  await history.getByText('当前无法显示这条回复。').waitFor();
  const area = page.locator('[data-conversation-scroll]');
  assert.ok(await area.evaluate(el => el.scrollHeight > el.clientHeight && el.scrollTop > 0));
  await area.evaluate(el => { el.scrollTop = 0; });
  const box = await input.boundingBox();
  assert.ok(box.y > 0 && box.y + box.height <= 844);
  assert.ok(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight + 1));
  await input.fill('第一行');
  await input.press('Shift+Enter');
  await page.keyboard.insertText('第二行');
  assert.equal(keys.length, 0);
  await input.press('Enter');
  await page.getByText('发送未确认，可以重试。').waitFor();
  assert.match(await input.innerText(), /第一行\s+第二行/);
  await page.getByRole('button', {name: '发送消息', exact: true}).click();
  await page.getByText('项目 PM 正在回复…').waitFor();
  assert.ok(keys[0] && keys[0] === keys[1]);
  assert.match(items.at(-1).question, /第一行\s+第二行/);
  deny = true;
  await page.getByText('当前项目对话不可访问。').waitFor();
  assert.equal(await history.count(), 0);
  assert.deepEqual(f.errors, []);
});

test('项目文件分类、筛选、来源和版本操作保留项目范围', {skip:!moduleName}, async t => {
  const browser = await require(moduleName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage({viewport:{width:390,height:844}});
  page.setDefaultTimeout(5000);
  await fixture(page);
  const row = {...driveFileDefaults,id:17, name:'现场照片.png', format_key:'image', format:'图片', category:'files', version_id:2,
    shared_at:'2026-11-21T12:00:00Z', sync_state:'complete', project_ids:[project.id],
    sources:[{project_id:project.id, chat_id:'oc_21', message_id:'msg1', chat_name:'联调群', sender_name:'成员甲', sent_at:'2026-11-21T12:00:00Z'}]};
  let search;
  await page.route(origin + root + '/files?**', route => {
    search = new URL(route.request().url()).searchParams;
    const items = ['all','images'].includes(search.get('category')) && row.name.includes(search.get('q') || '') ? [row] : [];
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
  assert.equal(search.get('category'),'images');
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
