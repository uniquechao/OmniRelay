import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {mkdir,mkdtemp,rm} from 'node:fs/promises';
import {build,preview} from 'vite';
import react from '@vitejs/plugin-react';
import {chromium} from 'playwright-core';

// 比较两份实际渲染结果，避开示例数据造成的文本宽度差异。
async function compareComponents(page,theme) {
  const reference=await page.context().newPage();
  try {
    const url=pathToFileURL(path.resolve('design/prototype/omni-relay-workbench.html'));
    url.searchParams.set('theme',theme);
    url.hash='/developer/components';
    await reference.goto(url.href);
    await reference.locator('[data-demo-tree] .tree-choice').first().waitFor({state:'attached'});
    const selectors=['.list-toolbar','.row-action','.combo-values','.person-name','.date-strip','.date-summary','.markdown-document','.rich-control','.or-loading[data-size="compact"]','.chip[data-tone="success"]'];
    const properties=['backgroundColor','color','borderRadius','padding','fontSize','fontWeight','minHeight'];
    const measure=(target)=>target.evaluate(({selectors,properties})=>Object.fromEntries(selectors.map(selector=>{
      const style=getComputedStyle(document.querySelector(selector));
      return [selector,properties.map(property=>style[property])];
    })),{selectors,properties});
    assert.deepEqual(await measure(page),await measure(reference));
  } finally {await reference.close();}
}

test('共享组件六个原型入口的交互和视觉状态',async t=>{
  const root=path.resolve('tests/fixtures/react-components');
  await mkdir('.runtime',{recursive:true});
  const outDir=await mkdtemp(path.resolve('.runtime/组件验收-'));
  t.after(()=>rm(outDir,{recursive:true,force:true}));
  const config={root,configFile:false,plugins:[react()],logLevel:'warn',build:{outDir,emptyOutDir:true,assetsInlineLimit:0}};
  await build(config);
  const server=await preview({...config,preview:{host:'127.0.0.1',port:0}});
  t.after(()=>server.close());
  const browser=await chromium.launch({executablePath:process.env.OMNIRELAY_CHROME || '/usr/bin/google-chrome'});
  t.after(()=>browser.close());
  for(const width of [1366,390]) for(const theme of ['light','dark']){
    const context=await browser.newContext({viewport:{width,height:width===390?844:900},reducedMotion:theme==='dark'?'reduce':'no-preference'});
    const p=await context.newPage();
    p.setDefaultTimeout(4500);
    const errors=[];
    p.on('pageerror',e=>errors.push(e.message));
    const button=(label)=>p.getByRole('button',{name:label,exact:true});
    try {
      await p.goto(server.resolvedUrls.local[0]+'?theme='+theme);
      await p.getByRole('heading',{name:'说明'}).waitFor();
      assert.equal(await p.locator('html').getAttribute('data-theme'),theme);
      await compareComponents(p,theme);
      const rail=p.locator('.rail');
      if(width===1366){
        assert.equal(Math.round((await rail.boundingBox()).width),166);
        await p.getByRole('separator').press('ArrowLeft');
        assert.equal(await p.locator('.shell').getAttribute('data-rail'),'collapsed');
      } else assert.ok(await p.getByLabel('主导航').isVisible());
      const swatch=p.getByRole('button',{name:/^主按钮 #/});
      const color=await swatch.locator('code').innerText();
      assert.match(color,/^#[a-f0-9]{6}$/i);
      const themeButton=p.locator('.theme-toggle');
      await themeButton.click();
      await p.waitForFunction(old=>document.documentElement.dataset.theme!==old,theme);
      await p.waitForFunction(old=>[...document.querySelectorAll('.palette-swatch')].find(b=>b.querySelector('strong').textContent==='主按钮').querySelector('code').textContent!==old,color);
      await p.context().grantPermissions(['clipboard-read','clipboard-write']);
      await swatch.focus();await p.keyboard.press('Enter');
      assert.equal((await p.evaluate(()=>navigator.clipboard.readText())).toLowerCase(),(await swatch.locator('code').innerText()).toLowerCase());
      await button('关闭提示').click();
      await p.locator('.login-brand[data-motion-ready]').waitFor({state:'attached'});
      const changing=await p.locator('.login-motion').evaluate(async canvas=>{
        const before=canvas.toDataURL();
        await new Promise(resolve=>setTimeout(resolve,130));
        return before!==canvas.toDataURL();
      });
      assert.equal(changing,theme!=='dark');
      const animations=await p.locator('.or-loading .ink').first().evaluate(el=>getComputedStyle(el).animationName);
      if(theme==='dark') assert.equal(animations,'none');
      assert.equal(await p.locator('.crumb [aria-current]').innerText(),'组件');
      await p.getByRole('tab',{name:'列表'}).press('End');
      assert.equal(await p.getByRole('tabpanel').innerText(),'详情视图');
      const search=p.getByRole('searchbox');
      await p.getByRole('button',{name:'筛选与选择'}).click();
      await p.locator('[data-mode]').selectOption('single');
      await button('收起').click();
      await p.locator('tbody input').first().check();
      assert.match(await p.locator('[data-selection-count]').innerText(),/已选 1 项/);
      await p.locator('[data-sort]').first().click();
      assert.equal(await p.locator('[aria-sort]').getAttribute('aria-sort'),'ascending');
      await search.fill('项目 12');
      await button('查看 项目 12').click();
      assert.equal(await p.locator('output').first().innerText(),'项目 12');
      await search.fill('');
      await button('下一页').click();
      assert.equal(await p.locator('tbody tr').count(),2);
      await button('确认').focus(); await p.keyboard.press('Enter');
      await p.getByRole('dialog',{name:'确认操作'}).waitFor();
      await p.keyboard.press('Escape');
      await p.waitForFunction(()=>document.activeElement?.textContent==='确认');
      for(const [label,size] of [['抽屉',480],['抽屉加宽',720]]){
        await button(label).click();
        const d=p.getByRole('dialog',{name:'编辑'});
        await d.waitFor();
        await d.getByLabel('草稿').fill('待保存');
        const bounds=await d.boundingBox();
        assert.ok(Math.abs(bounds.width-Math.min(width,size))<2,`抽屉宽度 ${bounds.width}`);
        await p.keyboard.press('Escape');
        await button('继续编辑').click();
        await p.waitForFunction(()=>document.activeElement?.name==='draft');
        await p.keyboard.press('Escape'); await button('不保存').click();
        await p.waitForFunction(s=>document.activeElement?.textContent===s,label);
      }
      await p.getByRole('textbox',{name:'标题',exact:true}).fill('组件验收');
      await p.getByLabel('备注').fill('备注内容');
      await p.getByLabel('日期',{exact:true}).fill('2026-12-31');
      for(const label of ['分类','人员']){
        const combo=p.getByRole('combobox',{name:label,exact:true});
        await combo.fill('linx');await combo.press('ArrowDown');await combo.press('Enter');await combo.press('Escape');
      }
      await button('所属组织，选择组织').click();
      await p.locator('dialog [data-org="dept"]').click();
      await button('确认选择').click();
      await p.getByRole('textbox',{name:'正文'}).fill('可编辑正文');
      await button('提交').click();
      const form=JSON.parse(await p.locator('#submitted').innerText());
      assert.deepEqual([form.people,form.category,form.org,form.note,form.date],['lin','lin','dept','备注内容','2026-12-31']);
      assert.match(form.body,/可编辑正文/);
      await button('切换编辑器').click(); await button('切换编辑器').click();
      await p.getByRole('textbox',{name:'正文'}).waitFor();
      await button('原文').click(); assert.ok(await p.getByLabel('Markdown 原文').isVisible());
      await button('渲染').click(); await p.getByRole('heading',{name:'说明'}).waitFor();
      await p.locator('.person-name').click();await p.keyboard.press('Escape');
      await button('后一天').click();assert.match(await p.locator('.date-summary').first().innerText(),/2027年1月1日/);
      await button('下一周').click();assert.match(await p.locator('.date-summary').last().innerText(),/2027 年第 1 周/);
      assert.equal(await p.getByRole('progressbar',{name:'正在读取'}).getAttribute('aria-valuenow'),'42');
      assert.ok(await button('正在保存').isDisabled());
      await button('提示').click(); await p.locator('.toast').waitFor();await button('关闭提示').click();
      assert.equal(await p.locator('.chip').first().innerText(),'已完成');
      assert.equal(await p.locator('.multiline').innerText(),'第一行\n第二行');
      assert.deepEqual(errors,[]);
      t.diagnostic(`${width} 像素、${theme} 初始主题通过`);
    } finally {await context.close();}
  }
});
