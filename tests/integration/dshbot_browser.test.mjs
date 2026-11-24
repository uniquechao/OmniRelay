import assert from 'node:assert/strict';
import test from 'node:test';
import {chromium} from 'playwright-core';

const origin = process.env.OMNIRELAY_BROWSER_ORIGIN;
const me = '/api/v1/me';
const grants = '/api/v1/system/role-grants';

async function login(browser, username) {
  const context = await browser.newContext({ignoreHTTPSErrors: true, locale: 'en-US'});
  const page = await context.newPage();
  await page.goto(origin);
  await page.locator('#login-username').fill(username);
  await page.locator('#login-password').fill('fixture-password-only');
  await page.locator('#password-login button[type=submit]').click();
  await page.waitForURL('**/#/workbench');
  const session = await (await context.request.get(origin + me)).json();
  return {page, context, session, headers: {Origin: origin, 'X-CSRF-Token': session.csrf_token}};
}

async function openConsole(page) {
  const [socket] = await Promise.all([
    page.waitForEvent('websocket', {predicate: ws => ws.url().startsWith(origin.replace('https:', 'wss:') + '/dshbot/')})
      .then(async ws => { await ws.waitForEvent('framereceived'); return ws; }),
    page.getByRole('link', {name: 'dshbot', exact: true}).click()
  ]);
  const native = page.frameLocator('iframe[title="DSH 原生调试界面"]');
  await native.locator('#root button').first().waitFor();
  assert.equal(socket.isClosed(), false);
  return socket;
}

test('系统管理员打开原生 DSH，退出和撤权均断开调试连接', {skip: !origin}, async t => {
  const browser = await chromium.launch({executablePath: process.env.OMNIRELAY_CHROME, args: ['--ignore-certificate-errors']});
  t.after(() => browser.close());
  const guest = await browser.newContext({ignoreHTTPSErrors: true});
  assert.equal((await guest.request.get(origin + '/dshbot/')).status(), 401);
  await guest.close();

  const admin = await login(browser, 'admin');
  const adminSocket = await openConsole(admin.page);
  assert.ok(!(await admin.context.cookies()).some(cookie => cookie.name.startsWith('dsh-auth-')));
  const withBearer = await admin.context.request.get(origin + '/dshbot/', {headers: {Authorization: 'Bearer fixture-only'}});
  assert.equal(withBearer.status(), 401);
  await admin.page.setViewportSize({width: 390, height: 844});
  await admin.page.emulateMedia({colorScheme: 'dark'});
  assert.equal(await admin.page.locator('#mobile-nav').inputValue(), 'dshbot');
  const frame = await admin.page.locator('iframe').boundingBox();
  assert.ok(frame.width <= 390 && frame.height > 500 && frame.y + frame.height <= 845);
  const foreign = await admin.context.request.post(origin + '/dshbot/api', {headers: {Origin: 'https://other.invalid'}});
  assert.equal(foreign.status(), 403);

  const member = await login(browser, 'historical');
  const grant = await admin.context.request.post(origin + grants, {
    headers: admin.headers, data: {accountId: member.session.principal.account_id, role: 'system_admin'}
  });
  assert.equal(grant.status(), 201);
  const grantID = (await grant.json()).id;
  await member.context.close();
  const promoted = await login(browser, 'historical');
  const promotedSocket = await openConsole(promoted.page);
  await Promise.all([
    promotedSocket.waitForEvent('close', {timeout: 8000}),
    admin.context.request.delete(origin + grants + '/' + grantID, {headers: admin.headers}).then(res => assert.equal(res.status(), 204))
  ]);
  assert.equal((await promoted.context.request.get(origin + '/dshbot/')).status(), 401);

  await Promise.all([
    adminSocket.waitForEvent('close', {timeout: 8000}),
    admin.context.request.post(origin + '/auth/logout', {headers: admin.headers}).then(res => assert.equal(res.status(), 204))
  ]);
  assert.equal((await admin.context.request.get(origin + '/dshbot/')).status(), 401);
});
