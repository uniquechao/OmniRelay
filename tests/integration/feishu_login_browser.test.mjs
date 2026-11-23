import { browserAsset } from './browser-assets.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFile, mkdtemp, rm } from 'node:fs/promises';
import { createServer } from 'node:https';
import { createRequire } from 'node:module';
import { join } from 'node:path';
import { after, before, test } from 'node:test';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.OMNIRELAY_PLAYWRIGHT_MODULE || 'playwright-core');
const goOrigin = process.env.OMNIRELAY_BROWSER_ORIGIN;
const screenshotDir = process.env.OMNIRELAY_BROWSER_SCREENSHOTS;
const callbacks = JSON.parse(await readFile('tests/fixtures/feishu/oauth.json', 'utf8'));
const botHash = '#/system/feishu-bot';
const storageKey = 'omnirelay.feishu.return-route';
const mockCSRF = 'browser-fixture';
const outcomes = new Map([
  ['cancelled', /已取消飞书授权/],
  ['denied', /管理员开通并启用/],
  ['unavailable', /请稍后重试/],
  ['legacy', /飞书登录未完成/],
  ['unknown', /飞书登录未完成/]
]);

let browser, server, scratch, origin;
let fixture;
const redirect = (response, status, location) => response.writeHead(status, { Location: location }).end();
const json = (response, status, data) =>
  response.writeHead(status, { 'Content-Type': 'application/json' }).end(JSON.stringify(data));

async function respond(request, response) {
  const url = new URL(request.url, origin);
  const path = url.pathname;
  if (path === '/auth/feishu/start') {
    fixture.target = url.searchParams.get('return_to');
    fixture.redirects.push(fixture.target);
    return redirect(response, 302, origin.replace('127.0.0.1', 'localhost') + '/authorize');
  }
  if (path === '/authorize') {
    const callback = origin + '/auth/feishu/callback';
    return response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }).end(
      [...outcomes.keys(), 'success'].map(name => '<a href="' + callback + '?scenario=' + name + '">' + name + '</a>').join(' ')
    );
  }
  if (path === '/auth/feishu/callback') {
    const scenario = url.searchParams.get('scenario');
    const sample = callbacks.find(item => item.name === scenario);
    if (sample) return redirect(response, sample.status, sample.location);
    if (scenario === 'unknown') return redirect(response, 303, '/#/login?error=fixture-provider-detail');
    fixture.signedIn = true;
    return redirect(response, 303, fixture.target);
  }
  if (path === '/api/v1/me') {
    fixture.meChecks++;
    if (fixture.probeStatus) return json(response, fixture.probeStatus, {});
    return fixture.signedIn ? json(response, 200, {
      principal: { account_id: fixture.accountId ?? 'account-1', name: '测试账号', auth_method: fixture.method },
      csrf_token: fixture.csrfMissing ? '' : mockCSRF,
      access: fixture.access ?? { admin: { feishu_bot: fixture.botAllowed }, roles: ['system_admin'], navigation: { workbench:true, system:fixture.botAllowed },
        permissions: { 'system.operate':fixture.botAllowed }, default_route:'/#/workbench' }
    }) : json(response, 401, { error: 'unauthorized' });
  }
  if (path === '/api/v1/system/feishu-bot') {
    fixture.botReads++;
    const sample = JSON.parse(await readFile('tests/fixtures/feishu/bot-config.json', 'utf8'));
    if (!fixture.signedIn) return json(response, 401, { error: 'unauthorized' });
    response.setHeader('ETag', sample[0].headers.ETag);
    return json(response, 200, sample[0].body);
  }
  if (path === '/auth/login') {
    fixture.signedIn = true;
    fixture.method = 'password';
    return response.writeHead(204).end();
  }
  if (path === '/auth/password' || path === '/auth/logout') {
    if (!fixture.signedIn) return json(response, 401, { error: 'unauthorized' });
    if (request.headers['x-csrf-token'] !== mockCSRF) return json(response, 403, {});
    if (path === '/auth/password') {
      fixture.passwordChanged = true;
      return json(response, 200, { csrf_token: mockCSRF });
    }
    fixture.signedIn = false;
    return response.writeHead(204).end();
  }
  const asset = await browserAsset(path);
  response.writeHead(asset.status, asset.headers).end(asset.body);
}

before(async () => {
  browser = await chromium.launch({
    executablePath: process.env.OMNIRELAY_CHROME,
    ignoreDefaultArgs: ['--disable-back-forward-cache'],
    args: ['--ignore-certificate-errors']
  });

  if (goOrigin) return;
  scratch = await mkdtemp('./.feishu-login-test-');
  const key = join(scratch, 'server.key');
  const cert = join(scratch, 'server.crt');
  execFileSync('openssl', ['req', '-x509', '-nodes', '-newkey', 'rsa:2048', '-days', '1',
    '-subj', '/CN=localhost', '-keyout', key, '-out', cert], { stdio: 'ignore' });
  server = createServer({ key: await readFile(key), cert: await readFile(cert) }, (request, response) => {
    respond(request, response).catch(() => response.writeHead(500).end());
  });
  await new Promise(resolve => server.listen(0, resolve));
  origin = 'https://127.0.0.1:' + server.address().port;
});

after(async () => {
  await browser?.close();
  server?.close();
  if (scratch) await rm(scratch, { recursive: true, force: true });
});

async function withPage(run, options = {}) {
  fixture = { signedIn: false, botAllowed: true, method: 'feishu', redirects: [], botReads: 0, meChecks: 0 };
  const context = await browser.newContext({ ignoreHTTPSErrors: true, ...options });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  try { await run(page); }
  finally { await context.close(); }
}

async function startFeishu(page) {
  await page.locator('#feishu-login').click();
  await page.waitForURL('**/authorize');
}

test('取消与失败后刷新登录页，重试仍返回原 Bot 页', { skip: !!goOrigin }, () => withPage(async page => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(origin + '/' + botHash);
  for (const [scenario, message] of outcomes) {
    await startFeishu(page);
    assert.equal(fixture.target, '/' + botHash);
    await page.getByRole('link', { name: scenario, exact: true }).click();
    const feedback = page.locator('#login-feedback');
    await feedback.waitFor();
    assert.match(await feedback.textContent(), message);
    assert.ok(await page.locator('#feishu-login').isEnabled());
    if (scenario === 'cancelled') {
      assert.ok(await feedback.evaluate(node => {
        const { top, bottom } = node.getBoundingClientRect();
        return top >= 0 && bottom <= innerHeight;
      }));
      if (screenshotDir) {
        await page.locator('#login-page [data-theme-toggle]').click();
        await feedback.scrollIntoViewIfNeeded();
        await page.screenshot({ path: join(screenshotDir, 'cancelled-mobile-dark.png') });
        await page.locator('#login-page [data-theme-toggle]').click();
      }
      await page.setViewportSize({ width: 1280, height: 800 });
      if (screenshotDir) await page.screenshot({ path: join(screenshotDir, 'cancelled-desktop-light.png') });
    }
    await page.reload();
  }
  const meChecks = fixture.meChecks;
  await startFeishu(page);
    assert.equal(fixture.target, '/' + botHash);
  await page.getByRole('link', { name: 'success' }).click();
  await page.locator('#page-heading').filter({ hasText: '飞书 Bot 配置' }).waitFor();
  assert.ok(fixture.meChecks > meChecks);
  assert.ok(fixture.botReads > 0);
  assert.deepEqual(fixture.redirects, Array(6).fill('/' + botHash));
  assert.equal(await page.evaluate(key => sessionStorage.getItem(key), storageKey), null);
}, { reducedMotion: 'reduce' }));

test('缺省能力拒绝访问，项目导航和操作点独立于角色名', { skip: !!goOrigin }, () => withPage(async page => {
  fixture.signedIn = true;
  fixture.access = { roles:['system_admin'], admin:{feishu_bot:true} };
  await page.goto(origin + '/' + botHash);
  await page.getByText('当前账号没有可访问的页面。').waitFor();
  assert.equal(fixture.botReads, 0);
  fixture.access = {
    roles:['system_operator'], admin:{feishu_bot:true}, navigation:{system:true,workbench:true},
    default_route:'/#/system/feishu-bot', permissions:{'system.read':true}
  };
  await page.reload();
  await page.locator('#bot-content').waitFor();
  assert.equal(await page.locator('#bot-test').isVisible(), false);
  assert.equal(await page.locator('#bot-form').isVisible(), false);
  await page.goto(origin + '/#/workbench');
  await page.getByRole('heading', {name:'工作台总览'}).waitFor();
  fixture.access.navigation.workbench = false;
  await page.reload();
  await page.waitForURL('**/' + botHash);
  assert.equal(await page.locator('#mobile-nav option[value=workbench]').evaluate(option => option.hidden), true);
}));

test('窗口聚焦复核失败后锁定表单，联网后恢复同账号草稿并清除密码', { skip: !!goOrigin }, () => withPage(async page => {
  fixture.signedIn = true;
  fixture.method = 'password';
  fixture.access = { admin:{feishu_bot:true}, navigation:{system:true}, default_route:'/#/system/feishu-bot', permissions:{'system.configure':true} };
  await page.goto(origin + '/' + botHash);
  const enabled = page.locator('#bot-enabled');
  await enabled.waitFor();
  assert.equal(await enabled.isChecked(), false);
  await enabled.check();
  await page.locator('.rail .account-trigger').click();
  await page.locator('[data-account="password"]').click();
  await page.locator('[name=currentPassword]').fill('测试密码');
  fixture.probeStatus = 503;
  await page.evaluate(() => dispatchEvent(new Event('focus')));
  await page.locator('.session-lock[open]').waitFor();
  assert.equal(await enabled.isVisible(), false);
  assert.equal(await page.locator('[name=currentPassword]').count(), 0);
  if(process.env.OMNIRELAY_BROWSER_DIST)assert.equal(await page.locator('#bot-save').isDisabled(),true);
  const requests=fixture.botReads;
  await page.locator('#bot-form').evaluate(form=>form.requestSubmit());
  assert.equal(fixture.botReads,requests);
  fixture.probeStatus = 0;
  await page.evaluate(() => dispatchEvent(new Event('online')));
  await enabled.waitFor();
  assert.equal(await enabled.isChecked(), true);
  await page.locator('#bot-save:enabled').waitFor();
  fixture.probeStatus = 503;
  await page.evaluate(() => dispatchEvent(new Event('focus')));
  await page.locator('.session-lock[open]').waitFor();
  fixture.probeStatus = 0;
  fixture.access.permissions['system.configure'] = false;
  await page.locator('#session-status button').click();
  await page.locator('#bot-content').waitFor();
  assert.equal(await enabled.isVisible(), false);
  fixture.access.permissions['system.configure'] = true;
  await page.evaluate(() => dispatchEvent(new Event('focus')));
  await enabled.waitFor();
  assert.equal(await enabled.isChecked(),false);
  await enabled.check();
  fixture.accountId='account-2';
  await page.evaluate(() => dispatchEvent(new Event('focus')));
  await page.waitForFunction(()=>document.querySelector('#bot-enabled')?.checked===false);
}));

test('后退缓存页可重试且重新校验权限', { skip: !!goOrigin }, () => withPage(async page => {
  await page.addInitScript(() => addEventListener('pageshow', event => {
    window.restoredFromCache = event.persisted;
  }));
  await page.goto(origin + '/' + botHash);
  await startFeishu(page);
  const beforeBack = fixture.meChecks;
  await page.goBack({ waitUntil: 'commit' });
  await page.locator('#feishu-login:enabled').waitFor();
  assert.equal(await page.evaluate(() => window.restoredFromCache), true);
  assert.ok(fixture.meChecks > beforeBack);
  await startFeishu(page);
  fixture.botAllowed = false;
  await page.getByRole('link', { name: 'success' }).click();
  await page.getByRole('heading', { name:'工作台总览' }).waitFor();
  assert.equal(fixture.botReads, 0);
  assert.deepEqual(fixture.redirects, ['/' + botHash, '/' + botHash]);

  fixture.botAllowed = true;
  await page.reload();
  await page.goto(origin + '/' + botHash);
  await page.getByRole('button', { name: '测试连接' }).waitFor();
  await page.goto(origin.replace('127.0.0.1', 'localhost') + '/authorize');
  fixture.botAllowed = false;
  const readsBeforeBack = fixture.botReads;
  await page.goBack({ waitUntil: 'commit' });
  await page.getByRole('heading', { name:'工作台总览' }).waitFor();
  assert.equal(await page.evaluate(() => window.restoredFromCache), true);
  assert.equal(fixture.botReads, readsBeforeBack);
}));

test('拒绝未知或外部目标，401 与退出后重新确认会话', { skip: !!goOrigin }, () => withPage(async page => {
  for (const target of ['#/system/unknown', 'https://example.invalid/#/workbench']) {
    await page.goto(origin + '/#/login');
    await page.evaluate(([key, value]) => sessionStorage.setItem(key, value), [storageKey, target]);
    await startFeishu(page);
    assert.equal(fixture.target, '/');
    await page.getByRole('link', { name: 'cancelled' }).click();
  }

  await page.goto(origin + '/#/system/unknown');
  await startFeishu(page);
  assert.equal(fixture.target, '/');
  await page.getByRole('link', { name: 'cancelled' }).click();

  fixture.signedIn = true;
  await page.goto(origin + '/#/workbench');
  await page.locator('#page-heading').waitFor();
  fixture.signedIn = false;
  await page.locator('#system-nav > summary').click();
  await page.getByRole('link',{name:'飞书 Bot 配置',exact:true}).click();
  await page.locator('#feishu-login:enabled').waitFor();
  await startFeishu(page);
  assert.equal(fixture.target, '/' + botHash);
  await page.getByRole('link', { name: 'success' }).click();
  await page.locator('#page-heading').waitFor();
  await page.locator('.rail .account-trigger').click();
  await page.locator('[data-account="logout"]').click();
  await page.getByRole('button', { name: '退出', exact: true }).click();
  await page.locator('#feishu-login:enabled').waitFor();
  assert.equal(fixture.signedIn, false);
  await page.goto(origin + '/' + botHash);
  await page.locator('#feishu-login:enabled').waitFor();

  fixture.signedIn = true;
  await page.reload();
  await page.locator('.rail .account-trigger').click();
  await page.locator('[data-account="logout"]').click();
  fixture.signedIn = false;
  await page.getByRole('button', { name: '退出', exact: true }).click();
  await page.locator('#feishu-login:enabled').waitFor();
}));

test('密码登录、改密、原型主题与减少动态效果', { skip: !!goOrigin }, () => withPage(async page => {
  const theme = page.locator('#login-page [data-theme-toggle]');
  for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.goto(origin + '/prototype/omni-relay-workbench.html?theme=light#/login');
    const brand = page.locator('.login-brand');
    const background = node => getComputedStyle(node).backgroundColor;
    const prototypeLight = await brand.evaluate(background);
    await theme.click();
    const prototypeDark = await brand.evaluate(background);
    await page.goto(origin + '/?theme=light#/login');
    assert.equal(await brand.evaluate(background), prototypeLight);
    await theme.click();
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark');
    assert.equal(await brand.evaluate(background), prototypeDark);
    await theme.click();
    assert.equal(await brand.evaluate(background), prototypeLight);
    const canvas = page.locator('.login-motion');
    const frame = await canvas.evaluate(node => node.toDataURL());
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    assert.equal(await canvas.evaluate(node => node.toDataURL()), frame);
    assert.ok(await page.locator('#feishu-login').evaluate(node => node.getBoundingClientRect().right <= innerWidth));
  }
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  const movingCanvas = page.locator('.login-motion');
  const firstFrame = await movingCanvas.evaluate(node => node.toDataURL());
  await page.waitForFunction(frame => document.querySelector('.login-motion').toDataURL() !== frame, firstFrame);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.locator('#password-login [name=username]').fill('测试账号');
  await page.locator('#password-login [name=password]').fill('仅限浏览器样例的旧密码');
  await page.locator('#password-login button[type=submit]').click();
  await page.locator('.rail .account-trigger').click();
  await page.locator('[data-account="password"]').click();
  await page.locator('[name=currentPassword]').fill('仅限浏览器样例的旧密码');
  await page.locator('[name=newPassword]').fill('仅限浏览器样例使用的新密码组合123');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await page.getByText('密码已更新。', { exact: true }).waitFor();
  assert.equal(fixture.passwordChanged, true);
}, { reducedMotion: 'reduce' }));

test('Go OAuth 接口与浏览器整页联调', { skip: !goOrigin }, () => withPage(async page => {
  await page.goto(goOrigin + '/' + botHash);
  for (const scenario of ['cancelled', 'denied', 'unavailable', 'success']) {
    let stage = '发起授权';
    try {
      await page.locator('#feishu-login').click();
      const authorize = page.getByRole('link', { name: scenario, exact: true });
      await authorize.waitFor();
      stage = '接收回调';
      const returned = page.waitForResponse(response => {
        const url = new URL(response.url());
        return url.origin === goOrigin && url.pathname === '/auth/feishu/callback';
      });
      await authorize.click();
      const response = await returned;
      stage = '检查回跳状态与目标';
      if (response.status() !== 303) stage += ' HTTP ' + response.status();
      assert.equal(response.status(), 303);
      assert.equal((await response.headerValue('location')),
        scenario === 'success' ? '/' + botHash : callbacks.find(item => item.name === scenario).location);
      if (scenario !== 'success') {
        stage = '检查本地化提示';
        await page.locator('#login-feedback').filter({ hasText: outcomes.get(scenario) }).waitFor();
        await page.reload();
      }
    } catch {
      throw new Error('Go OAuth 浏览器联调失败于场景 ' + scenario + '，阶段为' + stage);
    }
  }
  await page.locator('#page-heading').filter({ hasText: '飞书 Bot 配置' }).waitFor();
  await page.locator('.rail .account-trigger').click();
  await page.locator('[data-account="logout"]').click();
  const logout = page.waitForResponse(response => response.url() === goOrigin + '/auth/logout');
  await page.getByRole('button', { name: '退出', exact: true }).click();
  assert.equal((await logout).status(), 204);
  await page.locator('#feishu-login:enabled').waitFor();
  assert.equal(await page.evaluate(async () => (await fetch('/api/v1/me')).status), 401);
}));

test('React 书签与草稿保护、刷新和键盘焦点', {skip:!!goOrigin || !process.env.OMNIRELAY_BROWSER_DIST},()=>withPage(async page=>{
  fixture.signedIn=true;
  fixture.method='password';
  fixture.access={navigation:{system:true,workbench:true},admin:{settings:true,feishu_bot:true},
    permissions:{'system.configure':true},default_route:'/#/workbench'};
  await page.goto(origin+'/#/system/settings');
  await page.waitForURL('**/#/system/settings');
  await page.getByRole('link',{name:'飞书 Bot 配置',exact:true}).click();
  const toggle=page.locator('#bot-enabled');
  await toggle.waitFor();
  await toggle.setChecked(!await toggle.isChecked());
  const refresh=new Promise(resolve=>page.once('dialog',async dialog=>{assert.equal(dialog.type(),'beforeunload');await dialog.dismiss();resolve();}));
  await page.evaluate(()=>setTimeout(()=>location.reload(),0));
  await refresh;
  await page.goBack({waitUntil:'commit'});
  await page.getByRole('button',{name:'继续编辑'}).click();
  await page.waitForURL('**/#/system/feishu-bot');
  assert.equal(await toggle.evaluate(el=>el===document.activeElement),true);
  await page.goBack({waitUntil:'commit'});
  await page.getByRole('button',{name:'不保存'}).click();
  await page.getByRole('heading',{name:'连接设置'}).waitFor();
  await page.goForward();
  await toggle.waitFor();
  const trigger=page.locator('.rail .account-trigger');
  await trigger.focus();await page.keyboard.press('Enter');
  await page.locator('#account-menu:popover-open').waitFor();
  await page.keyboard.press('Home');await page.keyboard.press('Enter');
  await page.getByRole('dialog',{name:'个人信息'}).waitFor();
  await page.keyboard.press('Escape');
  await page.waitForFunction(()=>document.activeElement?.classList.contains('account-trigger'));
}));

test('Bot 版本复核、检测重试与跨页轮询', {skip:!!goOrigin || !process.env.OMNIRELAY_BROWSER_DIST},()=>withPage(async page=>{
  fixture.signedIn=true;
  fixture.access={navigation:{system:true},admin:{settings:true,feishu_bot:true},
    permissions:{'system.configure':true,'system.operate':true}};
  const bot='/api/v1/system/feishu-bot';
  const config=JSON.parse(await readFile('tests/fixtures/feishu/bot-config.json','utf8'))[0].body;
  const tasks=JSON.parse(await readFile('tests/fixtures/feishu/bot-checks.json','utf8'));
  let etag, conflict, writes=0, polls=0, state='queued';
  const keys=[];
  await page.route('**'+bot, async route=>{
    const request=route.request();
    if(request.method()==='PATCH'){
      writes++;
      assert.equal(request.headers()['if-match'],etag);
      if(conflict){
        etag='"bot-'+writes+'"';
        return route.fulfill({status:conflict,json:{}});
      }
      config.enabled=request.postDataJSON().enabled;
    }
    await route.fulfill({headers:etag?{ETag:etag}:{},json:config});
  });
  await page.route('**'+bot+'/checks',async route=>{
    keys.push(route.request().headers()['idempotency-key']);
    if(keys.length===1)return route.abort();
    await route.fulfill({status:202,json:{id:'check-1',state:'queued'}});
  });
  await page.route('**'+bot+'/checks/check-1',route=>{
    polls++;
    assert.equal(route.request().headers()['x-omnirelay-activity'],'poll');
    return route.fulfill({json:{...tasks.find(task=>task.name===state).body,id:'check-1'}});
  });
  await page.route('**/api/v1/system/connections',r=>r.fulfill({json:{items:[{id:'omnirelay',state:'local'},{id:'deepseek_harness',state:'other'}]}}));
  await page.goto(origin+'/#/system/feishu-bot');
  const toggle=page.locator('#bot-enabled'),save=page.locator('#bot-save'),check=page.locator('#bot-test');
  await toggle.check();
  assert.equal(await save.isDisabled(),true);
  await page.getByText(/无法获取配置版本/).waitFor();
  etag='"bot-0"';
  await page.locator('#bot-refresh').click();
  for(conflict of [412,428]){
    const previous=writes;
    await page.locator('#bot-save:enabled').waitFor();
    const refreshed=page.waitForResponse(r=>r.url().endsWith(bot) && r.request().method()==='GET');
    await save.evaluate(b=>{b.click();b.click();});
    await refreshed;
    await page.locator('#bot-save:enabled').waitFor();
    assert.equal(writes,previous+1);
    assert.equal(await toggle.isChecked(),true);
    assert.match(await page.locator('#bot-save-feedback').textContent(),/再次点击「保存」确认/);
  }
  conflict=0;
  await save.click();
  await page.locator('[data-bot=enabled]').getByText('已保存为启用').waitFor();
  assert.equal(writes,3);
  await check.click();
  await check.getByText('重试检测').waitFor();
  await check.evaluate(b=>{b.click();b.click();});
  await page.getByText('等待开始检测…').waitFor();
  assert.ok(keys[0]);assert.deepEqual(keys,[keys[0],keys[0]]);
  state='running';
  await page.getByText('正在检测飞书应用…').waitFor();
  assert.equal(await check.getAttribute('aria-busy'),'true');
  await page.getByRole('link',{name:'连接设置',exact:true}).click();
  await page.getByText('本地管理',{exact:true}).waitFor();
  await page.getByText('状态未知',{exact:true}).waitFor();
  const left=polls;
  await new Promise(resolve=>setTimeout(resolve,2100));
  assert.equal(polls,left);
  state='succeeded';
  await page.goBack();
  await page.locator('[data-bot=bot_info]').getByText('通过').waitFor();
  assert.equal(keys.length,2);
  assert.equal(await check.getAttribute('aria-busy'),null);
  state='bot_unavailable';
  await check.click();
  await page.locator('[data-bot=bot_info]').getByText('失败').waitFor();
  assert.notEqual(keys[2],keys[0]);
  assert.equal(await check.getAttribute('aria-busy'),null);
}));

test('配置、检测与目录同步分别检查能力，连接设置区分读取失败与拒绝访问', {skip:!!goOrigin || !process.env.OMNIRELAY_BROWSER_DIST},()=>withPage(async page=>{
  fixture.signedIn=true;
  fixture.access={navigation:{system:true},admin:{feishu_bot:true,settings:true,users:true},permissions:{}};
  await page.goto(origin+'/#/system/feishu-bot');
  const configure=page.locator('#bot-form'),detect=page.locator('#bot-test');
  const sync=page.getByRole('link',{name:'前往用户管理同步组织与用户'});
  for(const capability of ['system.read','system.configure','system.operate','directory.sync']){
    fixture.access.permissions={[capability]:true};
    const refreshed=page.waitForResponse(r=>r.url().endsWith('/system/feishu-bot'));
    await page.evaluate(()=>dispatchEvent(new Event('focus')));
    await refreshed;
    await page.locator('#bot-content').waitFor();
    assert.equal(await configure.isVisible(),capability==='system.configure');
    assert.equal(await detect.isVisible(),capability==='system.operate');
    assert.equal(await sync.isVisible(),capability==='directory.sync');
  }
  let status=503;
  await page.route('**/api/v1/system/connections',route=>route.fulfill({status,json:{items:[]}}));
  await page.getByRole('link',{name:'连接设置',exact:true}).click();
  await page.getByText('暂时无法读取连接设置，请重试。').waitFor();
  status=403;
  await page.getByRole('button',{name:'重试',exact:true}).click();
  await page.getByText('当前账号没有查看连接设置的权限。').waitFor();
  assert.equal(await page.getByRole('button',{name:'重试',exact:true}).count(),0);
}));
