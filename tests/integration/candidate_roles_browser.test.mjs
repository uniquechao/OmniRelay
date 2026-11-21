import assert from 'node:assert/strict';
import {test} from 'node:test';
import {chromium} from 'playwright-core';

const origin=process.env.OMNIRELAY_BROWSER_ORIGIN;
test('默认入口按本地授权显示菜单并约束直达页面和接口',{skip:!origin},async t=>{
  const browser=await chromium.launch({executablePath:process.env.OMNIRELAY_CHROME});
  t.after(()=>browser.close());
  const roles=[
    ['historical','workbench',true,false,false],
    ['operator','system/users',false,true,true],
    ['admin','workbench',true,true,true],
    ['auditor','system/audit',true,false,true],
    ['project_operator','system/users',true,true,true]
  ];
  for(const [name,landing,business,users,audit] of roles){
    const context=await browser.newContext({ignoreHTTPSErrors:true});
    try {
      const page=await context.newPage();
      page.setDefaultTimeout(7000);
      await page.goto(origin);
      await page.locator('#login-username').fill(name);
      await page.locator('#login-password').fill('fixture-password-only');
      await page.locator('#password-login button[type=submit]').click();
      await page.waitForURL('**/#/'+landing);
      const me=await (await page.request.get(origin+'/api/v1/me')).json();
      assert.equal(me.principal.name,name);
      assert.equal(me.access.default_route,'/#/'+landing);
      const checks=[
        ['workbench','workbench','/api/v1/projects',business],
        ['drive','feishu/drive','/api/v1/feishu/files/formats',business],
        ['users','system/users','/api/v1/system/accounts',users],
        ['audit','system/audit','/api/v1/system/audit-events',audit]
      ];
      for(const [nav,path,api,allowed] of checks) {
        const menu=page.locator('.rail a[data-nav="'+nav+'"]');
        await menu.waitFor({state:'attached'});
        assert.equal(await menu.evaluate(link=>!link.hidden),allowed,name+' '+nav);
        await page.goto(origin+'/#/'+path);
        await page.waitForURL('**/#/'+(allowed?path:landing));
        assert.equal((await page.request.get(origin+api)).status(),allowed?200:403,name+' '+api);
      }
    } finally {await context.close();}
  }
});
