import {defaultRepository, selectableRepository, FULL_SHA, escapeHTML as esc, parseLocation, normalizeSearch, highlightBytes, codeURL, searchURL, searchBody, splitGlobs, reconcileGlobs, formatGlobs} from './search-model.mjs';
const A = window.App, API = window.API, R = A.Repo, t = A.t, $ = s => document.querySelector(s);
const state = {repo:'', revision:'', branch:'', catalog:[], seq:0, result:null, showAll:false, expanded:new Set(), error:null, roundtrip:0, detail:null, details:new Map()};
let controller, timer, lastSent=0, composing=false, typing=null;
const prepareKeys=new Map();
function savedRepo(){try{return localStorage.getItem('cs.lastRepo')||'';}catch{return '';}}
function rememberRepo(repo){try{localStorage.setItem('cs.lastRepo',repo);}catch{}}
function canPrepare(){return API.policy?.public_prepare===true||A.canManage();}
const field = new window.Field($('#field')); // Decorative state only; it never manufactures progress counts.
const input = $('#q');
// Samples read like the rg calls an Agent would make; each sets the same options as the toggles.
const sampleSearches = [{label:"rg 'sourcegraph|context'",pattern:'sourcegraph|context'},{label:"rg -F 'context4ai/'",pattern:'context4ai/',fixedStrings:true},{label:"rg -g '*.md' 'MCP'",pattern:'MCP',glob:['*.md']}];
const options = {caseSensitive:true, regex:true};
let orderedGlobs=[];
function currentSearch(){orderedGlobs=reconcileGlobs(orderedGlobs,splitGlobs($('#glob-include').value),splitGlobs($('#glob-exclude').value));return {pattern:input.value, fixedStrings:!options.regex, ignoreCase:!options.caseSensitive, glob:orderedGlobs.slice()};}
function renderOptions(){
 const set=(id,on,label)=>{const b=$(id);b.setAttribute('aria-pressed',String(on));b.title=label;b.setAttribute('aria-label',label);};
 const globs=$('#glob-include').value||$('#glob-exclude').value;
 set('#opt-case',options.caseSensitive,t('区分大小写（关闭即 rg -i）','Match case (off is rg -i)'));
 set('#opt-regex',options.regex,t('正则（关闭即 rg -F）','Regular expression (off is rg -F)'));
 set('#opt-glob',!$('#glob-row').classList.contains('hidden')||!!globs,t('按 glob 过滤文件（rg -g）','Filter files by glob (rg -g)'));
}
function showGlobs(open){$('#glob-row').classList.toggle('hidden',!open);$('#glob-row').classList.toggle('grid',open);renderOptions();}
function applySearch(search){
 input.value=search.pattern||'';options.regex=!search.fixedStrings;options.caseSensitive=!search.ignoreCase;
 const glob=search.glob||[];orderedGlobs=glob.slice();$('#glob-include').value=formatGlobs(glob.filter(g=>!g.startsWith('!')));$('#glob-exclude').value=formatGlobs(glob.filter(g=>g.startsWith('!')).map(g=>g.slice(1)));
 showGlobs(glob.length>0);
}
const row = name => state.catalog.find(r=>r.name===name);
const searchable = selectableRepository;
function abort() { state.seq++; controller?.abort(); clearTimeout(timer); $('#progress').classList.remove('on'); }
function setState(kind) {
 const map={idle:[t('就绪','ready'),'text-muted/60'],typing:[t('输入中','typing'),'text-muted/60'],searching:[t('正在搜索','searching'),'dot-live'],done:[t('完成','complete'),'text-ok'],partial:[t('部分结果','partial results'),'text-warn'],empty:[t('无匹配','no matches'),'text-muted/60'],error:[t('查询有误','query error'),'text-danger'],failed:[t('未完成','not completed'),'text-danger']};
 const x=map[kind]||map.idle; $('#field-state').innerHTML='<span class="dot '+x[1]+'"></span>'+x[0];
}
function indexedFiles(){const v=state.detail&&R.servable(state.detail);return v&&Number.isFinite(v.files)?v.files:null;}
function metrics(out) {
 if(!out){
  const files=indexedFiles();
  $('#m-a').textContent='-';$('#m-a-u').textContent='';$('#m-a-l').textContent=t('引擎耗时','engine time');
  if(files!=null)A.roll($('#m-b'),files.toLocaleString());else $('#m-b').textContent='-';
  $('#m-b-l').textContent=t('已索引文件','indexed files');$('#m-c').textContent='-';$('#m-c-l').textContent=t('匹配','matches');
  return;
 }
 const partial=out.meta.Partial||out.meta.Truncated;
 if(out.engineMS!=null){A.roll($('#m-a'),out.engineMS<10?out.engineMS.toFixed(1):String(Math.round(out.engineMS)));$('#m-a-u').textContent='ms';}else{$('#m-a').textContent='-';$('#m-a-u').textContent='';}
 $('#m-a-l').textContent=t('引擎耗时 · 往返 '+state.roundtrip+' ms','engine · '+state.roundtrip+' ms round trip');
 A.roll($('#m-b'),String(out.files.length));$('#m-b-l').textContent=t('命中文件','files matched');
 A.roll($('#m-c'),String(out.displayedMatches));$('#m-c-l').textContent=partial?t('匹配 · 部分结果','matches · partial'):t('匹配 · 结果完整','matches · complete');
}
function scopeCommit(){
 if(state.result)return state.result.meta.Commit;
 if(FULL_SHA.test(state.revision))return state.revision;
 if(state.revision)return '';
 const v=state.detail&&R.servable(state.detail);return v?v.commit:'';
}
function scope() {
 const name=state.repo||(state.loaded&&!state.catalog.some(searchable)?t('暂无仓库','No repositories'):t('选择仓库','Choose repository')), commit=scopeCommit();
 const at=commit?A.short(commit):state.revision||(state.repo?'--------':'');
 $('#scope').innerHTML='<span class="min-w-0 truncate">'+esc(name.split('/').at(-1))+'</span>'+(at?'<span class="hidden text-muted sm:inline">@'+esc(at)+'</span>':'')+A.ICON.chevron;
 $('#scope').title=state.repo?name+(commit?' @ '+commit:state.revision?' @ '+state.revision:''):name;
 const files=indexedFiles();
 $('#field-meta').textContent=state.repo?[state.repo,state.branch||state.revision&&!FULL_SHA.test(state.revision)&&state.revision||R.head(row(state.repo)||{})?.branch,files!=null?t(files.toLocaleString()+' 个文件',files.toLocaleString()+' files'):''].filter(Boolean).join(' · '):state.loaded&&!state.catalog.some(searchable)?t('暂无可搜索的仓库','No searchable repositories yet'):t('选择一个仓库开始搜索','Choose a repository to start');
 $('#try').innerHTML=state.repo?'<span class="try-label">'+t('试试','Try')+'</span><span class="try-examples">'+sampleSearches.map((x,i)=>'<button data-try="'+i+'" class="text-ink/80 underline decoration-ink/20 underline-offset-2 hover:decoration-ink">'+esc(x.label)+'</button>').join('')+'</span>':'';
 $('#try').querySelectorAll('[data-try]').forEach(b=>b.onclick=()=>{const x=sampleSearches[+b.dataset.try];applySearch({...x,pattern:''});typeInto(x.pattern);});
}
function showMenu(open) {
 $('#scope-menu').classList.toggle('hidden',!open); $('#scope').setAttribute('aria-expanded',String(open));
 if(open){$('#scope-filter').value='';scopeList();$('#scope-filter').focus();}
}
function scopeList() {
 const q=$('#scope-filter').value.toLowerCase(), all=state.catalog.filter(searchable);
 const repos=all.filter(r=>r.name.toLowerCase().includes(q));
 const dot={ready:'text-ok',stale:'text-warn',preparing:'dot-live',failed:'text-danger',missing:'text-danger'};
 $('#scope-list').innerHTML=repos.length?repos.map(r=>{
  const d=state.details.get(r.name)||r, st=R.state(d), off=st==='disabled', h=R.head(r), v=R.servable(d);
  const sub=off?t('已停用','disabled'):st==='preparing'?t('首次准备中','preparing'):st==='stale'?t('最新提交准备失败，可搜上次版本','head failed, previous version searchable'):st==='pending'||st==='failed'?A.status(st).replace(/<[^>]+>/g,''):'@'+A.short((v||h||{}).commit)+(h?' · '+h.branch:'');
  return '<button role="option" aria-selected="'+(r.name===state.repo)+'" data-repo="'+esc(r.name)+'" '+(off?'disabled':'')+' class="flex w-full items-center gap-2.5 rounded-control px-2.5 py-2 text-left '+(off?'opacity-45':'hover:bg-sand')+(r.name===state.repo?' bg-sand':'')+'"><span class="dot '+(dot[st]||'text-muted/60')+'"></span><span class="min-w-0 flex-1"><span class="block truncate font-mono text-[13px]">'+esc(r.name)+'</span><span class="block truncate text-[12px] text-muted">'+esc(sub)+'</span></span></button>';
 }).join(''):'<p class="px-2.5 py-2 text-[13px] text-muted">'+(state.catalogError?'<span class="text-danger">'+t('读不到仓库列表：','Cannot load repositories: ')+esc(state.catalogError)+'</span>':all.length?t('没有匹配的仓库','No matching repository'):t('还没有可以搜索的仓库','No searchable repositories yet'))+'</p>';
 $('#scope-list').querySelectorAll('[data-repo]').forEach(b=>b.onclick=()=>pickRepo(b.dataset.repo));
}
function pickRepo(name){
 clearResults();$('#notice').innerHTML='';state.repo=name;state.revision='';state.branch='';state.detail=state.details.get(name)||null;scope();showMenu(false);field.reset();metrics(null);loadDetail();schedule(true);input.focus();
}
async function loadDetail(){
 const repo=state.repo;if(!repo)return;
 try{const d=state.details.get(repo)||await API.repo(repo);state.details.set(repo,d);if(repo!==state.repo)return;state.detail=d;scope();if(!state.result&&!state.error)metrics(null);}catch{}
}
$('#scope').onclick=e=>{e.stopPropagation();showMenu($('#scope-menu').classList.contains('hidden'));};
$('#scope-menu').onclick=e=>e.stopPropagation(); $('#scope-filter').oninput=scopeList;
document.addEventListener('click',()=>showMenu(false));
function queryURL(push, search=currentSearch()) { const u=searchURL(state.repo,state.revision,search,state.branch);if(location.pathname+location.search===u)return;history[push?'pushState':'replaceState'](null,'',u); }
function link(href,text,id){return '<a '+(id?'id="'+id+'" ':'')+'href="'+esc(href)+'" class="text-muted underline underline-offset-2 hover:text-ink">'+text+'</a>';}
function button(id,text,primary){return '<button id="'+id+'" class="rounded-control px-3 py-1.5 text-[13.5px] '+(primary?'bg-ink font-medium text-paper hover:opacity-90':'bg-ink/[0.05] hover:bg-ink/[0.08]')+'">'+text+'</button>';}
function noRepoNotice() {
 abort();state.error=null;state.result=null;field.reset();metrics(null);setState('idle');$('#results').innerHTML='';
 const live=state.catalog.filter(r=>r.enabled&&!r.deleted), searchableCount=live.filter(searchable).length;
 const [title,detail,action]=searchableCount?[t('先选择一个仓库','Choose a repository first'),t('搜索在单个仓库的固定提交上进行','Each search runs on one repository at a fixed commit'),button('pick-repo',t('选择仓库','Choose repository'),true)]
  :live.length?[t('仓库还在准备中','Repositories are still being prepared'),t('首次准备完成后就能在这里搜索代码','Search opens here once the first preparation finishes'),'<a href="/sourcegraph/repos" class="inline-flex rounded-control bg-ink/[0.05] px-3 py-1.5 text-[13.5px] hover:bg-ink/[0.08]">'+t('查看仓库','View repositories')+'</a>']
  :[t('还没有可以搜索的仓库','No repositories to search yet'),'','<a id="notice-add" href="/sourcegraph/repos#new" class="inline-flex items-center gap-1.5 rounded-control bg-ink px-3 py-1.5 text-[13.5px] font-medium text-paper hover:opacity-90">'+(A.user()?'':A.ICON.lock)+'<span>'+t('登记仓库','Register')+'</span></a>'];
 const html='<div class="fade-in py-6" data-empty-key="'+esc(title)+(A.user()?'1':'0')+'"><p class="text-[15px]">'+title+'</p>'+(detail?'<p class="mt-2 text-[14px] text-muted">'+detail+'</p>':'')+'<div class="mt-4">'+action+'</div></div>';
 const shown=$('#notice [data-empty-key]');if(!shown||shown.dataset.emptyKey!==title+(A.user()?'1':'0'))$('#notice').innerHTML=html;
 const pick=$('#pick-repo');if(pick)pick.onclick=e=>{e.stopPropagation();showMenu(true);};
 const add=$('#notice-add');if(add)add.onclick=e=>{if(!A.user()){e.preventDefault();A.requireLogin('/sourcegraph/repos#new');}};
}
function errorNotice(error) {
 state.error=error;field.reset();metrics(null);$('#results').innerHTML='';
 const code=error.code||'REQUEST_FAILED', st=state.detail?R.state(state.detail):'', old=state.detail&&R.servable(state.detail);
 const progress=state.repo?link('/sourcegraph/repo?repo='+encodeURIComponent(state.repo)+'&tab=jobs',t('查看进度','View progress')):'';
 const invalid=code==='INVALID_PATTERN'||code==='INVALID_GLOB'||code==='INVALID_OUTPUT';
 setState(invalid?'error':'failed');
 let msg;
 if(invalid){
  const literal=code==='INVALID_PATTERN'&&options.regex&&input.value.trim()&&!/\n/.test(input.value);
  msg='<p class="text-[14px] text-danger">'+esc(error.detail||error.message)+'</p>'+(literal?'<p class="mt-1.5 text-[14px] text-muted">'+t('要按原文查找这段文字，','To match this text literally, ')+'<button id="use-literal" class="text-ink underline decoration-ink/20 underline-offset-2 hover:decoration-ink">'+t('关闭正则（rg -F）','turn off regex (rg -F)')+'</button></p>':'')+'<p class="mt-1.5 text-[13px]"><a href="/sourcegraph/connect#syntax" class="text-muted hover:text-ink">'+t('搜索怎么写','How to write a search')+'</a></p>';
 }else if(st==='preparing'&&(code==='INDEX_NOT_READY'||code==='DEFAULT_BRANCH_NOT_OBSERVED')){
  msg='<div class="flex flex-wrap items-center gap-3 text-[14px]"><span class="dot dot-live"></span><span>'+t('这个仓库正在首次准备，完成后即可搜索。','This repository is being prepared for the first time. Search opens when it finishes.')+'</span>'+progress+'</div>';
 }else if(code==='INDEX_NOT_READY'){
  const useOld=old&&!FULL_SHA.test(state.revision)&&old.commit!==state.revision;
  msg='<div class="text-[14px]"><p class="flex items-center gap-2"><span class="dot text-warn"></span>'+t('这个版本还不能搜索，索引尚未准备好。','This version is not searchable yet; its index is not ready.')+'</p><div class="mt-3 flex flex-wrap items-center gap-2">'+
   (canPrepare()?button('prepare',t('准备这个版本','Prepare this version'),true):A.user()?'<span class="text-[13px] text-muted">'+t('准备版本需要管理权限','Preparing a version requires management access')+'</span>':button('search-login',t('登录后准备','Sign in to prepare'),true))+
   (useOld?button('use-old',t('搜索上次可用的 <span class="font-mono">'+A.short(old.commit)+'</span>','Search last available <span class="font-mono">'+A.short(old.commit)+'</span>')):'')+progress+'</div></div>';
 }else{
  const reasons={DEFAULT_BRANCH_UNDETERMINED:t('无法确定默认分支，请在仓库选择器里选一个分支。','The default branch is ambiguous. Pick a branch in the repository picker.'),DEFAULT_BRANCH_NOT_OBSERVED:t('默认分支还没有同步过。','The default branch has not been synchronized yet.'),AUTHENTICATION_REQUIRED:t('登录后才能搜索。','Sign in to search.'),REPOSITORY_DISABLED:t('这个仓库已停用，不能搜索。','This repository is disabled and cannot be searched.')};
  const login=error.status===401||code==='AUTHENTICATION_REQUIRED';
  msg='<p class="text-[14px] '+(code==='REPOSITORY_DISABLED'?'text-muted':'text-danger')+'">'+esc(reasons[code]||error.message)+'</p>'+(reasons[code]?'':'<p class="type-fig mt-1.5 break-all">'+esc(code)+(error.requestId?' · '+esc(error.requestId):'')+'</p>')+
   '<div class="mt-3 flex flex-wrap items-center gap-3 text-[13px]">'+(login?button('search-login',t('登录','Sign in'),true):'')+(code==='DEFAULT_BRANCH_NOT_OBSERVED'?progress:'')+(login||code==='REPOSITORY_DISABLED'?'':'<button id="retry" class="text-muted underline underline-offset-2 hover:text-ink">'+t('重试','Retry')+'</button>')+'</div>';
 }
 $('#notice').innerHTML=msg;
 const on=(id,fn)=>{const el=document.getElementById(id);if(el)el.onclick=fn;};
 on('retry',()=>schedule(true));
 on('use-literal',()=>{options.regex=false;renderOptions();schedule(true,true);});
 on('search-login',()=>A.requireLogin(location.pathname+location.search));
 on('prepare',prepare);
 on('use-old',()=>{state.revision=old.commit;state.branch='';scope();schedule(true);});
}
async function prepare() {
 if(!canPrepare()){errorNotice(Object.assign(new Error(t('当前账号没有准备权限','You cannot prepare this revision')),{code:'ACCESS_DENIED',status:403}));return;}
 const b=$('#prepare'), seq=state.seq, repo=state.repo, rev=state.revision;b.disabled=true;
 try {
  let commit=rev;
  if(!FULL_SHA.test(commit)){const out=await API.request('/v1/repo/resolve?repo='+encodeURIComponent(repo),{method:'POST',body:rev?{Revision:rev}:{}});commit=out.commit;}
  if(seq!==state.seq)return;
  if(!FULL_SHA.test(commit||''))throw new Error('Invalid resolved commit.');
  const intent=repo+'@'+commit;if(!prepareKeys.has(intent))prepareKeys.set(intent,API.idempotency());
  const result=await API.request('/api/admin/v1/repo/prepare?repo='+encodeURIComponent(repo),{method:'POST',body:{kind:'index',commit},headers:{'Idempotency-Key':prepareKeys.get(intent)}});
  const jobs=window.Management.jobReceipts(result), timing=jobs.map(job=>[job.group,window.Management.indexTiming(job)].filter(Boolean).join(' · ')).filter((_,i)=>window.Management.indexTiming(jobs[i]));
  prepareKeys.delete(intent);
  if(seq!==state.seq)return;
  state.revision=commit;scope();queryURL(false);
  $('#notice').innerHTML='<div class="flex flex-wrap items-center gap-3 text-[14px]"><span class="dot dot-live"></span><span>'+t('已开始准备 <span class="font-mono">'+A.short(commit)+'</span>，完成后即可搜索','Preparing <span class="font-mono">'+A.short(commit)+'</span>. Search opens when it finishes')+'</span><span class="sr-only">'+esc(jobs.map(job=>job.job_id).join(', '))+'</span>'+link('/sourcegraph/repo?repo='+encodeURIComponent(repo)+'&tab=jobs',t('查看进度','View progress'))+'<button id="retry" class="text-[13px] text-muted underline underline-offset-2 hover:text-ink">'+t('重新搜索','Search again')+'</button></div>';
  if(timing.length)$('#notice').insertAdjacentHTML('beforeend','<p class="mt-2 text-[13px] text-muted">'+timing.map(esc).join('<br>')+'</p>');
  $('#retry').onclick=()=>schedule(true);
 }catch(e){if(seq===state.seq)errorNotice(e);}
}
function renderResults() {
 const out=state.result;if(!out){$('#results').innerHTML='';return;}
 const commit=out.meta.Commit, repo=out.meta.Repository;
 const limit=out.meta.FilesOnly?t('命中内容过大，仅显示命中文件。请打开文件阅读，或缩小查询范围。','Matched content is too large; showing matching files only. Open a file to read it, or narrow the query.'):out.meta.TruncationReason==='response_bytes'?t('命中内容过大，已减少结果及上下文。可缩小查询范围或打开文件阅读。','Matched content is too large; results and context were reduced. Narrow the query or open a file to read.'):out.meta.Partial?t('搜索没有全部完成，结果可能不全','Search was incomplete; some matches may be missing'):out.meta.Truncated?t('结果较多，达到显示上限，只显示了一部分','Display limits were reached; only part of the results is shown'):'';
 $('#notice').innerHTML=limit&&out.files.length?'<p class="flex items-center gap-2 text-[14px]"><span class="dot text-warn"></span>'+limit+'</p>':'';
 if(!out.files.length&&(out.meta.Partial||out.meta.Truncated)){$('#results').innerHTML='<div class="fade-in py-6"><p class="text-[15px]">'+t('本次未返回匹配，但结果不完整。','No matches returned, but the results are incomplete.')+'</p><p class="mt-2 text-[14px] text-muted">'+t('稍后重试，或把查询写得更具体','Try again later, or make the query more specific')+'</p></div>';return;}
 if(!out.files.length){$('#results').innerHTML='<div class="fade-in py-6"><p class="text-[15px]">'+t('没有匹配。','No matches.')+'</p><p class="mt-2 text-[14px] text-muted">'+t('这个提交已收录的文件都检查过了；超过 2 MiB 的文件和二进制文件不在其中。可以换个写法，<a href="/sourcegraph/connect#syntax" class="text-ink underline decoration-ink/20 underline-offset-2 hover:decoration-ink">看看搜索怎么写</a>。','Every indexed file at this commit was checked; files over 2 MiB and binaries are not indexed. Try another query, or <a href="/sourcegraph/connect#syntax" class="text-ink underline decoration-ink/20 underline-offset-2 hover:decoration-ink">see how to write one</a>.')+'</p></div>';return;}
 const files=state.showAll?out.files:out.files.slice(0,5);
 $('#results').innerHTML='<div class="cascade space-y-7">'+files.map((f,i)=>{
  const first=f.lines[0]?.LineNumber||1, href=codeURL(repo,commit,f.path,[first,first],state.branch), slash=f.path.lastIndexOf('/');
  const path=f.pathMatches[0]?highlightBytes(f.pathMatches[0].Line,f.pathMatches[0].LineFragments):'<span class="text-muted">'+esc(f.path.slice(0,slash+1))+'</span>'+esc(f.path.slice(slash+1));
  const expanded=state.expanded.has(f.path), lines=expanded?f.lines:f.lines.slice(0,3);
  let previous=0;
  const snippets=lines.map(l=>{
   const gap=previous&&l.LineNumber>previous+1?'<div class="pl-12 text-muted" aria-hidden="true">⋯</div>':'';previous=l.LineNumber;
   return gap+'<a href="'+esc(codeURL(repo,commit,f.path,[l.LineNumber,l.LineNumber],state.branch))+'" class="code-line row-hover"><span class="w-12 shrink-0 select-none pr-4 text-right text-muted/60">'+l.LineNumber+'</span><span class="whitespace-pre pr-6">'+highlightBytes(l.Line,l.LineFragments).replace(/\r?\n$/,'')+'</span></a>';
  }).join('');
  const more=f.lines.length>3?'<button data-expand="'+esc(f.path)+'" class="pl-12 pt-0.5 text-[11.5px] text-muted hover:text-ink">'+(expanded?t('收起','Collapse'):t('另有 '+(f.lines.length-3)+' 行',(f.lines.length-3)+' more lines'))+'</button>':'';
  return '<article data-key="'+esc(f.path)+'" style="--i:'+i+'"><div class="flex items-baseline gap-3"><a href="'+esc(href)+'" class="min-w-0 truncate font-mono text-[13.5px] hover:underline">'+path+'</a><span class="type-fig shrink-0">'+(out.meta.FilesOnly?t('命中文件','matching file'):!f.lines.length?t('路径命中','path match'):t(f.count+' 处',f.count+(f.count>1?' matches':' match')))+'</span><button data-cite="'+i+'" class="ml-auto shrink-0 text-[12.5px] text-muted hover:text-ink">'+t('复制引用','Copy citation')+'</button></div>'+(snippets?'<div class="field mt-2.5 overflow-x-auto py-2 font-mono text-[12.5px] leading-[1.75]">'+snippets+more+'</div>':'')+'</article>';
 }).join('')+'</div>'+(out.files.length>5&&!state.showAll?'<button id="show-all" class="mt-6 rounded-control bg-ink/[0.05] px-3 py-1.5 text-[13.5px] hover:bg-ink/[0.08]">'+t('显示全部 '+out.files.length+' 个文件','Show all '+out.files.length+' files')+'</button>':'')+'<p class="type-fig mt-8 break-all">'+esc(repo)+'@'+commit+'</p>';
 $('#results').querySelectorAll('[data-key]').forEach(el=>{el.onmouseenter=()=>field.focus(el.dataset.key);el.onmouseleave=()=>field.focus(null);});
 $('#results').querySelectorAll('[data-cite]').forEach(b=>b.onclick=()=>{const f=files[+b.dataset.cite],line=f.lines[0]?.LineNumber;const url=new URL(codeURL(repo,commit,f.path,line?[line,line]:null,state.branch),location.origin);A.copy('['+repo+'@'+commit.slice(0,8)+':'+f.path+(line?':L'+line:'')+']('+url+')',t('已复制引用（完整 SHA）','Citation copied (full SHA)'));});
 $('#results').querySelectorAll('[data-expand]').forEach(b=>b.onclick=()=>{state.expanded.has(b.dataset.expand)?state.expanded.delete(b.dataset.expand):state.expanded.add(b.dataset.expand);renderResults();});
 const more=$('#show-all');if(more)more.onclick=()=>{state.showAll=true;renderResults();};
}
async function search(push=false) {
 const q=currentSearch();
 controller?.abort();const seq=++state.seq, repo=state.repo,revision=state.revision;lastSent=Date.now();
 if(!repo){if(state.catalogError)errorNotice(Object.assign(new Error(state.catalogError),{code:'CATALOG_UNAVAILABLE'}));else noRepoNotice();return;}
 rememberRepo(repo);state.error=null;state.expanded.clear();$('#notice').innerHTML='';setState('searching');field.scan();$('#progress').classList.add('on');queryURL(push,q);
 controller=new AbortController();const start=performance.now();
 try{
  const out=await API.request('/api/search?repo='+encodeURIComponent(repo),{method:'POST',body:searchBody(q,revision),signal:controller.signal});
  if(seq!==state.seq)return;
  state.result=normalizeSearch(out,repo,revision);state.roundtrip=Math.round(performance.now()-start);state.showAll=false;
  if(!FULL_SHA.test(revision)){const mains=(row(repo)?.policy?.branches||[]).filter(b=>b==='main'||b==='master');state.branch=revision||row(repo)?.default_branch||(mains.length===1?mains[0]:'');}
  state.revision=state.result.meta.Commit;scope();queryURL(false,q);
  field.settle(state.result.files.map(f=>{const line=f.lines[0]?.LineNumber;return {key:f.path,n:f.count,label:f.path.split('/').at(-1)+(line?':'+line:''),href:codeURL(repo,state.result.meta.Commit,f.path,line?[line,line]:null,state.branch)};}));metrics(state.result);setState(out.Meta.Partial||out.Meta.Truncated?'partial':state.result.files.length?'done':'empty');
  if(q.pattern===input.value&&document.startViewTransition&&!matchMedia('(prefers-reduced-motion: reduce)').matches)document.startViewTransition(renderResults);else renderResults();
 }catch(e){if(seq===state.seq&&e.name!=='AbortError')errorNotice(e);}finally{if(seq===state.seq)$('#progress').classList.remove('on');}
}
// While typing, send at most once every 200 ms; the latest input goes out when the interval ends.
// The previous results stay until the next response replaces them; stale responses are dropped by seq.
function clearResults(){abort();state.error=null;state.result=null;$('#results').innerHTML='';if(!$('#notice [data-empty-key]'))$('#notice').innerHTML='';metrics(null);}
function schedule(immediate=false,push=false) {
 clearTimeout(timer);$('#q-clear').classList.toggle('hidden',!input.value);
 const q=input.value;if(!q.trim()){clearResults();field.reset();setState('idle');queryURL(false);if(state.loaded&&!state.repo)noRepoNotice();return;}
 field.type(q,Math.min(1,(input.selectionStart||q.length)/Math.max(24,q.length)));setState('typing');
 if(composing)return;
 if(!immediate&&q.trim().length<3){clearResults();field.type(q,Math.min(1,q.length/24));setState('typing');return;}
 const wait=immediate?0:Math.max(0,lastSent+200-Date.now());if(wait)timer=setTimeout(()=>search(),wait);else search(push);
}
// Types a sample character by character, so the field reacts the same way it does for a person.
function typeInto(text){
 clearInterval(typing);input.value='';input.focus();let i=0;
 typing=setInterval(()=>{input.value=text.slice(0,++i);schedule();if(i>=text.length){clearInterval(typing);schedule(true,true);}},55);
}
input.addEventListener('compositionstart',()=>{composing=true;abort();});input.addEventListener('compositionend',()=>{composing=false;schedule();});
input.addEventListener('input',()=>{clearInterval(typing);schedule();});input.addEventListener('keydown',e=>{if(e.key==='Enter'&&!e.isComposing){e.preventDefault();schedule(true,true);}});
$('#q-enter').innerHTML=A.ICON.enter;$('#q-enter').onclick=()=>schedule(true,true);$('#q-enter').setAttribute('aria-label',t('搜索','Search'));
$('#opt-case').onclick=()=>{options.caseSensitive=!options.caseSensitive;renderOptions();schedule(true,true);};
$('#opt-regex').onclick=()=>{options.regex=!options.regex;renderOptions();schedule(true,true);};
$('#opt-glob').onclick=()=>{const open=$('#glob-row').classList.contains('hidden');showGlobs(open);if(open)$('#glob-include').focus();};
['#glob-include','#glob-exclude'].forEach(id=>{$(id).addEventListener('input',()=>{renderOptions();schedule();});$(id).addEventListener('keydown',e=>{if(e.key==='Enter'&&!e.isComposing){e.preventDefault();schedule(true,true);}});});
$('#q-clear').innerHTML=A.ICON.x;$('#q-clear').onclick=()=>{input.value='';schedule(true);input.focus();};$('#q-clear').setAttribute('aria-label',t('清除','Clear'));
document.addEventListener('keydown',e=>{if(e.key==='Escape'&&!$('#scope-menu').classList.contains('hidden')){showMenu(false);$('#scope').focus();}if(e.key==='/'&&!/INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName)&&!document.activeElement?.isContentEditable){e.preventDefault();input.focus();}});

// Skip the clone. TODO: the first two bars are samples for a 412 MiB repository; replace with measured figures before release.
const RACE=[{zh:'git clone，然后 rg',en:'git clone, then rg',s:41.8},{zh:'本地已有副本，直接 rg',en:'rg on an existing checkout',s:1.3},{zh:'Context search',en:'Context search',s:0.21,live:true}];
let raced=false;
function renderRace(play){
 const max=RACE[0].s;
 $('#race').innerHTML=RACE.map((x,i)=>{const pct=Math.max(0.6,Math.sqrt(x.s/max)*100);return '<div><div class="flex items-baseline justify-between text-[14px]"><span>'+t(x.zh,x.en)+'</span><span class="font-mono text-[13px]" data-race="'+i+'">'+(play?'0.00':x.s.toFixed(2))+' s</span></div><div class="mt-2 h-[6px] rounded-full bg-ink/[0.06]"><div class="h-full rounded-full '+(x.live?'bg-live':'bg-ink/70')+'" style="width:'+(play?0:pct)+'%;transition:width '+Math.min(2.4,0.5+x.s/16)+'s cubic-bezier(.2,.8,.2,1)" data-bar="'+pct+'"></div></div></div>';}).join('');
 if(!play)return;
 requestAnimationFrame(()=>{
  document.querySelectorAll('[data-bar]').forEach(b=>{b.style.width=b.dataset.bar+'%';});
  RACE.forEach((x,i)=>{const el=document.querySelector('[data-race="'+i+'"]'),t0=performance.now(),dur=Math.min(2400,500+x.s*60);(function tick(now){const k=Math.min(1,(now-t0)/dur);el.textContent=(x.s*(1-Math.pow(1-k,3))).toFixed(2)+' s';if(k<1)requestAnimationFrame(tick);})(t0);});
 });
}
new IntersectionObserver(es=>es.forEach(e=>{if(e.isIntersecting&&!raced){raced=true;renderRace(true);}}),{threshold:0.4}).observe($('#race'));

function ledger() {
 const rows=state.catalog.filter(r=>!r.deleted);
 $('#ledger').innerHTML=rows.map(r=>{const d=state.details.get(r.name)||r, v=R.servable(d), h=R.head(r);return '<tr class="row-hover cursor-pointer" data-href="/sourcegraph/repo?repo='+encodeURIComponent(r.name)+'"><td class="py-2.5 pr-4 font-mono text-[13px]"><a class="hover:underline" href="/sourcegraph/repo?repo='+encodeURIComponent(r.name)+'">'+esc(r.name)+'</a></td><td class="py-2.5 pr-4">'+A.status(R.state(d))+'</td><td class="py-2.5 pr-4 font-mono text-[12.5px] text-muted">'+esc(A.short((v||h||{}).commit))+'</td><td class="py-2.5 pr-4 text-right font-mono text-[12.5px]">'+(v&&Number.isFinite(v.files)?v.files.toLocaleString():'-')+'</td><td class="py-2.5 text-right font-mono text-[12.5px] text-muted">'+esc(A.clock(d.observed_at||r.observed_at))+'</td></tr>';}).join('')||'<tr><td colspan="5" class="py-4 text-muted">'+t('还没有登记仓库','No repositories yet')+'</td></tr>';
 $('#ledger').querySelectorAll('[data-href]').forEach(tr=>tr.onclick=e=>{if(!e.target.closest('a'))location.href=tr.dataset.href;});
 $('#add-repo').innerHTML=(A.user()?'':A.ICON.lock)+'<span>'+t('登记仓库','Register')+'</span>';
 $('#add-repo').onclick=e=>{if(!A.user()){e.preventDefault();A.requireLogin('/sourcegraph/repos#new');}};
}
// Ledger details (files, versions, jobs) come from availability, a few repositories at a time.
async function loadDetails(){
 const queue=state.catalog.filter(r=>!r.deleted&&!state.details.has(r.name)).slice(0,30);
 await Promise.all(Array.from({length:3},async()=>{for(let r=queue.shift();r;r=queue.shift()){try{state.details.set(r.name,await API.repo(r.name));}catch{}}}));
 ledger();if(state.repo){state.detail=state.details.get(state.repo)||state.detail;scope();if(!state.result&&!state.error)metrics(null);}
}
const clientPanel=window.ClientPanel.mount($('#client-panel'));
document.querySelectorAll('[data-connect-client]').forEach(a=>a.addEventListener('click',e=>{if(e.metaKey||e.ctrlKey||e.shiftKey||e.button)return;e.preventDefault();clientPanel.select(a.dataset.connectClient);$('#connect').scrollIntoView({behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'});}));
function renderStatic(){ledger();scope();renderOptions();renderRace(false);raced=false;}
document.addEventListener('langchange',()=>{renderStatic();if(state.loaded&&!state.repo)noRepoNotice();else if(state.error)errorNotice(state.error);else{metrics(state.result);if(state.result)renderResults();}});
document.addEventListener('app:auth',()=>{ledger();if(state.loaded&&!state.repo)noRepoNotice();});
window.addEventListener('pagehide',abort);
window.addEventListener('popstate',()=>{try{const p=parseLocation(location.search);Object.assign(state,{repo:p.repo,revision:p.revision,branch:p.branch,result:null});applySearch(p.search);scope();schedule(true);}catch(e){abort();errorNotice(e);}});
async function init(){
 renderStatic();metrics(null);setState('idle');
 let p;try{p=parseLocation(location.search);state.repo=p.repo;state.revision=p.revision;state.branch=p.branch;applySearch(p.search);}catch(e){errorNotice(e);return;}
 try{await API.ready;state.catalog=await API.catalog();state.loaded=true;if(!state.repo){state.repo=defaultRepository(state.catalog,savedRepo());}renderStatic();loadDetail();if(!state.repo)noRepoNotice();else if(input.value)schedule(true);loadDetails();}catch(e){state.catalogError=e.message;errorNotice(e);$('#ledger').innerHTML='<tr><td colspan="5" class="py-4 text-danger">'+esc(e.message)+'</td></tr>';}
}
init();
