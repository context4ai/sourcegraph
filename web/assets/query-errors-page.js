(function () {
  'use strict';
  const A=window.App,API=window.API,t=A.t,esc=A.esc,$=s=>document.querySelector(s),endpoint='/api/admin/settings/query-errors';
  let rows=[],next='',cursor='',previous=[],busy=false,sequence=0,detailSequence=0,controller,detailController,detail=null,clearing=false;
  function message(e){return (e.message||t('读取失败','Failed to load'))+(e.code?' · '+e.code:'');}
  function gate(){
    const allowed=!!A.user()&&A.canManage();
    $('#logs-content').classList.toggle('hidden',!allowed);
    $('#access-state').textContent=allowed?'':t('请使用管理员账号登录后查看错误日志。','Sign in as an administrator to view error logs.');
    if(!allowed){controller?.abort();detailController?.abort();sequence++;detailSequence++;rows=[];detail=null;$('#log-rows').replaceChildren();$('#detail-body').replaceChildren();$('#error-drawer').close();$('#clear-logs-dialog').close();}
    return allowed;
  }
  function render(){
    $('#log-rows').innerHTML=rows.map((e,i)=>'<tr class="row-hover align-top"><td class="py-4 pr-5 whitespace-nowrap"><button class="underline underline-offset-4" data-detail="'+i+'">'+esc(new Date(e.time).toLocaleString(A.lang()==='en'?'en-US':'zh-CN'))+'</button></td><td class="py-4 pr-5"><div class="font-mono">'+esc(e.operation)+' · '+esc(e.transport)+'</div><div class="mt-1 break-all text-muted">'+esc(e.repository)+'</div></td><td class="py-4 pr-5"><div class="font-mono text-danger">'+esc(e.code)+'</div><div class="mt-1 text-muted text-[12px]">'+t('历史失败 · 重试状态未知','Historical failure · retry status unknown')+'</div><div class="mt-1 break-words text-muted">'+esc(e.message)+'</div></td><td class="py-4 whitespace-nowrap">'+esc(e.duration_ms)+' ms</td></tr>').join('')||'<tr><td colspan="4" class="py-8 text-muted">'+t(busy?'正在读取…':'暂无已记录的查询失败',busy?'Loading…':'No recorded query failures')+'</td></tr>';
    $('#log-rows').querySelectorAll('tr').forEach((row,i)=>{if(rows[i]){row.style.cursor='pointer';row.onclick=()=>openDetail(rows[i].id);}});
    $('#previous-logs').disabled=busy||clearing||!previous.length;$('#next-logs').disabled=busy||clearing||!next;$('#refresh-logs').disabled=busy||clearing;$('#clear-logs').disabled=busy||clearing;$('#clear-logs-confirm').disabled=clearing;$('#clear-logs-cancel').disabled=clearing;
  }
  async function load(after='',back=[]){
    if(clearing||!gate())return;controller?.abort();controller=new AbortController();const seq=++sequence;busy=true;$('#logs-error').textContent='';render();
    try{
      const out=await API.request(endpoint+(after?'?after='+encodeURIComponent(after):''),{signal:controller.signal});
      if(seq!==sequence)return;rows=out.items;next=out.next_cursor;cursor=after;previous=back;
      $('#summary').textContent=t('当前页 '+rows.length+' 条','This page: '+rows.length+' records')+(out.dropped_current_instance?t(' · 当前实例丢弃 '+out.dropped_current_instance+' 条日志',' · Dropped on this instance: '+out.dropped_current_instance):'');
    }catch(e){if(seq!==sequence||e.name==='AbortError')return;$('#logs-error').textContent=message(e);if(e.status===401||e.status===403){await API.loadSession();gate();}if(e.code==='ERROR_LOG_LIST_CHANGED'){cursor='';previous=[];next='';rows=[];}}
    finally{if(seq===sequence){busy=false;render();}}
  }
  function renderDetail(){
    if(!detail)return;
    const labels={time:t('时间','Time'),operation:t('操作','Operation'),transport:t('协议','Transport'),repository:t('仓库','Repository'),request_id:'Request ID',id:t('日志 ID','Log ID'),instance:t('实例','Instance'),actor:t('调用身份','Actor'),duration_ms:t('耗时（ms）','Duration (ms)'),code:t('错误码','Error code'),message:t('原因','Reason')};
    $('#detail-body').innerHTML='<p class="mb-6 text-[13px] text-muted">'+t('这是一次历史失败，不代表仓库当前不可查询。未关联后续请求，无法判断是否已重试成功。','This is a historical failure, not the current repository health. Later requests are not linked, so retry success is unknown.')+'</p><dl class="space-y-5">'+Object.entries(labels).map(([key,label])=>'<div><dt class="text-[13px] text-muted">'+esc(label)+'</dt><dd class="mt-1 break-all font-mono text-[13px]">'+esc(String(detail[key]??'—'))+'</dd></div>').join('')+'</dl><h3 class="mt-8 font-medium">'+t('请求上下文','Request context')+'</h3><pre id="detail-context" class="mt-3 whitespace-pre-wrap break-all rounded-doc bg-sand p-4 text-[12px]"></pre>'+(detail.context_truncated?'<p class="mt-2 text-muted">'+t('上下文过长，已截断。','Context exceeded the limit and was truncated.')+'</p>':'');
    if(detail.query_fragment || detail.suggested_query || detail.suggestion_omitted){
      const recovery=document.createElement('section');
      recovery.className='mt-6 rounded-doc bg-sand p-4';
      recovery.innerHTML='<h3 class="font-medium">'+t('修正建议','Recovery guidance')+'</h3>';
      if(detail.query_fragment) recovery.innerHTML+='<p class="mt-3 text-[13px] text-muted">'+t('错误片段（可能截短）','Query fragment (may be shortened)')+'</p><pre class="mt-2 whitespace-pre-wrap break-all text-[12px]">'+esc(detail.query_fragment)+'</pre>';
      if(detail.suggested_query){
        recovery.innerHTML+='<div class="mt-4 flex items-center justify-between gap-3"><p class="text-[13px] text-muted">'+t('完整建议查询','Suggested full query')+'</p><button id="copy-suggested-query" class="text-[13px] hover:underline underline-offset-4">'+t('复制查询','Copy query')+'</button></div><pre class="mt-2 whitespace-pre-wrap break-all text-[12px]">'+esc(detail.suggested_query)+'</pre><p class="mt-3 text-[13px] text-muted">'+t('确认符合原意后，仅替换 q，保留原仓库、版本和目录范围，重试一次。服务未自动执行此建议。','If this matches your intent, replace only q and retry once with the same repository, revision and paths. The service did not execute this suggestion.')+'</p>';
      } else if(detail.suggestion_omitted) recovery.innerHTML+='<p class="mt-3 text-[13px] text-muted">'+t('建议查询过长或含敏感内容，未保存可执行副本。请检查原调用返回。','The suggestion was too long or contained sensitive data. No executable copy was saved; check the original tool response.')+'</p>';
      $('#detail-body').prepend(recovery);
      if(detail.suggested_query) $('#copy-suggested-query').onclick=()=>A.copy(detail.suggested_query);
    } else if(detail.code==='INVALID_QUERY'){
      const note=document.createElement('p');note.className='mt-5 text-[13px] text-muted';note.textContent=t('此记录没有保存建议查询，可能来自旧版本或该语法没有确定的修正建议。','No suggested query was saved; this may be an older record or syntax without a definite correction.');$('#detail-body').append(note);
    }
    let context=detail.context||'';try{context=JSON.stringify(JSON.parse(context),null,2);}catch(_){}$('#detail-context').textContent=context;
  }
  async function openDetail(id){
    if(clearing||!gate())return;detailController?.abort();detailController=new AbortController();const seq=++detailSequence;detail=null;$('#detail-body').textContent=t('正在读取…','Loading…');if(!$('#error-drawer').open)$('#error-drawer').showModal();$('#close-detail').focus();
    try{const out=await API.request(endpoint+'?id='+encodeURIComponent(id),{signal:detailController.signal});if(seq!==detailSequence)return;detail=out;renderDetail();}
    catch(e){if(seq===detailSequence&&e.name!=='AbortError'){$('#detail-body').textContent=message(e);if(e.status===401||e.status===403){await API.loadSession();gate();}}}
  }
  $('#close-detail').onclick=()=>$('#error-drawer').close();
  $('#error-drawer').addEventListener('close',()=>{detailSequence++;detailController?.abort();detail=null;$('#detail-body').replaceChildren();});
  $('#error-drawer').addEventListener('click',e=>{const r=e.currentTarget.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right) e.currentTarget.close();});
  const clearDialog=$('#clear-logs-dialog');
  $('#clear-logs').onclick=()=>{if(busy||clearing||!gate())return;$('#clear-logs-error').textContent='';clearDialog.showModal();$('#clear-logs-cancel').focus();};
  $('#clear-logs-cancel').onclick=()=>{if(!clearing)clearDialog.close();};
  clearDialog.addEventListener('cancel',e=>{if(clearing)e.preventDefault();});
  $('#clear-logs-confirm').onclick=async()=>{
    if(clearing||!gate())return;clearing=true;controller?.abort();sequence++;busy=false;render();
    try{
      await API.request(endpoint,{method:'DELETE'});
      detailSequence++;detailController?.abort();detail=null;$('#detail-body').replaceChildren();$('#error-drawer').close();
      rows=[];next='';cursor='';previous=[];$('#summary').textContent=t('日志已清除','Logs cleared');clearDialog.close();
      clearing=false;await load();
    }catch(e){$('#clear-logs-error').textContent=message(e);if(e.status===401||e.status===403){await API.loadSession();gate();}}
    finally{clearing=false;render();}
  };
  $('#refresh-logs').onclick=()=>load();$('#next-logs').onclick=()=>load(next,[...previous,cursor]);$('#previous-logs').onclick=()=>load(previous.at(-1),previous.slice(0,-1));
  document.addEventListener('app:auth',()=>{if(gate())load();});document.addEventListener('langchange',()=>{gate();render();renderDetail();});
  window.addEventListener('pagehide',()=>{sequence++;detailSequence++;controller?.abort();detailController?.abort();});
  API.ready.then(()=>load());
})();
