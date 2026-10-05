(function () {
  'use strict';
  const A=App,M=Management,R=A.Repo,t=A.t,esc=A.esc,$=s=>document.querySelector(s);
  let rows=[], loaded=false,last='', submitting=false;
  const details=new Map(), keys=M.operationKeys();
  const branchInput=BranchInput.mount($('#f-branches'),{defaultBranch:true,onChange:()=>{$('#form-error').textContent='';}});
  const pathInput=PathInput.mount($('#f-paths'),{onChange:()=>{$('#form-error').textContent='';}});
  $('#f-index-mode').onchange=()=>{$('#f-paths-wrap').classList.toggle('hidden',$('#f-index-mode').value!=='monorepo');$('#form-error').textContent='';};
  function renderSyncInterval(){const input=$('#f-sync-interval');input.innerHTML=M.syncIntervalOptions(input.value||M.DEFAULT_SYNC_INTERVAL_MINUTES);}
  renderSyncInterval();
  const initial=new URLSearchParams(location.search);
  $('#filter').value=initial.get('q')||''; $('#show-deleted').checked=initial.get('deleted')==='1';

  const KIND={sync:['同步','Sync'],index:['建索引','Index']};
  function jobLine(job){
    if(!job)return '';
    const k=KIND[job.kind]||[job.kind,job.kind], at='<span class="font-mono text-[12px]">'+esc(A.clock(job.updated_at||job.created_at))+'</span>';
    if(job.state==='failed')return '<span class="text-danger">'+t(k[0]+'失败',k[1]+' failed')+(job.error?' · '+esc(API.jobError(job)):'')+'</span>';
    if(job.state==='running')return t('正在'+k[0],k[1]+' running');
    if(job.state==='queued')return t(k[0]+'排队中',k[1]+' queued');
    if(job.state==='canceled'||job.state==='cancelled')return t(k[0]+'已取消 ',k[1]+' canceled ')+at;
    return t('最近'+k[0]+' ','Last '+k[1].toLowerCase()+' ')+at;
  }
  function currentLine(r){
    const jobs=R.currentJobs(r),active=jobs.find(M.active),failure=jobs.find(j=>j.state==='failed');
    if(active||failure)return jobLine(active||failure);
    const c=R.latestCheck(r);
    if(c)return t('最近检查 ','Last checked ')+esc(A.clock(c.checked_at))+' · '+(c.error?esc(API.jobError({error:c.error})):c.no_changes?t('无变化','No changes'):t('检查完成','Completed'));
    return jobLine(jobs[0]);
  }
  function diskLabel(bytes) {
    const units=['B','K','M','G','T'];let unit=0;
    while(bytes>=1000&&unit<units.length-1){bytes/=1000;unit++;}
    return (unit?bytes.toFixed(1):String(bytes))+' '+units[unit]+t(' 磁盘占用',' disk usage');
  }
  function card(r) {
    const d=details.get(r.name)||r, st=R.state(d), href='/sourcegraph/repo?repo='+encodeURIComponent(r.name);
    const h=R.head(r), v=R.servable(d), others=Object.keys(r.heads||{}).length-1;
    const version=!h?t('等待首次同步','Awaiting first sync'):v&&v.commit!==h.commit?t('最新 ','head ')+A.short(h.commit)+t(' · 可查 ',' · searchable ')+A.short(v.commit):A.short(h.commit);
    const timing=[r,d,...Object.values(d.scopes||{})].filter(x=>x.last_indexed_at&&x.last_index_duration_seconds>0).sort((a,b)=>Date.parse(b.last_indexed_at)-Date.parse(a.last_indexed_at))[0];
    const duration=timing?t('上次索引耗时 ','Last index ')+Number(timing.last_index_duration_seconds.toFixed(2))+t(' 秒',' s'):'';
    const usage=r.disk_usage;
    const disk=usage&&Number.isFinite(usage.bytes)?diskLabel(usage.bytes):t('磁盘待统计','Disk usage pending');
    const canSearch=v&&st!=='disabled'&&st!=='deleted';
    return '<article class="field relative flex flex-col p-5 transition-colors hover:bg-ink/[0.05]">'+
      '<div class="flex items-start gap-4"><a href="'+href+'" class="min-w-0 break-words font-mono text-[15px] font-medium leading-snug after:absolute after:inset-0 after:content-[\'\']">'+esc(r.name).replace('/','/<wbr>')+'</a><span class="ml-auto shrink-0 pt-0.5">'+A.status(st)+'</span></div>'+
      '<div class="mt-4 space-y-1 text-[13px] text-muted"><div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1" data-card-mode><span>'+M.indexModeLabel(r.index_mode)+(r.index_mode==='monorepo'?' · '+t((r.path_groups||[]).length+' 个分组',(r.path_groups||[]).length+' groups'):'')+'</span><span class="text-[12px]" title="'+esc(timing?t('最近一次完成的索引；Monorepo 为单个分组耗时，仅供参考','Most recently completed index; monorepo timing is for one group, for reference'):'')+'">'+esc(duration)+'</span></div><div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1" data-card-branch><span class="font-mono text-[12.5px]">'+(h?'<span class="text-ink">'+esc(h.branch)+'</span> ':'')+version+(others>0?'<span class="font-sans"> · '+t('另 '+others+' 个分支','+'+others+' branches')+'</span>':'')+'</span><span class="text-[12px]" title="'+esc(t('Git 与已发布索引合计','Git and published indexes combined')+(usage?' · '+A.clock(usage.sampled_at):''))+'">'+esc(disk)+'</span></div></div>'+
      '<div class="mt-auto flex items-center gap-3 pt-5 text-[13px] text-muted"><span class="min-w-0 flex flex-wrap gap-x-1"><span>'+currentLine(d)+'</span><span data-next-sync="'+esc(r.next_sync_at||'')+'"></span></span>'+
      '<a href="'+(canSearch?'/sourcegraph/?repo='+encodeURIComponent(r.name):href)+'" class="relative z-10 ml-auto shrink-0 rounded-control px-2 py-1 hover:bg-ink/[0.05] hover:text-ink">'+(canSearch?t('搜索','Search'):t('详情','Details'))+'</a></div></article>';
  }
  function updateSyncTimes(){
    document.querySelectorAll('[data-next-sync]').forEach(el=>{
      const at=Date.parse(el.dataset.nextSync);
      if(!Number.isFinite(at)){el.textContent='';return;}
      const seconds=Math.max(0,Math.ceil((at-Date.now())/1000));
      const parts=[Math.floor(seconds/3600),Math.floor(seconds/60)%60,seconds%60];
      if(!parts[0])parts.shift();
      const remaining=parts.map(n=>String(n).padStart(2,'0')).join(':');
      el.textContent=seconds?t('，下次同步 '+remaining+' 后',', next sync in '+remaining):t('，等待调度',', awaiting scheduling');
      el.title=t('到期后由每分钟扫描器入队；已有任务时不重复排队','Queued by the minute scanner when due; active work is not duplicated');
    });
  }
  const syncTicker=setInterval(()=>{if(!document.hidden)updateSyncTimes();},1000);
  window.addEventListener('pagehide',()=>clearInterval(syncTicker),{once:true});
  function render() {
    $('#add').innerHTML=(A.user()?'':A.ICON.lock)+'<span>'+t(A.user()?'登记仓库':'登录后登记仓库',A.user()?'Register a repository':'Sign in to register')+'</span>';
    const deleted=rows.some(r=>r.deleted);
    $('#show-deleted-wrap').classList.toggle('hidden',!deleted||!A.canManage());$('#show-deleted-wrap').classList.toggle('flex',deleted&&A.canManage());
    const f=$('#filter').value.toLowerCase(),list=rows.filter(r=>($('#show-deleted').checked||!r.deleted)&&r.name.toLowerCase().includes(f));
    const live=rows.filter(r=>!r.deleted);
    $('#summary').textContent=loaded?t(live.length+' 个仓库',live.length+' repositories'):'';
    $('#cards').innerHTML=list.map(card).join('');updateSyncTimes();$('#empty').classList.toggle('hidden',!loaded||!!list.length);
    $('#empty').textContent=rows.length?t('没有匹配的仓库','No repository matches'):t('还没有登记仓库','No repositories yet');
    $('#updated').textContent=last?t('更新于 ','updated ')+A.clock(last):'';
  }
  // Card details (versions, last job) come from availability, a few repositories at a time.
  async function loadDetails(){
    const queue=rows.filter(r=>!r.deleted&&(details.get(r.name)?.observed_at!==r.observed_at||details.get(r.name)?.disk_usage?.sampled_at!==r.disk_usage?.sampled_at)).slice(0,60);
    if(!queue.length)return;
    await Promise.all(Array.from({length:3},async()=>{for(let r=queue.shift();r;r=queue.shift()){try{details.set(r.name,await API.repo(r.name));}catch{}}}));
    render();
  }
  async function load() {
    try {rows=await API.catalog();loaded=true;last=new Date().toISOString();$('#load-error').textContent='';render();loadDetails();}
    catch(e){$('#load-error').textContent=M.error(e);if(!loaded)$('#summary').textContent='';throw e;}
  }
  function open(){if(!M.manage())return;$('#drawer').classList.remove('hidden');$('#drawer aside').setAttribute('role','dialog');$('#drawer aside').setAttribute('aria-modal','true');setTimeout(()=>$('#f-name').focus(),30);}
  function close(){if(submitting)return;$('#drawer').classList.add('hidden');const q=new URLSearchParams(location.search);q.delete('new');history.replaceState(null,'',location.pathname+(q.size?'?'+q:''));$('#add').focus();}
  $('#add').onclick=()=>{const q=new URLSearchParams(location.search);q.set('new','1');history.replaceState(null,'',location.pathname+'?'+q);open();};
  document.querySelectorAll('[data-close]').forEach(b=>b.onclick=e=>{e.preventDefault();close();});
  document.addEventListener('keydown',e=>{if(e.key==='Escape')close();if(e.key==='Tab'&&!$('#drawer').classList.contains('hidden')){const ns=[...$('#drawer').querySelectorAll('button,input,select')].filter(n=>!n.disabled);const a=ns[0],z=ns[ns.length-1];if(e.shiftKey&&document.activeElement===a){e.preventDefault();z.focus();}else if(!e.shiftKey&&document.activeElement===z){e.preventDefault();a.focus();}}});
  $('#f-name').addEventListener('input',()=>{$('#form-error').textContent='';});
  $('#f-sync-interval').addEventListener('change',()=>{$('#form-error').textContent='';});
  async function submit(e){if(e)e.preventDefault();if(submitting||!M.manage()||!$('#form').reportValidity())return;let body;
    try {const name=M.repositoryName($('#f-name').value);if(rows.some(r=>r.name===name))throw new Error(t('该仓库已登记，请到详情页修改','This repository is already registered. Edit it from its details page'));body={name,code_provider:$('#f-code-provider').value,index_mode:$('#f-index-mode').value,public_read:$('#f-ack').checked,policy:{branches:branchInput.value(),...(branchInput.usesDefault()?{default_branch:true}:{})},sync_interval_minutes:M.syncInterval($('#f-sync-interval').value)};if(body.index_mode==='monorepo')body.path_groups=pathInput.value();}
    catch(e){$('#form-error').textContent=e.message;return;}
    submitting=true;$('#submit').disabled=true;$('#submit').textContent=t('提交中','Submitting');$('#form-error').textContent='';
    try {const r=await API.request('/api/admin/v1/repos',{method:'POST',body,headers:{'Idempotency-Key':keys.get(body)}});keys.done(body);A.toast(t('已登记，首轮同步已排队','Registered, first sync queued'));location.assign('/sourcegraph/repo?repo='+encodeURIComponent(r.name)+'&tab=jobs');}
    catch(e){$('#form-error').textContent=M.error(e);}
    finally{submitting=false;$('#submit').disabled=false;$('#submit').textContent=t('登记并准备','Register and prepare');}
  }
  $('#submit').onclick=submit;$('#form').onsubmit=submit;
  function filter(){const p=new URLSearchParams();if($('#filter').value)p.set('q',$('#filter').value);if($('#show-deleted').checked)p.set('deleted','1');history.replaceState(null,'',location.pathname+(p.size?'?'+p:'')+location.hash);render();}
  $('#filter').oninput=filter;$('#show-deleted').onchange=filter;
  document.addEventListener('langchange',()=>{render();renderSyncInterval();branchInput.refresh();pathInput.refresh();});
  document.addEventListener('app:auth',render);
  const poller=M.poll(load,10000,60);
  App.ready.then(()=>{render();poller.refresh();if(initial.get('new')==='1'||location.hash==='#new')open();});
})();
