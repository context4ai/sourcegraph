import test from 'node:test';
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
const require=createRequire(import.meta.url);
globalThis.App={t:zh=>zh};
const M=require('../web/assets/management.js');
test('Git timeout choices and inherit are explicit',()=>{
 for(const n of [0,15,30,45,60]) assert.equal(M.gitTimeout(String(n)),n);
 for(const n of ['',null,undefined,true,1,16,61,90]) assert.throws(()=>M.gitTimeout(n));
 assert.throws(()=>M.gitTimeout(0,false));
 assert.match(M.gitTimeoutOptions(0),/value="0" selected/);
 assert.match(M.gitTimeoutOptions(60),/value="60" selected/);
 assert.doesNotMatch(M.gitTimeoutOptions(15,false),/value="0"/);
});
