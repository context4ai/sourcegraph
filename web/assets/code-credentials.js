(function(){
 'use strict';
 const A=App,t=A.t,esc=A.esc;
 const INPUT='mt-2 block w-full rounded-control bg-ink/[0.04] px-3 py-2 outline-none ring-1 ring-transparent focus:ring-ink/20 read-only:opacity-60';
 const ACTIONS='<div class="flex flex-wrap items-center gap-3 border-t rule pt-4"><p data-status role="status" class="mr-auto min-w-0 break-all text-[13px] text-muted"></p>';
 document.querySelectorAll('[data-code-credentials]').forEach(panel=>{
  const global=panel.dataset.codeCredentials==='global';
  const endpoint=global?'/api/admin/settings/code-credentials':'/auth/code-credentials';
  let state=null,sources=[],section=null;
  const box=panel.querySelector('.credential-forms');
  function message(v,source){
   if(!v.configured)return source?.anonymous?t('未配置 · 公开仓库无需令牌，私有仓库需要','Not configured · public repositories need no token; private ones do'):t('未配置','Not configured');
   const labels={ready:['已配置','Configured'],expired:['已过期，请更新','Expired; update required'],expiring:['将在 7 天内过期，请更新','Expires within 7 days; update required'],invalid:['认证失败，请更新','Authentication rejected; update required']};
   const text=labels[v.state]||labels.ready;
   return t(text[0],text[1])+(!v.expires_at?t(' · 未设置过期时间',' · No expiry provided'):'')+(v.source==='environment'?t(' · 使用部署环境凭证',' · From deployment environment'):'');
  }
  function hostLine(source){
   if(!source)return t('旧类型，仅供已登记的仓库使用；不再需要时请清除','Retired type, used only by already registered repositories; clear it when no longer needed');
   return source.host?t('连接 ','Connects to ')+source.host:t('尚未配置主机，无法登记该来源的仓库','No host configured; repositories cannot use this source yet');
  }
  // Website values of the editable source; fields set in the service configuration file stay read-only.
  function sourceForm(){
   const source=sources.find(s=>s.editable);
   if(!global||!source||!section)return '';
   const locked=new Set(source.locked||[]),v=section.values||{};
   const field=(key,label,placeholder,type)=>'<label class="block text-[14px]">'+label+'<input name="'+key+'" '+(type||'')+' maxlength="200" value="'+esc(locked.has(key)?'':v[key]||'')+'" placeholder="'+esc(locked.has(key)?t('由服务配置文件管理：','Managed by the service configuration file: ')+source[key]:placeholder)+'" '+(locked.has(key)?'readonly':'')+' class="'+INPUT+'"></label>';
   return '<form data-source-form class="field space-y-4 p-5 sm:p-6"><p class="text-[14px]">'+esc(source.name)+t(' 代码源',' code source')+'</p>'
    +field('name',t('显示名称','Display name'),source.name)
    +field('host',t('主机','Host'),source.host||'https://git.example.com','type="url" inputmode="url"')
    +field('username',t('Git 用户名','Git username'),source.username||'oauth2')
    +'<p class="text-[13px] text-muted">'+t('留空使用默认值。已有仓库登记在此主机时不能修改主机。','Leave blank for defaults. The host cannot change while repositories are registered with it.')+'</p>'
    +ACTIONS+'<button type="submit" class="rounded-control bg-ink px-3.5 py-1.5 text-[13px] text-paper disabled:opacity-40">'+t('保存','Save')+'</button></div></form>';
  }
  function render(){
   if(!state)return;
   const kinds=sources.map(s=>s.id).filter(id=>state[id]).concat(Object.keys(state).filter(id=>!sources.some(s=>s.id===id)));
   box.innerHTML=sourceForm()+kinds.map(kind=>{
    const v=state[kind],source=sources.find(s=>s.id===kind),label=source?source.name:kind;
    return '<form data-kind="'+esc(kind)+'" class="field space-y-4 p-5 sm:p-6"><label class="block text-[14px]">'+esc(label)+t(' 访问令牌',' access token')+'<span class="mt-1 block text-[13px] text-muted">'+esc(hostLine(source))+'</span><input name="token" type="password" autocomplete="new-password" maxlength="4096" placeholder="'+esc(v.configured?t('已配置，留空保持不变','Configured; leave blank to keep'):t('默认空','Empty by default'))+'" class="'+INPUT+'"></label><label class="block text-[14px]">'+t('过期时间','Expires at')+'<input name="expires" type="datetime-local" step="1" class="'+INPUT+'"></label><p class="text-[13px] '+(['expired','invalid'].includes(v.state)?'text-danger':v.state==='expiring'?'text-amber-600':'text-muted')+'">'+esc(message(v,source))+'</p>'+ACTIONS+'<button type="button" data-clear class="rounded-control px-3 py-1.5 text-[13px] text-muted hover:bg-ink/[0.05]" '+(!v.configured?'disabled':'')+'>'+t('清除','Clear')+'</button><button type="submit" class="rounded-control bg-ink px-3.5 py-1.5 text-[13px] text-paper disabled:opacity-40" '+(v.retired?'hidden':'')+'>'+t('保存','Save')+'</button></div></form>';
   }).join('');
   const sourceEl=box.querySelector('[data-source-form]');
   if(sourceEl)sourceEl.onsubmit=async e=>{
    e.preventDefault();
    const status=sourceEl.querySelector('[data-status]'),button=sourceEl.querySelector('button');status.textContent='';button.disabled=true;
    const values={};for(const key of ['name','host','username'])values[key]=sourceEl.elements[key].readOnly?'':sourceEl.elements[key].value.trim();
    try{section=await API.request('/api/admin/settings/code_source',{method:'PATCH',body:{revision:section.revision,values}});sources=await API.request('/v1/code-sources');render();A.toast(t('保存成功','Saved'));}
    catch(err){status.textContent=err.message;button.disabled=false;}
   };
   box.querySelectorAll('form[data-kind]').forEach(form=>{
    const kind=form.dataset.kind,v=state[kind];
    form.elements.expires.addEventListener('click',()=>{try{form.elements.expires.showPicker?.();}catch(_){/* Native keyboard editing remains available. */}});
    if(v.expires_at){const d=new Date(v.expires_at);form.elements.expires.value=new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,19);}
    async function save(clear){
     const status=form.querySelector('[data-status]');status.textContent='';
     const body={kind,revision:v.revision,clear,expires_at:form.elements.expires.value?new Date(form.elements.expires.value).toISOString():null};
     if(form.elements.token.value.trim())body.token=form.elements.token.value.trim();
     form.querySelectorAll('button').forEach(b=>b.disabled=true);
     try{const next=await API.request(endpoint,{method:'PUT',body});if(next.configured||!next.retired&&!v.retired)state[kind]=next;else delete state[kind];form.elements.token.value='';render();A.toast(t('保存成功','Saved'));}
     catch(e){status.textContent=e.message;form.querySelectorAll('button').forEach(b=>b.disabled=false);}
    }
    form.onsubmit=e=>{e.preventDefault();save(false);};
    form.querySelector('[data-clear]').onclick=()=>{if(confirm(t('确认清除此凭证？后续任务将使用其他可用凭证。','Clear this credential? Future tasks will use other available credentials.')))save(true);};
   });
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
