// Explicit repository maintenance. Every mutation is tied to a server impact preview.
(function (root) {
  'use strict';
  function mount(options) {
    const A=root.App,M=root.Management,t=A.t,esc=A.esc,d=document.querySelector('#confirm');
    const $=s=>d.querySelector(s),keys=M.operationKeys();
    const input='mt-2 block w-full rounded-control bg-ink/[0.04] px-3 py-2 text-[13.5px] outline-none focus-visible:ring-1 focus-visible:ring-ink/20';
    let epoch=0,working=false,indexState=null,preview=null,action='';
    const repo=()=>options.repository();
    const url=path=>'/api/admin/v1/repo/'+path+'?repo='+encodeURIComponent(repo().name);
    const fullSHA=commit=>typeof commit==='string'&&/^[0-9a-f]{40}$/.test(commit);
    const branchNames=()=>[...new Set([...(repo().policy.branches||[]),...(repo().policy.default_branch&&repo().default_branch?[repo().default_branch]:[])])];
    function error(e){
      const messages={REPOSITORY_BUSY:t('仓库正在执行任务，请先在任务页停止运行中的任务，再重试','Stop the running repository jobs from Jobs, then retry'),LATEST_INDEX_NOT_READY:t('部分监听分支的最新提交尚未建立索引，请先检查更新并完成索引，再清理','Some watched branch heads are not indexed. Check for updates and finish indexing before cleanup'),REVISION_NOT_ELIGIBLE:t('此提交不在当前保留范围内，请调整仓库设置后重试','This commit is outside the retention policy. Adjust repository settings before retrying'),REVISION_NOT_ON_BRANCH:t('此提交不属于所选分支，请选择对应分支或其他提交','This commit does not belong to the selected branch'),DEFAULT_BRANCH_NOT_OBSERVED:t('主干尚未同步，请先检查更新','The default branch has not synchronized. Check for updates first'),BRANCH_NOT_OBSERVED:t('此分支尚未同步，请先检查更新','This branch has not synchronized. Check for updates first'),REPOSITORY_STORAGE_MISSING:t('仓库本地材料尚未就绪，请先检查更新','Local repository data is not ready. Check for updates first')};
      const target=$('#maintenance-error');if(target)target.textContent=messages[e.code]||M.error(e);
    }
    function lock(value){working=value;options.busy(value);d.querySelectorAll('button,input,select').forEach(el=>el.disabled=value);}
    function close(){if(working)return;epoch++;d.close();indexState=null;preview=null;}
    function frame(title,content,footer){
      d.style.width='min(92vw, 600px)';d.style.maxHeight='90vh';d.style.overflowY='auto';
      d.innerHTML='<form id="maintenance-form"><div class="px-6 pt-6"><h2 id="maintenance-title" class="font-display text-[20px] font-medium">'+title+'</h2><p class="mt-1 break-all font-mono text-[12.5px] text-muted">'+esc(repo().name)+'</p>'+content+'<p id="maintenance-error" role="alert" class="mt-3 text-[13px] text-danger empty:hidden"></p></div><div class="flex justify-end gap-2 px-6 pb-6 pt-5">'+footer+'</div></form>';
      d.setAttribute('aria-labelledby','maintenance-title');
      d.oncancel=e=>{if(working)e.preventDefault();else close();};
      $('[data-dismiss]')?.addEventListener('click',close);
      if(!d.open)d.showModal();
    }
    const cancel=()=>'<button data-dismiss type="button" class="rounded-control px-3 py-1.5 text-[13.5px] text-muted hover:bg-sand">'+t('取消','Cancel')+'</button>';
    const submit=(text,danger=false)=>'<button id="maintenance-submit" type="submit" class="rounded-control px-3 py-1.5 text-[13.5px] font-medium disabled:opacity-40 '+(danger?'bg-danger text-white':'bg-ink text-paper')+'">'+text+'</button>';
    function commitRow(c,selected=false){return '<span class="flex min-w-0 items-baseline gap-2"><span class="shrink-0 font-mono text-[12px]">#'+esc(A.short(c.commit))+'</span><span class="min-w-0 truncate text-[13px]" title="'+esc(c.subject||'')+'">'+esc(c.subject||t('无提交说明','No commit message'))+'</span>'+(selected?'<span class="ml-auto shrink-0" aria-hidden="true">✓</span>':'')+'</span><time class="mt-1 block text-[11.5px] text-muted" datetime="'+esc(c.committer_time||'')+'">'+esc(M.time(c.committer_time))+'</time>';}
    function selectedCommit(){return indexState.commits.find(c=>c.commit===indexState.commit);}
    function renderCommitChoice(){
      const current=selectedCommit();
      $('#index-commit-summary').innerHTML=(current?'<span class="min-w-0 flex-1">'+commitRow(current)+'</span>':'<span class="text-muted">'+t('选择最近的提交','Choose a recent commit')+'</span>')+'<span class="shrink-0 text-muted">'+A.ICON.chevron+'</span>';
      $('#index-commit-list').innerHTML=indexState.commits.length?indexState.commits.map(c=>'<button type="button" data-select-commit="'+esc(c.commit)+'" class="block w-full min-w-0 rounded-control px-3 py-2 text-left hover:bg-sand '+(c.commit===indexState.commit?'bg-sand':'')+'" title="'+esc(c.commit)+'">'+commitRow(c,c.commit===indexState.commit)+'</button>').join(''):'<p class="px-3 py-2 text-[13px] text-muted">'+t('当前没有可选提交，请先检查更新','No commits available. Check for updates first')+'</p>';
      d.querySelectorAll('[data-select-commit]').forEach(button=>button.onclick=()=>{indexState.commit=button.dataset.selectCommit;$('#index-sha').value=indexState.commit;$('#index-commits').open=false;renderCommitChoice();});
      const note=$('#index-note');note.textContent=current?.eligible===false?t('此提交不在当前保留范围内，需要先调整仓库设置','This commit is outside retention; adjust repository settings first'):current?.indexed?t('这个提交已有索引，确认时会检查是否仍有范围需要准备','This commit is indexed. Confirmation checks for any remaining scopes'):t('列出此分支最近 20 个已同步提交，也可输入完整 SHA','Shows the latest 20 synchronized commits on this branch. You can also enter a full SHA');
    }
    function renderIndex(){
      const branches=indexState.branches;
      frame(t('新增索引','Add index'),'<div class="mt-5 space-y-4"><div><label for="index-branch" class="text-[13.5px]">'+t('分支','Branch')+'</label><select id="index-branch" class="'+input+'">'+branches.map(b=>'<option value="'+esc(b)+'" '+(b===indexState.branch?'selected':'')+'>'+esc(b)+'</option>').join('')+'</select></div><div><label for="index-sha" class="text-[13.5px]">Commit SHA</label><details id="index-commits" class="relative mt-2"><summary id="index-commit-summary" class="flex cursor-pointer list-none items-center gap-3 rounded-control bg-ink/[0.04] px-3 py-2"></summary><div id="index-commit-list" class="lift absolute inset-x-0 z-10 mt-1 max-h-64 overflow-y-auto rounded-surface bg-card p-1"></div></details><input id="index-sha" autocomplete="off" spellcheck="false" class="'+input+' font-mono" value="'+esc(indexState.commit)+'" placeholder="'+t('输入完整的 40 位 Commit SHA','Enter a full 40-character commit SHA')+'"><p id="index-note" class="mt-2 text-[12.5px] leading-5 text-muted"></p></div><p class="text-[13px] text-muted">'+t('按此仓库已登记的目录范围建立索引。下一步确认分支、提交和影响范围。','Index this repository’s registered scopes. Review the branch, commit and scopes in the next step.')+'</p></div>',cancel()+submit(t('下一步','Continue')));
      $('#index-branch').onchange=()=>loadCommits($('#index-branch').value);
      $('#index-sha').oninput=()=>{indexState.commit=$('#index-sha').value.trim().toLowerCase();preview=null;renderCommitChoice();$('#maintenance-error').textContent='';};
      $('#maintenance-form').onsubmit=previewIndex;
      renderCommitChoice();
      if(!branches.length)$('#maintenance-submit').disabled=true;
    }
    async function loadCommits(branch=''){
      const current=++epoch;
      lock(true);$('#maintenance-error').textContent='';
      try{
        const response=await root.API.request(url('commit-options')+(branch?'&branch='+encodeURIComponent(branch):''));
        if(current!==epoch||!d.open)return;
        indexState={branches:response.branches||branchNames(),branch:response.branch||branch,commits:response.commits||[],commit:response.head||response.commits?.[0]?.commit||''};
        preview=null;renderIndex();
      }catch(e){if(current===epoch){indexState.commit='';indexState.commits=[];renderIndex();error(e);}}
      finally{if(current===epoch){lock(false);if(!indexState.branches.length)$('#maintenance-submit').disabled=true;}}
    }
    async function previewIndex(e){
      e.preventDefault();if(working)return;
      indexState.commit=$('#index-sha').value.trim().toLowerCase();
      if(!fullSHA(indexState.commit)){error(new Error(t('请填写完整的 40 位 Commit SHA','Enter a full 40-character commit SHA')));return;}
      lock(true);
      try{preview=await root.API.request(url('index/preview'),{method:'POST',body:{branch:indexState.branch,commit:indexState.commit}});renderIndexConfirmation();}
      catch(e){error(e);}finally{lock(false);}
    }
    function pair(label,value){return '<div class="border-t rule py-3"><dt class="text-[12px] text-muted">'+label+'</dt><dd class="mt-1 break-all text-[13.5px]">'+value+'</dd></div>';}
    function renderIndexConfirmation(){
      const c=selectedCommit(),groups=(preview.groups||[]).map(g=>typeof g==='string'?g:g.name);
      frame(t('确认新增索引','Confirm new index'),'<dl class="mt-5">'+pair(t('分支','Branch'),esc(preview.branch))+pair('Commit SHA','<span class="font-mono">'+esc(preview.commit)+'</span>'+(c?'<p class="mt-1 text-muted">'+esc(c.subject||'')+' · '+esc(M.time(c.committer_time))+'</p>':''))+pair(t('索引范围','Index scope'),groups.length?groups.map(g=>'<span class="mr-2 font-mono">'+esc(g||t('全仓库','Full repository'))+'</span>').join(''):t('全仓库','Full repository'))+'</dl><p class="mt-3 text-[13px] text-muted">'+t('确认后异步准备该提交，已就绪的索引会复用。任务进度可在任务页查看。','Confirmation queues preparation for this commit. Ready indexes are reused. Follow progress in Jobs.')+'</p>',cancel()+'<button id="index-back" type="button" class="rounded-control px-3 py-1.5 text-[13.5px] hover:bg-sand">'+t('返回修改','Back')+'</button>'+submit(t('确认新增','Confirm')));
      $('#index-back').onclick=renderIndex;
      $('#maintenance-form').onsubmit=async event=>{
        event.preventDefault();if(working)return;lock(true);
        const body={branch:preview.branch,commit:preview.commit,preview:preview.preview};
        try{
          const result=await root.API.request(url('index'),{method:'POST',headers:{'If-Match':'"'+preview.revision+'"','Idempotency-Key':keys.get(body)},body});
          if(result.status!=='ready'&&result.status!=='already_indexed')M.jobReceipts(result);
          keys.done(body);d.close();options.jobs();await options.reload();A.toast(t('索引请求已提交','Index request submitted'));
        }catch(e){error(e);if(e.status===412)$('#maintenance-error').textContent+=t('；请返回修改后重新确认','; go back and confirm again');}
        finally{lock(false);}
      };
    }
    function cleanupContent(value){
      const keeps=value.keep||[];
      return '<p class="mt-4 text-[14px] leading-6 text-muted">'+t('每个监听分支、每个目录分组只保留当前最新提交的 SHA 索引。其余索引永久清理，之后需要时可重新准备。','Keep the current branch-head SHA index for each watched branch and path group. Other indexes are permanently removed and can be prepared again when needed.')+'</p><div class="mt-4 grid grid-cols-2 gap-3 rounded-surface bg-ink/[0.03] p-4 text-[13px]">'+pair(t('清理索引代数','Generations to remove'),esc(value.remove_generations??0))+pair(t('清理分片','Shards to remove'),esc(value.remove_shards??0))+pair(t('预计释放','Estimated recovery'),esc(M.bytes(value.reclaim_bytes)))+'</div><h3 class="mt-4 text-[13.5px] font-medium">'+t('保留的版本','Versions to keep')+'</h3><ul class="mt-2 max-h-52 space-y-2 overflow-auto text-[12.5px]">'+keeps.map(k=>'<li class="flex flex-wrap gap-x-3 gap-y-1"><span class="font-mono">'+esc(k.branch)+'</span><span class="text-muted">'+esc(k.group||t('全仓库','Full repository'))+'</span><span class="font-mono" title="'+esc(k.commit)+'">#'+esc(A.short(k.commit))+'</span></li>').join('')+'</ul><p class="mt-4 text-[12.5px] leading-5 text-muted">'+t('不会删除仓库登记或改变监听范围、保留策略。定时更新以后仍可能生成符合策略的索引。','Repository registration, watched scopes and retention policy stay in place. Scheduled updates can create indexes covered by that policy again.')+'</p>';
    }
    async function openCleanup(){
      action='cleanup';preview=null;
      frame(t('清理落后索引','Clean old indexes'),'<p class="mt-5 text-[14px] text-muted">'+t('正在计算清理范围…','Calculating the cleanup scope…')+'</p>',cancel());lock(true);
      try{preview=await root.API.request(url('cleanup/preview'),{method:'POST',body:{}});frame(t('确认清理落后索引','Confirm index cleanup'),cleanupContent(preview),cancel()+submit(t('确认清理','Confirm cleanup'),true));$('#maintenance-form').onsubmit=applyMaintenance;}
      catch(e){error(e);}finally{lock(false);}
    }
    async function openDelete(){
      action='delete';preview=null;
      frame(t('删除仓库及全部内容','Delete repository and all data'),'<p class="mt-5 text-[14px] text-muted">'+t('正在检查删除范围…','Checking the deletion scope…')+'</p>',cancel());lock(true);
      try{
        preview=await root.API.request(url('delete/preview'),{method:'POST',body:{}});
        frame(t('确认删除仓库及全部内容','Confirm repository deletion'),'<p class="mt-4 text-[14px] leading-6 text-muted">'+t('将取消排队任务，永久删除仓库登记、全部 Git 内容、所有目录分组及版本索引和临时产物，释放占用空间。运行中的任务须先停止。此操作无法撤销。','Cancels queued jobs and permanently removes the repository registration, all Git data, every path group and version index, and temporary artifacts. Stop running jobs first. This cannot be undone.')+'</p><label for="delete-name" class="mt-5 block text-[13px]">'+t('输入完整仓库名确认','Type the full repository name to confirm')+'</label><input id="delete-name" required autocomplete="off" class="'+input+' font-mono" placeholder="'+esc(repo().name)+'">',cancel()+submit(t('确认永久删除','Delete permanently'),true));
        $('#maintenance-form').onsubmit=applyMaintenance;
      }catch(e){error(e);}finally{lock(false);}
    }
    async function applyMaintenance(e){
      e.preventDefault();if(working||!preview)return;
      if(action==='delete'&&$('#delete-name').value!==repo().name){error(new Error(t('名称不一致','Name does not match')));return;}
      lock(true);
      try{
        await root.API.request(url(action),{method:'POST',headers:{'If-Match':'"'+preview.revision+'"'},body:{preview:preview.preview}});
        d.close();if(action==='delete'){location.assign('/sourcegraph/repos');return;}
        await options.reload();A.toast(t('落后索引已清理','Old indexes cleaned'));
      }catch(e){error(e);if(e.status===412)$('#maintenance-error').textContent+=t('；请关闭后重新预览','; close and preview again');}
      finally{lock(false);}
    }
    return {open:kind=>{
      if(working||options.isBusy()||!M.manage())return;
      if(kind==='index'){action=kind;indexState={branches:branchNames(),branch:branchNames()[0]||'',commits:[],commit:''};renderIndex();loadCommits(indexState.branch);}
      else if(kind==='cleanup')openCleanup();
      else if(kind==='delete'||kind==='purge')openDelete();
    }};
  }
  root.RepoMaintenance={mount};
})(window);
