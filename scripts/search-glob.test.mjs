import {test} from 'node:test';
import assert from 'node:assert/strict';
import {reconcileGlobs,parseLocation,searchURL} from '../web/assets/search-model.mjs';
test('URL and two-field edits preserve glob precedence',()=>{
 for(const glob of [['!*_test.go','*.go'],['*.go','!*_test.go'],['*.go','!x.go','x.go']]){
  const restored=parseLocation(new URL(searchURL('org/repo','',{pattern:'x',glob}),'https://example.test').search).search.glob;
  assert.deepEqual(restored,glob);
  assert.deepEqual(reconcileGlobs(restored,restored.filter(g=>!g.startsWith('!')),restored.filter(g=>g.startsWith('!')).map(g=>g.slice(1))),glob);
 }
 assert.deepEqual(reconcileGlobs(['!x.go','*.go'],['*.go','*.ts'],['x.go']),['!x.go','*.go','*.ts']);
 assert.deepEqual(reconcileGlobs(['!x.go','*.go'],['*.go'],[]),['*.go']);
});

test('comma, braces and character classes survive UI fields',async()=>{
 const {formatGlobs,splitGlobs,fileGlob}=await import('../web/assets/search-model.mjs');
 for(const glob of ['src/a,b.ts',fileGlob('src/{a,b}.ts'),'*.{ts,tsx}','[a,b].ts']){
  const text=formatGlobs([glob]);
  assert.equal(splitGlobs(text).length,1);
  assert.equal(formatGlobs(splitGlobs(text)),text);
 }
});
