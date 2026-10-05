(function () {
  'use strict';
  const A=window.App,API=window.API,t=A.t,esc=A.esc,$=s=>document.querySelector(s);
  const state={items:[],cursor:'',previous:[],next:'',loaded:false,busy:false,saving:false,seq:0,error:null,target:null};
  let controller;
  const date=x=>new Date(x).toLocaleDateString(A.lang()==='en'?'en-US':'zh-CN',{year:'numeric',month:'short',day:'numeric'});
  const expired=k=>!!k.expires_at&&Date.parse(k.expires_at)<=Date.now();
  const masked=k=>k.key_prefix+'…'+(k.key?k.key.slice(-4):'····');
  const defaultName=()=>{const u=A.user()||{},n=(u.name||(u.email||'').split('@')[0]||'').trim();return n?t(n+' 的 Token',n+"'s token"):'API Token';};
  function invalid(msg){const input=$('#token-name');input.setAttribute('aria-invalid',msg?'true':'false');$('#token-name-error').textContent=msg||'';}
  const errorText=e=>(e.message||t('请求失败','Request failed'))+(e.code?' · '+e.code:'');
  function clearSecret(){$('#token-secret').value='';$('#token-created').classList.add('hidden');$('#token-create-form').classList.remove('hidden');}
  function gate(){
    const allowed=!!A.user();$('#token-content').classList.toggle('hidden',!allowed);$('#token-access').classList.toggle('hidden',allowed);
    if(allowed)return true;
    $('#token-access').innerHTML='<p>'+t('登录后可管理自己的 API Token。','Sign in to manage your own API tokens.')+'</p><button id="tokens-login" class="mt-4 rounded-control bg-ink px-4 py-2 text-paper">'+t('登录','Sign in')+'</button>';
    $('#tokens-login').onclick=()=>A.requireLogin(location.pathname);controller?.abort();state.seq++;clearSecret();state.items=[];
    for(const id of ['token-create-dialog','token-delete-dialog'])if($('#'+id).open)$('#'+id).close();
    return false;
  }
  function render(){
    $('#token-error').textContent=state.error?errorText(state.error):'';
    const off=state.busy||state.saving?' disabled':'',btn='rounded-control px-2.5 py-1 text-[13px] text-muted hover:bg-ink/[0.05] hover:text-ink disabled:opacity-40 disabled:hover:bg-transparent';
    $('#token-rows').innerHTML=state.items.map((key,i)=>{
      const ends=expired(key)?'<span class="text-danger">'+t('已过期','Expired')+'</span>':key.expires_at?t(date(key.expires_at)+' 到期','Expires '+date(key.expires_at)):t('永不过期','No expiration');
      const copy=key.key?'':' disabled title="'+esc(t('此 Token 创建时未保存完整内容，无法复制；可删除后重新创建','Created before full tokens were kept; delete it and create a new one to copy'))+'"';
      return '<li class="flex flex-wrap items-center gap-x-6 gap-y-2 px-3 py-3.5 sm:px-4"><div class="min-w-0 flex-1"><p class="truncate font-medium" title="'+esc(key.name)+'">'+esc(key.name)+'</p><p class="mt-1 text-[12.5px] text-muted">'+t(date(key.created_at)+' 创建','Created '+date(key.created_at))+' · '+ends+(key.allow_prepare?' · '+t('准备索引','Prepare index'):'')+'</p></div>'+
        '<code class="font-mono text-[12.5px] text-muted">'+esc(masked(key))+'</code><div class="flex gap-1"><button data-copy-token="'+i+'" class="'+btn+'"'+(copy||off)+'>'+t('复制','Copy')+'</button><button data-delete-token="'+i+'" class="'+btn+' hover:text-danger"'+off+'>'+t('删除','Delete')+'</button></div></li>';
    }).join('')||'<li class="px-3 py-8 text-center text-[14px] text-muted sm:px-4">'+(state.loaded?t('尚未创建 Token','No tokens yet'):t('正在读取…','Loading…'))+'</li>';
    $('#token-rows').querySelectorAll('[data-copy-token]').forEach(b=>b.onclick=()=>{const k=state.items[+b.dataset.copyToken];if(k&&k.key)A.copy(k.key,t('Token 已复制','Token copied'));});
    $('#token-rows').querySelectorAll('[data-delete-token]').forEach(b=>b.onclick=()=>{state.target=state.items[+b.dataset.deleteToken];renderDelete();$('#token-delete-error').textContent='';$('#token-delete-dialog').showModal();$('#token-delete-cancel').focus();});
    const paged=state.previous.length>0||!!state.next;for(const id of ['previous-tokens','token-page','next-tokens'])$('#'+id).classList.toggle('hidden',!paged);
    $('#token-page').textContent=t('第 '+(state.previous.length+1)+' 页','Page '+(state.previous.length+1));
    $('#previous-tokens').disabled=state.busy||state.saving||!state.previous.length;$('#next-tokens').disabled=state.busy||state.saving||!state.next;
    $('#refresh-tokens').disabled=state.busy||state.saving;$('#new-token').disabled=state.busy||state.saving;
  }
  async function load(cursor=state.cursor,previous=state.previous){
    if(!gate())return;controller?.abort();controller=new AbortController();const seq=++state.seq;state.busy=true;state.error=null;render();
    try{const out=await API.request('/auth/api-keys?limit=20'+(cursor?'&after='+encodeURIComponent(cursor):''),{signal:controller.signal});if(seq!==state.seq)return;
      if(!Array.isArray(out.items)||typeof out.next_cursor!=='string'||out.next_cursor&&out.next_cursor===cursor)throw new Error(t('分页响应无效','Invalid pagination response'));
      state.items=out.items;state.next=out.next_cursor;state.cursor=cursor;state.previous=[...previous];state.loaded=true;
    }catch(e){if(seq===state.seq&&e.name!=='AbortError'){state.error=e;if(e.status===401){await API.loadSession();gate();}}}
    finally{if(seq===state.seq){state.busy=false;render();}}
  }
  function renderDelete(){if(state.target)$('#token-delete-detail').textContent=state.target.name+' · '+t('删除后，使用此 Token 的客户端将无法继续访问。此操作无法撤销。','Clients using this token will lose access. This cannot be undone.');}
  $('#new-token').onclick=()=>{if(state.busy||state.saving||!gate())return;clearSecret();$('#token-create-form').reset();$('#token-prepare').disabled=A.user()?.role!=='admin';$('#token-name').value=defaultName();invalid('');$('#token-create-error').textContent='';$('#token-create-dialog').showModal();$('#token-name').focus();$('#token-name').select();};
  $('#token-name').addEventListener('input',()=>invalid(''));
  $('#token-create-cancel').onclick=()=>{if(!state.saving)$('#token-create-dialog').close();};$('#token-done').onclick=()=>$('#token-create-dialog').close();
  $('#token-copy').onclick=()=>{if($('#token-secret').value)A.copy($('#token-secret').value);};
  $('#token-create-dialog').addEventListener('close',clearSecret);
  for(const id of ['token-create-dialog','token-delete-dialog'])$('#'+id).addEventListener('cancel',e=>{if(state.saving)e.preventDefault();});
  $('#token-create-form').onsubmit=async e=>{
    e.preventDefault();if(state.saving||!gate())return;
    const name=$('#token-name').value.trim();
    if(!name||new TextEncoder().encode(name).length>128){invalid(name?t('名称最多 128 字节','Use at most 128 UTF-8 bytes'):t('请填写名称，便于之后识别这个 Token','Enter a name so you can recognise this token later'));$('#token-name').focus();return;}
    const days=Number($('#token-expiry').value),body={name};if($('#token-prepare').checked&&!$('#token-prepare').disabled)body.allow_prepare=true;if(days)body.expires_at=new Date(Date.now()+days*86400000).toISOString();
    state.saving=true;render();$('#token-create-submit').disabled=true;$('#token-create-cancel').disabled=true;$('#token-create-error').textContent='';
    try{const out=await API.request('/auth/api-keys',{method:'POST',body});if(typeof out.key!=='string'||!out.key.startsWith('sgk_'))throw new Error(t('创建回执无效','Invalid creation response'));
      if(A.user()&&$('#token-create-dialog').open){$('#token-secret').value=out.key;$('#token-create-form').classList.add('hidden');$('#token-created').classList.remove('hidden');$('#token-copy').focus();}
    }catch(err){$('#token-create-error').textContent=errorText(err)+t('；如结果不明，请先关闭弹窗并刷新列表确认，勿重复提交。','; if the result is uncertain, close this dialog and refresh the list before trying again.');}
    finally{state.saving=false;$('#token-create-submit').disabled=false;$('#token-create-cancel').disabled=false;await load('',[]);}
  };
  $('#token-delete-cancel').onclick=()=>{if(!state.saving)$('#token-delete-dialog').close();};
  $('#token-delete-confirm').onclick=async()=>{
    if(state.saving||!state.target||!gate())return;state.saving=true;render();$('#token-delete-confirm').disabled=true;$('#token-delete-cancel').disabled=true;$('#token-delete-error').textContent='';let removed=false;
    try{await API.request('/auth/api-keys/'+encodeURIComponent(state.target.id),{method:'DELETE'});removed=true;$('#token-delete-dialog').close();A.toast(t('Token 已删除','Token deleted'));}
    catch(e){$('#token-delete-error').textContent=errorText(e);}
    finally{state.saving=false;$('#token-delete-confirm').disabled=false;$('#token-delete-cancel').disabled=false;render();}
    if(removed)await load('',[]);
  };
  $('#refresh-tokens').onclick=()=>load();$('#next-tokens').onclick=()=>{if(state.next&&!state.busy)load(state.next,[...state.previous,state.cursor]);};$('#previous-tokens').onclick=()=>{if(state.previous.length&&!state.busy)load(state.previous.at(-1),state.previous.slice(0,-1));};
  document.addEventListener('langchange',()=>{gate();render();renderDelete();});document.addEventListener('app:auth',gate);window.addEventListener('pagehide',()=>{clearSecret();state.seq++;controller?.abort();});
  API.ready.then(()=>{if(gate())load();});
})();
