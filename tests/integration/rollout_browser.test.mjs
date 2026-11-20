import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
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
