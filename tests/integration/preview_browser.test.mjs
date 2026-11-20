import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import { browserAsset } from './browser-assets.mjs';

const origin = process.env.OMNIRELAY_PREVIEW_ORIGIN;
const setupOrigin = process.env.OMNIRELAY_SETUP_ORIGIN;
const dist = process.env.OMNIRELAY_BROWSER_DIST;
const require = createRequire(import.meta.url);

test('预发布构建运行工作台和首次配置两个入口', { skip: !dist && !(origin && setupOrigin) }, async () => {
  const { chromium } = require(process.env.OMNIRELAY_PLAYWRIGHT_MODULE || 'playwright-core');
  const browser = await chromium.launch({ executablePath: process.env.OMNIRELAY_CHROME });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const workbench = origin || 'http://workbench.test';
    const setup = setupOrigin || 'http://setup.test';
    if (!origin) await page.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/me') return route.fulfill({ status: 401 });
      return route.fulfill(await browserAsset(url.pathname + url.search, { setup: url.origin === setup }));
    });
    await page.goto(workbench + '/#/workbench');
    await page.getByRole('heading', { name: '工作台' }).waitFor();
    await page.getByRole('status').filter({ hasText: origin ? /已登录|尚未登录|无法读取会话/ : '尚未登录' }).waitFor();
    const missing = await page.evaluate(async () => {
      const entry = document.querySelector('script[type=module]').src;
      return (await fetch('/missing/' + new URL(entry).pathname.split('/').at(-1))).status;
    });
    assert.equal(missing, 404);
    await page.goto(setup + (setupOrigin ? '/' : '/setup'));
    assert.equal(page.url(), setup + '/setup');
    await page.getByRole('heading', { name: '首次配置' }).waitFor();
    await page.getByText(setup + '/auth/feishu/callback', { exact: true }).waitFor();
    await page.goto(setup + '/?setup=complete');
    await page.getByText('配置已保存。').waitFor();
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});
