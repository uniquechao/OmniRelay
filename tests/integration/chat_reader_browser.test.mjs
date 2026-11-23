import { browserAsset } from './browser-assets.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';

const require = createRequire(import.meta.url);
const origin = 'https://relay.example.test';
const base = '/api/v1/feishu/chats';
const sent_at = '2026-11-19T16:14:00Z';
const person = { name: '林晓', person_key: 'person_alice', source: 'feishu', organization_paths: [['研发中心', '平台组']] };
const group = { id: 'oc_group', kind: 'group', name: '北辰项目群', last_message_at: sent_at };
const direct = { id: 'oc_direct', kind: 'p2p', name: person.name, person, last_message_at: sent_at };
const oldMessage = { id: 'om_old', chat_id: group.id, sender: person, type: 'text', body: '{"text":"会议纪要"}', sent_at, state: 'available' };
const recentMessage = { ...oldMessage, id: 'om_recent', sender: { name: '黄利利', source: 'feishu', resigned: true }, body: '会议安排' };
const task = { id: 'om_task', chat_id: group.id, sender: { name: '飞书 Bot' }, sender_kind: 'app', type: 'interactive',
  body: '{"header":{"title":{"content":"任务已分派"}}}',
  business_receipt: { id: 'rc_1', project_id: 'beichen', target_kind: 'task', target_id: 'task_1' }, sent_at };
const briefing = { ...task, id: 'om_brief', body: '{"title":"项目简报"}',
  business_receipt: { id: 'rc_2', project_id: 'beichen', target_kind: 'briefing', target_id: 'brief_1' } };
const p2pMessage = { ...oldMessage, id: 'om_direct', chat_id: direct.id, body: '收到，谢谢' };
const packageName = process.env.OMNIRELAY_PLAYWRIGHT_MODULE;

async function mountFixture(page, {
  signedIn = true, syncScope = 'current_memberships_and_own_private_chats'
} = {}) {
  let groupAccess = true;
  let aborted = () => false;
  let directoryWait;
  let releaseSearch;
  let releaseMembers;
  const queries = [];
  const items = (values, extra = {}) => ({ json: { items: values, total: values.length, ...extra } });
  const error = (status, code) => ({ status, json: { error: code } });
  const session = { csrf_token: 'fixture-csrf', principal: { account_id: 'alice', open_id: 'ou_alice' },
    access: { navigation: { workbench: true, bot_chats: true }, default_route: '/#/workbench' } };
  await page.route(origin + '/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const query = url.searchParams;
    if (path === '/api/v1/me') return route.fulfill(signedIn ? { json: session } : error(401, 'unauthorized'));
    if (path === '/auth/login') {
      signedIn = true;
      return route.fulfill({ json: session });
    }
    if (path === '/auth/feishu/start') {
      queries.push([path, query.get('return_to')]);
      signedIn = true;
      return route.fulfill({ status: 302, headers: { location: query.get('return_to') } });
    }
    if (path === base || path.startsWith(base + '/')) {
      const relative = path.slice(base.length);
      const visible = groupAccess ? [group, direct] : [direct];
      queries.push([request.method(), relative, query.toString()]);
      if (relative.startsWith('/oc_group') && !groupAccess) return route.fulfill(error(404, 'chat_access_lost'));
      if (aborted(relative, request.method())) return route.abort('internetdisconnected');
      if (relative === '') {
        if (directoryWait) await directoryWait;
        return route.fulfill(items(visible.filter(row => !query.get('kind') || row.kind === query.get('kind'))));
      }
      if (relative === '/search') {
        if (query.get('kind') === 'p2p' || !groupAccess) return route.fulfill(items([]));
        if (query.get('q') === '会议' && !query.has('cursor')) {
          return route.fulfill(items([recentMessage], { total: 2, next_cursor: 'older-search' }));
        }
        if (query.get('q') === '会议') assert.equal(query.get('cursor'), 'older-search');
        return route.fulfill(items([oldMessage]));
      }
      if (relative === '/sync' && request.method() === 'POST') {
        assert.equal(request.headers()['x-csrf-token'], 'fixture-csrf');
        return route.fulfill({ status: 202, json: { state: 'queued' } });
      }
      if (relative === '/sync') return route.fulfill(items(visible.map(row => ({
        chat_id: row.id, state: row.kind === 'group' ? 'partial' : 'complete',
        coverage: row.kind === 'group' ? 'partial' : 'all_source_visible_history',
        completed_at: sent_at, covered_until: sent_at, pending_threads: row.kind === 'group' ? 1 : 0
      })), { scope: syncScope }));
      if (relative === '/oc_group') return route.fulfill(items([group]));
      if (relative === '/oc_direct') return route.fulfill(items([direct]));
      if (relative === '/oc_group/messages') {
        return route.fulfill(query.has('cursor') ? items([oldMessage]) : items([briefing, task, recentMessage], { next_cursor: 'older' }));
      }
      if (relative === '/oc_direct/messages' || relative === '/oc_direct/search') return route.fulfill(items([p2pMessage]));
      if (relative === '/oc_direct/messages/om_direct/context') return route.fulfill(items([p2pMessage]));
      if (relative === '/oc_group/search') {
        await new Promise(resolve => { releaseSearch = resolve; });
        return route.fulfill(items([{ ...oldMessage, body: '迟到的北辰搜索' }])).catch(() => {});
      }
      if (relative === '/oc_group/members' && query.get('offset') === '0' && !releaseMembers) {
        await new Promise(resolve => { releaseMembers = resolve; });
        return route.fulfill(items([{ ...person, name: '迟到的北辰成员' }], { state: 'complete' })).catch(() => {});
      }
      if (relative === '/oc_group/members' || relative === '/oc_direct/members') return route.fulfill(items([person], { state: 'complete' }));
      if (relative === '/oc_group/messages/om_old/context') return route.fulfill(items([oldMessage, task], { anchor_id: 'om_old' }));
      if (relative === '/oc_group/messages/om_deleted/context') return route.fulfill(error(410, 'message_deleted'));
      return route.fulfill(error(404, 'message_not_found'));
    }
    return route.fulfill(await browserAsset(path));
  });
  return {
    queries,
    revoke: () => { groupAccess = false; },
    restore: () => { groupAccess = true; },
    abortRequests: (predicate = () => false) => { aborted = predicate; },
    holdDirectory() {
      let release;
      directoryWait = new Promise(resolve => { release = resolve; });
      return () => { release(); directoryWait = undefined; };
    },
    resolveSearch: () => releaseSearch?.(),
    resolveMembers: () => releaseMembers?.()
  };
}

test('群聊正文和搜索摘要显示消息本身的提及姓名', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({executablePath: process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage();
  await mountFixture(page);
  const message = {...oldMessage, body: JSON.stringify({text: '<p>配送@_user_1，复核@_user_10</p><p>5或者8</p>'}),
    display_text: '配送@林一，复核@陈十\n5或者8'};
  const reply = route => route.fulfill({json: {items: [message], total: 1}});
  await page.route(origin + base + '/oc_group/messages*', reply);
  await page.route(origin + base + '/search?*', reply);
  await page.goto(origin + '/#/feishu/bot-chats/oc_group');
  const bubble = page.locator('[data-message="om_old"] .chat-bubble');
  await bubble.waitFor();
  assert.equal(await bubble.innerText(), message.display_text);
  await page.context().grantPermissions(['clipboard-read','clipboard-write'],{origin});
  const copyButton=page.locator('[data-message="om_old"]').getByRole('button',{name:'复制消息'});
  await copyButton.click();
  assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),message.display_text);
  if (process.env.OMNIRELAY_CHAT_EVIDENCE) {
    await mkdir(process.env.OMNIRELAY_CHAT_EVIDENCE, {recursive:true});
    await page.locator('[data-message="om_old"]').screenshot({path: process.env.OMNIRELAY_CHAT_EVIDENCE + '/chat-paragraph-mentions.png'});
  }
  await page.getByRole('searchbox', {name: '搜索会话或消息'}).fill('林一');
  const snippet = page.locator('.chat-result .chat-conversation-preview');
  await snippet.waitFor();
  assert.equal(await snippet.textContent(), message.display_text);
  assert.equal(await snippet.locator('mark').textContent(), '林一');
});

test('聊天视频和文件形式的视频可播放，关闭后停止播放', {skip:!packageName},async t=>{
  const browser=await require(packageName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(()=>browser.close());
  const page=await browser.newPage();
  await mountFixture(page);
  const movie={...oldMessage,id:'om_movie',type:'media',body:'{"file_key":"clip","file_name":"录屏.mp4"}',resources:[{kind:'video',key:'clip',name:'录屏.mp4'}]};
  const upload={...movie,id:'om_upload',type:'file',resources:[{kind:'attachment',key:'clip',name:'附件.mp4',object_kind:'video'}]};
  await page.route(origin+base+'/oc_group/messages',route=>route.fulfill({json:{items:[movie,upload]}}));
  await page.goto(origin+'/#/feishu/bot-chats/oc_group');
  const bytes=await page.evaluate(async()=>{
    const canvas=document.createElement('canvas');canvas.width=32;canvas.height=32;
    const ctx=canvas.getContext('2d');const stream=canvas.captureStream(10);
    const recorder=new MediaRecorder(stream,{mimeType:'video/webm;codecs=vp8'});
    const parts=[];recorder.ondataavailable=e=>parts.push(e.data);
    const stopped=new Promise(resolve=>recorder.onstop=resolve);
    recorder.start();let tick=0;const timer=setInterval(()=>{ctx.fillStyle=tick++%2?'blue':'red';ctx.fillRect(0,0,32,32);},75);
    await new Promise(resolve=>setTimeout(resolve,750));clearInterval(timer);recorder.stop();await stopped;
    stream.getTracks().forEach(track=>track.stop());return Array.from(new Uint8Array(await new Blob(parts).arrayBuffer()));
  });
  await page.route('**/messages/*/resources/*/clip',route=>route.fulfill({contentType:'video/webm',body:Buffer.from(bytes)}));
  for(const name of ['录屏.mp4','附件.mp4']){
    await page.getByRole('button',{name:'播放视频：'+name}).click();
    await page.waitForFunction(()=>document.querySelector('video')?.currentTime>0);
    assert.ok(await page.locator('video[controls]').isVisible());
    await page.keyboard.press('Escape');
    await page.locator('video').waitFor({state:'detached'});
  }
});

test('机器人记录切换、搜索、成员、回执及分隔栏', { skip: !packageName }, async t => {
  const { chromium } = require(packageName);
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 }, reducedMotion: 'reduce' });
  const pageErrors = [];
  page.on('pageerror', error => pageErrors.push(error.message));
  const fixture = await mountFixture(page, { syncScope: 'all_authorized_bot_chats' });
  await page.goto(origin + '/#/bot-chats');
  await page.getByRole('heading', { name: group.name }).waitFor();
  assert.equal(new URL(page.url()).hash, '#/feishu/bot-chats');
  const departed = page.locator('[data-message="om_recent"]');
  await departed.locator('.person-name').getByText('离职', { exact: true }).waitFor();
  for (const trigger of ['.chat-person-button', '.person-name']) {
    await departed.locator(trigger).click();
    const detail = page.getByRole('dialog', { name: '黄利利的基本信息' });
    await detail.getByText('离职', { exact: true }).waitFor();
    assert.equal(await detail.locator('.person-resigned svg').count(), 1);
    await page.keyboard.press('Escape');
  }
  await page.getByRole('link', { name: '查看关联任务' }).waitFor();
  assert.equal(await page.getByRole('link', { name: '查看关联任务' }).getAttribute('href'), '#/projects/beichen/tasks/task_1');
  assert.equal(await page.getByRole('link', { name: '查看项目简报' }).getAttribute('href'), '#/projects/beichen/reports/brief/brief_1');

  await page.getByRole('button', { name: '加载更早消息' }).click();
  await page.getByText('会议纪要', { exact: true }).waitFor();
  const split = page.getByRole('separator', { name: '调整会话列表宽度' });
  const width = () => page.locator('.chat-directory').evaluate(node => node.clientWidth);
  const range = name => split.getAttribute('aria-value' + name).then(Number);
  const total = await page.locator('.bot-chat').evaluate(node => node.clientWidth);
  await split.press('Home');
  assert.equal(await range('now'), await range('min'));
  await split.press('ArrowRight');
  assert.equal(await range('now'), await range('min') + 16);
  await split.press('ArrowLeft');
  assert.equal(await range('now'), await range('min'));
  await split.press('End');
  assert.equal(await range('now'), await range('max'));
  await split.press('ArrowLeft');
  const previous = await width();
  const beforeDrag = page.url();
  const bounds = await split.boundingBox();
  await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + 100);
  await page.mouse.down();
  await page.mouse.move(bounds.x - 52, bounds.y + 100, { steps: 5 });
  await page.mouse.up();
  const dragged = await width();
  assert.ok(dragged < previous);
  assert.equal(page.url(), beforeDrag);
  assert.ok(await page.getByRole('heading', { name: group.name }).isVisible());
  assert.ok(await page.getByText('会议纪要', { exact: true }).isVisible());
  assert.equal(await page.locator('.bot-chat').evaluate(node => node.clientWidth), total);
  await page.reload();
  await page.getByRole('heading', { name: group.name }).waitFor();
  assert.equal(await width(), dragged);
  await page.setViewportSize({ width: 820, height: 900 });
  await page.waitForFunction(() => Number(document.querySelector('.chat-split-handle').getAttribute('aria-valuemax')) < 500);
  assert.ok(await width() < dragged);
  assert.ok(await page.locator('.chat-reading').evaluate(node => node.getBoundingClientRect().width >= 284));
  await page.setViewportSize({ width: 1366, height: 900 });
  await page.waitForFunction(expected => document.querySelector('.chat-directory').clientWidth === expected, dragged);

  await page.getByRole('button', { name: '搜索当前会话' }).click();
  const searchRequest = page.waitForRequest(request => new URL(request.url()).pathname === base + '/oc_group/search');
  await page.getByRole('searchbox', { name: '搜索当前会话' }).fill('北辰');
  await searchRequest;
  await page.getByRole('button', { name: '打开会话 林晓' }).click();
  fixture.resolveSearch();
  await page.getByText('收到，谢谢').waitFor();
  assert.equal(await page.getByRole('searchbox', { name: '搜索当前会话' }).count(), 0);
  assert.equal(await page.getByText('迟到的北辰搜索').count(), 0);

  const directoryTone = await page.locator('.chat-direct-row .person-avatar').getAttribute('data-avatar-tone');
  const titleTone = await page.locator('.chat-reading-title .person-avatar').getAttribute('data-avatar-tone');
  assert.equal(titleTone, directoryTone);
  await page.locator('.chat-reading-title .person-name').click();
  const popover = page.locator('.person-popover:popover-open');
  await popover.getByText('研发中心 / 平台组').waitFor();
  await page.keyboard.press('Escape');
  await popover.waitFor({ state: 'hidden' });

  await page.getByRole('button', { name: '打开会话 北辰项目群' }).click();
  const memberRequest = page.waitForRequest(request => new URL(request.url()).pathname === base + '/oc_group/members');
  await page.getByRole('button', { name: '会话成员' }).click();
  await memberRequest;
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: '打开会话 林晓' }).click();
  fixture.resolveMembers();
  await page.getByRole('button', { name: '会话成员' }).click();
  const members = page.getByRole('dialog', { name: '会话成员' });
  await members.getByText('研发中心 / 平台组').waitFor();
  assert.equal(await members.getByText('迟到的北辰成员').count(), 0);
  assert.equal(await members.locator('.person-avatar').getAttribute('data-avatar-tone'), titleTone);
  await page.keyboard.press('Escape');

  await page.getByRole('button', { name: '搜索当前会话' }).click();
  await page.getByRole('searchbox', { name: '搜索当前会话' }).fill('谢谢');
  await page.getByRole('button', { name: '定位消息' }).click();
  await page.locator('[data-source-message]').getByText('收到，谢谢').waitFor();
  assert.match(page.url(), /oc_direct\/om_direct$/);

  await page.getByRole('button', { name: '筛选会话' }).click();
  await page.getByRole('button', { name: '群聊', exact: true }).click();
  await page.getByRole('heading', { name: group.name }).waitFor();
  assert.equal(await page.getByRole('button', { name: '打开会话 林晓' }).count(), 0);
  await page.getByRole('searchbox', { name: '搜索会话或消息' }).fill('会议');
  await page.getByRole('button', { name: '加载更多消息结果' }).click();
  await page.locator('.chat-result[data-message-id="om_old"]').click();
  const anchor = page.locator('[data-source-message]');
  await anchor.waitFor();
  assert.ok(await anchor.evaluate(node => node === document.activeElement));
  assert.match(page.url(), /#\/feishu\/bot-chats\/oc_group\/om_old$/);
  await page.getByRole('button', { name: '会话成员' }).click();
  await members.getByText('研发中心 / 平台组').waitFor();
  assert.equal(await members.locator('.person-avatar').getAttribute('data-avatar-tone'), titleTone);
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: '查看同步详情' }).click();
  const syncDialog = page.getByRole('dialog', { name: '会话同步详情' });
  await syncDialog.getByText('此部署已授权的 Bot 会话').waitFor();
  const groupSync = syncDialog.locator('details').filter({ hasText: group.name });
  await groupSync.locator('summary').click();
  await groupSync.getByText('部分历史').waitFor();
  assert.notEqual(await groupSync.getByText('覆盖截止').locator('..').locator('dd').textContent(), '暂无');
  assert.equal(await groupSync.getByText('待同步话题').locator('..').locator('dd').textContent(), '1');
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: '刷新会话' }).click();
  await page.locator('[data-sync]').getByText('已请求同步').waitFor();
  assert.deepEqual(pageErrors, []);
});

test('同步断网保留记录，撤权即时清理，窄屏还焦到重绘后的旧消息搜索结果', { skip: !packageName }, async t => {
  const { chromium } = require(packageName);
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } });
  const fixture = await mountFixture(page);
  await page.goto(origin + '/#/bot-chats');
  const directory = page.locator('.chat-directory');
  const timeline = page.locator('.chat-timeline');
  const refresh = page.getByRole('button', { name: '刷新会话' });
  const count = directory.locator('[data-count]');
  const groupRow = page.getByRole('button', { name: '打开会话 ' + group.name });
  const directRow = page.getByRole('button', { name: '打开会话 ' + direct.name });
  const result = directory.locator('.chat-result[data-message-id="om_old"]');
  const anchor = timeline.locator('[data-source-message]');
  const syncPosts = () => fixture.queries.filter(([method, path]) => method === 'POST' && path === '/sync').length;
  await timeline.getByText('任务已分派').waitFor();
  await page.getByRole('button', { name: '查看同步详情' }).click();
  await page.getByRole('dialog').getByText('当前群成员身份及本人的 1:1 会话').waitFor();
  await page.keyboard.press('Escape');
  await page.getByRole('searchbox', { name: '搜索会话或消息' }).fill('纪要');
  await result.click();
  await anchor.getByText('会议纪要').waitFor();
  assert.equal(await count.textContent(), '2');
  const originalResult = await result.elementHandle();

  fixture.abortRequests(() => true);
  await refresh.click();
  const syncRetry = page.getByRole('button', { name: '重试同步' });
  await syncRetry.waitFor();
  assert.match(page.url(), /oc_group\/om_old$/);
  assert.ok(await result.isVisible());
  assert.ok(await anchor.isVisible());
  assert.equal(await count.textContent(), '2');

  fixture.abortRequests((path, method) => method === 'GET' && (path === '' || path === '/search' || path.endsWith('/context')));
  await syncRetry.click();
  const directoryRetry = directory.getByRole('button', { name: '重试', exact: true });
  const messageRetry = timeline.getByRole('button', { name: '重试', exact: true });
  await directoryRetry.waitFor();
  await messageRetry.waitFor();
  assert.ok(await result.isVisible());
  assert.ok(await anchor.isVisible());
  assert.ok(await timeline.getByRole('link', { name: '查看关联任务' }).isVisible());

  fixture.abortRequests();
  await directoryRetry.click();
  await messageRetry.click();
  await directoryRetry.waitFor({ state: 'hidden' });
  await messageRetry.waitFor({ state: 'hidden' });
  await result.waitFor();
  assert.equal(await originalResult.evaluate(node => node.isConnected), false);
  assert.equal(syncPosts(), 2);
  await page.setViewportSize({ width: 320, height: 844 });
  await page.getByRole('separator').waitFor({ state: 'hidden' });
  await page.getByRole('button', { name: '返回会话列表' }).click();
  assert.ok(await result.evaluate(node => node === document.activeElement));
  assert.equal(await timeline.isVisible(), false);
  await result.press('Enter');
  await anchor.waitFor();
  await page.setViewportSize({ width: 1366, height: 900 });

  fixture.revoke();
  fixture.abortRequests(path => path === '' || path === '/search');
  await refresh.click();
  await timeline.getByText('当前账号已无法访问此会话。').waitFor();
  await directoryRetry.waitFor();
  assert.equal(await result.count(), 0);
  assert.equal(await anchor.count(), 0);
  assert.equal(await groupRow.count(), 0);
  assert.equal(await timeline.getByRole('link').count(), 0);
  assert.equal(await count.textContent(), '1');
  assert.ok(await directRow.isVisible());
  fixture.abortRequests();
  await directoryRetry.click();
  await directoryRetry.waitFor({ state: 'hidden' });
  await directRow.click();
  await timeline.getByText('收到，谢谢').waitFor();
});

test('手机飞书登录回跳、深链失权与触摸返回', { skip: !packageName }, async t => {
  const { chromium } = require(packageName);
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  const fixture = await mountFixture(page, { signedIn: false });
  const releaseDirectory = fixture.holdDirectory();
  t.after(releaseDirectory);
  await page.goto(origin + '/#/bot-chats/oc_group/om_old');
  await page.locator('#feishu-login').tap();
  await page.locator('[data-source-message]').waitFor();
  assert.ok(fixture.queries.some(([type, target]) => type === '/auth/feishu/start' && target === '/#/feishu/bot-chats/oc_group/om_old'));
  assert.equal(await page.getByRole('separator', { name: '调整会话列表宽度' }).count(), 0);
  assert.equal(await page.locator('.chat-directory').isVisible(), false);
  await page.getByRole('button', { name: '返回会话列表' }).tap();
  const listSearch = page.getByRole('searchbox', { name: '搜索会话或消息' });
  assert.ok(await listSearch.evaluate(node => node === document.activeElement));
  releaseDirectory();
  const row = page.getByRole('button', { name: '打开会话 北辰项目群' });
  await row.and(page.locator(':focus')).waitFor();
  assert.equal(await page.locator('.chat-reading').isVisible(), false);
  await page.goto(origin + '/#/feishu/bot-chats/oc_group/om_deleted');
  await page.getByText('这条消息已撤回。').waitFor();
  fixture.revoke();
  await page.reload();
  await page.getByText('当前账号已无法访问此会话。').waitFor();
  await page.getByRole('button', { name: '返回会话列表' }).tap();
  await page.getByRole('button', { name: '打开会话 林晓' }).waitFor();
  assert.equal(await page.getByRole('button', { name: '打开会话 北辰项目群' }).count(), 0);
});

test('项目宿主复用会话组件，按容器宽度切换并忽略过期适配器响应', { skip: !packageName }, async t => {
  const { chromium } = require(packageName);
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } });
  await mountFixture(page);
  const first = { ...group, id: 'project_first' };
  const second = { ...group, id: 'project_second', name: '第二个关联群' };
  const records = [first, second, ...Array.from({length:35}, (_,i)=>({...first,id:'other_'+i,name:'关联群 '+i}))];
  const projectAPI='/api/v1/projects/embed';
  await page.route(origin+projectAPI+'/**', route=>{
    const path=new URL(route.request().url()).pathname.slice(projectAPI.length);
    if(path==='/overview')return route.fulfill({json:{data:{workspace:{project:{id:'embed',name:'嵌入测试'}}}}});
    if(path==='/chat-reader')return route.fulfill({json:{items:records,total:records.length}});
    if(path.endsWith('/send-capabilities'))return route.fulfill({json:{can_send:false}});
    if(path==='/chat-reader/'+second.id+'/messages') {
      const messages=Array.from({length:30},(_,i)=>({...task,id:'history_'+i,chat_id:second.id,type:'text',body:'第二群记录 '+i}));
      return route.fulfill({json:{items:[{...task,chat_id:second.id},...messages]}});
    }
    return route.fulfill({json:{items:records.filter(record=>path.endsWith('/'+record.id))}});
  });
  await page.addInitScript(({path})=>{
    const original=window.fetch.bind(window);
    window.fetch=(input,options)=>{
      if(new URL(input instanceof Request?input.url:input,location.href).pathname===path)
        return new Promise(resolve=>{window.lateProjectMessages=data=>resolve(new Response(JSON.stringify(data),{headers:{'Content-Type':'application/json'}}));});
      return original(input,options);
    };
  },{path:projectAPI+'/chat-reader/'+first.id+'/messages'});
  await page.goto(origin+'/#/projects/embed/chats');
  await page.locator('.project-chat-reader').evaluate(host=>{
    host.style.cssText='display:flex; inline-size:760px; flex:0 0 430px';
  });
  await page.getByRole('heading', { name: '项目关联群' }).waitFor();
  await page.waitForFunction(() => typeof window.lateProjectMessages === 'function');
  await page.getByRole('button', { name: '打开会话 第二个关联群' }).click();
  await page.getByText('第二群记录 29').waitFor();
  await page.locator('[data-project-chat] .chat-card').getByRole('heading', { name: '任务已分派' }).waitFor();
  await page.evaluate(message => window.lateProjectMessages({ items: [message] }),
    { ...oldMessage, body: '迟到的项目会话' });
  assert.equal(await page.getByText('迟到的项目会话').count(), 0);
  assert.ok(await page.locator('.chat-timeline').evaluate(node => node.scrollHeight > node.clientHeight));
  assert.ok(await page.locator('.chat-conversations').evaluate(node => node.scrollHeight > node.clientHeight));
  await page.locator('.chat-conversations').evaluate(node => { node.scrollTop = 200; });
  assert.ok(await page.locator('.chat-timeline').evaluate(node => node.scrollTop > 0));
  await page.locator('.project-chat-reader').evaluate(host=>{host.style.inlineSize='470px';});
  await page.getByRole('separator', { name: '调整会话列表宽度' }).waitFor({ state: 'hidden' });
  await page.getByRole('button', { name: '返回会话列表' }).click();
  assert.ok(await page.getByRole('button', { name: '打开会话 第二个关联群' }).evaluate(node => node === document.activeElement));
  await page.getByRole('link',{name:'返回项目列表'}).click();
  await page.getByRole('heading',{name:'项目列表'}).waitFor();
});

test('按桌面和手机视口核对原型的浅色及深色聊天界面', { skip: !packageName }, async t => {
  const { chromium } = require(packageName);
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const evidence = process.env.OMNIRELAY_CHAT_EVIDENCE;
  if (evidence) await mkdir(evidence, { recursive: true });
  for (const viewport of [{ width: 1366, height: 900 }, { width: 390, height: 844 }]) {
    const page = await browser.newPage({ viewport, hasTouch: viewport.width < 500, isMobile: viewport.width < 500 });
    await mountFixture(page);
    for (const theme of ['light', 'dark']) {
      const results = [];
      for (const [version, path] of [['prototype', '/prototype/omni-relay-workbench.html'], ['workbench', '/']]) {
        await page.goto(origin + path + '?theme=' + theme + '#/feishu/bot-chats');
        await page.locator('.chat-conversation').first().waitFor();
        results.push(await page.evaluate(() => {
          const directory = document.querySelector('.chat-directory');
          const reading = document.querySelector('.chat-reading');
          const tools = document.querySelector('.chat-directory-tools');
          const split = document.querySelector('.chat-split-handle');
          const panelStyle = getComputedStyle(directory);
          return {
            theme: document.documentElement.dataset.theme,
            background: panelStyle.backgroundColor,
            border: panelStyle.borderColor,
            radius: panelStyle.borderRadius,
            padding: getComputedStyle(tools).padding,
            reading: getComputedStyle(reading).display,
            split: getComputedStyle(split).display
          };
        }));
        if (viewport.width < 500) await page.locator('.chat-conversation').first().click();
        const composer = page.locator('.chat-composer');
        await composer.waitFor();
        const geometry = await composer.evaluate(form => {
          const style = node => getComputedStyle(node);
          const box = form.querySelector('.chat-composer-box');
          const toolbar = form.querySelector('.chat-composer-tools');
          return { padding: style(form).padding, border: style(box).border, radius: style(box).borderRadius,
            fill: style(box).backgroundColor, textSize: style(form.querySelector('textarea')).fontSize,
            buttons: [...toolbar.children].map(node => [node.offsetWidth, node.offsetHeight]),
            fits: box.scrollWidth <= box.clientWidth + 1 };
        });
        assert.ok(geometry.fits);
        Object.assign(results.at(-1), { composer: geometry });
        if (evidence) await page.screenshot({ path: evidence + '/' + version + '-' + viewport.width + '-' + theme + '.png' });
        if (version === 'prototype' && viewport.width === 1366 && theme === 'light') {
          const draft = page.getByRole('textbox', { name: '消息内容' });
          await draft.fill('分栏调整前的草稿');
          const split = page.getByRole('separator', { name: '调整会话列表宽度' });
          const current = page.locator('[data-chat-id][aria-current="true"]');
          const selected = await current.getAttribute('data-chat-id');
          const handle = await split.boundingBox();
          await page.mouse.move(handle.x + handle.width / 2, handle.y + 100);
          await page.mouse.down();
          await page.mouse.move(handle.x + 80, handle.y + 100, { steps: 5 });
          await page.mouse.up();
          assert.equal(await draft.inputValue(), '分栏调整前的草稿');
          assert.equal(await current.getAttribute('data-chat-id'), selected);
        }
      }
      assert.deepEqual(results[1], results[0]);
    }
    await page.close();
  }
});

test('飞书卡片保留标题、正文、操作和说明，并适配主题和屏幕宽度', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const evidence = process.env.OMNIRELAY_CHAT_EVIDENCE;
  if (evidence) await mkdir(evidence, { recursive: true });
  const card = { ...task, business_receipt: undefined, body: JSON.stringify({
    title: '测试群 321 · 任务进度验收',
    elements: [
      [{ tag: 'text', text: 'OmniGrid 四项能力验收 20261113', style: ['bold'] },
        { tag: 'text', text: '\n负责人：徐世超\n截止：2026-11-14 18:00（北京时间）\n当前测试进度：0%\n请点击下方提交50%进度，用于核对真实回调与任务更新。' }],
      [{ tag: 'button', text: '提交50%进度（测试）', type: 'primary' },
        { tag: 'button', text: '打开原生任务', type: 'default' }],
      [{ tag: 'note', elements: [{ tag: 'text', text: '仅用于本次验收；卡片已发送不代表进度已保存。' }] }]
    ]
  }) };
  const originalCard = { ...briefing, business_receipt: undefined, body: JSON.stringify({
    schema: '2.0', header: { template: 'green', title: { content: '项目进展' } }, body: { elements: [
      { tag: 'markdown', content: '**已更新** <img src=x>' },
      { tag: 'button', text: { content: '打开详情' }, behaviors: [{ type: 'open_url', default_url: 'https://example.test/task' }] },
      { tag: 'button', text: '无效链接', url: 'javascript:alert(1)' }
    ] }
  }) };
  const summary = { ...task, id:'om_legacy', business_receipt:undefined,
    body:JSON.stringify({title:'旧卡片',elements:[[{tag:'img',image_key:'cover'},{tag:'text',text:'请升级至最新版本客户端，以查看内容'}]]}),
    resources:[{kind:'image',key:'cover'}] };
  const illustrated = { ...task, id:'om_picture', business_receipt:undefined,
    body:JSON.stringify({schema:'2.0',header:{title:{content:'原始卡片'}},body:{elements:[{tag:'img',img_key:'picture'}]}}),
    resources:[{kind:'image',key:'picture'}] };
  const malformed = { ...task, id: 'om_bad_card', body: '{', business_receipt: undefined };
  const deleted = { ...card, id: 'om_deleted_card', state: 'deleted' };
  const headers = [];
  for (const viewport of [{ width: 1366, height: 900 }, { width: 390, height: 844 }]) {
    const mobile = viewport.width < 500;
    const page = await browser.newPage({ viewport, isMobile: mobile, hasTouch: mobile });
    await mountFixture(page);
    await page.route(origin + base + '/oc_group/{messages,search}*', route => {
      const url = new URL(route.request().url());
      return route.fulfill({ json: { items: url.searchParams.has('q') ? [card] : [deleted, malformed, summary, illustrated, originalCard, card] } });
    });
    for (const theme of ['light', 'dark']) {
      await page.goto(origin + '/?theme=' + theme + '#/feishu/bot-chats/' + group.id);
      const message = page.locator('[data-message="om_task"]');
      const heading = message.getByRole('heading', { name: '测试群 321 · 任务进度验收' });
      await heading.waitFor();
      assert.equal(await message.locator('.chat-bubble').count(), 0);
      const preview = message.locator('.chat-card');
      const body = preview.locator('.chat-card-content');
      assert.match(await body.locator('.chat-card-row').first().textContent(), /\n负责人：徐世超\n截止：/u);
      assert.equal(await body.locator('strong').textContent(), 'OmniGrid 四项能力验收 20261113');
      const buttons = body.locator('.chat-card-actions').getByRole('button');
      assert.equal(await buttons.count(), 2);
      assert.ok(await buttons.first().isDisabled());
      assert.ok(await buttons.last().isDisabled());
      assert.ok(await body.locator('.chat-card-note').isVisible());
      if (evidence) await preview.screenshot({ path: evidence + '/card-' + viewport.width + '-' + theme + '.png' });
      const geometry = await preview.evaluate(node => ({
        width: node.offsetWidth,
        fits: node.scrollWidth <= node.clientWidth + 1 && node.clientWidth <= node.closest('.chat-timeline').clientWidth,
        header: getComputedStyle(node.querySelector('.chat-card-header')).backgroundColor,
        surface: getComputedStyle(node).backgroundColor,
        actionHeight: node.querySelector('button').offsetHeight
      }));
      assert.ok(geometry.fits && geometry.width <= 605);
      assert.notEqual(geometry.header, geometry.surface);
      assert.ok(geometry.actionHeight >= (mobile ? 44 : 34));
      headers.push(geometry.header);
    }
    const oldCard = page.locator('[data-message="om_legacy"]');
    assert.equal(await oldCard.locator('.chat-resource-image').count(),0);
    assert.ok(await oldCard.getByRole('link',{name:'在飞书中打开会话'}).getAttribute('href'));
    const illustratedMessage=page.locator('[data-message="om_picture"]');
    assert.equal(await illustratedMessage.locator('.chat-resource-image').count(),1);
    assert.equal(await illustratedMessage.locator('.chat-card .chat-resource-image').count(),1);
    const raw = page.locator('[data-message="om_brief"] .chat-card');
    const link = raw.getByRole('link', { name: '打开详情' });
    const target = 'https://example.test/task';
    assert.equal(await link.getAttribute('href'), target);
    await page.context().route(target, route => route.fulfill({ body: '任务详情' }));
    await link.focus();
    assert.equal(await link.evaluate(node => document.activeElement === node), true);
    const [popup] = await Promise.all([page.waitForEvent('popup'), link.press('Enter')]);
    await popup.waitForURL(target);
    await popup.close();
    assert.ok(await raw.getByRole('button', { name: '无效链接' }).isDisabled());
    assert.equal(await raw.locator('img').count(), 0);
    assert.equal(await raw.getByText('已更新', { exact: true }).evaluate(node => node.tagName), 'STRONG');
    assert.equal(await page.locator('[data-message="om_bad_card"] .chat-bubble').innerText(), '卡片消息');
    assert.equal(await page.locator('[data-message="om_deleted_card"] .chat-bubble').innerText(), '这条消息已撤回。');
    await page.getByRole('button', { name: '搜索当前会话' }).click();
    await page.getByRole('searchbox', { name: '搜索当前会话' }).fill('进度验收');
    await page.locator('.chat-card-header mark').getByText('进度验收', { exact: true }).waitFor();
    await page.close();
  }
  assert.notEqual(headers[0], headers[1]);
  assert.deepEqual(headers.slice(0, 2), headers.slice(2));
});

test('两个消息入口展示独立飞书文档卡片并直接预览同步正文', {skip: !packageName}, async t => {
  const browser = await require(packageName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const png = await readFile(new URL('../../design/prototype/omni-relay-avatar.png', import.meta.url));
  const messages = [
    {...oldMessage, id:'om_pdf', type:'file', body:'{"file_name":"操作手册.pdf"}', resources:[{kind:'attachment', key:'pdf', name:'操作手册.pdf'}]},
    {...oldMessage, id:'om_image', type:'image', body:'{"image_key":"img"}', resources:[{kind:'image', key:'img'}]},
    ...['wiki', 'sheets', 'minutes', 'docx'].map(kind => {
      const url = 'https://example.feishu.cn/' + kind + '/example';
      const titles = {wiki:'知识库周报',sheets:'费用明细',minutes:'评审会议',docx:'操作指南'};
      const resources = [{kind, key:'example', name:titles[kind], url, object_kind:kind === 'wiki' ? 'docx' : kind, state:'ready'}];
      if (kind === 'docx') resources.push(
        {kind, key:'example-long', name:'', url:url + '-long', state:'resource_forbidden'},
        {kind, key:'missing-scope', name:'', url:url + '-scope', state:'application_scope_missing'},
      );
      const links = resources.map(resource => resource.url).join(' ');
      return {...oldMessage, id:'om_' + kind, body:JSON.stringify({text:kind === 'minutes' ? '请查看 ' + links : links}), resources};
    }),
    {...oldMessage, id:'om_unsupported', type:'file', body:'{"file_name":"工具.exe"}', resources:[{kind:'attachment', key:'exe', name:'工具.exe'}]},
    {...oldMessage, id:'om_deleted', type:'file', state:'deleted', resources:[{kind:'attachment', key:'gone', name:'已撤回.pdf'}]}
  ];
  for (const project of [false, true]) {
    const width = project ? 390 : 1366;
    const page = await browser.newPage({viewport:{width, height:844}, isMobile:project, hasTouch:project});
    page.setDefaultTimeout(7000);
    await mountFixture(page);
    const prefix = project ? '/api/v1/projects/p1/chat-reader' : base;
    const hash = (project ? '#/projects/p1/chats/' : '#/feishu/bot-chats/') + group.id;
    let failed = true;
    let imageRecalled = false;
    let pending = true;
    if (project) {
      await page.route(origin + '/api/v1/projects/p1/overview**', route => route.fulfill({json:{data:{workspace:{project:{id:'p1', name:'北辰'}}}}}));
      await page.route(origin + '/api/v1/projects/p1/chats**', route => route.fulfill({json:{items:[group], primary_version:0}}));
    }
    await page.route(origin + prefix + '**', route => {
      const {pathname, searchParams} = new URL(route.request().url());
      const success = json => route.fulfill({json});
      const failure = (status, error, kind) => route.fulfill({status, json:{error, kind}});
      if (pathname.endsWith('/sync')) return success({items:[]});
      if (pathname.endsWith('/send-capabilities')) return success({can_send:false});
      if (!pathname.includes('/messages')) return success({items:[group], total:1});
      if (!pathname.includes('/resources/')) {
        const items = [...messages].reverse().map(item => item.id === 'om_sheets' && pending ?
          {...item,resources:item.resources.map(resource => ({...resource,name:'',state:'pending'}))} : item);
        pending = false;
        return success({items});
      }
      const [,kind,key] = pathname.match(/\/resources\/([^/]+)\/([^/]+)$/);
      const pageNumber = Number(searchParams.get('page') ?? 1);
      const meta = {page:pageNumber, pages:2, total:2};
      if (kind === 'image') return imageRecalled ? failure(410, 'message_deleted') : route.fulfill({body:png,contentType:'image/png'});
      if (key === 'exe') return success({stage:'unsupported'});
      if (kind === 'attachment') {
        if (failed) { failed = false; return failure(503, 'source_unavailable'); }
        return success({stage:'ready', kind:'pdf', meta, png:png.toString('base64')});
      }
      if (kind === 'sheets') return success({stage:'ready', kind:'xlsx', meta:{...meta, sheets:['明细','汇总'], sheet:0}, data:[['表格预览']]});
      return success({stage:'ready', kind:'docs', meta, data:{text:kind === 'minutes' ? '会议逐字稿' : '知识库正文'}});
    });
    await page.goto(origin + '/?theme=' + (project ? 'dark' : 'light') + hash);
    const dialog = page.getByRole('dialog');
    const close = () => dialog.getByRole('button',{name:'关闭对话框'}).click();
    const doc = page.locator('[data-message="om_docx"]');
    await doc.getByRole('button',{name:/操作指南/u}).waitFor();
    assert.ok(!(await doc.innerText()).includes('https://'));
    const sourceLink = doc.getByRole('link',{name:'查看关联任务',exact:true});
    if (project) {
      const copy = doc.getByRole('button',{name:'复制消息',exact:true});
      assert.equal(await sourceLink.getAttribute('href'),'#/projects/p1/tasks/source/message/om_docx?chat_id=oc_group');
      assert.equal(await sourceLink.innerText(),'');
      assert.ok(await sourceLink.locator('svg use').count());
      const [a,b]=await Promise.all([sourceLink.boundingBox(),copy.boundingBox()]);
      assert.ok(a && b && a.y===b.y && a.x+a.width<=b.x && b.width>=44 && b.height>=44);
    } else assert.equal(await sourceLink.count(),0);
    assert.equal(await page.locator('[data-message="om_deleted"] .chat-message-actions').count(),0);
    const sheetCard = page.locator('[data-message="om_sheets"] .chat-document');
    await sheetCard.getByText('正在同步').waitFor();
    await sheetCard.getByText('费用明细').waitFor();
    assert.equal(await sheetCard.isEnabled(),true);
    const icon = kind => page.locator('[data-message="om_' + kind + '"] .chat-document img').first();
    assert.equal(await icon('wiki').getAttribute('src'),await icon('docx').getAttribute('src'));
    assert.notEqual(await icon('sheets').getAttribute('src'),await icon('wiki').getAttribute('src'));
    await page.waitForFunction(() => [...document.querySelectorAll('.chat-document img')].every(img => img.naturalWidth > 0));
    const longer = doc.getByRole('button',{name:/机器人无权读取或来源已失效/u});
    assert.equal(await longer.getAttribute('data-resource'), '1');
    assert.equal(await longer.isDisabled(),true);
    assert.equal(await doc.getByRole('button',{name:/机器人应用缺少此类型的读取权限/u}).isDisabled(),true);
    const pdf = page.getByRole('button',{name:/操作手册.pdf/u});
    await pdf.click();
    await dialog.getByText('暂时无法读取来源，请重试。').waitFor();
    await dialog.getByRole('button',{name:'重试'}).click();
    await dialog.getByRole('img',{name:'PDF 第 1 页'}).waitFor();
    const box = await dialog.boundingBox();
    assert.ok(Math.abs(box.x + box.width - width) <= 2 && box.width <= width);
    await dialog.getByRole('button',{name:'下一页'}).click();
    await dialog.getByRole('img',{name:'PDF 第 2 页'}).waitFor();
    await page.keyboard.press('Escape');
    await dialog.waitFor({state:'hidden'});
    assert.ok(await pdf.evaluate(node => node === document.activeElement));
    const image = page.getByRole('button',{name:'放大图片'});
    await image.scrollIntoViewIfNeeded();
    await page.waitForFunction(() => document.querySelector('.chat-resource-image img')?.naturalWidth > 0);
    await image.click();
    await dialog.getByRole('img',{name:'图片预览'}).waitFor();
    await close();
    imageRecalled = true;
    await image.click();
    await dialog.getByText('这条消息已撤回。').waitFor();
    await close();
    assert.equal(await page.getByRole('button',{name:/已撤回.pdf/u}).count(), 0);
    await page.getByRole('button',{name:/工具.exe/u}).click();
    await dialog.getByText('此格式暂不支持预览。').waitFor();
    await close();
    for (const [kind, body] of [['wiki','知识库正文'], ['sheets','表格预览'], ['minutes','会议逐字稿']]) {
      await page.locator('[data-message="om_' + kind + '"] .chat-document').click();
      await dialog.getByText(body, {exact:true}).waitFor();
      assert.equal(await dialog.getByRole('link',{name:'授权飞书'}).count(),0);
      await close();
    }
    await page.close();
  }
});

test('共享发送框按会话保存草稿并按原操作对账', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage();
  await mountFixture(page);
  const sends = [], uploads = [];
  let uploadFails = true, result = 'unknown';
  const parts = () => [
    { position: 0, kind: 'post', state: 'success', message_id: 'om_text' },
    { position: 1, kind: 'image', attachment_id: 'image_ref', state: result,
      message_id: result === 'success' ? 'om_image' : undefined }
  ];
  await page.route('**/send-capabilities', route => route.fulfill({ json: {
    can_send: true, sender: { kind: 'bot', name: '测试机器人' }, mentions: true,
    max_file_bytes: 10000, max_image_bytes: 10000, max_attachments: 5
  } }));
  await page.route('**/members?purpose=mention*', route => route.fulfill({ json: {
    items: [{ name: '林晓', mention_open_id: 'ou_user' }], total: 1
  } }));
  await page.route('**/oc_group/attachments?kind=image', route => {
    uploads.push(route.request().headers());
    return route.fulfill(uploadFails ? { status: 422, json: { error: 'attachment_failed' } } :
      { status: 201, json: { id: 'image_ref', expires_at: new Date(Date.now() + 60000).toISOString() } });
  });
  await page.route('**/oc_group/messages', route => {
    if (route.request().method() !== 'POST') return route.fallback();
    const body = route.request().postDataJSON();
    sends.push(body);
    return route.fulfill({ json: { operation_key: body.operation_key, chat_id: group.id, state: result, parts: parts() } });
  });
  await page.route('**/oc_group/deliveries/*', route => {
    assert.ok(route.request().url().endsWith('/' + sends.at(-1).operation_key));
    return route.fulfill({ json: { operation_key: sends.at(-1).operation_key, chat_id: group.id, state: result, parts: parts() } });
  });
  await page.goto(origin + '/#/feishu/bot-chats/oc_group');
  const box = page.locator('.chat-composer'), editor = box.locator('textarea');
  const tool = name => box.getByRole('button', { name, exact: true });
  await box.getByText('发送身份：机器人 测试机器人').waitFor();
  await editor.fill('  ');
  assert.ok(await tool('发送消息').isDisabled());
  await editor.fill('正文');
  await editor.press('ControlOrMeta+a');
  await tool('文本格式').click();
  await tool('加粗').click();
  await editor.press('End');
  await editor.press('Shift+Enter');
  await tool('表情').click();
  await tool('🎉').click();
  await tool('提及成员').click();
  const trigger = await tool('提及成员').boundingBox();
  const choices = await box.getByRole('group', { name: '提及成员' }).boundingBox();
  assert.ok(choices.x <= trigger.x + trigger.width && choices.x + choices.width >= trigger.x);
  assert.ok(choices.y + choices.height <= trigger.y);
  await tool('林晓').click();
  await tool('展开输入框').click();
  assert.equal(await tool('收起输入框').getAttribute('aria-expanded'), 'true');
  await editor.dispatchEvent('keydown', { key: 'Enter', isComposing: true });
  assert.equal(sends.length, 0);
  await box.locator('[type=file]').setInputFiles({ name: '图.png', mimeType: 'image/png', buffer: Buffer.from([137,80,78,71]) });
  await tool('重试上传').waitFor();
  assert.ok(await tool('发送消息').isDisabled());
  uploadFails = false;
  await tool('重试上传').click();
  await box.getByText('图.png · 已上传').waitFor();
  assert.match(uploads[1]['content-type'], /^multipart\/form-data; boundary=/);
  assert.equal(uploads[1]['x-csrf-token'], 'fixture-csrf');
  await page.locator('[data-chat-id="oc_direct"]').first().click();
  assert.equal(await editor.inputValue(), '');
  await page.locator('[data-chat-id="oc_group"]').first().click();
  assert.equal(await editor.inputValue(), '**正文**\n🎉@林晓 ');
  assert.deepEqual(await box.locator('.chat-composer-mirror mark').allTextContents(), ['@林晓']);
  await editor.press('Enter');
  await tool('查询发送结果').waitFor();
  assert.deepEqual(sends[0].paragraphs, [[{ text: '正文', styles: ['bold'] }], [{ text: '🎉' }, { mention_open_id: 'ou_user' }, { text: ' ' }]]);
  assert.deepEqual(sends[0].attachments, ['image_ref']);
  assert.ok(await tool('发送消息').isDisabled());
  await page.reload();
  result = 'failed';
  await tool('查询发送结果').click();
  await tool('编辑未发送部分').waitFor();
  assert.equal(await editor.inputValue(), '**正文**\n🎉@林晓 ');
  await box.getByText('图.png · 已上传').waitFor();
  await tool('编辑未发送部分').click();
  assert.equal(await editor.inputValue(), '');
  await page.route('**/oc_group/messages', route => {
    if (route.request().method() !== 'POST') return route.fallback();
    const body = route.request().postDataJSON();
    sends.push(body);
    return route.fulfill({ json: { operation_key: body.operation_key, chat_id: group.id, state: 'success',
      parts: [{ position: 0, attachment_id: 'image_ref', state: 'success', message_id: 'om_image' }] } });
  });
  await tool('发送消息').click();
  await box.getByText(/飞书已确认接收/).waitFor();
  assert.equal(sends.length, 2);
  assert.deepEqual(sends[1].attachments, ['image_ref']);
  assert.equal(sends[1].paragraphs, undefined);
  assert.equal(await box.locator('li').count(), 0);
});

test('私聊发信确认后直接出现在列表，刷新后仍只有一条', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const viewport of [{ width: 1366, height: 900 }, { width: 390, height: 844 }]) {
    for (const theme of ['light', 'dark']) {
      const mobile = viewport.width < 500;
      const page = await browser.newPage({ viewport, isMobile: mobile, hasTouch: mobile });
      const fixture = await mountFixture(page);
      let sends = 0;
      const sent = {
        ...p2pMessage, id: 'om_bot_sent', sender: { name: '测试机器人' }, sender_kind: 'app',
        body: JSON.stringify({ text: '发送后无需手动同步' }), sent_at: '2026-11-19T16:15:00Z'
      };
      await page.route('**/oc_direct/send-capabilities', route => route.fulfill({ json: {
        can_send: true, sender: { kind: 'bot', name: '测试机器人' }
      } }));
      await page.route('**/oc_direct/messages', route => {
        if (route.request().method() === 'POST') {
          sends++;
          const { operation_key, text } = route.request().postDataJSON();
          assert.equal(text, '发送后无需手动同步');
          return route.fulfill({ json: {
            operation_key, chat_id: direct.id, state: 'success',
            parts: [{ position: 0, kind: 'text', state: 'success', message_id: sent.id }]
          } });
        }
        return route.fulfill({ json: { items: sends ? [sent, p2pMessage] : [p2pMessage] } });
      });
      await page.goto(origin + '/?theme=' + theme + '#/feishu/bot-chats/oc_direct');
      await page.getByText('收到，谢谢', { exact: true }).waitFor();
      await page.getByRole('textbox', { name: '消息内容' }).fill('发送后无需手动同步');
      await page.getByRole('button', { name: '发送消息' }).click();
      const row = page.locator('[data-message="om_bot_sent"][data-author="bot"]');
      await row.getByText('发送后无需手动同步', { exact: true }).waitFor();
      await page.reload();
      await row.getByText('发送后无需手动同步', { exact: true }).waitFor();
      assert.equal(await row.count(), 1);
      assert.equal(sends, 1);
      assert.ok(!fixture.queries.some(([method, path]) => method === 'POST' && path === '/sync'));
      await page.close();
    }
  }
});

test('群聊输入 @ 可筛选和选择成员，私聊按普通文本处理', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const width of [1366, 390]) for (const theme of ['light', 'dark']) {
    const page = await browser.newPage({ viewport: { width, height: 844 }, hasTouch: width === 390, isMobile: width === 390 });
    page.setDefaultTimeout(8000);
    await mountFixture(page);
    const sends = [], memberRequests = [];
    await page.route('**/send-capabilities', route => route.fulfill({ json: {
      can_send: true, sender: { name: '测试机器人' }, mentions: route.request().url().includes('/oc_group/')
    } }));
    await page.route('**/members?purpose=mention*', route => {
      const { pathname, searchParams } = new URL(route.request().url());
      const offset = searchParams.get('offset');
      memberRequests.push(pathname);
      return route.fulfill({ json: {
        items: offset === '0' ? [{ name: '王宁', mention_open_id: 'ou_wang' }] : [{ name: '林晓', mention_open_id: 'ou_lin' }],
        total: 2
      } });
    });
    await page.route('**/oc_group/messages', route => {
      if (route.request().method() !== 'POST') return route.fallback();
      const body = route.request().postDataJSON();
      sends.push(body);
      return route.fulfill({ json: { operation_key: body.operation_key, chat_id: group.id, state: 'success',
        parts: [{ position: 0, state: 'success', message_id: 'om_mention' }] } });
    });
    await page.goto(origin + '/?theme=' + theme + '#/feishu/bot-chats/' + group.id);
    const box = page.getByRole('form', { name: '发送消息' });
    const editor = box.getByRole('textbox', { name: '消息内容' });
    const picker = box.getByRole('group', { name: '提及成员' });
    await box.getByText('发送身份：机器人 测试机器人').waitFor();
    await editor.fill('🎉');
    await page.keyboard.insertText('@');
    await picker.getByRole('button', { name: '王宁' }).waitFor();
    assert.ok(await editor.evaluate(node => node === document.activeElement));
    await page.keyboard.insertText('林');
    await picker.getByText('已加载成员中没有匹配项。').waitFor();
    await picker.getByRole('button', { name: '更多成员' }).click();
    const member = picker.getByRole('button', { name: '林晓' });
    await member.waitFor();
    const bounds = await picker.boundingBox();
    const field = await editor.boundingBox();
    assert.ok(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= width + 1);
    assert.ok(bounds.x >= field.x - 1 && bounds.x <= field.x + 30);
    assert.ok(bounds.y + bounds.height <= field.y + 8);
    if (width === 390) await member.tap();
    else {
      await editor.press('ArrowDown');
      assert.ok(await member.evaluate(node => node === document.activeElement));
      await member.press('Enter');
    }
    assert.deepEqual(sends, []);
    assert.equal(await editor.inputValue(), '🎉@林晓 ');
    const mirror = box.locator('.chat-composer-mirror');
    const marks = mirror.locator('mark');
    assert.deepEqual(await marks.allTextContents(), ['@林晓']);
    assert.ok(await marks.first().isVisible());
    assert.ok(await marks.first().evaluate(mark => getComputedStyle(mark).color !== getComputedStyle(mark.parentElement).color));
    await page.keyboard.insertText('请查收 ');
    await page.keyboard.insertText('@');
    await picker.getByRole('button', { name: '王宁' }).waitFor();
    if (process.env.OMNIRELAY_CHAT_EVIDENCE) {
      await page.screenshot({ path: process.env.OMNIRELAY_CHAT_EVIDENCE + '/mention-' + width + '-' + theme + '.png' });
    }
    await editor.press('Escape');
    await picker.waitFor({ state: 'hidden' });
    await page.keyboard.insertText('王宁');
    assert.deepEqual(await marks.allTextContents(), ['@林晓']);
    assert.equal(await mirror.textContent(), await editor.inputValue());
    await box.getByRole('button', { name: '提及成员' }).click();
    await picker.getByRole('button', { name: '王宁' }).click();
    assert.deepEqual(await marks.allTextContents(), ['@林晓', '@王宁']);
    await editor.press('Backspace');
    await editor.press('Backspace');
    assert.deepEqual(await marks.allTextContents(), ['@林晓']);
    await editor.press('Enter');
    await box.getByText('飞书已确认接收。').waitFor();
    assert.equal(sends.length, 1);
    assert.deepEqual(sends[0].paragraphs, [[{ text: '🎉' }, { mention_open_id: 'ou_lin' }, { text: ' 请查收 @王宁@王' }]]);

    await editor.fill('换行正文\n'.repeat(10));
    await editor.press('ControlOrMeta+End');
    await page.keyboard.insertText('@');
    await picker.getByRole('button', { name: '王宁' }).click();
    await page.keyboard.insertText(' 后续文字');
    await page.keyboard.insertText('＠');
    await picker.getByRole('button', { name: '王宁' }).waitFor();
    const scrolledField = await editor.boundingBox();
    const visibleMention = await marks.boundingBox();
    const scrolledPicker = await picker.boundingBox();
    assert.ok(await editor.evaluate(node => node.scrollTop > 0));
    assert.ok(visibleMention.y >= scrolledField.y && visibleMention.y + visibleMention.height <= scrolledField.y + scrolledField.height);
    assert.ok(scrolledPicker.y + scrolledPicker.height <= visibleMention.y);
    assert.ok(scrolledPicker.y + scrolledPicker.height >= visibleMention.y - 8);
    await editor.press('Escape');

    await page.goto(origin + '/?theme=' + theme + '#/feishu/bot-chats/' + direct.id);
    await box.getByRole('textbox', { name: '消息内容' }).and(page.getByPlaceholder('发送给 林晓')).waitFor();
    await box.getByText('发送身份：机器人 测试机器人').waitFor();
    assert.ok(await box.getByRole('button', { name: '提及成员' }).isDisabled());
    await editor.focus();
    await page.keyboard.insertText('@');
    assert.equal(await editor.inputValue(), '@');
    assert.ok(await picker.isHidden());
    assert.deepEqual(memberRequests, Array(6).fill(base + '/oc_group/members'));
    await page.close();
  }
});

test('分栏与切会话保留草稿，超时后并发恢复只投递一次', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const page = await browser.newPage();
  await mountFixture(page);
  const requests = [], queries = [];
  let delivered = 0;
  let release;
  const held = new Promise(resolve => { release = resolve; });
  await page.route('**/send-capabilities', route => route.fulfill({ json: {
    can_send: true, sender: { name: '回归机器人' }
  } }));
  await page.route('**/oc_group/messages', async route => {
    if (route.request().method() !== 'POST') return route.fallback();
    const body = route.request().postDataJSON();
    requests.push(body);
    if (requests.length === 1) {
      await held;
      return route.abort('timedout');
    }
    delivered++;
    return route.abort('timedout');
  });
  await page.route('**/oc_group/deliveries/*', route => {
    const key = new URL(route.request().url()).pathname.split('/').pop();
    queries.push(key);
    if (!delivered) return route.fulfill({ status: 404, json: { error: 'operation_not_found' } });
    return route.fulfill({ json: { operation_key: key, chat_id: group.id, state: 'success',
      parts: [{ position: 0, state: 'success', message_id: 'om_confirmed' }] } });
  });
  await page.goto(origin + '/#/feishu/bot-chats/oc_group');
  const editor = page.getByRole('textbox', { name: '消息内容' });
  const button = name => page.getByRole('button', { name, exact: true });
  await page.getByText('发送身份：机器人 回归机器人').waitFor();
  await editor.fill('第一行');
  await editor.dispatchEvent('compositionstart');
  await editor.dispatchEvent('keydown', { key: 'Enter' });
  assert.equal(await editor.getAttribute('readonly'), null);
  await editor.dispatchEvent('compositionend');
  await editor.press('Shift+Enter');
  await editor.press('End');
  await page.keyboard.insertText('第二行');
  const draft = '第一行\n第二行';
  const split = page.getByRole('separator', { name: '调整会话列表宽度' });
  await split.press('End');
  await split.press('Home');
  assert.equal(await editor.inputValue(), draft);
  await page.reload();
  await page.getByText('发送身份：机器人 回归机器人').waitFor();
  assert.equal(await editor.inputValue(), draft);
  const first = page.waitForRequest(req => req.method() === 'POST' && req.url().endsWith('/oc_group/messages'));
  await button('发送消息').evaluate(node => { node.click(); node.click(); });
  await first;
  await button('打开会话 林晓').click();
  await editor.fill('独立草稿');
  release();
  await button('打开会话 北辰项目群').click();
  await button('查询发送结果').click();
  await button('按原操作重试').evaluate(node => { node.click(); node.click(); });
  await button('查询发送结果').click();
  await page.getByText(/飞书已确认接收/).waitFor();
  assert.equal(delivered, 1);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].text, draft);
  assert.deepEqual(requests[1], requests[0]);
  assert.deepEqual(queries, [requests[0].operation_key, requests[0].operation_key]);
  assert.equal(await editor.inputValue(), '');
  await button('打开会话 林晓').click();
  assert.equal(await editor.inputValue(), '独立草稿');

  await page.route('**/oc_direct/messages', route => {
    if (route.request().method() !== 'POST') return route.fallback();
    return route.fulfill({ json: {
      operation_key: route.request().postDataJSON().operation_key, chat_id: direct.id, state: 'failed',
      parts: [{ position: 0, state: 'failed', reason: 'wrong_target_receipt' }]
    } });
  });
  await button('发送消息').click();
  await page.getByText(/飞书回执的收件会话与目标不符/).waitFor();
  assert.equal(await editor.inputValue(), '独立草稿');
  assert.equal(await page.getByText(/飞书已确认接收/).count(), 0);
});

test('项目群主群替换、改绑、失败重试和清除在两个入口即时一致', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const context = await browser.newContext();
  const global = await context.newPage();
  const scoped = await context.newPage();
  const projects = [{ id: 'p1', name: '北辰' }, { id: 'p2', name: '启明' }];
  const groups = [group, { ...group, id: 'oc_second', name: '候选群' }].map((row, i) => ({
    ...row, project_assignment: { project_id: 'p1', primary: i === 0, version: 1 }
  }));
  const candidate = groups[1];
  let primaryVersion = 1;
  let failOnce = true;
  let conflictOnce = true;
  const scopedCapabilities = new Set();
  for (const page of [global, scoped]) {
    await mountFixture(page);
    await page.route(/\/api\/v1\/(projects|feishu\/chats)(\/|\?|$)/, async route => {
      const { pathname } = new URL(route.request().url());
      const respond = json => route.fulfill({ json });
      const project = projects.find(item => pathname.startsWith('/api/v1/projects/' + item.id + '/'));
      if (pathname === '/api/v1/projects') return respond({ items: projects, total: projects.length });
      if (pathname.endsWith('/overview')) return respond({ data: { workspace: { project } } });
      const visible = groups.filter(item => !project || item.project_assignment.project_id === project.id);
      if (pathname.endsWith('/chats/primary')) {
        const body = route.request().postDataJSON();
        assert.deepEqual(body, { chat_id: candidate.id, assignment_version: 1, primary_version: primaryVersion });
        for (const item of visible) {
          item.project_assignment.primary = item === candidate;
          item.project_assignment.version++;
        }
        return respond({ chat_id: candidate.id, primary_version: ++primaryVersion });
      }
      if (pathname.endsWith('/project')) {
        const body = route.request().postDataJSON();
        assert.equal(body.version, candidate.project_assignment.version);
        if (failOnce) {
          failOnce = false;
          return route.fulfill({ status: 503, json: { error: 'project_unavailable' } });
        }
        if (!body.project_id && conflictOnce) {
          conflictOnce = false;
          candidate.project_assignment.version++;
          return route.fulfill({ status: 409, json: { error: 'project_conflict' } });
        }
        candidate.project_assignment = { project_id: body.project_id, version: body.version + 1, primary: false };
        return respond(candidate.project_assignment);
      }
      if (project && pathname.endsWith('/chats'))
        return respond({ primary_version: primaryVersion, items: visible.map(row => ({ ...row, ...row.project_assignment })) });
      const suffix = pathname.split(project ? '/chat-reader' : base)[1];
      if (suffix === '') return respond({ items: visible, total: visible.length });
      if (suffix === '/sync') return respond({ items: [], total: 0 });
      const item = visible.find(row => row.id === suffix?.split('/')[1]);
      if (!item) return route.fulfill({ status: 404, json: { error: 'chat_access_lost' } });
      if (suffix.endsWith('/send-capabilities')) {
        if (project) scopedCapabilities.add(item.id);
        return respond({ can_send: false, reason: 'send_disabled' });
      }
      if (suffix.endsWith('/context')) return respond({ items: [{ ...oldMessage, chat_id: item.id }] });
      if (suffix.endsWith('/messages')) return respond({ items: [] });
      return respond({ items: [item], total: 1 });
    });
  }
  // 两种视口和主题对照已确认原型的标题星标及归属按钮。
  const prototype = await context.newPage();
  for (const width of [1366, 390]) {
    const viewport = { width, height: width === 390 ? 844 : 900 };
    await Promise.all([scoped.setViewportSize(viewport), prototype.setViewportSize(viewport)]);
    for (const theme of ['light', 'dark']) {
      await scoped.goto(origin + '/?theme=' + theme + '#/projects/p1/chats/' + group.id);
      const reference = new URL('../../design/prototype/omni-relay-workbench.html', import.meta.url);
      reference.search = '?theme=' + theme;
      reference.hash = '/projects/beichen/chats/main';
      await prototype.goto(reference.href);
      const geometry = async page => {
        const star = page.locator('.chat-reading-title .chat-primary-star');
        await star.waitFor();
        const button = page.getByRole('button', { name: '修改项目归属' });
        return [
          await star.evaluate(el => [el.offsetWidth, el.offsetHeight, getComputedStyle(el).color]),
          await button.evaluate(el => [el.offsetWidth, el.offsetHeight, getComputedStyle(el).color])
        ];
      };
      assert.deepEqual(await geometry(scoped), await geometry(prototype));
    }
  }
  await prototype.close();
  await scoped.setViewportSize({ width: 1366, height: 900 });
  await global.goto(origin + '/#/feishu/bot-chats/' + candidate.id);
  await scoped.goto(origin + '/#/projects/p1/chats/' + candidate.id + '/' + oldMessage.id);
  await scoped.locator('[data-source-message]').waitFor();
  await scoped.locator('.chat-composer').waitFor();
  assert.ok(scopedCapabilities.has(candidate.id));
  await scoped.getByRole('button', { name: '设为项目主群', exact: true }).click();
  await scoped.getByText('主群将从「北辰项目群」更换为当前群。').waitFor();
  await scoped.getByRole('button', { name: '确认设置' }).click();
  for (const page of [global, scoped]) {
    await page.locator('[data-title] .chat-primary-star').waitFor();
    assert.equal(await page.locator('[data-chat-id="oc_group"] .chat-primary-star').count(), 0);
  }
  await scoped.getByRole('button', { name: '修改项目归属' }).click();
  await scoped.getByRole('combobox').fill('启');
  await scoped.getByRole('option', { name: '启明' }).click();
  await scoped.getByRole('button', { name: '保存', exact: true }).click();
  await scoped.getByRole('alert').filter({ hasText: '保存失败' }).waitFor();
  await global.locator('.chat-project-meta').getByRole('link', { name: '北辰' }).waitFor();
  await scoped.getByRole('button', { name: '重试', exact: true }).click();
  await global.locator('.chat-project-meta').getByRole('link', { name: '启明' }).waitFor();
  await scoped.locator('[data-chat-id="oc_second"]').waitFor({ state: 'detached' });
  assert.equal(await scoped.locator('.chat-reader-host a[href*="bot-chats"]').count(), 0);
  await scoped.goto(origin + '/#/projects/p2/chats/' + candidate.id);
  await scoped.locator('[data-title]').getByText('候选群').waitFor();
  assert.equal(await scoped.locator('[data-chat-id="oc_group"]').count(), 0);
  await scoped.locator('[data-title] .chat-primary-star').waitFor({ state: 'detached' });
  await Promise.all([scoped.reload(), global.reload()]);
  await scoped.locator('[data-title]').getByText('候选群').waitFor();
  assert.deepEqual(await scoped.locator('.chat-conversation').evaluateAll(rows => rows.map(row => row.dataset.chatId)), [candidate.id]);
  await global.locator('.chat-project-meta').getByRole('link', { name: '启明' }).waitFor();
  await scoped.goto(origin + '/#/projects/p1/chats/' + group.id);
  await scoped.locator('[data-title]').getByText(group.name).waitFor();
  assert.deepEqual(await scoped.locator('.chat-conversation').evaluateAll(rows => rows.map(row => row.dataset.chatId)), [group.id]);
  assert.equal(await scoped.locator('.chat-primary-star').count(), 0);
  await scoped.goto(origin + '/#/projects/p2/chats/' + candidate.id);
  await scoped.locator('[data-title]').getByText('候选群').waitFor();
  for (const attempt of [1, 2]) {
    await global.getByRole('button', { name: '修改项目归属' }).click();
    await global.getByRole('button', { name: '移除 启明' }).click();
    await global.getByRole('button', { name: '保存', exact: true }).click();
    if (attempt === 1) await global.getByRole('button', { name: '重新读取' }).click();
  }
  await global.locator('.chat-project-meta').getByText('未关联项目').waitFor();
  await scoped.getByText('暂无项目关联群。').waitFor();
  await Promise.all([scoped.reload(), global.reload()]);
  await scoped.getByText('暂无项目关联群。').waitFor();
  await global.locator('.chat-project-meta').getByText('未关联项目').waitFor();
});

test('网盘显示未关联项目的文件，按真实同步状态刷新列表', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const viewport of [{width:1366,height:900}, {width:390,height:844}]) {
    for (const theme of ['light','dark']) {
      const page = await browser.newPage({viewport});
      await page.clock.install();
      await mountFixture(page);
      const status = {groups:1, history_pending:1, history_attention:0, unindexed_messages:3,
        unresolved_wiki:0, pending_sources:1, complete_sources:0, attention_sources:0};
      let completed = false;
      await page.route(origin + '/api/v1/feishu/files/sync', route => route.fulfill({json:completed ? {
        ...status, history_pending:0, unindexed_messages:0, pending_sources:0, complete_sources:1
      } : status}));
      await page.route(origin + '/api/v1/feishu/files?*', route => {
        return route.fulfill({json:{
          projects:{}, total:1, items:[{
            id:71, name:'未关联群的手册.pdf', category:'files', format_key:'pdf', format:'PDF',
            project_ids:[], inherited_project_ids:[], sources:[{chat_id:'oc_group',message_id:'om_old',chat_name:'北辰项目群'}],
            sync_state:completed ? 'complete' : 'pending', version_id:completed ? 1 : null
          }]
        }});
      });
      await page.goto(origin + '/?theme=' + theme + '#/feishu/drive');
      const progress = page.getByRole('region', {name:'文件同步进度'});
      await progress.getByRole('progressbar', {name:'正在同步群历史…'}).waitFor();
      assert.equal(await progress.getByRole('progressbar').getAttribute('aria-valuenow'), null);
      assert.match(await progress.innerText(), /待同步 1 条/);
      const file = page.locator('.drive-file-name');
      await file.getByText('未关联群的手册.pdf').waitFor();
      await page.getByRole('cell', {name:'等待同步', exact:true}).waitFor();
      assert.equal(await file.isDisabled(), true);
      completed = true;
      await page.clock.runFor(2300);
      await progress.getByText(/已同步 1 条来源/).waitFor();
      await page.getByRole('cell', {name:'已同步', exact:true}).waitFor();
      assert.equal(await file.isDisabled(), false);
      assert.equal(await progress.getByRole('progressbar').count(), 0);
      await page.close();
    }
  }
});

test('网盘的每个来源分别定位对应群的原消息', {skip:!packageName}, async t => {
  const browser = await require(packageName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const second = {id:'oc_second_group', kind:'group', name:'启明试点群'};
  const secondMessage = {...oldMessage, id:'om_second_group', chat_id:second.id, body:JSON.stringify({text:'来自启明的分享'})};
  for (const mobile of [false,true]) {
    const page = await browser.newPage({viewport:{width:mobile ? 390 : 1366,height:850}, isMobile:mobile, hasTouch:mobile});
    await mountFixture(page);
    await page.route(origin + base + '/' + second.id, route => route.fulfill({json:{items:[second],total:1}}));
    await page.route(origin + base + '/' + second.id + '/messages/' + secondMessage.id + '/context',
      route => route.fulfill({json:{items:[secondMessage], anchor_id:secondMessage.id}}));
    await page.route(origin + '/api/v1/feishu/files?*', route => route.fulfill({json:{total:1,projects:{},items:[{
      id:31,name:'同一文件.pdf',category:'files',format:'PDF',format_key:'pdf',project_ids:[],sync_state:'pending',
      sources:[{chat_id:group.id,chat_name:group.name,message_id:oldMessage.id,sender_name:person.name,sent_at},
        {chat_id:second.id,chat_name:second.name,message_id:secondMessage.id,sender_name:person.name,sent_at}]
    }]}}));
    await page.goto(origin + '/?theme=' + (mobile ? 'dark' : 'light') + '#/feishu/drive');
    const links = page.locator('tbody .drive-source');
    await links.last().waitFor();
    assert.deepEqual(await links.evaluateAll(nodes => nodes.map(link => link.getAttribute('aria-label'))),
      ['定位原消息：北辰项目群（来源 1）','定位原消息：启明试点群（来源 2）']);
    const open = link => mobile ? link.tap() : link.click();
    await open(links.first());
    await page.locator('[data-source-message]:focus').getByText('会议纪要').waitFor();
    await page.goBack();
    await open(links.last());
    await page.locator('[data-source-message]:focus').getByText('来自启明的分享').waitFor();
    assert.ok(page.url().endsWith('#/feishu/bot-chats/' + second.id + '/' + secondMessage.id));
    await page.close();
  }
});

test('文件网盘仅滚动文件行，短页的分页紧跟列表', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  const files = Array.from({length:51}, (_, i) => ({
    id:i + 1, name:'文件 ' + (i + 1), category:'files', format:'PDF', format_key:'pdf',
    sync_state:'complete', project_ids:[], sources:[]
  }));
  for (const width of [1366,390]) {
    const page = await browser.newPage({viewport:{width,height:844}, hasTouch:width === 390});
    await mountFixture(page);
    await page.route(origin + '/api/v1/projects?**', route => route.fulfill({json:{items:[],total:0}}));
    await page.route(origin + '/api/v1/feishu/files?**', route => {
      const params = new URL(route.request().url()).searchParams;
      const offset = Number(params.get('offset'));
      const rows = params.get('category') === 'all' ? files : [];
      return route.fulfill({json:{items:rows.slice(offset, offset + Number(params.get('limit'))), total:rows.length, projects:{}}});
    });
    await page.goto(origin + '/?theme=' + (width === 390 ? 'dark' : 'light') + '#/feishu/drive');
    await page.locator('select[data-size]').selectOption('50');
    await page.locator('tr[data-row="50"]').waitFor({state:'attached'});
    const scroller = page.locator('.list-scroll');
    const positions = () => page.locator('.head, .drive-categories, .list-toolbar, thead, .list-footer').evaluateAll(
      nodes => nodes.map(node => Math.round(node.getBoundingClientRect().top)));
    const before = await positions();
    const pageFit = await page.locator('#page').evaluate(node => ({
      overflow:node.scrollHeight - node.clientHeight,
      bottomGap:node.getBoundingClientRect().bottom - parseFloat(getComputedStyle(node).paddingBottom)
        - node.querySelector('.list-footer').getBoundingClientRect().bottom
    }));
    assert.ok(pageFit.overflow <= 1 && Math.abs(pageFit.bottomGap) <= 1, JSON.stringify(pageFit));
    await scroller.focus();
    await page.keyboard.press('End');
    await page.waitForFunction(() => document.querySelector('.list-scroll').scrollTop > 0);
    assert.deepEqual(await positions(), before);
    await page.locator('[data-next]').click();
    await page.locator('tr[data-row="51"]').waitFor();
    const shortPageFits = () => scroller.evaluate(node => {
      const content = node.querySelector('[data-empty]').hidden ? node.querySelector('table') : node.querySelector('[data-empty]');
      const footer = node.parentElement.querySelector('.list-footer').getBoundingClientRect();
      return node.scrollHeight === node.clientHeight && footer.top - content.getBoundingClientRect().bottom < 32
        && footer.bottom < innerHeight;
    });
    assert.ok(await shortPageFits(), '末页分页紧跟文件行');
    await page.locator('[data-drive-category="docs"]').click();
    await page.getByText('暂无文件。').waitFor();
    assert.ok(await shortPageFits(), '空列表分页紧跟空状态');
    await page.close();
  }
});

test('文件来源回焦、多项目单次保存、改名筛选和抽屉同步', { skip: !packageName }, async t => {
  const browser = await require(packageName).chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  t.after(() => browser.close());
  for (const width of [1366,390]) for (const theme of ['light','dark']) {
    const mobile = width === 390;
    const page = await browser.newPage({ viewport:{width,height:844}, isMobile:mobile, hasTouch:mobile });
    page.setDefaultTimeout(6000);
    const act = target => mobile ? target.tap() : target.click();
    const fixture = await mountFixture(page);
    let second = '启明';
    const row = {id:17,name:'验收签字单.pdf',category:'files',format:'PDF',format_key:'pdf',
      manual_project_ids:[], inherited_project_ids:['a'], project_ids:['a'],project_version:3,version_id:1,sync_state:'complete',sources:[oldMessage,oldMessage,task].map(message =>
        ({chat_id:group.id,message_id:message.id,chat_name:group.name,sender_name:person.name,sent_at}))};
    const other = {...row, id:18, name:'会议安排.pdf', project_ids:['a'],
      sources:[{...row.sources[0], message_id:recentMessage.id}]};
    await page.route(origin + base + '/' + group.id + '/messages/' + task.id + '/context', route =>
      route.fulfill({json:{items:[oldMessage,task],total:2,anchor_id:task.id}}));
    const otherContext = base + '/' + group.id + '/messages/' + recentMessage.id + '/context';
    let deleted = false;
    await page.route(origin + otherContext, route => route.fulfill(deleted
      ? {status:410,json:{error:'message_deleted'}}
      : {json:{items:[recentMessage],total:1,anchor_id:recentMessage.id}}));
    const patches = [];
    await page.route(origin + '/api/v1/projects?**', route =>
      route.fulfill({json:{items:[{id:'a',name:'北辰'},{id:'b',name:second}],total:2}}));
    await page.route(origin + '/api/v1/feishu/files**', route => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname.endsWith('/sync')) return route.fulfill({json:{groups:2,pending_sources:0,complete_sources:2}});
      if (url.pathname.endsWith('/projects')) {
        const change = request.postDataJSON();
        patches.push(change);
        if (change.project_version !== row.project_version) return route.fulfill({status:409,json:{}});
        row.manual_project_ids = row.manual_project_ids.filter(id => !change.remove_project_ids.includes(id)).concat(change.add_project_ids);
        row.project_ids = [...new Set([...row.inherited_project_ids,...row.manual_project_ids])];
        return route.fulfill({json:{project_version:++row.project_version}});
      }
      if (url.pathname.endsWith('/17')) return route.fulfill({json:row});
      const filter = url.searchParams.get('project_id');
      const items = [row,other].filter(file => !filter || file.project_ids.includes(filter));
      return route.fulfill({json:{items,total:items.length,projects:Object.fromEntries(row.project_ids.map(id => [id,id]))}});
    });
    await page.goto(origin + '/?theme=' + theme + '#/feishu/drive');
    await act(page.locator('[data-drive-category=files]'));
    const otherLink = page.locator('tbody [data-source-file="18"]');
    const sources = page.locator('tbody td').filter({has:page.locator('[data-source-file="17"]')});
    const locators = sources.getByRole('link');
    assert.deepEqual((await sources.innerText()).trim().split(/\s+/),[group.name,group.name]);
    assert.equal(await locators.count(),2);
    for (const [index, link] of (await locators.all()).entries()) {
      assert.equal(await link.getAttribute('aria-label'),'定位原消息：' + group.name + '（来源 ' + (index + 1) + '）');
      assert.equal(await link.locator('svg[aria-hidden="true"]').count(),1);
      const hit = await link.boundingBox();
      assert.ok(Math.min(hit.width,hit.height) >= (mobile ? 44 : 32));
    }
    if (process.env.OMNIRELAY_CHAT_EVIDENCE) {
      await locators.first().scrollIntoViewIfNeeded();
      await page.screenshot({path:process.env.OMNIRELAY_CHAT_EVIDENCE + '/drive-source-' + width + '-' + theme + '.png'});
    }
    await act(otherLink);
    await page.locator('[data-source-message]:focus').getByText('会议安排').waitFor();
    deleted = true;
    await page.reload();
    await page.getByText('这条消息已撤回。').waitFor();
    assert.equal(await page.locator('[data-source-message]').getByText('会议安排').count(),0);
    await page.goBack();
    const edit = page.locator('tbody [data-drive-projects="17"]');
    await act(edit);
    const input = page.getByRole('combobox', {name:'人工关联项目'});
    await input.fill('启');
    await act(page.getByRole('option',{name:'启明'}));
    assert.equal(await input.getAttribute('aria-expanded'),'true');
    const save = page.getByRole('button',{name:'保存',exact:true});
    await act(save);
    await edit.getByText('启明').waitFor();
    assert.deepEqual(patches,[{project_version:3,add_project_ids:['b'],remove_project_ids:[]}]);
    second = '启明新名称';
    await page.reload();
    await act(page.locator('[data-more]'));
    assert.equal(await page.locator('html').getAttribute('data-theme'),theme);
    await page.locator('[data-filter="project_id"]').selectOption({label:second});
    await page.keyboard.press('Escape');
    await edit.getByText(second).waitFor();
    const links = page.locator('tbody .drive-source');
    assert.equal(await links.count(),2);
    await act(links.first());
    await page.locator('[data-source-message]:focus').getByText('会议纪要').waitFor();
    await page.goBack();
    await links.first().and(page.locator(':focus')).waitFor();
    await act(edit);
    await act(page.getByRole('button',{name:'移除 ' + second}));
    const cancel = page.getByRole('button',{name:'取消',exact:true});
    await act(cancel);
    await act(page.getByRole('button',{name:'继续编辑'}));
    assert.equal(await page.locator('[data-remove=b]').count(),0);
    await act(cancel);
    await act(page.getByRole('button',{name:'不保存'}));
    assert.equal(patches.length,1);
    await page.route(origin + '/api/v1/feishu/files/17/versions/1/previews**', route => {
      if (route.request().url().endsWith('/result')) return route.fulfill({
        path:'design/prototype/omni-relay-logo.png', contentType:'image/png'
      });
      return route.fulfill({json:{version_id:1, task_id:'pdf', stage:'ready',
        meta:{page:1,pages:1,total:1,first:1,last:1}}});
    });
    await act(page.locator('.drive-file-name[data-drive-preview="17"]'));
    const pdf = page.getByRole('img',{name:'PDF 第 1 页'});
    await pdf.waitFor();
    const bounds = await page.locator('dialog[open]').boundingBox();
    assert.equal(Math.round(bounds.width), Math.min(720,width));
    assert.equal(await pdf.evaluate(node => node.closest('.dialog-body').scrollWidth <= node.closest('.dialog-body').clientWidth),true);
    await page.keyboard.press('Escape');
    await page.locator('.drive-file-name:focus').waitFor();
    await act(page.locator('.drive-file-name[data-drive-preview="17"]'));
    const drawer = page.locator('[data-metadata]');
    const routes = nodes => nodes.evaluateAll(anchors => anchors.map(link => link.hash));
    const expected = [oldMessage,task].map(message => '#/feishu/bot-chats/' + group.id + '/' + message.id);
    assert.deepEqual(await routes(links),expected);
    assert.deepEqual(await routes(drawer.locator('.drive-source')),expected);
    await act(drawer.locator('.drive-source').last());
    await page.locator('[data-source-message]:focus').getByText('任务已分派').waitFor();
    await page.goBack();
    await links.last().and(page.locator(':focus')).waitFor();
    await act(page.locator('.drive-file-name[data-drive-preview="17"]'));
    await act(drawer.locator('[data-drive-projects]'));
    await act(page.getByRole('button',{name:'移除 ' + second}));
    await act(save);
    await drawer.locator('[data-drive-projects]:focus').waitFor();
    assert.deepEqual(await drawer.locator('.kind').allTextContents(), ['北辰 · 来自群聊']);
    assert.equal(await edit.count(),0);
    assert.deepEqual(patches[1],{project_version:4,add_project_ids:[],remove_project_ids:['b']});
    await act(drawer.locator('[data-drive-projects]'));
    await act(page.getByRole('option',{name:second}));
    row.project_version++;
    await act(save);
    await page.getByText('归属已变化，请重新打开编辑并确认。',{exact:true}).waitFor();
    await act(page.locator('[data-more]'));
    await page.locator('[data-filter="project_id"]').selectOption('');
    await page.keyboard.press('Escape');
    await edit.getByText('北辰').waitFor();
    await act(edit);
    assert.equal(await page.locator('[data-remove=b]').count(),0);
    await act(page.getByRole('option',{name:second}));
    await act(cancel);
    await act(page.getByRole('button',{name:'保存并关闭'}));
    await edit.getByText(second).waitFor();
    assert.deepEqual(patches.slice(2), [
      {project_version:5,add_project_ids:['b'],remove_project_ids:[]},
      {project_version:6,add_project_ids:['b'],remove_project_ids:[]}
    ]);
    await page.reload();
    await edit.getByText(second).waitFor();
    assert.deepEqual(await routes(page.locator('tbody [data-source-file="17"]')),expected);
    await act(page.locator('.drive-file-name[data-drive-preview="17"]'));
    fixture.revoke();
    await act(drawer.locator('.drive-source').first());
    await page.getByText('当前账号已无法访问此会话。').waitFor();
    assert.equal(await page.getByText('会议纪要',{exact:true}).count(),0);
    fixture.restore();
    await act(page.locator('[data-action=retry-selection]'));
    await page.locator('[data-source-message]:focus').waitFor();
    await page.close();
  }
});

test('文件预览分页、工作表、目录、转换重试与撤权', {skip:!packageName}, async t => {
  const browser = await require(packageName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(6000);
  await mountFixture(page);
  let access = true, attempts = 0, releaseOldPage;
  const file = {id:8,version_id:53,category:'files',format:'ZIP',format_key:'zip',name:'归档.zip',
    project_ids:[],sources:[],sync_state:'partial'};
  await page.route(origin + '/api/v1/feishu/files**', async route => {
    const url = new URL(route.request().url());
    const respond = json => route.fulfill({json});
    if (url.pathname.endsWith('/sync')) return respond({groups:Number(access),pending_sources:0,complete_sources:Number(access)});
    if (!url.pathname.includes('/previews')) return respond({items:access ? [file] : [],total:access ? 1 : 0,projects:{}});
    if (!access) return route.fulfill({status:403,json:{error:'forbidden'}});
    const params = url.searchParams;
    const creating = route.request().method() === 'POST';
    const path = url.pathname.split('/');
    const id = creating ? [params.get('entry') || 0,params.get('sheet') || 0,params.get('page')].join('-') :
      path.at(-1) === 'result' ? path.at(-2) : path.at(-1);
    const [entry,sheet,current] = id.split('-').map(Number);
    if (creating) attempts++;
    const meta = {page:current,pages:2,total:2,first:current,last:current};
    if (entry) Object.assign(meta,{name:'账单.xlsx',sheet,sheets:['明细','汇总']});
    if (path.at(-1) === 'result' && entry && sheet === 1 && current === 2) {
      await new Promise(resolve => { releaseOldPage = resolve; });
    }
    if (path.at(-1) === 'result') return respond(entry ? [['工作表' + sheet + '页' + current]] :
      [{name:'账单.xlsx',entry:1,state:'ready'},{name:'加密文件',entry:2,state:'encrypted'}]);
    return respond({task_id:id,version_id:53,stage:creating ? 'queued' : attempts === 1 ? 'failed' : 'ready',meta});
  });
  await page.goto(origin + '/#/feishu/drive');
  await page.locator('.drive-file-name').click();
  const dialog = page.getByRole('dialog');
  await dialog.getByText('预览已排队').waitFor();
  await dialog.getByText('文件转换失败').waitFor();
  await dialog.getByRole('button',{name:'重试'}).click();
  await dialog.getByText(/部分结果/).waitFor();
  await dialog.getByRole('button',{name:'账单.xlsx'}).click();
  await dialog.getByText('工作表0页1').waitFor();
  await dialog.getByRole('button',{name:'下一页'}).click();
  assert.equal(await dialog.getByText('工作表0页1').count(),0);
  await dialog.getByText('工作表0页2').waitFor();
  await dialog.getByLabel('工作表').selectOption('1');
  await dialog.getByText('工作表1页1').waitFor();
  const oldResult = page.waitForRequest(request => request.url().endsWith('/1-1-2/result'));
  await dialog.getByRole('button',{name:'下一页'}).click();
  await oldResult;
  await dialog.getByRole('button',{name:'返回目录'}).click();
  await dialog.getByText(/加密文件/).waitFor();
  await dialog.getByRole('button',{name:'返回并刷新列表'}).waitFor();
  releaseOldPage();
  assert.equal(await dialog.getByText('工作表1页2').count(),0);
  access = false;
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await dialog.getByText('已无权查看此文件').waitFor();
  assert.equal(await page.getByText('归档.zip').count(),0);
  assert.equal(await dialog.locator('[data-preview-body]').innerText(),'');
  assert.equal(await dialog.locator('[data-metadata] *, [data-preview-controls] *, a[download]').count(),0);
  access = true;
  await page.keyboard.press('Escape');
  await page.reload();
  await page.clock.install();
  await page.locator('.drive-file-name').click();
  await dialog.getByRole('button',{name:'账单.xlsx'}).waitFor();
  access = false;
  await page.clock.fastForward(31000);
  await dialog.getByText('已无权查看此文件').waitFor();
  assert.equal(await dialog.locator('[data-preview-body]').innerText(),'');
});

test('文件预览格式失败后刷新列表并打开新版本', {skip:!packageName}, async t => {
  const browser = await require(packageName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const sampleDir = new URL('../../internal/drive/testdata/preview/', import.meta.url);
  const convert = (kind, input) => JSON.parse(execFileSync('python3',
    ['-B','internal/drive/preview/convert.py',kind,'1','0','0'],{input}));
  const pdf = await readFile(new URL('scan.pdf',sampleDir));
  const repaired = convert('pdf',pdf);
  assert.equal(repaired.stage,'ready');
  const failures = [
    [convert('pdf',await readFile(new URL('encrypted.pdf',sampleDir))),'未加密的副本'],
    [convert('csv',Buffer.from('姓名,项目\n张宁,验收')),'支持预览的格式'],
    [convert('pdf',Buffer.concat([pdf,Buffer.alloc(16 * 1024 * 1024)])),'拆分文件']
  ];
  for (const [width, theme] of [[1366,'light'],[1366,'dark'],[390,'light'],[390,'dark']]) {
    const page = await browser.newPage({viewport:{width,height:844},colorScheme:theme});
    page.setDefaultTimeout(6000);
    await mountFixture(page);
    let version = 11, result;
    const file = {id:7,category:'files',format:'PDF',format_key:'pdf',name:'验收签字单.pdf',
      project_ids:[],sources:[],sync_state:'complete'};
    await page.route(origin + '/api/v1/feishu/files**', async route => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith('/sync')) return route.fulfill({json:{groups:1,pending_sources:0,complete_sources:1}});
      if (!path.includes('/previews')) {
        const row = result.stage === 'unsupported' ? {...file,name:'验收名单.csv',format:'其他文件',format_key:'generic'} : file;
        return route.fulfill({json:{items:[{...row,version_id:version}],total:1,projects:{}}});
      }
      const root = '/api/v1/feishu/files/7/versions/' + version + '/previews';
      if (path === root) return route.fulfill({json:{task_id:'sample',version_id:version,stage:result.stage,meta:result.meta}});
      if (result.stage === 'ready' && path === root + '/sample/result') {
        return route.fulfill({contentType:result.mime,body:Buffer.from(result.body,'base64')});
      }
      return route.fulfill({status:404,json:{error:'version_unavailable'}});
    });
    const trigger = page.locator('.drive-file-name');
    const dialog = page.getByRole('dialog');
    for (const [failure, recovery] of failures) {
      result = failure;
      const address = origin + '/?theme=' + theme + '#/feishu/drive';
      if (page.url() === address) await page.reload();
      else await page.goto(address);
      assert.equal(await page.locator('html').getAttribute('data-theme'),theme);
      await trigger.click();
      await dialog.getByRole('status').filter({hasText:recovery}).waitFor();
      assert.equal(await dialog.locator('[data-preview-body]').innerText(),'');
      version++;
      result = repaired;
      await dialog.getByRole('button',{name:'返回并刷新列表'}).press('Enter');
      await trigger.and(page.locator(':focus')).waitFor();
      await trigger.click();
      await dialog.getByText('PDF · 版本 ' + version).waitFor();
      const image = dialog.getByRole('img',{name:'PDF 第 1 页'});
      await image.waitFor();
      const pixel = await image.evaluate(c => [...c.getContext('2d').getImageData(20,20,1,1).data]);
      assert.ok(pixel[0] > 245 && pixel[2] < 16);
      await page.keyboard.press('Escape');
    }
    await page.close();
  }
});

test('群图片和视频使用已授权的版本地址预览和下载', {skip:!packageName}, async t => {
  const browser = await require(packageName).chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const page = await browser.newPage();
  await mountFixture(page);
  const source = {chat_id:group.id, message_id:oldMessage.id, chat_name:group.name};
  const rows = ['image','video'].map((kind,i) => ({
    id:i+1, version_id:10+i, format_key:kind, name:i ? '项目视频' : '项目图片', format:i ? '视频' : '图片',
    category:i ? 'videos' : 'images', sync_state:'complete', project_ids:['p'], inherited_project_ids:['p'], manual_project_ids:[], sources:[source]
  }));
  const pdf = {...rows[0], id:3, name:'普通附件.pdf', category:'files', format_key:'pdf', format:'PDF', version_id:12};
  rows.push(pdf);
  let revoked = false;
  await page.route(origin + '/api/v1/projects?**', route => route.fulfill({json:{items:[],total:0}}));
  await page.route(origin + '/api/v1/feishu/files**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/feishu/files/sync') return route.fulfill({json:{
      groups:Number(!revoked), history_pending:0, history_attention:0, unindexed_messages:0,
      unresolved_wiki:0, pending_sources:0, complete_sources:revoked ? 0 : 3, attention_sources:0
    }});
    if (path.endsWith('/files')) {
      const category = new URL(route.request().url()).searchParams.get('category');
      const items = revoked ? [] : rows.filter(row => category === 'all' || category === row.category);
      return route.fulfill({json:{items,total:items.length,projects:{p:'项目'}}});
    }
    if (revoked) return route.fulfill({status:404,json:{error:'file_unavailable'}});
    const row = rows.find(item => path.startsWith('/api/v1/feishu/files/' + item.id + '/'));
    if (path.endsWith('/versions')) return route.fulfill({json:{items:[{id:row.version_id}]}});
    if (row.format_key === 'image') return route.fulfill({path:'design/prototype/omni-relay-logo.png',contentType:'image/png'});
    return route.fulfill({status:415});
  });
  await page.goto(origin + '/#/feishu/drive');
  const names = page.locator('.drive-file-name strong');
  await page.getByText('普通附件.pdf', {exact:true}).waitFor();
  assert.deepEqual(await names.allTextContents(), ['项目图片','项目视频','普通附件.pdf']);
  await page.getByRole('button', {name:'文件', exact:true}).click();
  await page.getByText('普通附件.pdf', {exact:true}).waitFor();
  assert.deepEqual(await names.allTextContents(), ['普通附件.pdf']);
  await page.getByRole('button', {name:'图片', exact:true}).click();
  await page.getByText('项目图片', {exact:true}).waitFor();
  assert.deepEqual(await names.allTextContents(), ['项目图片']);
  const trigger = page.locator('.drive-file-name').first();
  await trigger.click();
  const img = page.getByRole('img',{name:'项目图片',exact:true});
  await img.waitFor();
  await img.evaluate(node => node.decode());
  assert.equal(new URL(await img.getAttribute('src'),origin).pathname,'/api/v1/feishu/files/1/versions/10/content');
  await page.keyboard.press('Escape');
  const link = page.getByRole('link',{name:'下载',exact:true}).first();
  const download = page.waitForEvent('download');
  await link.click();
  assert.match((await download).url(),/versions\/10\/content\?download=1$/);
  await page.getByRole('button', {name:'视频', exact:true}).click();
  await page.getByText('项目视频', {exact:true}).waitFor();
  assert.deepEqual(await names.allTextContents(), ['项目视频']);
  await page.locator('.drive-file-name').first().click();
  await page.locator('video[controls]').waitFor();
  await page.getByText('暂时无法预览此格式，可返回文件列表下载原件。').waitFor();
  revoked = true;
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await page.getByText('已无权查看此文件').waitFor();
  assert.equal(await page.locator('video').count(),0);
});
