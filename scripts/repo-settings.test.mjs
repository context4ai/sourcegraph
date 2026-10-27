import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createRequire} from 'node:module';
import vm from 'node:vm';
const require=createRequire(import.meta.url);
globalThis.App={t:zh=>zh};
const M=require('../web/assets/management.js');
const source=readFileSync(new URL('../web/assets/repo-page.js',import.meta.url),'utf8');
const settings=source.slice(source.indexOf('  function ensureDraft()'),source.indexOf('  function capture()'));
test('settings render for seeded default-branch repositories and preserve explicit branches',()=>{
 for(const branches of [null,undefined,[],['release']]){
  const repo={policy:{branches,default_branch:true,count:1},index_mode:'full'};
  const ctx={repo,M,A:{user:()=>({})},canRepo:()=>true,keep:()=>[1,'commit'],t:zh=>zh,esc:String,btn2:''};
  vm.runInNewContext('let draft;'+settings+'; result={html:settings(),draft};',ctx);
  assert.match(ctx.result.html,/id="pgit-timeout"/);
  assert.match(ctx.result.html,/value="0" selected/);
  assert.equal(ctx.result.draft.defaultBranch,true);
  assert.deepEqual(Array.from(ctx.result.draft.branches),branches||[]);
  ctx.result.draft.branches.push('another');
  if(branches)assert.ok(!branches.includes('another'));
 }
});
