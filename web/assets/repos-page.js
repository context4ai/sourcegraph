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
  $('#f-git-timeout').innerHTML=M.gitTimeoutOptions(0);
  const initial=new URLSearchParams(location.search);
  $('#filter').value=initial.get('q')||''; $('#show-deleted').checked=initial.get('deleted')==='1';
  let managerChoice=initial.get('manager')||'', sortChoice=initial.get('sort')==='disk'?'disk':'';
  // Managers come with the catalog for signed-in callers only; the filter is hidden otherwise.
  function managerKey(p){return p.id||p.email||p.name||'';}
  function managerName(p){return p.name||p.email||p.id;}
  function renderControls(){
    const people=new Map();
    for(const r of rows)for(const p of r.managers||[])if(managerKey(p))people.set(managerKey(p),p);
    const me=A.user(), sel=$('#manager-filter');
    const show=!!me&&rows.some(r=>Array.isArray(r.managers));
    $('#manager-filter-wrap').classList.toggle('hidden',!show);
    if(show){
      const list=[...people.values()].sort((a,b)=>managerName(a).localeCompare(managerName(b)));
      sel.innerHTML='<option value="">'+esc(t('全部管理员','All managers'))+'</option><option value="me">'+esc(t('我管理的','Managed by me'))+'</option><option value="none">'+esc(t('仅站点管理员','Site administrators only'))+'</option>'+
        list.map(p=>'<option value="'+esc(managerKey(p))+'" title="'+esc(p.email||'')+'">'+esc(managerName(p))+'</option>').join('');
      if(managerChoice&&!['me','none'].includes(managerChoice)&&!people.has(managerChoice))managerChoice='';
      sel.value=managerChoice;
    }
    $('#sort').innerHTML='<option value="">'+esc(t('默认排序','Default order'))+'</option><option value="disk">'+esc(t('按占用空间排序','Sort by disk usage'))+'</option>';
    $('#sort').value=sortChoice;
  }
  function matchesManager(r){
    if(!managerChoice||!Array.isArray(r.managers))return true;
    if(managerChoice==='none')return !r.managers.length;
    const me=A.user(), key=managerChoice==='me'?(me&&me.id):managerChoice;
    return r.managers.some(p=>managerKey(p)===key);
  }
  function diskBytes(r){const u=(details.get(r.name)||r).disk_usage;return u&&Number.isFinite(u.bytes)?u.bytes:-1;}

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
    renderControls();
    const f=$('#filter').value.toLowerCase(),list=rows.filter(r=>($('#show-deleted').checked||!r.deleted)&&r.name.toLowerCase().includes(f)&&matchesManager(r));
    if(sortChoice==='disk')list.sort((a,b)=>diskBytes(b)-diskBytes(a)||a.name.localeCompare(b.name));
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
  // Only sources with a configured host can register repositories; the editable source is preselected.
  async function loadSources(){const select=$('#f-code-provider');try{const list=(await API.request('/v1/code-sources')).filter(s=>s.host);const chosen=select.value||(list.find(s=>s.editable)||list[0])?.id||'';select.innerHTML=list.map(s=>'<option value="'+esc(s.id)+'" title="'+esc(s.host)+'">'+esc(s.name)+'</option>').join('');select.value=list.some(s=>s.id===chosen)?chosen:(list[0]?.id||'');}catch(e){$('#form-error').textContent=M.error(e);}}
  // Administrators always register; others only when the site allows user repositories.
  function canRegister(){return A.canManage()||!!(A.user()&&API.policy&&API.policy.user_repositories);}
  function open(){if(!M.manage(canRegister()))return;loadSources();$('#drawer').classList.remove('hidden');$('#drawer aside').setAttribute('role','dialog');$('#drawer aside').setAttribute('aria-modal','true');setTimeout(()=>$('#f-name').focus(),30);}
  function close(){if(submitting)return;$('#drawer').classList.add('hidden');const q=new URLSearchParams(location.search);q.delete('new');history.replaceState(null,'',location.pathname+(q.size?'?'+q:''));$('#add').focus();}
  $('#add').onclick=()=>{const q=new URLSearchParams(location.search);q.set('new','1');history.replaceState(null,'',location.pathname+'?'+q);open();};
  document.querySelectorAll('[data-close]').forEach(b=>b.onclick=e=>{e.preventDefault();close();});
  document.addEventListener('keydown',e=>{if(e.key==='Escape')close();if(e.key==='Tab'&&!$('#drawer').classList.contains('hidden')){const ns=[...$('#drawer').querySelectorAll('button,input,select')].filter(n=>!n.disabled);const a=ns[0],z=ns[ns.length-1];if(e.shiftKey&&document.activeElement===a){e.preventDefault();z.focus();}else if(!e.shiftKey&&document.activeElement===z){e.preventDefault();a.focus();}}});
  $('#f-name').addEventListener('input',()=>{$('#form-error').textContent='';});
  $('#f-sync-interval').addEventListener('change',()=>{$('#form-error').textContent='';});
  // A name registered by someone else may be hidden; point to its registrant instead.
  function taken(c){const who=c&&c.name?c.name+(c.email?' ('+c.email+')':''):'';return who?t('该仓库已由 '+who+' 登记。如需管理，请联系对方将你加入仓库管理员','This repository is already registered by '+who+'. To manage it, ask them to add you as a repository manager'):t('该仓库已登记。如需管理，请联系站点管理员将你加入仓库管理员','This repository is already registered. To manage it, ask a site administrator to add you as a repository manager');}
  // Registration checks that the repository can be fetched with the caller's tokens before
  // storing anything; the form stays locked meanwhile and keeps its content when it fails.
  const ACCESS={
    GIT_CREDENTIAL_UNAVAILABLE:['这个代码源还没有可用的访问令牌，无法拉取该仓库。请在用户设置里绑定你的个人访问令牌。','This code source has no usable access token, so the repository cannot be fetched. Bind your personal access token in user settings.'],
    GIT_AUTH_FAILED:['代码服务拒绝了当前令牌，令牌可能已过期或被撤销。请在用户设置里更新你的个人访问令牌。','The code host rejected the token; it may have expired or been revoked. Update your personal access token in user settings.'],
    GIT_ACCESS_DENIED:['当前可用的令牌没有读取该仓库的权限。请在用户设置里绑定能读取它的个人访问令牌。','The available tokens cannot read this repository. Bind a personal access token that can read it in user settings.'],
    GIT_REPOSITORY_UNAVAILABLE:['找不到该仓库，或当前令牌看不到它。请先核对仓库名；仓库存在时，在用户设置里绑定能读取它的个人访问令牌。','Repository not found, or the token cannot see it. Check the name first; if it exists, bind a personal access token that can read it in user settings.'],
    GIT_FETCH_FAILED:['暂时连不上代码服务，请稍后重新检查。','The code host cannot be reached right now; check again shortly.'],
    ACCESS_CHECK_TIMEOUT:['代码服务没有及时响应，请稍后重新检查。','The code host did not answer in time; check again shortly.']};
  let accessCode='';
  function formStatus(text){$('#form-status').textContent=text;}
  function lock(on,label){submitting=on;$('#form').inert=on;$('#form').setAttribute('aria-busy',String(on));document.querySelectorAll('#drawer [data-close]').forEach(b=>b.disabled=on);$('#submit').disabled=on;$('#submit').textContent=on?label:t('登记并准备','Register and prepare');}
  function showAccess(code){
    accessCode=code;$('#access-help').classList.toggle('hidden',!code);if(!code)return;
    const token=!['GIT_FETCH_FAILED','ACCESS_CHECK_TIMEOUT'].includes(code);
    $('#access-help-text').textContent=t(ACCESS[code][0],ACCESS[code][1])+(token?t('表单内容已保留，绑定后回到这里点「重新检查」。',' The form is kept; come back and choose Check again after binding.'):'');
    $('#access-settings').classList.toggle('hidden',!token);
  }
  $('#access-settings').onclick=()=>{window.open('/sourcegraph/user-settings#code-source-tokens','_blank','noopener');$('#access-retry').focus();};
  $('#access-retry').onclick=()=>submit();
  async function submit(e){if(e)e.preventDefault();if(submitting||!M.manage(canRegister())||!$('#form').reportValidity())return;let body;
    try {const name=M.repositoryName($('#f-name').value);if(rows.some(r=>r.name===name))throw new Error(t('该仓库已登记，请到详情页修改','This repository is already registered. Edit it from its details page'));body={name,git_timeout_minutes:M.gitTimeout($('#f-git-timeout').value),code_provider:$('#f-code-provider').value,index_mode:$('#f-index-mode').value,public_read:$('#f-ack').checked,policy:{branches:branchInput.value(),...(branchInput.usesDefault()?{default_branch:true}:{})},sync_interval_minutes:M.syncInterval($('#f-sync-interval').value)};if(body.index_mode==='monorepo')body.path_groups=pathInput.value();}
    catch(e){$('#form-error').textContent=e.message;return;}
    $('#form-error').textContent='';showAccess('');lock(true,t('检查中','Checking'));formStatus(t('正在检查能否拉取该仓库，通过后登记…','Checking that the repository can be fetched, then registering…'));
    try {const r=await API.request('/api/admin/v1/repos',{method:'POST',body,headers:{'Idempotency-Key':keys.get(body)}});keys.done(body);A.toast(t('已登记，首轮同步已排队','Registered, first sync queued'));location.assign('/sourcegraph/repo?repo='+encodeURIComponent(r.name)+'&tab=jobs');}
    catch(e){formStatus('');if(ACCESS[e.code])showAccess(e.code);else $('#form-error').textContent=e.code==='REPOSITORY_EXISTS'?taken(e.creator):M.error(e);}
    finally{lock(false);}
  }
  $('#submit').onclick=submit;$('#form').onsubmit=submit;
  function filter(){managerChoice=$('#manager-filter-wrap').classList.contains('hidden')?managerChoice:$('#manager-filter').value;sortChoice=$('#sort').value;const p=new URLSearchParams();if($('#filter').value)p.set('q',$('#filter').value);if(managerChoice)p.set('manager',managerChoice);if(sortChoice)p.set('sort',sortChoice);if($('#show-deleted').checked)p.set('deleted','1');history.replaceState(null,'',location.pathname+(p.size?'?'+p:'')+location.hash);render();}
  $('#filter').oninput=filter;$('#show-deleted').onchange=filter;$('#manager-filter').onchange=filter;$('#sort').onchange=filter;
  document.addEventListener('langchange',()=>{render();if(accessCode)showAccess(accessCode);if(!submitting)$('#submit').textContent=t('登记并准备','Register and prepare');renderSyncInterval();branchInput.refresh();pathInput.refresh();});
  document.addEventListener('app:auth',render);
  const poller=M.poll(load,10000,60);
  App.ready.then(()=>{render();poller.refresh();if(initial.get('new')==='1'||location.hash==='#new')open();});
})();
