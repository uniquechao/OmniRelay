import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile, mkdir} from 'node:fs/promises';
import {createRequire} from 'node:module';
import test from 'node:test';
const require=createRequire(import.meta.url);
const {chromium}=require(process.env.OMNIRELAY_PLAYWRIGHT_MODULE || 'playwright-core');
const policy="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'";
const fixture=`import {renderResult,renderDocumentImages} from '/drive-preview.js';
window.OmniRelayUI={escape:v=>String(v).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;')};
const c=document.createElement('canvas');c.width=80;c.height=40;c.getContext('2d').fillRect(0,0,80,40);
const data={document:[{type:'heading',level:2,runs:[{text:'会议总结',bold:true}]},{type:'text',runs:[{text:'保留加粗',bold:true},{text:' 与斜体',italic:true}]},{type:'text',runs:[{text:'参会人：'},{text:'@张宁 ',kind:'person',bold:true},{text:'@<img src=x onerror=alert(1)> ',kind:'person'},{text:'验收文档',kind:'document',link:'https://example.feishu.cn/docx/doc'},{text:'危险链接',link:'javascript:alert(1)'}]},{type:'bullet',runs:[{text:'整改项一'}]},{type:'bullet',runs:[{text:'整改项二'}]},{type:'image',image:{mime:'image/png',data:c.toDataURL().split(',')[1]}},{type:'image',image:{mime:'image/webp',data:'UklGRhwAAABXRUJQVlA4TA8AAAAvAQAAAAcQ/Y/+ByKi/wEA'}},{type:'table',rows:[[[{type:'text',runs:[{text:'负责人'}]}],[{type:'text',runs:[{text:'张宁'}]}]]]}]};
const root=document.querySelector('main');root.innerHTML=renderResult('docs',data,{});await renderDocumentImages(root,data,new AbortController().signal);window.ready=true;`;
test('图文预览在 CSP 下保留格式并绘出同步图片，兼容桌面手机及浅深主题',async()=>{
 const server=createServer(async(req,res)=>{
  const files={'/drive-preview.js':'frontend/src/features/drive/drive-preview.js','/style.css':'frontend/src/ui/workbench.css','/colors.css':'design/prototype/omni-relay-colors.css'};
  res.setHeader('Content-Security-Policy',policy);
  if(req.url==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/colors.css"><link rel="stylesheet" href="/style.css"></head><body><main class="drive-preview-content"></main><script type="module" src="/fixture.js"></script></body></html>');return;}
  if(req.url==='/fixture.js'){res.setHeader('Content-Type','text/javascript');res.end(fixture);return;}
  if(files[req.url]){res.setHeader('Content-Type',req.url.endsWith('.js')?'text/javascript':'text/css');res.end(await readFile(files[req.url]));return;}
  res.writeHead(404).end();
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
 try{
  const evidence=process.env.OMNIRELAY_DOCUMENT_EVIDENCE;
  if(evidence)await mkdir(evidence,{recursive:true});
  for(const width of [1280,390])for(const theme of ['light','dark']){
   const page=await browser.newPage({viewport:{width,height:800}});
   const violations=[];page.on('console',m=>{if(m.type()==='error')violations.push(m.text());});
   await page.goto('http://127.0.0.1:'+server.address().port);
   await page.waitForFunction(()=>window.ready);
   await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
   assert.equal(await page.locator('h2 strong').textContent(),'会议总结');
   assert.equal(await page.locator('.document-person').count(),2);
   assert.equal(await page.locator('strong .document-person').textContent(),'@张宁 ');
   assert.equal(await page.locator('.document-person img').count(),0);
   assert.equal(await page.locator('a').count(),1);
   assert.equal(await page.locator('a').getAttribute('href'),'https://example.feishu.cn/docx/doc');
   assert.equal(await page.locator('li').count(),2);
   assert.equal(await page.locator('td').count(),2);
   assert.equal(await page.locator('[data-document-image]').first().evaluate(c=>c.width),80);
   assert.equal(await page.locator('[data-document-image]').nth(1).evaluate(c=>c.width),2);
   assert.equal(await page.locator('figcaption:not([hidden])').count(),0);
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
   assert.equal(violations.filter(v=>v.includes('Content Security Policy')).length,0);
   if(evidence)await page.screenshot({path:evidence+'/'+width+'-'+theme+'.png',fullPage:true});
   await page.close();
  }
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
});
