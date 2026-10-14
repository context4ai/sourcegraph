(function(){
 'use strict';
 const A=App,t=A.t,esc=A.esc;
 const INPUT='mt-2 block w-full rounded-control bg-ink/[0.04] px-3 py-2 outline-none ring-1 ring-transparent focus:ring-ink/20 read-only:opacity-60';
 const HELP='mt-1 block text-[13px] text-muted';
 const PRIMARY='rounded-control bg-ink px-3.5 py-1.5 text-[13px] text-paper disabled:opacity-40';
 const QUIET='rounded-control px-3 py-1.5 text-[13px] text-muted hover:bg-ink/[0.05] disabled:opacity-40';
 const ACTIONS='<div class="flex flex-wrap items-center gap-3 border-t rule pt-4"><p data-status role="status" class="mr-auto min-w-0 break-all text-[13px] text-muted"></p>';
 // One card per code source: where repositories are fetched from, then the token used to fetch them.
 document.querySelectorAll('[data-code-credentials]').forEach(panel=>{
  const global=panel.dataset.codeCredentials==='global';
  const endpoint=global?'/api/admin/settings/code-credentials':'/auth/code-credentials';
  let state=null,sources=[],section=null;
  const box=panel.querySelector('.credential-forms');
  function tokenStatus(v,source){
   if(!v.configured)return source?.anonymous?t('未配置 · 公开仓库可匿名拉取','Not configured · public repositories are fetched anonymously'):t('未配置','Not configured');
   const labels={ready:['已配置','Configured'],expired:['已过期，请更新','Expired; update required'],expiring:['将在 7 天内过期，请更新','Expires within 7 days; update required'],invalid:['认证失败，请更新','Authentication rejected; update required']};
   const text=labels[v.state]||labels.ready;
   return t(text[0],text[1])+(!v.expires_at?t(' · 未填写过期时间',' · No expiry provided'):'')+(v.source==='environment'?t(' · 来自部署环境变量',' · From the deployment environment'):'');
  }
  function statusClass(v){return ['expired','invalid'].includes(v.state)?'text-danger':v.state==='expiring'?'text-amber-600':'text-muted';}
  function hostLine(source){
   if(source.host)return t('从 ','Fetches from ')+source.host+t(' 拉取代码','');
   return global&&source.editable?t('尚未填写主机；填写后才能登记这个代码源的仓库','No host yet; set one before registering repositories from this source'):t('管理员尚未配置主机，暂不能登记这个代码源的仓库','An administrator has not configured a host; repositories cannot use this source yet');
  }
  function tokenHelp(source){
   const scope=global?t('登记人没有可用的个人令牌时使用。','Used when the registering user has no working personal token.'):t('你登记的仓库优先使用；未配置、过期或认证失败时改用管理员配置的全局令牌。','Used first for repositories you register; the global token is used when this one is missing, expired or rejected.');
   return (source.anonymous?t('公开仓库不需要令牌；读取私有仓库或避免匿名限流时填写只读令牌。','Public repositories need no token; add a read-only token for private repositories or to avoid anonymous rate limits.'):t('读取仓库需要令牌。','Fetching repositories requires a token.'))+t('',' ')+scope;
  }
  function field(name,label,value,placeholder,help,extra){
   return '<label class="block text-[14px]">'+label+'<span class="'+HELP+'">'+help+'</span><input name="'+name+'" '+(extra||'')+' maxlength="200" value="'+esc(value)+'" placeholder="'+esc(placeholder)+'" class="'+INPUT+'"></label>';
  }
  // Connection fields of the editable source; fields set in the service configuration file stay read-only.
  function connection(source){
   if(!global||!source.editable||!section)return '';
   const locked=new Set(source.locked||[]),v=section.values||{};
   const managed=key=>t('由服务配置文件管理：','Managed by the service configuration file: ')+(source[key]||'');
   const one=(key,label,placeholder,help,extra)=>locked.has(key)?field(key,label,'',managed(key),help,(extra||'')+' readonly'):field(key,label,v[key]||'',placeholder,help,extra);
   return one('name',t('显示名称','Display name'),source.name,t('登记仓库时显示的名称；留空使用默认值','Shown when registering repositories; blank keeps the default'))
    +one('host',t('主机','Host'),source.host||t('例如 https://git.example.com','For example https://git.example.com'),t('代码托管的 HTTPS 地址；留空使用默认值，已有仓库登记在这个主机时不能修改','HTTPS address of the code host; blank keeps the default, and it cannot change while repositories use it'),'type="url" inputmode="url"')
    +one('username',t('HTTPS 用户名','HTTPS username'),source.username||'oauth2',t('Git 通过 HTTPS 拉取时与令牌一起发送的用户名，多数代码托管用 oauth2；留空使用默认值','Sent with the token when Git fetches over HTTPS; most hosts use oauth2; blank keeps the default'));
  }
  function sourceCard(source){
   const v=state[source.id]||{configured:false,revision:0},fields=connection(source);
   return '<form data-source="'+esc(source.id)+'" class="field space-y-4 p-5 sm:p-6"><div><h3 class="text-[15px] font-medium">'+esc(source.name)+'</h3><p class="mt-1 break-all text-[13px] text-muted">'+esc(hostLine(source))+'</p></div>'
    +(fields?fields+'<div class="border-t rule"></div>':'')
    +'<label class="block text-[14px]">'+(global?t('全局访问令牌','Global access token'):t('个人访问令牌','Personal access token'))+'<span class="'+HELP+'">'+esc(tokenHelp(source))+'</span><input name="token" type="password" autocomplete="new-password" maxlength="4096" placeholder="'+esc(v.configured?t('已配置，留空保持不变','Configured; leave blank to keep'):t('未配置','Not configured'))+'" class="'+INPUT+'"></label>'
    +'<label class="block text-[14px]">'+t('令牌过期时间','Token expiry')+'<span class="'+HELP+'">'+t('可选，用于到期前 7 天提醒','Optional; used to warn 7 days before expiry')+'</span><input name="expires" type="datetime-local" step="1" class="'+INPUT+'"></label>'
    +'<p class="text-[13px] '+statusClass(v)+'">'+esc(tokenStatus(v,source))+'</p>'
    +ACTIONS+'<button type="button" data-clear class="'+QUIET+'" '+(!v.configured?'disabled':'')+'>'+t('清除令牌','Clear token')+'</button><button type="submit" class="'+PRIMARY+'">'+t('保存','Save')+'</button></div></form>';
  }
  function retiredCard(kind){
   const v=state[kind];
   return '<form data-retired="'+esc(kind)+'" class="field space-y-4 p-5 sm:p-6"><div><h3 class="text-[15px] font-medium">'+esc(kind)+t(' 令牌（已退役类型）',' token (retired type)')+'</h3><p class="mt-1 text-[13px] text-muted">'+t('这个类型已不能登记新仓库，原来登记的仓库已归入对应的代码源，这个令牌不再用于拉取，可以清除。','This type can no longer register repositories and existing ones now belong to its replacement source. The token is no longer used for fetching and can be cleared.')+'</p></div>'
    +'<p class="text-[13px] '+statusClass(v)+'">'+esc(tokenStatus(v))+'</p>'
    +ACTIONS+'<button type="button" data-clear class="'+QUIET+'">'+t('清除','Clear')+'</button></div></form>';
  }
  function render(){
   if(!state)return;
   const retired=Object.keys(state).filter(id=>!sources.some(s=>s.id===id));
   box.innerHTML=sources.map(sourceCard).join('')+retired.map(retiredCard).join('');
   box.querySelectorAll('form').forEach(bind);
  }
  function localExpiry(iso){if(!iso)return '';const d=new Date(iso);return new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,19);}
  function bind(form){
   const kind=form.dataset.source||form.dataset.retired,v=state[kind]||{configured:false,revision:0};
   const status=form.querySelector('[data-status]'),buttons=[...form.querySelectorAll('button')];
   async function run(work){
    status.textContent='';buttons.forEach(b=>b.disabled=true);
    try{const changed=await work();await load();A.toast(changed===false?t('没有需要保存的改动','Nothing to save'):t('保存成功','Saved'));}
    catch(e){status.textContent=e.message;buttons.forEach(b=>b.disabled=false);}
   }
   const clear=form.querySelector('[data-clear]');
   if(clear)clear.onclick=()=>{if(confirm(t('确认清除这个令牌？之后拉取将改用其他可用令牌或匿名访问。','Clear this token? Later fetches use other available tokens or anonymous access.')))run(()=>API.request(endpoint,{method:'PUT',body:{kind,revision:v.revision,clear:true,expires_at:null}}));};
   if(form.dataset.retired)return;
   const expires=form.elements.expires;
   expires.value=localExpiry(v.expires_at);
   expires.addEventListener('click',()=>{try{expires.showPicker?.();}catch(_){/* Native keyboard editing remains available. */}});
   form.onsubmit=e=>{e.preventDefault();run(async()=>{
    let changed=false;
    if(form.elements.host){
     const current=section.values||{},values={};
     for(const key of ['name','host','username'])values[key]=form.elements[key].readOnly?'':form.elements[key].value.trim();
     if(['name','host','username'].some(key=>(values[key]||'')!==(current[key]||''))){section=await API.request('/api/admin/settings/code_source',{method:'PATCH',body:{revision:section.revision,values}});changed=true;}
    }
    const token=form.elements.token.value.trim();
    if(token||expires.value!==localExpiry(v.expires_at)){
     const body={kind,revision:v.revision,clear:false,expires_at:expires.value?new Date(expires.value).toISOString():null};
     if(token)body.token=token;
     await API.request(endpoint,{method:'PUT',body});changed=true;
    }
    return changed;
   });};
  }
  async function load(){
   if(!A.user()||(global&&!A.canManage())){state=null;box.innerHTML='';return;}
   try{
    const [status,list,settings]=await Promise.all([API.request(endpoint),API.request('/v1/code-sources'),global?API.request('/api/admin/settings'):null]);
    state=status;sources=Array.isArray(list)?list:[];section=settings?.sections?.code_source||null;render();
   }catch(e){box.textContent=e.message;}
  }
  A.ready.then(load);
  document.addEventListener('app:auth',load);
  document.addEventListener('langchange',()=>{if(!box.querySelector('input:focus'))render();});
 });
})();
