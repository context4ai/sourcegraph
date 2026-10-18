import {test} from 'node:test';
import assert from 'node:assert/strict';
import model from '../web/assets/query-error-model.js';
const entry=b=>({operation:'read_many',batch:b});
test('counts successful empty files and isolated failures',()=>{
 assert.deepEqual(model.batch(entry({total:3,succeeded:2,failed:1,content_items:2})),{total:3,succeeded:2,failed:1,unfinished:0,hasContent:true});
});
test('partial content is not mislabeled as full item success',()=>{
 assert.deepEqual(model.batch(entry({total:2,succeeded:0,failed:1,content_items:1})),{total:2,succeeded:0,failed:1,unfinished:1,hasContent:true});
});
test('old and invalid records do not invent success counts',()=>{
 for(const row of [{operation:'read_many'},{operation:'read'},entry({total:2,succeeded:3,failed:0,content_items:3}),entry({total:2,succeeded:1,failed:1,content_items:0}),entry({total:0,succeeded:0,failed:0,content_items:0})])assert.equal(model.batch(row),null);
});
