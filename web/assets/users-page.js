(function () {
  'use strict';
  const A=window.App, API=window.API, t=A.t, esc=A.esc, $=s=>document.querySelector(s);
  const state={items:[],cursor:'',previous:[],next:'',adminCount:null,filters:{q:'',department:'',role:''},busy:false,loaded:false,error:null,target:null,saving:false,seq:0};
  let controller;
  function errorText(e){return (e.message||t('请求失败','Request failed'))+(e.code?' · '+e.code:'')+(e.requestId?' · '+e.requestId:'');}
  function roleName(role){return role==='admin'?t('管理员','Administrator'):role==='user'?t('普通用户','User'):role;}
  function time(value){if(!value||value.startsWith('0001'))return t('未提供','Not provided');const d=new Date(value);return Number.isNaN(+d)?t('未提供','Not provided'):d.toLocaleString(A.lang()==='en'?'en-US':'zh-CN');}
  function cannotDemote(u){return u.role==='admin'&&(u.id===A.user()?.id||state.adminCount===1);}
  function permitted(){return !!A.user()&&A.canManage();}
  function gate(){
    const allowed=permitted();$('#users-content').classList.toggle('hidden',!allowed);$('#access-state').classList.toggle('hidden',allowed);
    if(allowed)return true;
    if(!A.user())$('#access-state').innerHTML='<p>'+t('登录管理员账号后可以管理用户。','Sign in with an administrator account to manage users.')+'</p><button id="users-login" class="mt-4 rounded-control bg-ink px-4 py-2 text-paper">'+t('登录','Sign in')+'</button>';
    else $('#access-state').innerHTML='<p>'+t('当前账号是普通用户，没有管理用户的权限。','Your account does not have permission to manage users.')+'</p><a href="/sourcegraph/" class="mt-4 inline-block underline">'+t('返回搜索','Back to search')+'</a>';
    const login=$('#users-login');if(login)login.onclick=()=>A.requireLogin(location.pathname+location.search);
    controller?.abort();state.seq++;if($('#role-dialog').open)$('#role-dialog').close();return false;
  }
  function render(){
    $('#users-summary').textContent=state.loaded?t('当前页 '+state.items.length+' 位用户','This page: '+state.items.length+' users')+(Number.isInteger(state.adminCount)?' · '+t('管理员 '+state.adminCount+' 位',state.adminCount+' administrators'):''):t('正在读取用户…','Loading users…');
    $('#users-error').textContent=state.error?errorText(state.error)+(state.loaded?t('；保留当前列表','; current list retained'):''):'';
    $('#user-rows').innerHTML=state.items.map((u,i)=>'<tr class="row-hover align-top"><td class="py-3 pr-5"><div class="font-medium">'+esc(u.name||t('未提供姓名','Name not provided'))+(u.id===A.user()?.id?'<span class="ml-2 text-[12px] text-muted">'+t('你','you')+'</span>':'')+'</div></td><td class="max-w-64 break-all py-3 pr-5 font-mono text-[12px] text-muted">'+esc(u.email||t('未提供邮箱','Email not provided'))+'</td><td class="max-w-56 break-words py-3 pr-5">'+esc(u.department||t('未提供','Not provided'))+'</td><td class="py-3 pr-5 whitespace-nowrap">'+esc(roleName(u.role))+'</td><td class="py-3 pr-5 whitespace-nowrap font-mono text-[12px] text-muted">'+esc(time(u.last_login_at))+'</td><td class="py-3 text-right"><button data-role-index="'+i+'" class="whitespace-nowrap rounded-control bg-ink/[0.05] px-3 py-1.5 text-[13px] hover:bg-ink/[0.08] disabled:opacity-40" '+(state.busy||state.saving||cannotDemote(u)?'disabled':'')+'>'+t(u.role==='admin'?'设为普通用户':'设为管理员',u.role==='admin'?'Make user':'Make administrator')+'</button></td></tr>').join('')||(state.loaded?'<tr><td colspan="6" class="py-8 text-muted">'+t('没有符合条件的用户','No users match these filters')+'</td></tr>':'');
    $('#user-rows').querySelectorAll('[data-role-index]').forEach(b=>b.onclick=()=>openRole(state.items[+b.dataset.roleIndex]));
    $('#previous-users').disabled=state.busy||state.saving||state.previous.length===0;$('#next-users').disabled=state.busy||state.saving||!state.next;
    $('#users-page').textContent=t('第 '+(state.previous.length+1)+' 页','Page '+(state.previous.length+1));
    for(const id of ['apply-filters','reset-filters','refresh-users'])$('#'+id).disabled=state.busy||state.saving;
  }
  function syncURL(){const p=new URLSearchParams();for(const [k,v]of Object.entries(state.filters))if(v)p.set(k,v);if(state.cursor)p.set('cursor',state.cursor);history.replaceState(null,'',location.pathname+(p.size?'?'+p:''));}
  async function load({cursor=state.cursor,previous=state.previous,filters=state.filters}={}){
    if(state.saving||!gate())return;
    if(new TextEncoder().encode(filters.q).length>128){state.error=new Error(t('搜索词最多为 128 字节，请缩短后重试。','Search text must be at most 128 UTF-8 bytes. Shorten it and retry.'));render();return;}
    controller?.abort();controller=new AbortController();const seq=++state.seq;let resetCursor=false;state.busy=true;state.error=null;render();
    const q=new URLSearchParams({...filters,limit:'50'});if(cursor)q.set('cursor',cursor);
    try{
      const out=await API.request('/auth/admin/users?'+q,{signal:controller.signal});if(seq!==state.seq)return;
      if(!Array.isArray(out.items)||typeof out.next_cursor!=='string'||out.next_cursor&&(out.next_cursor===cursor))throw new Error(t('用户分页响应无效','Invalid user pagination response'));
      state.items=out.items;state.next=out.next_cursor;state.adminCount=out.admin_count;state.cursor=cursor;state.previous=[...previous];state.filters={...filters};state.loaded=true;syncURL();
    }catch(e){if(seq===state.seq&&e.name!=='AbortError'){state.error=e;if(e.code==='USER_LIST_CHANGED'&&cursor){resetCursor=true;A.toast(t('用户列表已更新，正在返回第一页。','The user list changed. Returning to the first page.'));}if(e.status===401||e.status===403){await API.loadSession();gate();}}}
    finally{if(seq===state.seq){state.busy=false;render();}}
    if(resetCursor&&seq===state.seq)await load({cursor:'',previous:[],filters});
  }
  function openRole(user){if(!permitted()||state.busy||state.saving||cannotDemote(user))return;state.target={...user};renderRole();$('#role-error').textContent='';$('#role-dialog').showModal();$('#role-cancel').focus();}
  function renderRole(){const u=state.target;if(!u)return;const next=u.role==='admin'?'user':'admin';$('#role-title').textContent=t('将此用户设为'+roleName(next),'Make this user '+roleName(next).toLowerCase());$('#role-detail').textContent=(u.name||t('未提供姓名','Name not provided'))+' · '+(u.email||t('未提供邮箱','Email not provided'))+'。 '+(next==='admin'?t('管理员可以修改仓库、管理用户和系统设置。','Administrators can change repositories, users and system settings.'):t('移除管理权限；读取能力仍遵循站点访问策略。','Management access will be removed; read access follows the site policy.'));
  }
  $('#role-cancel').onclick=()=>{if(!state.saving)$('#role-dialog').close();};$('#role-dialog').addEventListener('cancel',e=>{if(state.saving)e.preventDefault();});
  $('#role-confirm').onclick=async()=>{
    const u=state.target;if(!u||state.busy||state.saving||!permitted()||cannotDemote(u))return;let reload=false;state.saving=true;render();$('#role-confirm').disabled=true;$('#role-cancel').disabled=true;$('#role-error').textContent='';
    try{
      const out=await API.request('/auth/admin/users/'+encodeURIComponent(u.id)+'/role',{method:'PATCH',body:{role:u.role==='admin'?'user':'admin',expected_role:u.role}});
      if(out.user?.id!==u.id||!['user','admin'].includes(out.user.role))throw new Error(t('角色更新回执无效','Invalid role update receipt'));
      state.items=state.items.map(x=>x.id===u.id?out.user:x);state.adminCount=out.admin_count;$('#role-dialog').close();A.toast(t('角色已更新','Role updated'));
      if(u.id===A.user()?.id){await API.loadSession();gate();}reload=permitted();
    }catch(e){$('#role-error').textContent=e.code==='SELF_DEMOTION_FORBIDDEN'?t('管理员不能将自己设为普通用户。','Administrators cannot demote themselves.'):e.code==='LAST_ADMIN_REQUIRED'?t('至少需要保留一位管理员，此次修改未生效。','At least one administrator must remain; the change was not applied.'):e.code==='ROLE_CONFLICT'?t('此用户的角色已发生变化，请取消并刷新列表后重试。','This user’s role changed. Cancel and refresh the list before retrying.'):errorText(e);}
    finally{state.saving=false;$('#role-confirm').disabled=false;$('#role-cancel').disabled=false;render();}
    if(reload){state.cursor='';state.previous=[];state.next='';syncURL();await load({cursor:'',previous:[]});}
  };
  $('#user-filters').onsubmit=e=>{e.preventDefault();load({cursor:'',previous:[],filters:{q:$('#user-q').value.trim(),department:$('#user-department').value.trim(),role:$('#user-role').value}});};
  $('#reset-filters').onclick=()=>{for(const id of ['user-q','user-department','user-role'])$('#'+id).value='';load({cursor:'',previous:[],filters:{q:'',department:'',role:''}});};
  $('#refresh-users').onclick=()=>load();$('#next-users').onclick=()=>{if(state.next&&!state.busy)load({cursor:state.next,previous:[...state.previous,state.cursor]});};$('#previous-users').onclick=()=>{if(state.previous.length&&!state.busy)load({cursor:state.previous.at(-1),previous:state.previous.slice(0,-1)});};
  document.addEventListener('langchange',()=>{gate();render();renderRole();});document.addEventListener('app:auth',gate);window.addEventListener('pagehide',()=>{state.seq++;controller?.abort();});
  API.ready.then(()=>{const p=new URLSearchParams(location.search);state.filters={q:p.get('q')||'',department:p.get('department')||'',role:['admin','user'].includes(p.get('role'))?p.get('role'):''};state.cursor=p.get('cursor')||'';$('#user-q').value=state.filters.q;$('#user-department').value=state.filters.department;$('#user-role').value=state.filters.role;if(gate())load();});
})();
