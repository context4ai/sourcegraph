(function(){
 'use strict';
 const A=App,t=A.t,esc=A.esc;
 document.querySelectorAll('[data-code-credentials]').forEach(panel=>{
  const global=panel.dataset.codeCredentials==='global';
  const endpoint=global?'/api/admin/settings/code-credentials':'/auth/code-credentials';
  let state=null;
  const box=panel.querySelector('.credential-forms');
  function message(v){
   if(!v.configured)return t('未配置','Not configured');
   const labels={ready:['已配置','Configured'],expired:['已过期，请更新','Expired; update required'],expiring:['将在 7 天内过期，请更新','Expires within 7 days; update required'],invalid:['认证失败，请更新','Authentication rejected; update required']};
   const text=labels[v.state]||labels.ready;
   return t(text[0],text[1])+(!v.expires_at?t(' · 未设置过期时间',' · No expiry provided'):'')+(v.source==='environment'?t(' · 使用部署环境凭证',' · From deployment environment'):'');
  }
  function render(){
   if(!state)return;
   box.innerHTML=['github','git'].map(kind=>{
    const v=state[kind],label=kind==='github'?'GitHub':'Git';
    return '<form data-kind="'+kind+'" class="field space-y-4 p-5 sm:p-6"><label class="block text-[14px]">'+label+t(' 访问令牌',' access token')+'<input name="token" type="password" autocomplete="new-password" maxlength="4096" placeholder="'+esc(v.configured?t('已配置，留空保持不变','Configured; leave blank to keep'):t('默认空','Empty by default'))+'" class="mt-2 block w-full rounded-control bg-ink/[0.04] px-3 py-2 outline-none ring-1 ring-transparent focus:ring-ink/20"></label><label class="block text-[14px]">'+t('过期时间','Expires at')+'<input name="expires" type="datetime-local" step="1" class="mt-2 block w-full rounded-control bg-ink/[0.04] px-3 py-2 outline-none ring-1 ring-transparent focus:ring-ink/20"></label><p class="text-[13px] '+(['expired','invalid'].includes(v.state)?'text-danger':v.state==='expiring'?'text-amber-600':'text-muted')+'">'+esc(message(v))+'</p><div class="flex flex-wrap items-center gap-3 border-t rule pt-4"><p data-status role="status" class="mr-auto text-[13px] text-muted"></p><button type="button" data-clear class="rounded-control px-3 py-1.5 text-[13px] text-muted hover:bg-ink/[0.05]" '+(!v.configured?'disabled':'')+'>'+t('清除','Clear')+'</button><button type="submit" class="rounded-control bg-ink px-3.5 py-1.5 text-[13px] text-paper disabled:opacity-40">'+t('保存','Save')+'</button></div></form>';
   }).join('');
   box.querySelectorAll('form').forEach(form=>{
    const kind=form.dataset.kind,v=state[kind];
    form.elements.expires.addEventListener('click',()=>{try{form.elements.expires.showPicker?.();}catch(_){/* Native keyboard editing remains available. */}});
    if(v.expires_at){const d=new Date(v.expires_at);form.elements.expires.value=new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,19);}
    async function save(clear){
     const status=form.querySelector('[data-status]');status.textContent='';
     const body={kind,revision:v.revision,clear,expires_at:form.elements.expires.value?new Date(form.elements.expires.value).toISOString():null};
     if(form.elements.token.value.trim())body.token=form.elements.token.value.trim();
     form.querySelectorAll('button').forEach(b=>b.disabled=true);
     try{state[kind]=await API.request(endpoint,{method:'PUT',body});form.elements.token.value='';render();A.toast(t('保存成功','Saved'));}
     catch(e){status.textContent=e.message;form.querySelectorAll('button').forEach(b=>b.disabled=false);}
    }
    form.onsubmit=e=>{e.preventDefault();save(false);};
    form.querySelector('[data-clear]').onclick=()=>{if(confirm(t('确认清除此凭证？后续任务将使用其他可用凭证。','Clear this credential? Future tasks will use other available credentials.')))save(true);};
   });
  }
  async function load(){
   if(!A.user()||(global&&!A.canManage())){state=null;box.innerHTML='';return;}
   try{state=await API.request(endpoint);render();}catch(e){box.textContent=e.message;}
  }
  A.ready.then(load);
  document.addEventListener('app:auth',load);
  document.addEventListener('langchange',()=>{if(!box.querySelector('input[name="token"]:focus'))render();});
 });
})();
