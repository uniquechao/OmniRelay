import assert from 'node:assert/strict';
import {readdir,readFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import { browserAsset } from './browser-assets.mjs';

const origin = process.env.OMNIRELAY_PREVIEW_ORIGIN;
const setupOrigin = process.env.OMNIRELAY_SETUP_ORIGIN;
const dist = process.env.OMNIRELAY_BROWSER_DIST;
const require = createRequire(import.meta.url);

test('生产资源均由工作台或初始化入口引用',async()=>{
  const root=resolve(dist || 'internal/webui/dist');
  assert.deepEqual((await readdir(root)).sort(),['assets','index.html','setup.html']);
  const names=await readdir(resolve(root,'assets'));
  const pending=['index.html','setup.html'];
  const visited=new Set();
  while(pending.length){
    const path=pending.pop();
    if(!/\.(html|js|css)$/.test(path))continue;
    const source=await readFile(resolve(root,path),'utf8');
    assert.doesNotMatch(source,/omni-relay-examples\.js|飞书 · 示例账号|北辰交付准备/);
    for(const name of names){
      if(!visited.has(name) && source.includes(name)){
        visited.add(name);
        pending.push('assets/'+name);
      }
    }
  }
  assert.deepEqual([...visited].sort(),names.sort());
});

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
    await page.getByRole('heading', { name: '登录工作台' }).waitFor();
    await page.getByRole('button', {name:'使用飞书登录'}).waitFor();
    const favicon = () => page.locator('link[rel="icon"]').evaluate(async link => {
      const icon = new Image();
      icon.src = link.href;
      await icon.decode();
      return {path: new URL(link.href).pathname, width: icon.naturalWidth, height: icon.naturalHeight};
    });
    const brand = await favicon();
    assert.ok(brand.width > 0 && brand.width === brand.height);
    const missing = await page.evaluate(async () => {
      const entry = document.querySelector('script[type=module]').src;
      return (await fetch('/missing/' + new URL(entry).pathname.split('/').at(-1))).status;
    });
    assert.equal(missing, 404);
    await page.goto(setup + (setupOrigin ? '/' : '/setup'));
    assert.equal(page.url(), setup + '/setup');
    await page.getByRole('heading', { name: '首次配置' }).waitFor();
    assert.deepEqual(await favicon(), brand);
    await page.getByText(setup + '/auth/feishu/callback', { exact: true }).waitFor();
    await page.goto(setup + '/?setup=complete');
    await page.getByText('配置已保存，正在启动登录服务…').waitFor();
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});
