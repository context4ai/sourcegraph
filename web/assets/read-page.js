import {FULL_SHA,escapeHTML as esc,parseLocation,parseRange,lineFragment,codeURL,searchURL,sourceURL,fileGlob,parseRead,selectedText,readWindow} from './search-model.mjs';
const A=window.App, API=window.API, t=A.t, $=s=>document.querySelector(s);
const state={repo:'',revision:'',commit:'',path:'',branch:'',lines:new Map(),dirs:new Map(),open:new Set(['']),sel:null,anchor:null,eof:null,file:null,error:null,busy:false,seq:0,directory:false};
let controller=new AbortController(),rangeWarning='';
function endpoint(kind){return '/v1/repo/'+kind+'?repo='+encodeURIComponent(state.repo);}
function currentURL(range=state.sel){return codeURL(state.repo,state.commit||state.revision,state.path,range,state.branch);}
function fixedURL(){if(!FULL_SHA.test(state.commit))throw new Error(t('版本还没确定，稍后再复制','The commit is not resolved yet'));return new URL(currentURL(),location.origin).href;}
function rangeText(){return lineFragment(state.sel).slice(1);}
function notify(message,error=false){$('#read-notice').className='mt-4 text-[13px] '+(error?'text-danger':'text-muted');$('#read-notice').textContent=message;}
function moveDrawer(open){$('#file-sidebar').classList.toggle('open',open);$('#files-toggle').setAttribute('aria-expanded',String(open));if(!open)$('#files-toggle').focus();}
$('#files-toggle').onclick=()=>moveDrawer(!$('#file-sidebar').classList.contains('open'));$('#files-close').onclick=()=>moveDrawer(false);
document.addEventListener('keydown',e=>{if(e.key==='Escape'&&$('#file-sidebar').classList.contains('open'))moveDrawer(false);});
function renderHead(){
 $('#repo-link').textContent=state.repo;$('#repo-link').href='/sourcegraph/repo?repo='+encodeURIComponent(state.repo);
 $('#sha').textContent=state.commit?'@'+state.commit.slice(0,12):'@…';$('#sha').title=t('复制完整 SHA','Copy full SHA');$('#sha').disabled=!state.commit;
 $('#back-search').href=searchURL(state.repo,state.commit||state.revision,{},state.branch);
 const parts=state.path.split('/').filter(Boolean);
 $('#crumbs').innerHTML='<a href="'+esc(codeURL(state.repo,state.commit||state.revision,''))+'" class="text-muted hover:underline">'+t('根目录','root')+'</a>'+parts.slice(0,-1).map((part,i)=>'<span class="text-muted/50">/</span><a class="text-muted hover:underline" href="'+esc(codeURL(state.repo,state.commit||state.revision,parts.slice(0,i+1).join('/'),null,state.branch))+'">'+esc(part)+'</a>').join('');
 $('#title').innerHTML='<span class="font-mono text-[0.72em] tracking-normal">'+esc(parts.at(-1)||t('根目录','Repository root'))+'</span>';
 const lines=sortedLines();
 const detail=state.directory?t('目录','Directory'):state.file?.Kind==='submodule'?'Git submodule':state.file?.Kind==='symlink'?t('符号链接','Symlink'):state.file?.IsLFSPointer?'Git LFS pointer':state.file&&!lines.length?t('空文件','Empty file'):state.eof!=null?t(state.eof+' 行',state.eof+' lines'):'';
 const pin=state.commit?t('内容固定在提交 '+state.commit.slice(0,8)+(state.branch?'，来自分支 '+state.branch:''),'pinned to '+state.commit.slice(0,8)+(state.branch?' from '+state.branch:'')):'';
 $('#meta').textContent=[detail,pin].filter(Boolean).join(' · ');
 $('#in-file').classList.toggle('hidden',!state.path||state.directory);$('#in-file').href=searchURL(state.repo,state.commit,{glob:[fileGlob(state.path)]},state.branch);
 $('#latest').classList.toggle('hidden',!state.branch);$('#latest').href=codeURL(state.repo,state.branch,state.path);
 $('#source-link').classList.toggle('hidden',!state.path||!state.commit);$('#source-link').href=sourceURL(state.repo,state.commit,state.path,state.sel);
 $('#copy-path').disabled=!state.path;$('#copy-code').disabled=!lines.length||state.directory;
 $('#permalink').disabled=!state.commit;$('#cite').disabled=!state.commit;
}
function sortedLines(){return [...state.lines.values()].sort((a,b)=>a.number-b.number);}
function syntax(text){
 // Lightweight lexical coloring only. Escape every source token before introducing markup.
 if(text.length>12000)return esc(text);
 const re=/(\/\/[^\n]*|#[^\n]*|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\b(?:package|import|func|return|if|else|defer|const|var|type|export|async|await|function|interface|from|default|new|for|range)\b)/g;
 let at=0,out='';for(const m of text.matchAll(re)){out+=esc(text.slice(at,m.index));const value=m[0],kind=value.startsWith('//')||value.startsWith('#')?'tok-c':value.startsWith('"')||value.startsWith("'")?'tok-s':'tok-k';out+='<span class="'+kind+'">'+esc(value)+'</span>';at=m.index+value.length;}return out+esc(text.slice(at));
}
function findHTML(text,query){
 if(!query)return syntax(text);
 const hay=text.toLocaleLowerCase(),needle=query.toLocaleLowerCase();let at=0,out='',pos;
 while((pos=hay.indexOf(needle,at))>=0){out+=esc(text.slice(at,pos))+'<mark class="hit">'+esc(text.slice(pos,pos+query.length))+'</mark>';at=pos+query.length;}
 return out+esc(text.slice(at));
}
function renderSelection(){
 $('#sel').textContent=state.sel?t('已选 '+rangeText()+' · Shift + 点击行号扩展','selected '+rangeText()+' · shift-click to extend'):t('点击行号选择，引用会带上行号','click a line number to select; citations include it');
 $('#copy-code').textContent=state.sel?t('复制所选代码','Copy selected code'):t('复制已加载代码','Copy loaded code');
 $('#source-link').href=sourceURL(state.repo,state.commit,state.path,state.sel);
}
function renderBlob(){
 if(state.directory){renderDirectory();return;}
 const lines=sortedLines(),find=$('#find').value;let prev=0,matches=0;
 $('#blob').innerHTML=lines.map(line=>{
  const gap=prev&&line.number!==prev+1?'<div class="pl-14 text-muted" aria-hidden="true">⋯</div>':'';prev=line.number;
  if(find){let at=0,pos;const text=line.text.toLocaleLowerCase(),needle=find.toLocaleLowerCase();while((pos=text.indexOf(needle,at))>=0){matches++;at=pos+needle.length;}}
  const selected=state.sel&&line.number>=state.sel[0]&&line.number<=state.sel[1];
  return gap+'<div id="L'+line.number+'" class="code-line '+(selected?'sel':'row-hover')+'"><a href="#L'+line.number+'" data-ln="'+line.number+'" class="w-14 shrink-0 select-none pr-5 text-right text-muted/60 hover:text-ink">'+line.number+'</a><span class="whitespace-pre pr-6">'+(findHTML(line.text,find)||' ')+'</span></div>';
 }).join('');
 if(!lines.length&&state.file){$('#blob').innerHTML='<p class="px-5 py-4 text-muted">'+(state.file.Kind==='submodule'?t('子模块指向提交 ','Submodule points to commit ')+esc(state.file.BlobOID)+t('，这里不展开','; not expanded here'):t('这是一个空文件','This file is empty'))+'</p>';}
 $('#find-count').textContent=find?t(matches+' 处',matches+' matches'):'';
 $('#blob').querySelectorAll('[data-ln]').forEach(a=>a.onclick=e=>{if(e.metaKey||e.ctrlKey||e.altKey)return;e.preventDefault();const n=+a.dataset.ln;if(e.shiftKey&&state.anchor)state.sel=[Math.min(n,state.anchor),Math.max(n,state.anchor)];else if(state.sel?.[0]===n&&state.sel[1]===n){state.sel=null;state.anchor=null;}else{state.sel=[n,n];state.anchor=n;}history.replaceState(null,'',currentURL());renderBlob();renderSelection();});
 renderSelection();
}
function renderExtent(){
 const lines=sortedLines(),min=lines[0]?.number,max=lines.at(-1)?.number;
 $('#load-above').classList.toggle('hidden',!min||min<=1||state.directory);$('#load-above').disabled=state.busy;
 $('#load-above').textContent=min?t('↑ 继续读取上方 '+Math.min(200,min-1)+' 行','↑ Read '+Math.min(200,min-1)+' lines above'):'';
 const canBelow=max!=null&&(state.eof==null||max<state.eof);
 $('#load-below').classList.toggle('hidden',!canBelow||state.directory);$('#load-below').disabled=state.busy;
 $('#load-below').textContent=t('↓ 继续读取 200 行','↓ Read 200 more lines');
 $('#read-extent').textContent=state.directory||!lines.length?'':!canBelow?t('已读到文件末尾','end of file'):t('已读到第 '+max+' 行','read up to line '+max);
}
function error(error){
 state.error=error;const messages={BINARY_FILE:t('这是二进制文件，不能预览','This is a binary file and cannot be previewed'),UNSUPPORTED_TEXT_ENCODING:t('文件不是 UTF-8 文本，不能预览','The file is not UTF-8 text and cannot be previewed'),FILE_LINE_TOO_LARGE:t('有一行太长，请在 代码托管站 查看','A line is too long. View it on the code host'),READ_SCAN_LIMIT:t('这几行离文件开头太远，请在 代码托管站 查看','These lines are too far into the file. View them on the code host'),LINE_RANGE_NOT_SATISFIABLE:t('文件没有这么多行','The file does not have that many lines'),FILE_NOT_FOUND:t('这个提交里没有这个文件','This file does not exist at this commit'),REVISION_NOT_ELIGIBLE:t('这个版本不在保留范围内','This version is outside the retention window')};
 const known=messages[error.code], msg=known||error.message;
 $('#read-notice').className='mt-4 text-[13px] text-danger';$('#read-notice').innerHTML=esc(msg)+(known?'':'<span class="mt-1 block font-mono text-[11px]">'+esc(error.code||'REQUEST_FAILED')+(error.requestId?' · '+esc(error.requestId):'')+'</span>')+'<span class="mt-2 flex gap-3 text-muted">'+(error.status===401?'<button id="read-login" class="underline underline-offset-2 hover:text-ink">'+t('登录','Sign in')+'</button>':'')+(error.code==='LINE_RANGE_NOT_SATISFIABLE'?'<button id="read-start" class="underline underline-offset-2 hover:text-ink">'+t('从文件开头读','Read from the start')+'</button>':'<button id="read-retry" class="underline underline-offset-2 hover:text-ink">'+t('重试','Retry')+'</button>')+'</span>';
 const login=$('#read-login');if(login)login.onclick=()=>A.requireLogin(location.pathname+location.search+location.hash);
 const first=$('#read-start');if(first)first.onclick=()=>{state.sel=null;state.anchor=null;history.replaceState(null,'',currentURL());loadLines(1,100);};
 const retry=$('#read-retry');if(retry)retry.onclick=()=>{if(!state.commit)init();else if(state.lastRange)loadLines(...state.lastRange);};
}
async function loadLines(start,end,scroll=false){
 if(state.busy)return;
 state.busy=true;state.lastRange=[start,end];const seq=state.seq, oldHeight=$('#blob').getBoundingClientRect().height, oldMin=sortedLines()[0]?.number;
 renderExtent();notify(t('读取中…','Reading…'));
 try{
  const data=await API.request(endpoint('read'),{method:'POST',body:{Revision:state.commit,Path:state.path,StartLine:start,EndLine:end},signal:controller.signal});
  if(seq!==state.seq)return;
  const lines=parseRead(data,state.repo,state.commit,state.path);for(const line of lines)state.lines.set(line.number,line);
  state.file=data;state.error=null;if(data.HasMore===false)state.eof=data.ReturnedEndLine||0;
  let note=rangeWarning;
  if(data.IsLFSPointer)note=t('这是 Git LFS 指针，实际文件内容不在这里','This is a Git LFS pointer; the actual content is not stored here');
  else if(data.Kind==='symlink')note=t('这是符号链接，显示的是它指向的路径','This is a symlink; its target path is shown');
  if(state.sel&&state.eof!=null&&state.sel[1]>state.eof&&state.sel[0]<=state.eof){state.sel=[state.sel[0],state.eof];note=t('所选范围超出文件末尾，已选到最后一行','The selection ran past the end of the file; it now ends at the last line');history.replaceState(null,'',currentURL());}
  notify(note);renderHead();renderBlob();
  if(start<oldMin)window.scrollBy(0,$('#blob').getBoundingClientRect().height-oldHeight);
  if(scroll&&state.sel)document.getElementById('L'+state.sel[0])?.scrollIntoView({block:'center'});
 }catch(e){if(seq!==state.seq||e.name==='AbortError')return;if(e.code==='NOT_A_FILE'){state.directory=true;await loadDir(state.path);renderHead();renderDirectory();notify('');}else error(e);}finally{if(seq===state.seq){state.busy=false;renderExtent();}}
}
function renderTree(){
 function draw(path,depth){const node=state.dirs.get(path);if(!node)return '';
  let html='';for(const e of node.entries){const dir=e.Kind==='directory',open=state.open.has(e.Path);const padding=8+depth*12;
   html+='<li><div class="flex items-center">'+(dir?'<button aria-expanded="'+open+'" data-dir="'+esc(e.Path)+'" class="shrink-0 px-1 text-muted" aria-label="'+esc((open?t('收起 ','Collapse '):t('展开 ','Expand '))+e.Name)+'">'+(open?'▾':'▸')+'</button>':'')+'<a href="'+esc(codeURL(state.repo,state.commit,e.Path,null,state.branch))+'" class="block min-w-0 truncate rounded-[6px] py-1 pr-2 '+(e.Path===state.path?'bg-ink/[0.06] text-ink':'text-muted hover:text-ink')+'" style="padding-left:'+padding+'px">'+esc(e.Name)+(dir?'/':e.Kind==='symlink'?' ↗':e.Kind==='submodule'?' ⊞':'')+'</a></div>'+(dir&&open?'<ul>'+draw(e.Path,depth+1)+'</ul>':'')+'</li>';
  }
  if(node.busy)html+='<li class="p-2 text-muted">'+t('读取中…','Loading…')+'</li>';
  if(node.error)html+='<li class="p-2 text-danger text-[11px]">'+esc(node.error)+' <button data-retry-dir="'+esc(path)+'" class="underline">'+t('重试','Retry')+'</button></li>';
  else if(node.more)html+='<li><button data-more="'+esc(path)+'" class="p-2 text-muted underline">'+t('更多目录项','More entries')+'</button></li>';
  return html;
 }
 $('#tree').innerHTML=draw('',0)||'<li class="p-2 text-muted">'+t('目录尚未加载','Directory not loaded')+'</li>';
 $('#tree').querySelectorAll('[data-dir]').forEach(b=>b.onclick=()=>{const path=b.dataset.dir;if(state.open.has(path)){state.open.delete(path);renderTree();}else{state.open.add(path);if(!state.dirs.has(path))loadDir(path);else renderTree();}});
 $('#tree').querySelectorAll('[data-more]').forEach(b=>b.onclick=()=>loadDir(b.dataset.more,true));
 $('#tree').querySelectorAll('[data-retry-dir]').forEach(b=>b.onclick=()=>loadDir(b.dataset.retryDir,!!state.dirs.get(b.dataset.retryDir)?.entries.length));
}
async function loadDir(path,more=false){
 let node=state.dirs.get(path);if(node?.busy)return;if(!node){node={entries:[],busy:false,more:false,cursor:'',error:''};state.dirs.set(path,node);}
 node.busy=true;node.error='';renderTree();const seq=state.seq;
 try{const out=await API.request(endpoint('list'),{method:'POST',body:{Revision:state.commit,Path:path,First:100,After:more?node.cursor:''},signal:controller.signal});if(seq!==state.seq)return;if(out.Commit!==state.commit||!Array.isArray(out.Entries))throw new Error('Directory response does not match the requested commit.');if(out.HasMoreResults&&(!out.NextCursor||out.NextCursor===node.cursor))throw new Error('Invalid directory continuation cursor.');
  node.entries=more?[...node.entries,...out.Entries]:out.Entries;node.cursor=out.NextCursor||'';node.more=out.HasMoreResults===true;
 }catch(e){if(seq===state.seq&&e.name!=='AbortError')node.error=e.message;}finally{node.busy=false;if(seq===state.seq){renderTree();if(state.directory)renderDirectory();}}
}
function renderDirectory(){
 const node=state.dirs.get(state.path);if(!node){$('#blob').innerHTML='<p class="p-4 text-muted">'+t('读取目录中…','Loading directory…')+'</p>';return;}
 $('#blob').innerHTML=node.entries.map(e=>'<a href="'+esc(codeURL(state.repo,state.commit,e.Path,null,state.branch))+'" class="flex items-center justify-between gap-4 px-5 py-1 row-hover"><span class="min-w-0 break-all">'+esc(e.Name)+(e.Kind==='directory'?'/':'')+'</span><span class="type-fig shrink-0">'+esc(e.Kind)+(Number.isFinite(e.Size)?' · '+e.Size+' B':'')+'</span></a>').join('')+(node.error?'<p class="px-5 py-2 text-danger">'+esc(node.error)+'</p>':'')+(!node.entries.length&&!node.busy&&!node.error?'<p class="p-4 text-muted">'+t('空目录','Empty directory')+'</p>':'');
 if(node.more){const b=document.createElement('button');b.className='m-4 underline text-[13px]';b.textContent=t('更多目录项','More entries');b.disabled=node.busy;b.onclick=()=>loadDir(state.path,true);$('#blob').append(b);}
 if(node.error){const b=document.createElement('button');b.className='m-4 underline text-[13px]';b.textContent=t('重试','Retry');b.onclick=()=>loadDir(state.path,!!node.entries.length);$('#blob').append(b);}
 $('#sel').textContent='';
}
$('#load-above').onclick=()=>{const min=sortedLines()[0]?.number;if(min>1)loadLines(Math.max(1,min-200),min-1);};
$('#load-below').onclick=()=>{const max=sortedLines().at(-1)?.number;if(max)loadLines(max+1,max+200);};
async function jump(){try{const range=parseRange($('#jump').value.trim());if(!range)return;state.sel=range;state.anchor=range[0];history.replaceState(null,'',currentURL());renderSelection();const [start,end]=readWindow(range);if(!state.lines.has(range[0]))await loadLines(start,end,true);else{renderBlob();document.getElementById('L'+range[0])?.scrollIntoView({block:'center'});}if(range[1]-range[0]>=200)notify(t('范围较长，继续读取后才能完整复制','Read the rest of this range before copying it'));}catch(e){notify(e.message,true);}}
$('#jump-go').onclick=jump;$('#jump').onkeydown=e=>{if(e.key==='Enter')jump();};$('#find').oninput=renderBlob;
$('#cite').onclick=()=>{try{const url=fixedURL();A.copy('['+state.repo+'@'+state.commit.slice(0,8)+':'+state.path+(state.sel?':'+rangeText():'')+']('+url+')',t('已复制引用（含完整 SHA）','Citation copied (full SHA)'));}catch(e){notify(e.message,true);}};
$('#permalink').onclick=()=>{try{A.copy(fixedURL(),t('已复制固定链接','Permalink copied'));}catch(e){notify(e.message,true);}};
$('#sha').onclick=()=>A.copy(state.commit,t('已复制完整 SHA','Full SHA copied'));$('#copy-path').onclick=()=>{$('#more').open=false;A.copy(state.path,t('已复制路径','Path copied'));};$('#more-chev').innerHTML=A.ICON.chevron;
$('#copy-code').onclick=()=>{$('#more').open=false;try{A.copy(selectedText(sortedLines(),state.sel));}catch(e){notify(e.message,true);}};
window.addEventListener('hashchange',()=>{try{state.sel=parseRange(location.hash);state.anchor=state.sel?.[0]||null;renderBlob();if(state.sel&&!state.lines.has(state.sel[0])){const r=readWindow(state.sel);loadLines(...r,true);}else if(state.sel)document.getElementById('L'+state.sel[0])?.scrollIntoView({block:'center'});}catch(e){notify(e.message,true);}});
window.addEventListener('pagehide',()=>{state.seq++;controller.abort();});
document.addEventListener('langchange',()=>{renderHead();renderTree();renderBlob();renderExtent();if(state.error)error(state.error);});
async function init(){
 state.seq++;controller.abort();controller=new AbortController();const seq=state.seq;
 try{
  const p=parseLocation(location.search);Object.assign(state,{repo:p.repo,revision:p.revision,path:p.path,branch:p.branch});
  if(!state.repo)throw new Error(t('需要 repo 参数，请先从仓库或搜索结果进入。','The repo parameter is required. Open a repository or a search result.'));
  try{state.sel=parseRange(location.hash);state.anchor=state.sel?.[0]||null;}catch(e){rangeWarning=t('行号无效，从文件开头显示','Invalid line number; showing the start of the file');state.sel=null;}
  renderHead();notify('');await API.ready;
  if(FULL_SHA.test(state.revision))state.commit=state.revision;
  else{const out=await API.request(endpoint('resolve'),{method:'POST',body:state.revision?{Revision:state.revision}:{},signal:controller.signal});if(seq!==state.seq)return;if(!FULL_SHA.test(out.commit||''))throw new Error('Invalid resolved commit.');state.commit=out.commit;if(state.revision)state.branch=state.revision;}
  history.replaceState(null,'',currentURL());renderHead();
  const dirs=[''];const parts=state.path.split('/').filter(Boolean);for(let i=1;i<parts.length;i++){const path=parts.slice(0,i).join('/');dirs.push(path);state.open.add(path);}
  // Only direct ancestors are loaded, never the whole repository tree.
  const directoryRequests=dirs.map(dir=>loadDir(dir));
  if(!state.path){state.directory=true;await Promise.all(directoryRequests);notify('');renderHead();renderDirectory();renderExtent();}
  else{await loadLines(...readWindow(state.sel),true);await Promise.all(directoryRequests);}
 }catch(e){if(seq===state.seq&&e.name!=='AbortError')error(e);}
}
init();
