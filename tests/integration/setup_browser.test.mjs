import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createRequire } from 'node:module';

const origin = process.env.OMNIRELAY_SETUP_ORIGIN;
const code = process.env.OMNIRELAY_TEST_SETUP_CODE;
test('首次配置、授权回跳和服务切换', { skip: !origin || !code }, async () => {
  const { chromium } = createRequire(import.meta.url)(process.env.OMNIRELAY_PLAYWRIGHT_MODULE);
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  try {
    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await context.newPage();
    const seen = [];
    const secret = 'fixture-only';
    page.on('request', req => seen.push(req.url()));
    page.on('console', msg => seen.push(msg.text()));
    const violations = [];
    await page.exposeFunction('recordViolation', () => violations.push(true));
    await page.addInitScript(() => document.addEventListener('securitypolicyviolation', window.recordViolation));
    await page.goto(origin);
    assert.equal(page.url(), origin + '/setup');
    await page.getByText(origin + '/auth/feishu/callback', { exact: true }).waitFor();
    const submit = page.getByRole('button', { name: '使用飞书授权并保存' });
    await submit.focus();
    await page.keyboard.press('Enter');
    assert.equal(await page.locator(':focus').getAttribute('name'), 'setup_code');
    const fill = async (tab, token = code) => {
      await tab.getByLabel('一次性设置码').fill(token);
      await tab.getByLabel('飞书 App ID').fill('cli_fixture');
      await tab.getByLabel('飞书 App Secret').fill(secret);
    };
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: '切换深色主题' }).click();
    await page.locator('html[data-theme=dark]').waitFor();
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await fill(page, 'invalid');
    await page.getByLabel('飞书 App ID').fill('invalid');
    await submit.click();
    assert.equal(await page.locator(':focus').getAttribute('name'), 'app_id');
    await page.getByLabel('飞书 App ID').fill('cli_fixture');
    await submit.click();
    await page.getByText(/设置码无效/).waitFor();
    assert.equal(await page.locator(':focus').getAttribute('role'), 'status');

    await page.getByRole('button', { name: '切换浅色主题' }).click();
    await page.setViewportSize({ width: 1366, height: 768 });

    let posts = 0;
    let release;
    const pause = new Promise(resolve => { release = resolve; });
    await page.route(origin + '/setup', async route => {
      if (route.request().method() === 'POST' && ++posts === 1) await pause;
      await route.continue();
    });
    await page.route('https://accounts.feishu.cn/**', route => route.fulfill({ contentType: 'text/html', body: '<title>飞书授权</title>' }));
    const stale = await context.newPage();
    await stale.goto(origin);
    await stale.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
    await stale.waitForLoadState('networkidle');
    await fill(stale);
    for (const [outcome, error] of [['cancelled', 'cancelled'], ['fixture-code-userinfo-outage', 'unavailable'], ['ou_admin', '']]) {
      await fill(page);
      await page.getByLabel('飞书 App Secret').press('Enter');
      if (outcome === 'cancelled') {
        await page.locator('.or-loading-button .or-loading-fill').waitFor();
        await page.locator('form').evaluate(form => form.requestSubmit());
        release();
      }
      await page.waitForURL('https://accounts.feishu.cn/**');
      const state = new URL(page.url()).searchParams.get('state');
      assert.ok(state);
      const query = new URLSearchParams({ state, ...(outcome === 'cancelled' ? { error: 'access_denied' } : { code: outcome }) });
      await page.goto(origin + '/auth/feishu/callback?' + query);
      if (error) {
        assert.equal(page.url(), origin + '/setup?error=' + error);
        await page.locator('[role=status]:focus').waitFor();
        assert.equal(await page.getByLabel('飞书 App Secret').inputValue(), '');
      }
    }
    assert.equal(posts, 3);
    assert.equal(page.url(), origin + '/?setup=complete');
    await page.getByRole('progressbar').waitFor();
    const animation = () => page.locator('.or-loading-fill').evaluate(el => getComputedStyle(el).animationName);
    assert.notEqual(await animation(), 'none');
    await page.emulateMedia({ reducedMotion: 'reduce' });
    assert.equal(await animation(), 'none');
    const conflict = stale.waitForResponse(r => r.request().method() === 'POST' && r.status() === 409);
    await stale.getByRole('button', { name: '使用飞书授权并保存' }).click();
    await conflict;
    await stale.waitForURL(origin + '/?setup=complete');
    await stale.close();
    await Promise.all([page.waitForEvent('load'), page.evaluate(() =>
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })))]);
    await page.getByRole('progressbar').waitFor();
    const retry = page.getByRole('button', { name: '重新检查' });
    await retry.waitFor({ timeout: 20000 });
    assert.equal(await retry.evaluate(el => el === document.activeElement), true);
    assert.equal((await context.request.post(origin + '/test/service-ready')).status(), 204);
    await retry.press('Enter');
    await page.getByRole('heading', { name: '登录工作台' }).waitFor();
    seen.push(await page.evaluate(() => JSON.stringify([localStorage, sessionStorage])));
    assert.ok(!seen.some(value => value.includes(code) || value.includes(secret)));
    assert.deepEqual(violations, []);
  } finally {
    await browser.close();
  }
});
