(function () {
  'use strict';
  const A=App,M=Management,R=A.Repo,t=A.t,esc=A.esc,$=s=>document.querySelector(s);
  const QUEUE_PAGE=10;
  let queueShown=QUEUE_PAGE,data=null,minutes=15,repos=null,queueError='',queueBusy=false,controller=null,instances=null;
  const KIND={prepare:['更新索引','Prepare'],sync:['同步','Sync'],index:['建索引','Index']};
  const ORDER={running:0,queued:1,failed:2,succeeded:3,canceled:4,cancelled:4};
  const cell=(label,value)=>'<div><div class="type-fig">'+label+'</div><div class="mt-1.5 text-[14px]">'+value+'</div></div>';
  const percent=x=>Number.isFinite(x)?x.toFixed(1)+'%':'-';
  function span(ms){if(!Number.isFinite(ms)||ms<0)return '-';const s=Math.round(ms/1000);if(s<60)return s+'s';const m=Math.round(s/60);if(m<60)return m+'m';return Math.floor(m/60)+'h'+(m%60?m%60+'m':'');}
  function ago(x){if(!A.validTime(x))return '-';const s=Math.max(0,Math.round((Date.now()-Date.parse(x))/1000));return s<60?t(s+' 秒前',s+'s ago'):s<3600?t(Math.round(s/60)+' 分钟前',Math.round(s/60)+'m ago'):t(Math.round(s/3600)+' 小时前',Math.round(s/3600)+'h ago');}
  function allJobs(){return repos?repos.flatMap(r=>R.currentJobs(r).map(j=>({...j,repo:r.name}))):null;}

  function health(){
    if(data?.credential_warnings?.length)return data.credential_warnings.map(w=>{
      const name=w.kind==='git'?'Git':'GitHub';
      const expired=w.state==='expired'||w.state==='invalid';
      const days=w.expires_at?Math.max(0,Math.ceil((Date.parse(w.expires_at)-Date.now())/86400000)):0;
      const text=expired?t('全局 '+name+' 凭证已经失效，请尽快更新','Global '+name+' credential is invalid or expired; update it soon'):w.state==='unavailable'?t('全局凭证状态暂时无法读取','Global credential status is unavailable'):t('全局 '+name+' 凭证即将于 '+days+' 天后失效，请更新','Global '+name+' credential expires in '+days+' days; update it');
      return '<span class="block '+(expired?'text-danger':'text-amber-600')+'">'+esc(text)+'</span>';
    }).join('');
    if(!repos)return t('服务正常','Service is up');
    const count=s=>repos.filter(r=>R.state(r)===s).length, stale=count('stale'), preparing=count('preparing'), failed=count('failed'), missing=count('missing');
    const parts=[];
    if(stale)parts.push(t(stale+' 个仓库更新失败，仍可搜旧版本',stale+(stale>1?' repositories':' repository')+' failed to update; older versions still searchable'));
    if(failed)parts.push(t(failed+' 个仓库准备失败，暂时不能搜索',failed+(failed>1?' repositories':' repository')+' failed to prepare and cannot be searched yet'));
    if(missing)parts.push(t(missing+' 个仓库需要重新同步',missing+(missing>1?' repositories need':' repository needs')+' a new sync'));
    if(preparing)parts.push(t(preparing+' 个仓库正在首次准备',preparing+(preparing>1?' repositories are':' repository is')+' being prepared for the first time'));
    return (parts.length?t('服务正常：','Service is up: '):t('服务正常，所有仓库都可以搜索','Service is up and every repository is searchable'))+parts.join(t('；','. '))+(parts.length?' <a href="/sourcegraph/repos" class="text-muted underline-offset-2 hover:text-ink hover:underline">'+t('查看仓库','See repositories')+'</a>':'');
  }
  function render(){
    $('#query-errors-link').classList.toggle('hidden',!App.canManage());
    $('#win').innerHTML=[[15,'15 分钟','15 min'],[60,'1 小时','1 hour'],[1440,'24 小时','24 hours']].map(w=>'<button data-w="'+w[0]+'" class="rounded-[6px] px-2.5 py-1 '+(minutes===w[0]?'bg-card text-ink shadow-sm':'text-muted hover:text-ink')+'">'+t(w[1],w[2])+'</button>').join('');
    $('#win').querySelectorAll('[data-w]').forEach(b=>b.onclick=()=>{minutes=+b.dataset.w;render();});
    if(!data){$('#headline').textContent=t('运行情况','Status');$('#health').innerHTML='';renderInstances();renderQueue();return;}
    const d=M.aggregate(data,minutes);
    $('#headline').textContent=t('过去 '+(minutes>=60?(minutes/60)+' 小时':minutes+' 分钟')+'完成了 '+d.requests.toLocaleString()+' 次查询',d.requests.toLocaleString()+' queries completed in the last '+(minutes>=60?(minutes/60)+' hours':minutes+' minutes'));
    $('#health').innerHTML=health();
    $('#updated').textContent=t('更新于 ','updated ')+A.clock(data.sampled_at);
    const jobs=allJobs(), running=jobs?jobs.filter(j=>j.state==='running').length:0, queued=jobs?jobs.filter(j=>j.state==='queued').length:0;
    const p95=d.p95===null?'':(d.p95Overflow?' > ':' ≤ ')+d.p95+' ms';
    const figs=[
      ['f-req',d.requests.toLocaleString(),'',t('请求 · search、read 与 read_many','requests · search, read and read_many')],
      ['f-avg',d.average===null?'-':String(Math.round(d.average)),'ms',t('平均耗时','mean time')+(p95?' · P95'+p95:'')+(d.max!==null?t(' · 最大 ',' · max ')+d.max+' ms':'')],
      ['f-err',d.rate===null?'-':d.rate.toFixed(1),'%',t('错误率','error rate')],
      ['f-job',jobs?String(running+queued):'-','',jobs?t('活动任务 · '+running+' 运行 '+queued+' 排队','active jobs · '+running+' running, '+queued+' queued'):t('活动任务','active jobs')]
    ];
    if(!$('#f-req'))$('#figs').innerHTML=figs.map(f=>'<div><div class="font-display text-[40px] font-medium leading-none tracking-tight"><span id="'+f[0]+'"></span><span id="'+f[0]+'-u" class="ml-1 text-[18px] text-muted"></span></div><div class="type-fig mt-2" id="'+f[0]+'-l"></div></div>').join('');
    figs.forEach(f=>{A.roll($('#'+f[0]),f[1]);$('#'+f[0]+'-u').textContent=f[1]==='-'?'':f[2];$('#'+f[0]+'-l').textContent=f[3];});
    // First and last bars sit flush with the content edges so the figures above line up with them.
    const step=minutes===1440?60:1,count=minutes/step;
    chartBars=Array.from({length:count},(_,i)=>({minute:d.start+i*step*60,requests:0,errors:0,total:0,average:null,max:null,ops:{}}));
    for(const b of d.series){const i=Math.floor((b.minute-d.start)/(step*60)),v=chartBars[i];if(!v)continue;v.requests+=b.requests;v.errors+=b.errors;v.total+=(b.average||0)*b.requests;v.max=Math.max(v.max||0,b.max||0);for(const [op,n] of Object.entries(b.ops||{}))v.ops[op]=(v.ops[op]||0)+n;}
    chartBars.forEach(b=>{b.average=b.requests?b.total/b.requests:null;b.interval=step*60;});
    const max=Math.max(1,...chartBars.map(b=>b.requests)),bw=600/count*.64,x=i=>count>1?i*(600-bw)/(count-1):(600-bw)/2;
    $('#chart').setAttribute('role','img');$('#chart').setAttribute('aria-label',step===60?t('每小时请求和失败次数','Requests and failures per hour'):t('每分钟请求和失败次数','Requests and failures per minute'));
    chartX=x;chartW=bw;
    $('#chart').innerHTML=chartBars.map((b,i)=>{const h=b.requests/max*130,eh=b.errors/max*130;return '<g data-bar="'+i+'"><rect data-track x="'+x(i)+'" y="10" width="'+bw+'" height="130" rx="1" fill="oklch(var(--ink) / '+(i===chartHover?'0.1':'0.045')+')"/><rect x="'+x(i)+'" y="'+(140-h)+'" width="'+bw+'" height="'+h+'" rx="1" fill="oklch(var(--ink) / 0.72)"/>'+(eh?'<rect x="'+x(i)+'" y="'+(140-h-eh-1)+'" width="'+bw+'" height="'+eh+'" rx="1" fill="oklch(var(--danger))"/>':'')+'</g>';}).join('');
    if(chartHover!==null)chartTip(chartHover);
    $('#range').textContent=A.clock(new Date(d.start*1000).toISOString())+' – '+A.clock(new Date(d.end*1000).toISOString());
    renderInstances();renderQueue();
  }

  // Hover a minute to see its requests, failures and timings.
  let chartBars=[],chartX=null,chartW=0,chartHover=null;
  const hm=sec=>A.clock(new Date(sec*1000).toISOString());
  function chartTip(i){
    const b=chartBars[i],tip=$('#chart-tip');if(!b){tip.classList.add('hidden');return;}
    const ops=Object.entries(b.ops||{}).filter(e=>e[1]>0).map(e=>esc(e[0])+' '+e[1]).join(' · ');
    const row=(k,v)=>'<div class="flex justify-between gap-6"><span class="text-muted">'+k+'</span><span class="font-mono">'+v+'</span></div>';
    tip.innerHTML='<div class="mb-1.5 font-mono text-[12px] text-muted">'+esc(hm(b.minute))+' – '+esc(hm(b.minute+b.interval))+'</div>'+(b.requests?
      row(t('请求','Requests'),b.requests+(ops?' <span class="text-muted">('+ops+')</span>':''))+row(t('失败','Failures'),b.errors?'<span class="text-danger">'+b.errors+'</span>':'0')+row(t('平均耗时','Mean'),Math.round(b.average)+' ms')+row(t('最大耗时','Max'),b.max+' ms'):
      '<div class="text-muted">'+t('这一时段没有请求','No requests in this interval')+'</div>');
    tip.style.left=Math.min(92,Math.max(8,(chartX(i)+chartW/2)/6))+'%';tip.classList.remove('hidden');
  }
  function chartHoverAt(i){
    if(i===chartHover)return;chartHover=i;
    $('#chart').querySelectorAll('[data-track]').forEach((r,k)=>r.setAttribute('fill','oklch(var(--ink) / '+(k===i?'0.1':'0.045')+')'));
    if(i===null)$('#chart-tip').classList.add('hidden');else chartTip(i);
  }
  $('#chart').addEventListener('mousemove',e=>{
    if(!chartBars.length)return;const r=$('#chart').getBoundingClientRect(),px=(e.clientX-r.left)/r.width*600;
    let best=0;chartBars.forEach((_,i)=>{if(Math.abs(chartX(i)+chartW/2-px)<Math.abs(chartX(best)+chartW/2-px))best=i;});chartHoverAt(best);
  });
  $('#chart').addEventListener('mouseleave',()=>chartHoverAt(null));

  // Everyone sees the instance that answered; administrators see every instance with a recent heartbeat.
  function instanceCard(id,observed,s,r,stale,load){
    const used=s?s.total_bytes-s.available_bytes:null,pct=s&&s.total_bytes?Math.min(100,Math.max(0,used/s.total_bytes*100)):0;
    // Every column stacks the same rows (label, value, detail) so values line up across columns.
    const col=(label,aside,value,detail,extra='')=>'<div class="min-w-0 '+extra+'"><div class="flex h-4 items-center justify-between gap-3"><span class="type-fig">'+label+'</span>'+(aside||'')+'</div>'+
      '<div class="mt-1.5 truncate font-mono text-[14px] leading-5"'+(value.title?' title="'+esc(value.title)+'"':'')+'>'+value.html+'</div>'+(detail?'<div class="mt-2">'+detail+'</div>':'')+'</div>';
    const health=A.status(stale?'failed':'ready').replace(stale?t('失败','Failed'):t('可查询','Searchable'),stale?t('心跳过期','Stale'):t('健康','Healthy')).replace('text-[13px]','text-[12px]');
    return '<div class="field grid grid-cols-2 items-start gap-x-8 gap-y-5 p-5 md:grid-cols-[minmax(0,1.3fr)_minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)]">'+
      col(t('实例','instance'),health,{html:esc(id),title:id},load?'<div class="truncate text-[12.5px] text-muted">'+load+'</div>':'','col-span-2 md:col-span-1')+
      col(t('磁盘','disk'),s?'<span class="font-mono text-[11px] text-muted">'+t('可用 ','Available ')+M.bytes(s.available_bytes)+'</span>':'',{html:s?M.bytes(used)+' / '+M.bytes(s.total_bytes):'<span class="text-muted">-</span>'},s?'<div class="h-[4px] rounded-full bg-ink/[0.07]"><div class="h-full rounded-full bg-ink/70" style="width:'+pct+'%"></div></div>':'','col-span-2 md:col-span-1')+
      col(t('心跳','heartbeat'),'',{html:esc(ago(observed))},'')+
      col(t('CPU 主机 · 进程','CPU host · process'),'',{html:percent(r.host_cpu_percent)+' · '+percent(r.process_cpu_percent)},'<div class="truncate text-[12.5px] text-muted">'+t('内存 ','memory ')+(Number.isFinite(r.process_rss_bytes)?M.bytes(r.process_rss_bytes):'-')+'</div>')+
    '</div>';
  }
  function renderInstances(){
    const rows=instances&&instances.instances;
    if(rows&&rows.length){
      const stale=row=>Date.parse(instances.sampled_at)-Date.parse(row.observed_at)>180000, bad=rows.filter(stale).length;
      const own=data&&!rows.some(row=>row.instance_id===data.instance_id);
      const n=rows.length+(own?1:0);
      $('#inst-sum').textContent=t('当前已经部署的服务实例 '+n+' 个，已通过接入规则分配实例服务不同仓库'+(bad?'；'+bad+' 个心跳过期':''),n+' service instance'+(n>1?'s':'')+' deployed; access rules assign instances to different repositories'+(bad?'; '+bad+' stale':''));
      $('#inst').innerHTML=(own?instanceCard(data.instance_id,data.sampled_at,data.storage,data.resources||{},false,''):'')+rows.map(row=>{const v=row.payload||{};return instanceCard(row.instance_id,row.observed_at,v.storage,v.resources||{},stale(row),t('1 小时 '+Number(v.requests_60m||0).toLocaleString()+' 次查询 · 失败 '+Number(v.errors_60m||0).toLocaleString(),Number(v.requests_60m||0).toLocaleString()+' queries in 1 hour · '+Number(v.errors_60m||0).toLocaleString()+' failed'));}).join('');
      return;
    }
    if(!data){$('#inst').innerHTML='';$('#inst-sum').textContent='';return;}
    $('#inst-sum').textContent=t('当前已经部署的服务实例，已通过接入规则分配实例服务不同仓库','Deployed service instances; access rules assign instances to different repositories');
    const r=data.resources||{};
    $('#inst').innerHTML=instanceCard(data.instance_id,data.sampled_at,data.storage,r,false,'');
  }

  function timing(j){
    const created=Date.parse(j.created_at),updated=Date.parse(j.updated_at);
    if(j.state==='running')return t('已运行 ','running ')+span(Date.now()-created);
    if(j.state==='queued')return t('已等待 ','waiting ')+span(Date.now()-created);
    if(j.state==='failed')return t(span(updated-created)+' 后失败','failed after '+span(updated-created));
    return t('用时 ','took ')+span(updated-created);
  }
  function renderQueue(){
    const jobs=allJobs();
    if(!jobs){$('#queue-sum').textContent=queueBusy?t('读取中','loading'):'';$('#jobs').innerHTML=queueError?'<p class="px-3 py-3 text-[13px] text-danger">'+esc(queueError)+'</p>':'';return;}
    const running=jobs.filter(j=>j.state==='running').length,queued=jobs.filter(j=>j.state==='queued').length;
    $('#queue-sum').textContent=t(running+' 个运行中，'+queued+' 个排队',running+' running, '+queued+' queued');
    const sorted=jobs.slice().sort((a,b)=>(ORDER[a.state]??9)-(ORDER[b.state]??9)||Date.parse(b.created_at)-Date.parse(a.created_at)),shown=sorted.slice(0,queueShown),rest=sorted.length-shown.length;
    $('#jobs').innerHTML=(queueError?'<p class="px-3 py-3 text-[13px] text-danger">'+esc(queueError)+'</p>':'')+(!sorted.length?'<p class="px-3 py-3 text-[14px] text-muted">'+t('没有任务','No jobs')+'</p>':'')+shown.map(j=>{
      const k=KIND[j.kind]||[j.kind,j.kind];
      return '<a href="/sourcegraph/repo?repo='+encodeURIComponent(j.repo)+'&tab=jobs" class="row-hover flex items-baseline gap-x-3 rounded-control px-3 py-3 text-[14px] sm:gap-x-6">'+
        '<span class="w-16 shrink-0 sm:w-24">'+A.status(j.state)+'</span>'+
        '<span class="min-w-0 flex-1 truncate"><span class="font-mono text-[13.5px]">'+esc(j.repo)+'</span><span class="ml-2 text-muted">'+t(k[0],k[1])+'</span>'+(j.error?'<span class="text-muted/50"> · </span><span class="text-danger">'+esc(API.jobError(j))+'</span>':'')+'</span>'+
        '<span class="flex shrink-0 items-baseline gap-6"><span class="hidden text-[13px] text-muted sm:inline">'+timing(j)+'</span><span class="w-12 text-right font-mono text-[12px] text-muted">'+esc(A.clock(j.created_at).slice(-5))+'</span></span></a>';
    }).join('')+(rest>0?'<button id="queue-more" type="button" class="mt-2 rounded-control px-3 py-2 text-[13.5px] text-muted hover:bg-sand hover:text-ink">'+t('查看更多（还有 '+rest+' 个）','Show more ('+rest+' more)')+'</button>':'');
    const more=$('#queue-more');if(more)more.onclick=()=>{queueShown+=QUEUE_PAGE;renderQueue();};
  }

  async function load(){try{data=await API.request('/api/admin/v1/statistics');$('#load-error').textContent='';render();}catch(e){$('#load-error').textContent=M.error(e);throw e;}}
  // Jobs live on each repository record; read up to 100 of them, three at a time.
  async function loadQueue(){
    if(queueBusy)return;queueBusy=true;controller=new AbortController();renderQueue();
    try{
      const catalog=(await API.catalog(controller.signal)).filter(r=>!r.deleted).slice(0,100),out=[],errors=[];let next=0;
      await Promise.all(Array.from({length:Math.min(3,catalog.length)},async()=>{while(next<catalog.length&&!controller.signal.aborted){const r=catalog[next++];try{out.push(await API.repo(r.name,controller.signal));}catch(e){if(e.name!=='AbortError')errors.push(r.name);}}}));
      if(!controller.signal.aborted){repos=out;queueError=errors.length?t(errors.length+' 个仓库暂时读不到任务',errors.length+' repositories could not be read'):'';}
    }catch(e){if(e.name!=='AbortError')queueError=M.error(e);}finally{queueBusy=false;controller=null;render();}
  }
  let instancesBusy=false;
  async function loadInstances(){if(!App.canManage()||instancesBusy)return;instancesBusy=true;try{instances=await API.request('/api/admin/v1/instances');renderInstances();}catch(_){}finally{instancesBusy=false;}}
  const poller=M.poll(load,30000,40), queuePoller=M.poll(loadQueue,60000,20), instancePoller=M.poll(loadInstances,60000,20);
  document.addEventListener('visibilitychange',()=>{if(document.hidden&&controller)controller.abort();});
  document.addEventListener('langchange',render);
  document.addEventListener('app:auth',()=>{$('#query-errors-link').classList.toggle('hidden',!App.canManage());if(App.canManage()&&!instances)loadInstances();});
  App.ready.then(()=>{render();poller.refresh();queuePoller.refresh();if(App.canManage()&&!instances)instancePoller.refresh();});
})();
