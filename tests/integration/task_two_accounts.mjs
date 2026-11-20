import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const { chromium } = createRequire(import.meta.url)(process.env.OMNIRELAY_PLAYWRIGHT_MODULE);
const origin = process.env.H3_ORIGIN;
const root = '/api/v1/projects/h3';
const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
const errors = [];

async function login(username) {
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.on('pageerror', error => errors.push(error.message));
  const headers = { Origin: origin };
  const login = await context.request.post(origin + '/auth/login', {
    headers, data: { username, password: process.env.H3_PASSWORD },
  });
  assert.ok(login.ok(), username + ' 登录失败 ' + login.status());
  const me = await (await context.request.get(origin + '/api/v1/me')).json();
  assert.equal(me.principal.account_id, username);
  headers['X-CSRF-Token'] = me.csrf_token;
  async function api(method, path, data, status = 200) {
    const response = await context.request.fetch(origin + root + path, { method, headers, data });
    const body = await response.json();
    assert.equal(response.status(), status, method + ' ' + path + ' ' + JSON.stringify(body));
    return body;
  }
  return { page, api };
}

try {
  const manager = await login('manager');
  const member = (role) => manager.api('PUT', '/members/second/access', { role, member_id: 'v' });
  await manager.api('PUT', '/members/manager/access', { role: 'manager', member_id: 'u' });
  await member('reader');
  const second = await login('second');
  const task = await manager.api('POST', '/tasks', { title: '初始标题', owner_member_id: 'v' });
  const sibling = await manager.api('POST', '/tasks', { title: '初始标题' });
  const path = '/tasks/' + task.id;
  const detail = origin + '/#/projects/h3' + path;
  const node = await manager.api('POST', '/milestones', { title: '验收节点', task_id: task.id });
  await manager.api('PUT', '/task-tags/check', { name: '联验' });
  await manager.api('PUT', '/sources/document/doc', {
    title: '受限依据', body: '仅负责人可见的正文', reader_account_ids: ['manager'],
  });
  await manager.api('PUT', path + '/evidence/document/doc');
  const a = manager.page, b = second.page;
  const field = (page, key) => page.locator('.task-edit-grid [name="' + key + '"]');
  const button = (page, name) => page.getByRole('button', { name, exact: true });
  const saved = page => page.getByText('任务已保存。', { exact: true }).waitFor();
  await Promise.all([a.goto(detail), b.goto(detail)]);
  await button(a, '保存任务').waitFor();
  await button(b, '保存任务').waitFor();
  assert.ok(await button(a, '保存任务').isDisabled());
  assert.ok(await field(b, 'title').isDisabled());
  await a.getByText('受限依据', { exact: true }).click();
  await a.getByText('仅负责人可见的正文').waitFor();
  await b.locator('[data-evidence] p').waitFor();
  assert.deepEqual((await second.api('GET', path + '/evidence')).items.map(i => i.accessible), [false]);
  assert.doesNotMatch(await b.locator('[data-evidence]').innerText(), /受限依据|仅负责人/);
  await second.api('PUT', path, { title: '不可编辑', expected_task_version: 1 }, 404);

  // 第二人获得编辑权，在第一人仍持有原版本时提交。
  await member('editor');
  await b.reload();
  await field(a, 'title').fill('两账号核验');
  await field(b, 'description').fill('第二人保存的正文');
  await button(b, '保存任务').click();
  await saved(b);
  await button(a, '保存任务').click();
  await button(a, '应用比对结果').click();
  assert.equal(await field(a, 'description').inputValue(), '第二人保存的正文');
  assert.equal(await field(a, 'title').inputValue(), '两账号核验');
  await field(a, 'priority').selectOption('P0');
  await field(a, 'milestone_id').selectOption(node.id);
  await field(a, 'start_at').fill('2026-11-19');
  await field(a, 'due_at').fill('2026-11-23');
  await a.getByText('验收与参与人', {exact:true}).click();
  await field(a, 'acceptance').fill('以同一任务为准');
  for (const [key, term, id] of [['owner_member_id','项目负责人','u'], ['participant_ids','项目成员','v'], ['tag_ids','联验','check']]) {
    if (key === 'tag_ids') await a.getByText('添加标签', {exact:true}).click();
    await field(a, key).fill(term);
    await a.locator('[data-picker="' + key + '"] [data-pick="' + id + '"]').click();
  }
  await button(a, '保存任务').click();
  await saved(a);
  assert.equal(a.url(), detail);
  const record = await second.api('GET', path);
  assert.equal(record.version, 3);
  assert.equal(record.title, '两账号核验');
  assert.equal(record.description, '第二人保存的正文');
  assert.equal(record.start_at, '2026-11-19');
  assert.equal(record.due_at, '2026-11-23');
  assert.equal(record.updated_by, 'manager');
  assert.equal(record.owner_member_id, 'u');
  assert.equal(record.priority, 'P0');
  assert.equal(record.milestone_id, node.id);
  assert.deepEqual(record.participant_ids, ['v']);
  assert.deepEqual(record.tag_ids, ['check']);
  assert.equal(record.acceptance, '以同一任务为准');
  for (const route of ['tasks','tasks/list','overview']) {
    await b.goto(origin + '/#/projects/h3/' + route);
    await b.reload();
    const link = b.locator('a[href="#/projects/h3' + path + '"]').first();
    await link.waitFor();
    const row = route === 'tasks' ? link : link.locator('xpath=ancestor::tr');
    assert.match(await row.innerText(), /项目负责人/);
    if (route === 'tasks') assert.match(await row.innerText(), /联验/);
    if (route !== 'overview') assert.match(await link.innerText(), /两账号核验/);
    await b.locator('time[datetime="2026-11-23"]').first().waitFor();
    await link.click();
    assert.equal(await field(b, 'title').inputValue(), record.title);
    assert.equal(await field(b, 'description').inputValue(), record.description);
  }

  const changes = () => second.api('GET', path + '/activity?kind=change');
  const history = (await changes()).items;
  assert.equal(history.length, 3);
  assert.equal(history[0].before['标题'], '初始标题');
  assert.equal(history[0].after['标题'], record.title);
  assert.deepEqual(history.slice(0,2).map(e => e.actor_name), ['项目负责人','项目成员']);
  assert.ok(history.every(e => Number.isFinite(Date.parse(e.created_at))));
  for (const edit of [{title:' '},{title:record.title, start_at:'2026-11-23',due_at:'2026-11-22'},
    {title:record.title,owner_member_id:'invalid'},{title:record.title,status:'blocked'},
    {title:record.title,status:'cancelled'}]) {
    await manager.api('PUT', path, {expected_task_version:3,...edit}, 400);
  }
  assert.deepEqual((await changes()).items, history);
  assert.equal((await second.api('GET',path)).version, 3);

  await member('member');
  await b.reload();
  assert.ok(await field(b, 'title').isDisabled());
  assert.ok(await field(b, 'status').isEnabled());
  await b.getByPlaceholder('留下评论…').fill('第二人的评论');
  await button(b, '提交评论').click();
  await b.getByText('评论已发送。').waitFor();
  await a.reload();
  await button(a,'评论').click();
  const comment = a.locator('li[data-kind="comment"]').filter({hasText:'第二人的评论'});
  await comment.waitFor();
  assert.match(await comment.innerText(), /项目成员/);
  const events = (await second.api('GET', path + '/activity?kind=comment')).items;
  assert.equal(events.length, 1);
  assert.equal(events[0].actor_id, 'second');
  assert.equal(await comment.locator('time').textContent(), events[0].created_at);
  await button(b,'提交进展反馈').click();
  await b.locator('[name="proposed_status"]').selectOption('done');
  await b.locator('[name="note"]').fill('已完成联合核验');
  await button(b,'确认提交').click();
  await b.getByText('已完成联合核验', {exact:true}).waitFor();
  const item = (await second.api('GET', path + '/update-suggestions')).items[0];
  await second.api('POST', '/task-update-suggestions/' + item.id + '/review',
    {expected_version:item.version,action:'accept',reason:'自行审核'},404);
  await a.reload();
  await button(a,'采纳').click();
  await a.locator('[name="note"]').fill('负责人核对完成结果');
  await button(a,'确认提交').click();
  await a.getByText('审核说明　负责人核对完成结果').waitFor();
  await b.reload();
  assert.equal(await field(b,'status').inputValue(),'done');
  const done = await second.api('GET',path);
  assert.equal(done.completion_note, '已完成联合核验');
  await manager.api('PUT',path,{title:done.title,status:'in_progress',expected_task_version:done.version},400);
  assert.deepEqual(await second.api('GET',path),done);
  const leave = {
    '关闭': () => a.getByRole('link', {name:'收起任务详情'}).click(),
    'Escape': () => a.keyboard.press('Escape'),
    '外点': () => a.locator('.head').click({position:{x:1,y:1}}),
    '切页': () => a.getByRole('tab',{name:'成员',exact:true}).click(),
  };
  for (const [entry, exit] of Object.entries(leave)) {
    for (const choice of ['继续编辑','不保存','保存并关闭']) {
      await a.goto(detail);
      const before = await second.api('GET',path);
      const draft = entry + '／' + choice;
      await field(a,'title').fill(draft);
      await exit();
      await button(a,choice).click();
      if (choice === '继续编辑') {
        assert.equal(await field(a,'title').inputValue(),draft);
        assert.equal(a.url(), detail);
        await exit();
        await button(a,'不保存').click();
      }
      await field(a,'title').waitFor({state:'detached'});
      const after = await second.api('GET',path);
      const committed = choice === '保存并关闭';
      assert.equal(after.title, committed ? draft : before.title);
      assert.equal(after.version, before.version + Number(committed));
    }
  }

  const version = (await second.api('GET',path)).version;
  const expected = ['第二人的评论'];
  for (let i=1; i<=25; i++) {
    const body = '分页核验 ' + i;
    await manager.api('POST',path+'/comments',{expected_version:version,request_key:'page-'+i,body});
    expected.unshift(body);
  }
  await b.reload();
  await button(b,'评论').click();
  const comments = b.locator('li[data-kind="comment"] p');
  await button(b,'加载更多').waitFor();
  assert.deepEqual(await comments.allTextContents(),expected.slice(0,25));
  await manager.api('POST',path+'/comments',{expected_version:version,request_key:'insert',body:'翻页期间新增'});
  await button(b,'加载更多').click();
  await comments.last().getByText('第二人的评论',{exact:true}).waitFor();
  assert.deepEqual(await comments.allTextContents(),expected);
  await b.reload();
  await button(b,'评论').click();
  await b.getByText('翻页期间新增',{exact:true}).waitFor();
  const otherEvents = await second.api('GET', '/tasks/' + sibling.id + '/activity');
  assert.deepEqual(otherEvents.items.map(event => event.kind), ['change']);
  assert.equal((await second.api('GET', '/tasks/' + sibling.id)).title, '初始标题');
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}
