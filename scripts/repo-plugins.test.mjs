import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
const source=readFileSync(new URL('../web/assets/repo-plugins.js',import.meta.url),'utf8');
async function render(data){
 const list={isConnected:true,children:[],innerHTML:'',setAttribute(){},removeAttribute(){}};
 const refresh={disabled:false};
 const container={innerHTML:'',querySelector(s){return s==='#plugin-list'?list:refresh;}};
 const root={App:{t:(zh,en)=>en,esc:s=>String(s).replaceAll('&','&amp;').replaceAll('"','&quot;').replaceAll('<','&lt;')},API:{request:async()=>data}};
 vm.runInNewContext(source,{window:root});root.RepoPlugins.mount(container,{name:'org/repo'});
 await new Promise(resolve=>setImmediate(resolve));
 return list.innerHTML;
}
test('empty plugin catalog is a normal state',async()=>{
 const html=await render({plugins:[]});assert.match(html,/No plugins found/);assert.doesNotMatch(html,/text-danger|scan could not complete/);
});
test('failed discovery does not claim an empty catalog',async()=>{
 const html=await render({plugins:[],issues:[{code:'PLUGIN_DISCOVERY_FAILED',message:'scope . (UNSUPPORTED_PATH_ENCODING)'}]});
 assert.match(html,/scan could not complete/);assert.match(html,/UNSUPPORTED_PATH_ENCODING/);assert.doesNotMatch(html,/No plugins found|text-danger/);
});
test('partial discovery keeps known plugin cards',async()=>{
 const html=await render({plugins:[{name:'good',root:'docs',path:'docs/good.sourcegraph.wasm',metadata:{}}],issues:[{code:'PLUGIN_DISCOVERY_FAILED',message:'scope other'}]});
 assert.match(html,/docs\/good.sourcegraph.wasm/);assert.match(html,/scan could not complete/);
});
