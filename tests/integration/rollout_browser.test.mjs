import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {mkdir,mkdtemp,readFile,readdir,writeFile} from 'node:fs/promises';
import {createServer} from 'node:net';
import {resolve} from 'node:path';
import test from 'node:test';
import {chromium} from 'playwright-core';

const old=process.env.OMNIRELAY_ROLLOUT_OLD;
const candidate=process.env.OMNIRELAY_ROLLOUT_NEW;
const archive=process.env.OMNI_UI_ARCHIVE;

test('混合副本、缓存的旧页面与回滚后仍打开的工作台', {skip:!(old && candidate && archive)}, async t=>{
  await mkdir('.runtime',{recursive:true});
  const dir=await mkdtemp(resolve('.runtime/rollout-'));
  const probe=createServer();
  await new Promise(done=>probe.listen(0,'127.0.0.1',done));
  const port=probe.address().port;
  await new Promise(done=>probe.close(done));
  const origin='http://127.0.0.1:'+port;
  const config=dir+'/nginx.conf';
  const snippet=(await readFile('deploy/k8s/base/go/nginx/ui.conf','utf8')).replaceAll('/var/lib/omnirelay/ui',resolve(archive));
  const command=(...args)=>execFileSync('nginx',['-p',dir,'-c',config,...args],{stdio:'ignore'});
  async function select(origins,start=false){
    const upstream=origins.map(url=>'server '+new URL(url).host+';').join('\n');
    await writeFile(config,`pid "${dir}/pid";
error_log "${dir}/error.log";
events {}
http {
  include /etc/nginx/mime.types;
  access_log off;
  client_body_temp_path "${dir}/body";
  proxy_temp_path "${dir}/proxy";
  fastcgi_temp_path "${dir}/fastcgi";
  uwsgi_temp_path "${dir}/uwsgi";
  scgi_temp_path "${dir}/scgi";
  upstream app { ${upstream} }
  server { listen 127.0.0.1:${port}; ${snippet}
    location / { proxy_pass http://app; }
  }
}`);
    command(...(start?[]:['-s','reload']));
  }
  const get=async url=>(await fetch(url,{headers:{connection:'close'}})).text();
  async function waitEntry(html){
    for(let i=0;i<40;i++){
      if(await get(origin)===html)return;
      await new Promise(done=>setTimeout(done,100));
    }
    assert.fail('入口切换超时');
  }
  const oldHTML=await get(old),newHTML=await get(candidate);
  assert.match(oldHTML,/src="app\.js"/);
  assert.match(newHTML,/src="\/assets\/index-/);
  await select([old,candidate],true);
  t.after(()=>command('-s','quit'));
  const seen=new Set();
  for(let i=0;i<12 && seen.size<2;i++)seen.add(await get(origin));
  assert.deepEqual(seen,new Set([oldHTML,newHTML]));
  const browser=await chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(()=>browser.close());
  const page=await browser.newPage();
  await select([candidate]);
  await waitEntry(newHTML);
  await page.route(origin+'/',r=>r.fulfill({contentType:'text/html',body:oldHTML}));
  await page.goto(origin);
  await page.locator('#password-login').waitFor();
  await page.unroute(origin+'/');
  const entry='**/assets/index-*.js';
  await page.route(entry,r=>r.fulfill({status:404}));
  await page.reload();
  await page.getByText('正在加载页面。若长时间没有响应，请刷新重试。').waitFor();
  await page.unroute(entry);
  await page.reload();
  await page.getByRole('heading',{name:'登录工作台'}).waitFor();
  assert.equal(await page.locator('script[src^="/assets/index-"]').count(),1);
  const chunks=(await readdir(resolve(archive,'assets'))).filter(name=>/^task-detail-.*\.js$/.test(name));
  let lazy;
  for(const name of chunks)if((await fetch(candidate+'/assets/'+name)).ok){lazy=name;break;}
  assert.ok(lazy);
  await select([old]);
  await waitEntry(oldHTML);
  assert.equal(await page.evaluate(async url=>typeof (await import(url)).mountTaskDetail,'/assets/'+lazy),'function');
  const missing=await fetch(origin+'/assets/missing.js');
  assert.equal(missing.status,404);
  assert.equal(missing.headers.get('cache-control'),'no-store');
});

// 由演练执行者在同一个 Deployment 上升级、回退。浏览器通过稳定的候选入口观察切换。
const clusterOrigin = process.env.OMNIRELAY_ROLLOUT_CLUSTER;
test('集群候选回滚期间保留已打开页面并逐文件核对同源归档', {skip: !clusterOrigin, timeout: 900_000}, async t => {
  assert.match(clusterOrigin, /^https:\/\/127\.0\.0\.1:\d+$/);
  const manifest = JSON.parse(await readFile(process.env.OMNIRELAY_ROLLOUT_MANIFEST, 'utf8'));
  const assets = Object.entries(manifest);
  assert.ok(assets.some(([name]) => name === 'legacy/app.js'));
  const chunk = assets.find(([name]) => /^assets\/task-detail-[\w-]+\.js$/.test(name))?.[0];
  assert.ok(chunk);
  const browser = await chromium.launch({executablePath: process.env.OMNIRELAY_CHROME});
  t.after(() => browser.close());
  const context = await browser.newContext({ignoreHTTPSErrors: true});
  const page = await context.newPage();
  const oldHTML = await (await context.request.get(clusterOrigin)).text();
  assert.match(oldHTML, /src="app\.js"/);
  await page.goto(clusterOrigin);
  await page.locator('#password-login').waitFor();
  const oldDocument = await page.evaluateHandle(() => document);
  console.log('旧版页面已打开');
  async function waitForVersion(matches) {
    const deadline = Date.now() + 400_000;
    while (Date.now() < deadline) {
      const response = await context.request.get(clusterOrigin, {timeout: 5000}).catch(() => null);
      if (response?.ok() && matches(await response.text())) return;
      await new Promise(done => setTimeout(done, 500));
    }
    assert.fail('等待集群入口完成指定版本切换超时');
  }
  async function verifyArchive() {
    for (const [file, expected] of assets) {
      assert.match(file, /^(legacy|assets)\/[\w.-]+$/);
      const path = file.replace(/^legacy\//, '');
      const response = await context.request.get(clusterOrigin + '/' + path);
      assert.equal(response.status(), 200, path);
      assert.equal(response.headers()['x-omnirelay-asset-archive'], '1', path);
      assert.equal(createHash('sha256').update(await response.body()).digest('hex'), expected, path);
    }
  }
  await waitForVersion(html => html.includes('src="/assets/index-'));
  assert.ok(await page.evaluate(doc => doc === document, oldDocument));
  assert.equal(await page.evaluate(async () => (await fetch('/app.js', {cache: 'reload'})).status), 200);
  await verifyArchive();
  const cachedPage = await context.newPage();
  await cachedPage.route(clusterOrigin + '/', route => route.fulfill({contentType: 'text/html', body: oldHTML}));
  await cachedPage.goto(clusterOrigin);
  await cachedPage.locator('#password-login').waitFor();
  await cachedPage.close();
  await page.reload();
  await page.getByRole('heading', {name: '登录工作台'}).waitFor();
  const candidateDocument = await page.evaluateHandle(() => document);
  console.log('候选页面已打开，旧 HTML 与归档校验通过');
  await waitForVersion(html => html === oldHTML);
  assert.ok(await page.evaluate(doc => doc === document, candidateDocument));
  const lazyResponse = page.waitForResponse(response => response.url() === clusterOrigin + '/' + chunk);
  assert.equal(await page.evaluate(async path => typeof (await import(path)).mountTaskDetail, '/' + chunk), 'function');
  assert.equal((await lazyResponse).status(), 200);
  await verifyArchive();
  const missing = await context.request.get(clusterOrigin + '/assets/missing.js');
  assert.equal(missing.status(), 404);
  assert.equal(missing.headers()['cache-control'], 'no-store');
  await page.reload();
  await page.locator('#password-login').waitFor();
  t.diagnostic('切回后，原候选标签页加载任务详情成功，刷新后旧登录页可用');
});
