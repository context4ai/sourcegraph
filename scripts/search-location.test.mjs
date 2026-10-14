import {test} from 'node:test';
import assert from 'node:assert/strict';
import {parseLocation,latestURL} from '../web/assets/search-model.mjs';

const SHA='73671aca1a4c8c65e8f31adaac7aebba1d505aae';

test('a branch name in commit resolves like revision',()=>{
 assert.equal(parseLocation('?repo=org/repo&path=a.ts&commit=main').revision,'main');
 assert.equal(parseLocation('?repo=org/repo&commit='+SHA).revision,SHA);
 assert.equal(parseLocation('?repo=org/repo').revision,'');
});

test('malformed addresses carry INVALID_LOCATION',()=>{
 for(const search of ['?repo=org/repo&commit=main&revision=dev','?repo=bad','?repo=org/repo&path=../x','?repo=org/repo&commit=a%20b']){
  assert.throws(()=>parseLocation(search),e=>e.code==='INVALID_LOCATION');
 }
});

test('latest links keep the path and range and record only a full requested SHA',()=>{
 assert.equal(latestURL('org/repo','src/a.ts',[3,5],SHA),'/sourcegraph/code?repo=org%2Frepo&path=src%2Fa.ts&requested='+SHA+'#L3-L5');
 assert.equal(parseLocation(new URL(latestURL('org/repo','a.ts',null,SHA),'https://example.test').search).requested,SHA);
 assert.equal(parseLocation('?repo=org/repo&requested=main').requested,'');
});
