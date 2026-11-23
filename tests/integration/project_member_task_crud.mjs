import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import {mkdir} from 'node:fs/promises';
const {chromium}=createRequire(import.meta.url)(process.env.OMNIRELAY_PLAYWRIGHT_MODULE);
const origin=process.env.H3_ORIGIN,root='/api/v1/projects/h3';
const browser=await chromium.launch({executablePath:process.env.OMNIRELAY_CHROME,args:['--no-sandbox']});
const errors=[];
const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1440,height:1000},reducedMotion:'reduce'});
const page=await context.newPage();page.setDefaultTimeout(8000);page.on('pageerror',err=>errors.push(err.message));
try{
 const login=await context.request.post(origin+'/auth/login',{headers:{Origin:origin},data:{username:'manager',password:process.env.H3_PASSWORD}});assert.ok(login.ok());
 const me=await(await context.request.get(origin+'/api/v1/me')).json();
 const headers={Origin:origin,'X-CSRF-Token':me.csrf_token};
 async function api(method,path,data,status=200){const r=await context.request.fetch(origin+root+path,{method,headers,data});const body=await r.json();assert.equal(r.status(),status,JSON.stringify(body));return body;}
 await api('PUT','/members/manager/access',{role:'manager'});
 await page.goto(origin+'/#/projects/h3/members');
 await page.getByRole('button',{name:'添加成员',exact:true}).click();
 let dialog=page.getByRole('dialog');
 await dialog.locator('[data-user-choice]').first().waitFor();
 await mkdir('.runtime/evidence',{recursive:true});
 await page.screenshot({path:'.runtime/evidence/system-user-picker.png'});
 await dialog.getByLabel('选择人员').fill('second');
 await dialog.locator('[data-user-choice]').filter({hasText:'项目成员'}).click();
 assert.deepEqual(await dialog.getByLabel('项目角色',{exact:true}).locator('option').allTextContents(),['项目管理员','项目成员']);
 await dialog.getByLabel('项目角色',{exact:true}).selectOption('project_admin');await dialog.getByLabel('项目职责').fill('负责联调验收');
 await dialog.getByRole('button',{name:'添加成员',exact:true}).click();
 await page.getByRole('button',{name:'编辑 项目成员',exact:true}).waitFor();
 let members=(await api('GET','/members')).data.members;let second=members.find(m=>m.account_id==='second');assert.equal(second.responsibility,'负责联调验收');assert.equal(second.project_role,'project_admin');assert.equal(second.role,'member');
 await page.getByRole('button',{name:'编辑 项目成员',exact:true}).click();
 dialog=page.getByRole('dialog');await dialog.getByLabel('项目职责').fill('更新后的验收职责');
 // A concurrent plugin/API update must prevent the stale browser draft from overwriting it.
 await api('PUT','/members/second',{expected_version:second.version,responsibility:'其他入口更新'});
 await dialog.getByRole('button',{name:'保存修改',exact:true}).click();await dialog.getByText(/人员或角色已更新/).waitFor();assert.equal(await dialog.getByLabel('项目职责').inputValue(),'更新后的验收职责');
 await dialog.press('Escape');await page.getByRole('dialog').last().getByRole('button',{name:'不保存',exact:true}).click();
 await page.reload();await page.getByRole('button',{name:'编辑 项目成员',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByLabel('项目职责').fill('最终验收职责');
 for(const width of [1440,390])for(const theme of ['light','dark']){
   await page.setViewportSize({width,height:1000});await page.evaluate(t=>{document.documentElement.dataset.theme=t;},theme);await page.screenshot({path:`.runtime/evidence/member-editor-${width}-${theme}.png`});
 }
 await page.setViewportSize({width:1440,height:1000});await page.evaluate(()=>{document.documentElement.dataset.theme='light';});
 await dialog.getByRole('button',{name:'保存修改',exact:true}).click();await page.getByRole('cell',{name:'最终验收职责',exact:true}).waitFor();
 await mkdir('.runtime/evidence',{recursive:true});await page.screenshot({path:'.runtime/evidence/members-1440-light.png'});
 await page.goto(origin+'/#/projects/h3/tasks/new');
 await page.getByLabel('标题',{exact:true}).fill('浏览器创建任务');await page.getByLabel('正文',{exact:true}).fill('验证任务创建与删除');await page.getByLabel('截止日期').fill('2026-12-01');
 // Owner and participants select the same system identities, within project membership.
 await page.getByLabel('主负责人',{exact:true}).fill('second');
 await page.locator('[data-picker=owner_member_id] [data-pick=second]').waitFor();
 await page.getByLabel('主负责人',{exact:true}).press('ArrowDown');await page.keyboard.press('Enter');
 await page.locator('summary').filter({hasText:'验收条件'}).click();
 await page.getByLabel('参与人',{exact:true}).fill('manager');
 await page.locator('[data-picker=participant_ids] [data-pick=manager]').click();

 await page.getByRole('button',{name:'创建任务',exact:true}).click();await page.getByRole('button',{name:'保存任务',exact:true}).waitFor();
 const taskId=page.url().split('/').at(-1);let task=await api('GET','/tasks/'+taskId);assert.equal(task.due_at,'2026-12-01');assert.equal(task.owner_member_id,'second');assert.deepEqual(task.participant_ids,['manager']);
 await page.getByLabel('标题',{exact:true}).fill('浏览器修改任务');await page.getByRole('button',{name:'保存任务',exact:true}).click();await page.getByText('任务已保存。',{exact:true}).waitFor();
 task=await api('GET','/tasks/'+taskId);assert.equal(task.title,'浏览器修改任务');
 for(const width of [1440,390])for(const theme of ['light','dark']){
   await page.setViewportSize({width,height:1000});await page.evaluate(t=>{document.documentElement.dataset.theme=t;},theme);await page.screenshot({path:`.runtime/evidence/task-${width}-${theme}.png`});
   assert.ok(await page.getByRole('button',{name:'删除任务',exact:true}).isVisible());
   const pathBox=await page.locator('.task-inspector-path').boundingBox(),deleteBox=await page.locator('[data-delete]').boundingBox();
   assert.ok(pathBox.x+pathBox.width<=deleteBox.x || pathBox.y+pathBox.height<=deleteBox.y,'任务路径不得与操作按钮重叠');
 }
 await page.getByRole('button',{name:'删除任务',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByLabel('删除原因').fill('验收样本清理');await dialog.getByRole('button',{name:'删除任务',exact:true}).click();
 await page.getByRole('button',{name:'新建任务',exact:true}).waitFor();await api('GET','/tasks/'+taskId,undefined,404);
 let board=(await api('GET','/tasks')).data;assert.equal(board.total,0);
 // Membership removal must not orphan unfinished tasks.
 const assigned=await api('POST','/tasks',{title:'需交接任务',owner_member_id:'second'});second=(await api('GET','/members')).data.members.find(m=>m.account_id==='second');await api('PUT','/members/second',{role:'revoked',expected_version:second.version},409);
 await api('DELETE','/tasks/'+assigned.id,{expected_version:assigned.version,change_reason:'清理',request_key:'cleanup-assigned'});
 await page.goto(origin+'/#/projects/h3/members');await page.getByRole('button',{name:'编辑 项目成员',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByRole('button',{name:'移除成员',exact:true}).click();await page.getByRole('dialog').last().getByRole('button',{name:'移除成员',exact:true}).click();await page.getByRole('button',{name:'添加成员',exact:true}).waitFor();
 await page.getByRole('button',{name:'编辑 项目成员',exact:true}).waitFor({state:'detached'});
 assert.equal((await api('GET','/members')).data.members.length,1);
 const manager=(await api('GET','/members')).data.members[0];await api('PUT','/members/manager',{role:'revoked',expected_version:manager.version},409);
 // Reader sees data but cannot manage it through either the page or direct API calls.
 await api('PUT','/members/second',{role:'reader',expected_version:second.version+1});
 const reader=await browser.newContext({ignoreHTTPSErrors:true});const rp=await reader.newPage();await reader.request.post(origin+'/auth/login',{headers:{Origin:origin},data:{username:'second',password:process.env.H3_PASSWORD}});const rme=await(await reader.request.get(origin+'/api/v1/me')).json();
 await rp.goto(origin+'/#/projects/h3/members');await rp.getByRole('heading',{name:'项目成员',exact:true}).waitFor();assert.equal(await rp.getByRole('button',{name:'添加成员',exact:true}).count(),0);
 const denied=await reader.request.put(origin+root+'/members/manager',{headers:{Origin:origin,'X-CSRF-Token':rme.csrf_token},data:{expected_version:manager.version,responsibility:'不允许'}});assert.equal(denied.status(),404);
 await rp.goto(origin+'/#/projects/h3/tasks');await rp.getByRole('region',{name:'正式任务看板',exact:true}).waitFor();assert.equal(await rp.getByRole('button',{name:'新建任务',exact:true}).count(),0);
 const visible=await api('POST','/tasks',{title:'只读成员查看任务'});
 await rp.goto(origin+'/#/projects/h3/tasks?task='+visible.id);
 await rp.getByLabel('任务标题',{exact:true}).waitFor();
 assert.equal(await rp.getByRole('button',{name:'删除任务',exact:true}).count(),0);
 assert.deepEqual(errors,[]);console.log('PASS real API/browser member add/edit/conflict/remove/last-manager/reader; task create/update/delete; desktop/mobile light/dark');
}finally{await browser.close();}
