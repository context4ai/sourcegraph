import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import model from '../web/assets/query-error-model.js';
const source=readFileSync(new URL('../web/assets/query-errors-page.js',import.meta.url),'utf8');
async function render(entries,lang='en'){
 const nodes=new Map();
 const get=s=>{if(!nodes.has(s))nodes.set(s,{innerHTML:'',textContent:'',classList:{toggle(){}},replaceChildren(){this.innerHTML='';},querySelectorAll(){return [];},addEventListener(){},close(){},showModal(){},focus(){}});return nodes.get(s);};
 const app={t:(zh,en)=>lang==='en'?en:zh,esc:s=>String(s).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('"','&quot;'),lang:()=>lang,user:()=>({}),canManage:()=>true};
 const win={App:app,QueryErrorModel:model,API:{ready:Promise.resolve(),request:async()=>({items:entries,next_cursor:''})},addEventListener(){}};
 vm.runInNewContext(source,{window:win,document:{querySelector:get,addEventListener(){}},AbortController});
 await new Promise(resolve=>setImmediate(resolve));return get('#log-rows').innerHTML;
}
test('page renders partial success and escapes diagnostic data in both languages',async()=>{
 const row={id:'one',operation:'read_many',transport:'mcp',time:'2026-01-01',repository:'<img src=x>',code:'FILE_NOT_FOUND',message:'<script>bad</script>',batch:{total:5,succeeded:4,failed:1,content_items:4}};
 const en=await render([row]);assert.match(en,/Partial success/);assert.match(en,/4\/5 items succeeded, 1 failed/);assert.doesNotMatch(en,/<img|<script/);
 const zh=await render([row],'zh');assert.match(zh,/部分成功/);assert.match(zh,/成功 4\/5 项，失败 1 项/);
});
test('old logs do not fabricate batch success',async()=>{
 const html=await render([{id:'old',operation:'read_many',time:'2026-01-01'}]);
 assert.match(html,/retry status unknown/);assert.doesNotMatch(html,/Partial success/);
});
