import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {matchPattern,resolveTarget,permitted} from '../../frontend/src/routes/targets.ts';

const samples=JSON.parse(await readFile(new URL('../fixtures/workbench-paths.json',import.meta.url)));
test('共享路径样例逐角色校验',()=>{
  for(const [raw,canonical,roles] of samples.cases){
    const target=resolveTarget(raw);
    assert.equal(target?.path ?? '',canonical,raw);
    for(const [role,access] of Object.entries(samples.access))
      assert.equal(!!target && permitted(target.route,access),roles.includes(role),role+' '+raw);
  }
});
test('测试注册表中的参数化资源路径',()=>{
  for(const [pattern,raw,valid] of samples.patterns){
    const registry=[{id:'sample',path:'/#/sample',title:'测试资源',group:'工作空间',capability:'workbench',
      parse:path=>matchPattern(pattern,path)?{path}:undefined}];
    assert.equal(!!resolveTarget(raw,registry),valid,raw);
    assert.equal(resolveTarget(raw),undefined);
  }
});
