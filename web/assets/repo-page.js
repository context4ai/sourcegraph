(function () {
  'use strict';
  const A=App,M=Management,R=A.Repo,t=A.t,esc=A.esc,$=s=>document.querySelector(s),p=new URLSearchParams(location.search);
  let routeName='';try{if(location.pathname.startsWith('/sourcegraph/repos/'))routeName=decodeURIComponent(location.pathname.slice('/sourcegraph/repos/'.length));}catch(_){}
  const name=p.get('repo')||routeName,enc=encodeURIComponent(name||''),endpoint='/api/admin/v1/repo?repo='+enc;
  if(name&&!p.has('repo'))p.set('repo',name);
  const TABS=[['overview','概览','Overview'],['versions','版本','Versions'],['jobs','任务','Jobs'],['plugins','插件','Plugins'],['policy','设置','Settings']];
  const JOB_PAGE=20;
  let tab=TABS.some(x=>x[0]===p.get('tab'))?p.get('tab'):'overview',repo=null,draft=null,preview=null,busy=false,loading=false,versionLimit=20,jobLimit=JOB_PAGE;
  let branchInput=null,pathInput=null;
  const keys=M.operationKeys();
  const maintenance=RepoMaintenance.mount({repository:()=>repo,busy:value=>{busy=value;},isBusy:()=>busy,reload:()=>load(true),jobs:()=>{tab='jobs';p.set('tab',tab);history.replaceState(null,'',location.pathname+'?'+p);}});
  const btn2='rounded-control bg-ink/[0.05] px-3 py-1.5 text-[13.5px] hover:bg-ink/[0.08]';
  const act='shrink-0 inline-flex items-center gap-1 rounded-control px-2 py-1 text-[13px] text-muted hover:bg-ink/[0.05] hover:text-ink disabled:opacity-40';
  const SEP=' <span class="px-1 text-muted/50">·</span> ';
  const KIND={prepare:['更新索引','Prepare'],sync:['同步','Sync'],index:['建索引','Index']};
  const mono=s=>'<span class="font-mono text-[13.5px]">'+s+'</span>';
  const codeHref=sha=>'/sourcegraph/code?repo='+enc+'&commit='+encodeURIComponent(sha);

  function setTab(k){tab=k;p.set('tab',k);history.replaceState(null,'',location.pathname+'?'+p);render();}
  function keep(v){return v.days?[v.days,t('天','days')]:[v.count,t('个提交','commits')];}
  function canPrepare(){return !!(API.policy.public_prepare||A.canManage());}
  function prepareLabel(label){if(canPrepare())return label;const why=A.user()?t('需管理权限','management permission required'):t('需登录','sign-in required');return A.ICON.lock+'<span>'+label+'</span><span class="sr-only"> · '+why+'</span>';}
  function prepareTitle(){return canPrepare()?'':A.user()?t('需管理权限','Management permission required'):t('登录后可用','Available after sign-in');}
  function ago(x){
    if(!A.validTime(x))return '-';
    const m=Math.max(0,Math.round((Date.now()-Date.parse(x))/60000));
    if(m<60)return t(m+' 分钟前',m+'m ago');const h=Math.round(m/60);if(h<48)return t(h+' 小时前',h+'h ago');const d=Math.round(h/24);return t(d+' 天前',d+'d ago');
  }
  function span(ms){if(!Number.isFinite(ms)||ms<0)return '';const s=Math.round(ms/1000);if(s<60)return s+'s';const m=Math.round(s/60);if(m<60)return m+'m';return Math.floor(m/60)+'h'+(m%60?m%60+'m':'');}
  function kindName(k){const x=KIND[k]||[k,k];return t(x[0],x[1]);}
  function jobRecoveredAt(j){return R.recoveredAt(repo,j);}
  function jobText(j){
    if(!j)return '<span class="text-muted">'+t('暂无任务','No jobs yet')+'</span>';
    const k=KIND[j.kind]||[j.kind,j.kind], at=mono(esc(A.clock(j.updated_at||j.created_at)));
    if(jobRecoveredAt(j))return '<span class="text-muted">'+t('后续检查或任务已恢复 · ','Recovered by a later check or task · ')+esc(A.clock(jobRecoveredAt(j)))+'</span>';
    if(j.state==='failed')return '<span class="text-danger">'+t(k[0]+'失败',k[1]+' failed')+(j.error?' · '+esc(API.jobError(j)):'')+'</span>';
    if(j.state==='running')return t('正在'+k[0],k[1]+' running');
    if(j.state==='queued')return t(k[0]+'排队中',k[1]+' queued');
    if(j.state==='canceled'||j.state==='cancelled')return t('已取消','Canceled')+' · '+at;
    return t(k[0]+'完成',k[1]+' done')+' · '+at;
  }
  function deckTail(st){
    const v=R.servable(repo);
    if(st==='ready')return mono(A.short(v&&v.commit))+SEP+t(A.clock(repo.observed_at)+' 更新','updated '+A.clock(repo.observed_at));
    if(st==='stale')return t('搜索使用 ','Search uses ')+mono(A.short(v&&v.commit));
    if(st==='preparing')return t('完成后即可搜索','Search opens when it finishes');
    if(st==='failed')return t('准备失败，暂时不能搜索','Preparation failed; not searchable yet');
    if(st==='pending')return t('检查更新后开始准备','Check for updates to start preparing');
    if(st==='missing')return t('本地数据缺失，检查更新后恢复','Local data is missing; check for updates to restore it');
    if(st==='deleted')return t('不再提供查询','No longer serving searches');
    return t('不接受新的搜索','Not accepting searches');
  }
  function header(){
    const st=R.state(repo), h=R.head(repo), branches=repo.policy.branches||[];
    $('#title').textContent=repo.name;
    $('#deck').innerHTML=A.status(st).replace('inline-flex items-center gap-1.5 whitespace-nowrap text-[13px]','whitespace-nowrap text-ink').replace('class="dot ','class="dot mr-1.5 align-middle ')+
      (branches.length?SEP+mono(esc(branches.length>1?branches[0]+' +'+(branches.length-1):branches[0]||(h&&h.branch)||'')):'')+SEP+deckTail(st);
    $('#search').href='/sourcegraph/?repo='+enc;$('#search').classList.toggle('hidden',!R.servable(repo)||!repo.enabled||repo.deleted);
    $('#sync').disabled=busy||!repo.enabled||repo.deleted;$('#sync').className='inline-flex items-center gap-1.5 '+btn2+' disabled:opacity-40';$('#sync').innerHTML=prepareLabel(t('检查更新','Check for updates'));$('#sync').title=prepareTitle();
    const u=A.user();
    $('#manage').innerHTML=(u?'':A.ICON.lock)+t('管理','Manage')+A.ICON.chevron;
    const items=repo.deleted?[['purge','重试永久删除','Retry permanent deletion']]:[['policy','修改设置','Edit settings'],['index','新增索引','Add index'],['cleanup','清理落后索引','Clean old indexes'],['toggle',repo.enabled?'停用仓库':'启用仓库',repo.enabled?'Disable':'Enable'],['delete','删除仓库','Delete']];
    $('#manage-menu').innerHTML=(u?'':'<div class="px-2 pb-1.5 pt-1 text-[12.5px] text-muted">'+t('登录后可用','Available after sign-in')+'</div>')+items.map(x=>'<button data-act="'+x[0]+'" class="block w-full rounded-control px-2 py-1.5 text-left hover:bg-sand '+(['delete','purge'].includes(x[0])?'text-danger':'')+'">'+t(x[1],x[2])+'</button>').join('');
    $('#manage-menu').querySelectorAll('[data-act]').forEach(b=>b.onclick=()=>{b.closest('details').open=false;if(!M.manage())return;if(b.dataset.act==='policy')setTab('policy');else if(['index','cleanup','delete','purge'].includes(b.dataset.act))maintenance.open(b.dataset.act);else confirm(b.dataset.act);});
    $('#tabs').innerHTML=TABS.map(x=>'<button data-tab="'+x[0]+'" class="pb-1 '+(tab===x[0]?'text-ink shadow-[inset_0_-1.5px_0_currentColor]':'text-muted hover:text-ink')+'">'+t(x[1],x[2])+'</button>').join('');
    $('#tabs').querySelectorAll('[data-tab]').forEach(b=>b.onclick=()=>setTab(b.dataset.tab));
  }

  function heading(title,sum){return '<div class="flex flex-wrap items-baseline gap-x-4 gap-y-1"><h2 class="text-[16px] font-medium">'+title+'</h2>'+(sum?'<span class="text-[14px] text-muted">'+sum+'</span>':'')+'</div>';}
  // One card, one hairline per row; the aside wraps under the value on narrow screens.
  function rows(list){return '<dl class="field px-5 py-1 text-[14px]">'+list.map((r,i)=>'<div class="flex flex-wrap items-center gap-x-6 gap-y-1 py-3.5'+(i?' border-t rule':'')+'"><dt class="w-20 shrink-0 break-all text-muted">'+r[0]+'</dt><dd class="min-w-0 flex-1">'+r[1]+'</dd>'+(r[2]?'<dd class="flex items-center">'+r[2]+'</dd>':'')+'</div>').join('')+'</dl>';}
  function figure(value,unit,label){return '<div><div class="font-display text-[32px] font-medium leading-none tracking-tight">'+value+(unit?'<span class="ml-1 text-[16px] text-muted">'+unit+'</span>':'')+'</div><div class="type-fig mt-2">'+label+'</div></div>';}
  const sub=s=>'<span class="block text-[13px] text-muted sm:ml-3 sm:inline">'+s+'</span>';
  function activeFor(commit,kind){return R.jobs(repo).find(j=>M.active(j)&&(!kind||j.kind===kind)&&(!commit||j.commit===commit));}
  function prepareButton(commit,label,group=''){return '<button data-group="'+esc(group)+'" data-prepare="'+esc(commit)+'" class="'+act+'" title="'+esc(prepareTitle())+'" '+(busy?'disabled':'')+'>'+prepareLabel(label)+'</button>';}

  function scopeCards(){
    if(repo.index_mode!=='monorepo')return '';
    return '<div class="mt-12">'+heading(t('监听范围','Watched scopes'),M.indexModeLabel(repo.index_mode))+'<div class="mt-4 grid gap-3 md:grid-cols-2">'+(repo.path_groups||[]).map(group=>{
      const scope=repo.scopes?.[group.name]||{}, jobs=R.jobs(repo).filter(j=>j.group===group.name), ready=R.versions(scope), current=jobs.find(M.active);
      const timing=M.indexTiming(scope);
      return '<article class="field min-w-0 p-4"><div class="flex items-center gap-3"><h3 class="min-w-0 flex-1 break-all font-mono text-[14px]">'+esc(group.name)+'</h3>'+'<button data-group-sync="'+esc(group.name)+'" class="'+act+'" '+(busy||repo.deleted||!repo.enabled?'disabled':'')+'>'+prepareLabel(t('检查更新','Check updates'))+'</button></div>'+
        '<ul class="mt-2 space-y-1 font-mono text-[12.5px] text-muted">'+group.paths.map(path=>'<li class="break-all">'+esc(path)+'</li>').join('')+'</ul>'+
        '<p class="mt-3 text-[13px]">'+(current?jobText(current):t(ready.length+' 个已索引版本',ready.length+' indexed versions'))+'</p>'+
        (A.validTime(scope.last_indexed_at)?'<p class="mt-1 text-[12.5px] text-muted">'+t('最近索引完成：','Last indexed: ')+esc(M.time(scope.last_indexed_at))+'</p>':'')+
        (timing?'<p class="mt-1 text-[12.5px] text-muted">'+esc(timing)+'</p>':'')+'</article>';
    }).join('')+'</div></div>';
  }
  function overview(){
    const vs=R.versions(repo), v=R.servable(repo), k=keep(repo.policy), jobs=R.jobs(repo), live=repo.enabled&&!repo.deleted;
    const heads=[...new Set([...(repo.policy.branches||[]),...(repo.policy.default_branch&&repo.default_branch?[repo.default_branch]:[])])].map(b=>[b,(repo.heads||{})[b]]);
    const headRows=(heads.length?heads:[['','']]).map(([b,c])=>{
      const ok=c&&vs.some(x=>x.commit===c);
      const aside=!c||!live?'':ok?'<span class="text-[13px] text-muted">'+t('已可查询','Searchable')+'</span>':activeFor(c)||activeFor('', 'sync')?'<span class="text-[13px] text-muted">'+t('正在准备','Preparing')+'</span>':prepareButton(c,jobs.some(j=>j.commit===c&&j.state==='failed')?t('重新准备','Prepare again'):t('准备','Prepare'));
      return [heads.length>1?mono(esc(b)):t('最新提交','Latest'),c?mono(A.short(c))+sub(t(A.clock(repo.observed_at)+' 发现','seen '+A.clock(repo.observed_at))):'<span class="text-muted">'+t('等待首次同步','Awaiting first sync')+'</span>',aside];
    });
    return (repo.deleted?'<p class="mb-10 flex items-center gap-2 text-[14px]"><span class="dot text-muted/60"></span>'+t('已标记删除，不再提供查询。在「管理」里重试永久删除，完成剩余数据清理','Marked for deletion and no longer serving. Retry permanent deletion from Manage to finish clearing the remaining data')+'</p>':'')+
      '<div class="grid grid-cols-2 gap-8 lg:grid-cols-4">'+
        figure(String(vs.length),'',t('可查版本','versions'))+
        figure(v&&Number.isFinite(v.files)?v.files.toLocaleString():'-','',t('文件','files'))+
        figure(String((repo.policy.branches||[]).length+Number(!!repo.policy.default_branch)),'',t('监听分支','branches'))+
        figure(k[0],k[1],t('保留','retention'))+
      '</div>'+scopeCards()+
      '<div class="mt-14">'+heading(t('当前版本','Current version'))+'<div class="mt-4">'+rows(headRows.concat([
        [t('可查询','Searchable'),v?mono(A.short(v.commit))+sub(t('提交于 '+A.clock(v.committer_time),'committed '+A.clock(v.committer_time))):'<span class="text-muted">'+t('准备完成后可查询','Available once prepared')+'</span>',v?'<a class="'+act+'" href="'+codeHref(v.commit)+'">'+t('阅读代码','Browse code')+'</a>':''],
        ...(repo.index_mode!=='monorepo'&&M.indexTiming(repo)?[[t('最近索引','Last indexing'),esc(M.indexTiming(repo))+sub(A.validTime(repo.last_indexed_at)?esc(M.time(repo.last_indexed_at)):'')]]:[]),
        [t('最近任务','Last job'),'<span class="text-[13.5px]">'+jobText(jobs[0])+'</span>',jobs.length?'<button data-goto="jobs" class="'+act+'">'+t('查看','View')+'</button>':'']
      ]))+'</div></div>';
  }

  function versions(){
    const all=(repo.versions||[]).slice().sort((a,b)=>Date.parse(b.committer_time)-Date.parse(a.committer_time)), heads=new Set(Object.values(repo.heads||{})), live=repo.enabled&&!repo.deleted, jobs=R.jobs(repo);
    const list=all.slice(0,versionLimit).map(v=>{
      const running=jobs.find(j=>j.kind==='index'&&j.commit===v.commit&&M.active(j)), failed=!running&&!R.indexed(v)&&jobs.find(j=>j.commit===v.commit&&j.state==='failed');
      const state=running?A.status(running.state):R.indexed(v)?A.status('ready'):failed?A.status('failed'):'<span class="text-[13px] text-muted">'+t('未准备','Not prepared')+'</span>';
      const action=R.indexed(v)?'<a class="'+act+'" href="'+codeHref(v.commit)+'">'+t('阅读','Browse')+'</a>':running||!live?'<span class="w-10"></span>':prepareButton(v.commit,t('准备','Prepare'));
      return '<div class="row-hover flex items-center gap-x-3 rounded-control px-3 py-2.5 text-[14px] sm:gap-x-6">'+
        '<span class="flex w-[7.5rem] shrink-0 items-center gap-2 sm:w-44"><button class="copy-sha font-mono text-[13px]" data-sha="'+esc(v.commit)+'" title="'+esc(v.commit)+'">'+esc(A.short(v.commit))+'</button>'+(heads.has(v.commit)?'<span class="rounded-[5px] bg-ink/[0.06] px-1.5 text-[11.5px] text-muted">'+t('最新','latest')+'</span>':'')+'</span>'+
        '<span class="w-14 shrink-0 text-[13px] text-muted sm:w-20">'+esc(ago(v.committer_time))+'</span>'+
        '<span class="min-w-0 flex-1">'+state+'</span>'+action+'</div>';
    });
    const n=all.length, pol=repo.policy;
    const sum=pol.days?t('最近 '+pol.days+' 天的提交，共 '+n+' 个',n+' commits from the last '+pol.days+' days'):t('最近 '+pol.count+' 个提交，共 '+n+' 个','The latest '+pol.count+' commits, '+n+' recorded');
    return '<div><p class="text-[14px] text-muted">'+sum+'</p>'+
      (n?'<div class="-mx-3 mt-4">'+list.join('')+'</div>':'<p class="mt-4 text-[14px] text-muted">'+t('还没有版本，检查更新后会出现在这里','No versions yet. They appear here after an update check')+'</p>')+
      (n>versionLimit?'<button id="more-versions" class="mt-3 text-[13px] text-muted hover:text-ink">'+t('仅显示最近 '+versionLimit+' 个 · 再显示一些','Showing the latest '+versionLimit+' · show more')+'</button>':'')+'</div>';
  }

  // Steps: a numbered rail. Done is ink, the current stage is live or danger, the rest is muted.
  function jobs(){
    const js=R.jobs(repo);
    const checks=Object.entries(repo.sync_checks||{}).map(([group,c])=>'<p class="text-[13px] text-muted">'+(group?esc(group)+' · ':'')+t('最近检查 ','Last checked ')+esc(A.clock(c.checked_at))+' · '+(c.error?esc(API.jobError({error:c.error})):c.no_changes?t('版本未变化，无需索引','No version changes; indexing skipped'):t('检查完成','Check completed'))+(c.unchanged_count>1?t(' · 连续 '+c.unchanged_count+' 次无变化',' · '+c.unchanged_count+' consecutive unchanged checks'):'')+'</p>').join('');
    const summary=checks?'<div class="mb-8 space-y-2">'+checks+'</div>':'';
    if(!js.length)return summary+ '<p class="text-[14px] text-muted">'+t('还没有任务','No jobs yet')+'</p>';
    const live=repo.enabled&&!repo.deleted;
    const rest=js.length-jobLimit;
    return summary+'<div class="space-y-12">'+js.slice(0,jobLimit).map(j=>{
      const stages=[['queued',t('排队','queue')],['starting',t('开始','start')],['preparing',kindName(j.kind)],['complete',t('完成','done')]];
      const stage=j.stage==='recovery'?'starting':j.stage==='indexing'?'preparing':j.stage, at=Math.max(0,stages.findIndex(s=>s[0]===stage)), ok=j.state==='succeeded';
      const rail=stages.map((s,i)=>{
        const done=ok||i<at, cur=i===at&&!ok, bad=j.state==='failed'||j.state==='canceled'||j.state==='cancelled';
        const color=done?'text-ink':cur?(bad?'text-danger':'text-live'):'text-muted/50', bar=done?'bg-ink/70':cur?(bad?'bg-danger':'bg-live'):'bg-ink/10';
        return '<div class="flex-1"><div class="h-[2px] rounded-full '+bar+(cur&&j.state==='running'?' dot-live':'')+'"></div><div class="mt-2 flex items-baseline gap-2 '+color+'"><span class="font-mono text-[11px]">0'+(i+1)+'</span><span class="text-[13.5px]">'+s[1]+'</span></div></div>';
      }).join('');
      const end=j.state==='queued'||j.state==='running'?Date.now():Date.parse(j.updated_at), took=span(end-Date.parse(j.created_at));
      const phase=j.state==='queued'?t('已等待 ','waiting '):j.state==='running'?t('已运行 ','running '):t('用时 ','took ');
      const when=t('创建 '+A.clock(j.created_at),'created '+A.clock(j.created_at))+(took?' · '+phase+took:'')+(j.attempt>1?t(' · 第 '+j.attempt+' 次尝试',' · attempt '+j.attempt):'');
      const retry=(j.state==='failed'||j.state==='canceled'||j.state==='cancelled')&&live?' <button class="retry ml-2 inline-flex items-center gap-1 underline underline-offset-2 disabled:opacity-40" data-kind="'+esc(j.kind)+'" data-group="'+esc(j.group||'')+'" data-commit="'+esc(j.commit||'')+'" title="'+esc(prepareTitle())+'" '+(busy?'disabled':'')+'>'+prepareLabel(t('重新准备','Prepare again'))+'</button>':'';
      return '<article'+(jobRecoveredAt(j)?' class="opacity-60"':'')+'><div class="flex flex-wrap items-baseline gap-3"><span class="font-mono text-[13px]">'+esc(j.kind==='sync'?t('检查更新','Check updates'):kindName(j.kind))+'</span>'+A.status(j.state)+(j.group?'<span class="font-mono text-[12.5px] text-muted">'+esc(j.group)+'</span>':'')+(j.commit?'<span class="font-mono text-[12.5px] text-muted">'+esc(A.short(j.commit))+'</span>':'')+'<span class="type-fig">'+esc(j.job_id)+'</span><span class="type-fig ml-auto">'+(A.canManage()&&M.active(j)?'<button data-stop-job="'+esc(j.job_id)+'" class="mr-3 underline underline-offset-2 hover:text-ink disabled:opacity-40" '+(busy?'disabled':'')+'>'+t('停止','Stop')+'</button>':'')+when+'</span></div>'+
        '<div class="mt-4 flex gap-2">'+rail+'</div>'+(j.no_changes&&j.state==='succeeded'?'<p class="mt-3 text-[13px] text-muted">'+t('版本未变化，无需索引','No version changes; indexing skipped')+'</p>':'')+(j.kind!=='sync'&&M.indexTiming(j)?'<p class="mt-3 text-[13px] text-muted">'+esc(M.indexTiming(j))+'</p>':'')+
        (jobRecoveredAt(j)?'<p class="mt-3 text-[13px] text-muted">'+t('后续检查或任务已恢复 · ','Recovered by a later check or task · ')+esc(A.clock(jobRecoveredAt(j)))+t('；保留原失败记录','; original failure retained')+'</p>':'')+
        (j.error?'<p class="mt-4 break-words text-[14px]"><span class="text-danger">'+esc(API.jobError(j))+'</span>'+retry+'</p>':retry?'<p class="mt-4 text-[14px]">'+retry+'</p>':'')+'</article>';
    }).join('')+'</div>'+
      (rest>0?'<button id="more-jobs" type="button" class="-mx-3 mt-8 rounded-control px-3 py-2 text-[13.5px] text-muted hover:bg-sand hover:text-ink">'+t('加载更多（还有 '+rest+' 个）','Load more ('+rest+' more)')+'</button>':'');
  }

  function ensureDraft(){if(!draft)draft={branches:repo.policy.branches.slice(),defaultBranch:!!repo.policy.default_branch,publicRead:repo.public_read!==false,pending:'',kind:repo.policy.days?'days':'count',value:String(repo.policy.days||repo.policy.count),syncInterval:String(repo.sync_interval_minutes||M.DEFAULT_SYNC_INTERVAL_MINUTES),skipGenerated:!repo.index_generated,indexSymlinks:repo.index_symlinks!==false,pathState:{groups:repo.path_groups||[]}};}
  function settings(){
    if(repo.deleted)return '<p class="text-[14px] text-muted">'+t('仓库已标记删除，无法修改设置。请在「管理」里重试永久删除','This repository is marked for deletion and cannot be edited. Retry permanent deletion from Manage')+'</p>';
    const can=A.user()&&A.canManage(), k=keep(repo.policy);
    if(!can)return '<div>'+rows([[t('索引模式','Indexing mode'),M.indexModeLabel(repo.index_mode)],[t('监听分支','Branches'),mono(esc((repo.policy.branches||[]).join(', ')))],[t('保留','Retention'),t('最近 '+k[0]+' '+k[1],'Last '+k[0]+' '+k[1])],[t('同步间隔','Sync interval'),M.syncIntervalLabel(repo.sync_interval_minutes||M.DEFAULT_SYNC_INTERVAL_MINUTES)],[t('软链目标','Symlink targets'),repo.index_symlinks!==false?t('新索引展开仓库内软链','New indexes follow repository symlinks'):t('新索引不展开软链','New indexes do not follow symlinks')],[t('压缩文件','Minified files'),repo.index_generated?t('压缩 JS/CSS 与 source map 建立索引','Minified JS/CSS and source maps are indexed'):t('压缩 JS/CSS 与 source map 不建立索引','Minified JS/CSS and source maps are not indexed')]])+
      '<button id="policy-login" class="mt-5 inline-flex items-center gap-1.5 '+btn2+'">'+A.ICON.lock+t(A.user()?'当前账号不能修改':'登录后修改',A.user()?'Your account cannot edit':'Sign in to edit')+'</button></div>';
    ensureDraft();
    const inp='mt-2 rounded-control bg-ink/[0.04] px-3 py-2 font-mono text-[13.5px] outline-none ring-1 ring-transparent focus:ring-ink/20';
    return '<div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_280px]">'+
      '<form id="policy-form" class="field space-y-6 p-5 text-[14px]">'+
        '<div><span class="text-muted">'+t('索引模式','Indexing mode')+'</span><p class="mt-2">'+M.indexModeLabel(repo.index_mode)+'</p></div>'+
        (repo.index_mode==='monorepo'?'<div><label for="ppaths">'+t('监听路径与分组','Watched paths and groups')+'</label><input id="ppaths"></div>':'')+
        '<div><label for="pbranches">'+t('监听分支，最多8个','Watched branches, up to 8')+'</label><input id="pbranches"></div>'+
        '<div class="flex flex-col gap-6 sm:flex-row sm:items-start sm:gap-10"><label class="block"><span>'+t('保留','Retention')+'</span><span class="ml-2 text-[13px] text-muted">'+t('超出范围的版本不再可查','Older versions stop being searchable')+'</span><div class="flex items-center gap-2"><input id="pvalue" required type="number" min="1" max="'+(draft.kind==='days'?365:4096)+'" class="'+inp+' w-24" value="'+esc(draft.value)+'"><select id="pkind" class="mt-2 rounded-control bg-transparent py-2 text-muted hover:text-ink"><option value="days" '+(draft.kind==='days'?'selected':'')+'>'+t('天','days')+'</option><option value="count" '+(draft.kind==='count'?'selected':'')+'>'+t('个提交','commits')+'</option></select></div></label>'+
        '<div class="shrink-0"><label for="psync-interval">'+t('同步间隔','Sync interval')+'</label><select id="psync-interval" class="'+inp+' block w-40 max-w-full">'+M.syncIntervalOptions(draft.syncInterval)+'</select></div></div>'+
        '<label class="flex items-center gap-2 text-muted"><input id="ppublic" type="checkbox" '+(draft.publicRead?'checked':'')+' class="accent-current">'+t('是否允许匿名搜到或在网站内查看','Allow anonymous search and viewing on this site')+'</label>'+
        '<label class="flex items-start gap-2 text-muted"><input id="pskip-generated" type="checkbox" '+(draft.skipGenerated?'checked':'')+' class="mt-1 accent-current"><span>'+t('不为压缩的 JS/CSS 和 source map 建立索引','Leave minified JS/CSS and source maps out of the index')+'<span class="mt-0.5 block text-[13px]">'+t('平均行长超过 110 字节的 .js/.css 与 .map 文件不可搜索，仍可直接查看；lock 文件不受影响。下次建立索引时生效','.js/.css files averaging over 110 bytes per line and .map files are not searchable but can still be viewed; lock files are unaffected. Applies from the next index build')+'</span></span></label>'+
        '<label class="flex items-start gap-2 text-muted"><input id="pindex-symlinks" type="checkbox" '+(draft.indexSymlinks?'checked':'')+' class="mt-1 accent-current"><span>'+t('索引仓库内软链目标','Index repository symlink targets')+'<span class="mt-0.5 block text-[13px]">'+t('下次建立索引时生效；现有索引不重建。只跟随同一提交内的仓库路径','Applies to future builds; existing indexes stay unchanged. Follows repository paths at the same commit only')+'</span></span></label>'+
        '<p id="policy-error" role="alert" class="text-[13px] text-danger empty:hidden"></p>'+
        '<div class="flex gap-2"><button id="preview" type="button" class="'+btn2+' disabled:opacity-40">'+t('预览影响','Preview impact')+'</button><button id="save" type="submit" disabled class="rounded-control bg-ink px-3 py-1.5 text-[13.5px] font-medium text-paper disabled:opacity-40">'+t('保存','Save')+'</button></div>'+
      '</form>'+
      '<div id="impact" class="rounded-surface bg-ink/[0.03] p-5 text-[13.5px] text-muted">'+t('保存前先预览影响','Preview the impact before saving')+'</div>'+
    '</div>';
  }
  function capture(){if(!$('#pbranches')||!branchInput)return;draft={...branchInput.snapshot(),publicRead:$('#ppublic').checked,kind:$('#pkind').value,value:$('#pvalue').value,syncInterval:$('#psync-interval').value,skipGenerated:$('#pskip-generated').checked,indexSymlinks:$('#pindex-symlinks').checked,pathState:pathInput?pathInput.snapshot():draft?.pathState};}
  function target(){const branches=branchInput.value(),groups=pathInput?.value();capture();return {policy:M.policy(branches,draft.kind,draft.value,branchInput.usesDefault()),public_read:draft.publicRead,sync_interval_minutes:M.syncInterval(draft.syncInterval),index_generated:!draft.skipGenerated,index_symlinks:draft.indexSymlinks,enabled:repo.enabled,deleted:repo.deleted,...(groups?{path_groups:groups}:{})};}
  function invalidate(){capture();preview=null;if($('#save'))$('#save').disabled=true;if($('#impact'))$('#impact').textContent=t('保存前先预览影响','Preview the impact before saving');if($('#policy-error'))$('#policy-error').textContent='';}
  function impact(v,body){
    const cur=repo.policy,next=body.policy,n=v.retained_versions;
    const samePolicy=!!cur.default_branch===!!next.default_branch&&JSON.stringify(cur.branches)===JSON.stringify(next.branches)&&(cur.days||0)===(next.days||0)&&(cur.count||0)===(next.count||0);
    const pathsChanged=repo.index_mode==='monorepo'&&JSON.stringify(repo.path_groups||[])!==JSON.stringify(body.path_groups||[]);
    const intervalChanged=(repo.sync_interval_minutes||M.DEFAULT_SYNC_INTERVAL_MINUTES)!==body.sync_interval_minutes;
    const shrink=cur.days&&next.days?next.days<cur.days:cur.count&&next.count?next.count<cur.count:!samePolicy;
    const lines=[t('现有 '+n+' 个版本',n+' versions now')];
    const visibilityChanged=(repo.public_read!==false)!==body.public_read;
    if(visibilityChanged)lines.push(t(body.public_read?'允许匿名搜索和查看':'仅登录用户可以搜索和查看',body.public_read?'Allow anonymous search and viewing':'Require sign-in to search and view'));
    const symlinksChanged=(repo.index_symlinks!==false)!==body.index_symlinks;
    if(symlinksChanged)lines.push(t(body.index_symlinks?'新索引将包含仓库内软链目标':'新索引不再展开软链目标',body.index_symlinks?'New indexes will include repository symlink targets':'New indexes will not expand symlink targets')+t('；现有版本保持不变','; existing versions stay unchanged'));
    const generatedChanged=!!repo.index_generated!==body.index_generated;
    if(generatedChanged)lines.push(t(body.index_generated?'压缩文件与 source map 将建立索引':'压缩文件与 source map 不再建立索引',body.index_generated?'Minified files and source maps will be indexed':'Minified files and source maps will no longer be indexed')+t('，下次建立索引时生效，现有版本保持可查','; applies from the next index build, existing versions stay searchable'));
    if(samePolicy&&!intervalChanged&&!pathsChanged&&!visibilityChanged&&!generatedChanged&&!symlinksChanged)lines.push(t('没有变化','No change'));
    if(!samePolicy){
      if(!!cur.default_branch!==!!next.default_branch||JSON.stringify(cur.branches)!==JSON.stringify(next.branches))lines.push('<span class="text-ink">'+t('监听分支变化，需要重新同步','Branches change; a new sync is needed')+'</span>');
      lines.push(shrink?'<span class="text-ink">'+t('超出新保留范围的版本不再可查','Versions outside the new window stop being searchable')+'</span>':t('更早的提交在需要时准备','Older commits are prepared when needed'));
      lines.push(t('进行中的准备任务会取消，保存后检查更新','Active preparation is canceled; check for updates after saving'));
    }
    if(pathsChanged)lines.push('<span class="text-ink">'+t('监听路径或分组变化，已有范围需要重新同步和索引；进行中的准备任务会取消','Watched paths or groups change. Scopes must be synchronized and indexed again; active preparation is canceled')+'</span>');
    if(intervalChanged){
      lines.push(t('同步间隔改为 ','Sync interval changes to ')+M.syncIntervalLabel(body.sync_interval_minutes));
      lines.push(t('下次调度生效','Takes effect on the next scheduling cycle'));
      if(samePolicy&&!pathsChanged)lines.push(t('现有版本保持可查，进行中的任务继续运行','Existing versions stay searchable; active jobs continue running'));
    }
    return '<div class="font-medium text-ink">'+t('保存后','After saving')+'</div><ul class="mt-2 space-y-1.5">'+lines.map(x=>'<li>'+x+'</li>').join('')+'</ul>';
  }
  async function previewPolicy(){if(busy||!M.manage())return;let body;try{body=target();}catch(e){$('#policy-error').textContent=e.message;return;}busy=true;$('#preview').disabled=true;$('#policy-error').textContent='';try{const v=await API.request('/api/admin/v1/repo/preview?repo='+enc,{method:'POST',body});if(v.revision!==repo.revision){await load(true);throw Object.assign(new Error(t('仓库刚被修改过，输入已保留，请重新预览','The repository just changed; your input is kept. Preview again')),{code:'REVISION_CONFLICT',status:412});}if(JSON.stringify(target())!==JSON.stringify(body))throw new Error(t('输入已变化，请重新预览','Inputs changed; preview again'));preview={body,value:v};$('#impact').className='rounded-surface bg-ink/[0.03] p-5 text-[13.5px] text-muted';$('#impact').innerHTML=impact(v,body);$('#save').disabled=false;}catch(e){preview=null;$('#policy-error').textContent=M.error(e);}finally{busy=false;$('#preview').disabled=false;}}
  async function savePolicy(e){e.preventDefault();if(busy||!preview||!M.manage())return;capture();let body;try{body=target();}catch(e){$('#policy-error').textContent=e.message;return;}if(JSON.stringify(body)!==JSON.stringify(preview.body)){invalidate();return;}busy=true;$('#save').disabled=true;try{repo=await API.request(endpoint,{method:'PATCH',headers:{'If-Match':'"'+preview.value.revision+'"'},body:{...body,preview:preview.value.preview}});preview=null;draft=null;A.toast(t('设置已保存','Settings saved'));render();}catch(e){preview=null;if(e.status===412){try{await load(true);}catch(_){}}$('#policy-error').textContent=M.error(e)+(e.status===412?t('；输入已保留，请重新预览','; input kept, preview again'):'');}finally{busy=false;if(repo)header();}}
  async function prepare(kind,commit='',group=''){
    if(busy)return;if(!canPrepare()){M.manage();return;}busy=true;render();const body={kind,commit,...(group?{group}:{})};
    try{
      const result=await API.request('/api/admin/v1/repo/prepare?repo='+enc,{method:'POST',body,headers:{'Idempotency-Key':keys.get(body)}}),jobs=M.jobReceipts(result);
      keys.done(body);A.toast(jobs.length>1?t(jobs.length+' 个分组任务已登记',jobs.length+' group jobs registered'):kind==='sync'?t('已开始检查更新','Checking for updates'):t('已开始准备','Preparation started'),jobs.some(j=>j.state==='failed')?'bad':undefined);
      tab='jobs';p.set('tab',tab);history.replaceState(null,'',location.pathname+'?'+p);await load(true);
    }catch(e){$('#load-error').textContent=M.error(e);}finally{busy=false;render();}
  }

  function confirm(kind){
    if(busy||repo.deleted||kind!=='toggle'||!M.manage())return;
    const d=$('#confirm'),enable=!repo.enabled;
    const title=t(enable?'启用仓库':'停用仓库',enable?'Enable repository':'Disable repository');
    const msg=t(enable?'启用后恢复搜索和更新':'停用后不再接受新的搜索和更新，已有数据保留',enable?'Search and updates resume':'Search and updates stop. Existing data is kept');
    d.style.width='min(92vw, 480px)';d.removeAttribute('aria-labelledby');
    d.innerHTML='<form id="confirm-form"><div class="px-6 pt-6"><h2 class="font-display text-[18px] font-medium">'+title+'</h2><p class="mt-2 text-[14px] text-muted">'+msg+'</p><p id="confirm-error" role="alert" class="mt-2 text-[13px] text-danger empty:hidden"></p></div><div class="flex justify-end gap-2 px-6 pb-6 pt-5"><button id="cancel-confirm" type="button" class="rounded-control px-3 py-1.5 text-[13.5px] text-muted hover:bg-sand">'+t('取消','Cancel')+'</button><button id="cok" type="submit" class="rounded-control bg-ink px-3 py-1.5 text-[13.5px] font-medium text-paper disabled:opacity-40">'+t('确认','Confirm')+'</button></div></form>';
    d.showModal();$('#cancel-confirm').onclick=()=>{if(!busy)d.close();};d.oncancel=e=>{if(busy)e.preventDefault();};
    $('#confirm-form').onsubmit=async e=>{
      e.preventDefault();if(busy)return;busy=true;$('#cok').disabled=true;$('#confirm-error').textContent='';
      try{
        const body={policy:repo.policy,sync_interval_minutes:repo.sync_interval_minutes||M.DEFAULT_SYNC_INTERVAL_MINUTES,enabled:enable,deleted:false};
        const v=await API.request('/api/admin/v1/repo/preview?repo='+enc,{method:'POST',body});
        if(v.revision!==repo.revision)throw Object.assign(new Error(t('仓库刚被修改过，请重新确认','The repository just changed; confirm again')),{code:'REVISION_CONFLICT',status:412});
        repo=await API.request(endpoint,{method:'PATCH',headers:{'If-Match':'"'+v.revision+'"'},body:{...body,preview:v.preview}});preview=null;d.close();render();A.toast(t('已更新','Updated'));
      }catch(e){$('#confirm-error').textContent=M.error(e);if(e.status===412){await load(true);$('#confirm-error').textContent=M.error(e)+t('；已刷新，请重新确认','; refreshed, confirm again');}}
      finally{busy=false;$('#cok').disabled=false;if(repo)header();}
    };
  }
  async function stopJob(button){
    if(busy||!M.manage())return;
    busy=true;button.disabled=true;
    try{await API.request('/api/admin/v1/repo/jobs/cancel?repo='+enc,{method:'POST',body:{job_id:button.dataset.stopJob}});A.toast(t('已停止任务','Task stopped'));await load(true);}
    catch(e){A.toast(M.error(e),'bad');}
    finally{busy=false;render();}
  }
  function render(){
    if(!repo)return;header();$('#panel').innerHTML=({overview,versions,jobs,plugins:()=>'<div id="repo-plugins"></div>',policy:settings})[tab]();branchInput=null;pathInput=null;
    if(tab==='plugins')RepoPlugins.mount($('#repo-plugins'),repo);
    document.querySelectorAll('[data-stop-job]').forEach(b=>b.onclick=()=>stopJob(b));
    document.querySelectorAll('[data-prepare]').forEach(b=>b.onclick=()=>prepare('index',b.dataset.prepare,b.dataset.group));
    document.querySelectorAll('.retry').forEach(b=>b.onclick=()=>prepare(b.dataset.kind,b.dataset.commit,b.dataset.group));
    document.querySelectorAll('[data-group-sync]').forEach(b=>b.onclick=()=>prepare('sync','',b.dataset.groupSync));
    document.querySelectorAll('.copy-sha').forEach(b=>b.onclick=()=>A.copy(b.dataset.sha,t('已复制完整 SHA','Full SHA copied')));
    document.querySelectorAll('[data-goto]').forEach(b=>b.onclick=()=>setTab(b.dataset.goto));
    if($('#more-versions'))$('#more-versions').onclick=()=>{versionLimit+=50;render();};
    if($('#more-jobs'))$('#more-jobs').onclick=()=>{jobLimit+=JOB_PAGE;render();};
    if($('#policy-login'))$('#policy-login').onclick=()=>M.manage();
    if($('#preview'))$('#preview').onclick=previewPolicy;
    if($('#policy-form')){branchInput=BranchInput.mount($('#pbranches'),{...draft,onChange:invalidate});if($('#ppaths'))pathInput=PathInput.mount($('#ppaths'),{...draft.pathState,onChange:invalidate});$('#policy-form').onsubmit=savePolicy;['#pvalue','#pkind','#psync-interval','#ppublic','#pskip-generated'].forEach(s=>$(s).oninput=()=>{invalidate();$('#pvalue').max=$('#pkind').value==='days'?365:4096;});}
  }
  async function load(force=false){if(loading||!name)return;if(!force&&(busy||tab==='policy'||$('#confirm').open))return;loading=true;try{repo=await API.repo(name);$('#load-error').textContent='';render();}catch(e){$('#load-error').textContent=M.error(e);throw e;}finally{loading=false;}}
  const poller=M.poll(()=>load(),15000,80);$('#sync').disabled=true;$('#search').classList.add('hidden');$('#sync').onclick=()=>prepare('sync');
  document.addEventListener('app:auth',()=>{if(repo)render();});
  document.addEventListener('langchange',()=>{if(tab==='policy')capture();render();});
  App.ready.then(async()=>{if(!name){$('#load-error').textContent=t('缺少仓库名称，请从仓库列表进入','Repository name missing. Open this page from the repository list');$('#sync').disabled=true;$('#search').classList.add('hidden');return;}try{await load(true);}catch(_){}setTimeout(()=>poller.refresh(),15000);});
})();
