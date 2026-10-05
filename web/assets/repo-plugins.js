// Repository metadata only: discovery never invokes Wasm code.
(function(root){
 'use strict';
 const cache=new Map();
 root.RepoPlugins={mount(container,repo){
  const A=root.App,t=A.t,esc=A.esc;
  const selected=(repo.heads||{})[repo.default_branch]||Object.values(repo.heads||{})[0]||'';
  const key=repo.name+'@'+selected;
  container.innerHTML='<div class="flex items-start justify-between gap-5 mb-6"><p class="text-muted text-[14px] leading-relaxed">'+t('仓库插件为 MCP 读取补充业务上下文，','Repository plugins add business context to MCP reads. ')+'<a href="/sourcegraph/connect#plugins" class="hover:underline">'+t('了解更多','Learn more')+'</a></p><button type="button" id="refresh-plugins" class="shrink-0 text-muted text-[14px] hover:underline disabled:opacity-50">'+t('刷新插件','Refresh plugins')+'</button></div><div id="plugin-list" aria-live="polite"></div>';
  const list=container.querySelector('#plugin-list'),refresh=container.querySelector('#refresh-plugins');
  const field=(label,value)=>'<div class="min-w-0"><dt class="text-muted text-[12px] mb-1.5">'+label+'</dt><dd class="text-[13px] leading-relaxed break-words">'+value+'</dd></div>';
  const draw=data=>{
   if(!list.isConnected)return;
   const plugins=data.plugins||[];
   const scanIncomplete=(data.issues||[]).length>0;
   const notices=(data.issues||[]).map(i=>'<p role="status" class="text-muted text-[13px] mb-4" title="'+esc(i.message)+'">'+(i.code==='PLUGIN_DISCOVERY_FAILED'?t('插件扫描未完成，请刷新重试','Plugin scan could not complete. Refresh to retry'):esc(i.message))+'</p>').join('');
   list.innerHTML=notices+(plugins.length?'<div class="grid grid-cols-1 lg:grid-cols-2 gap-4">'+plugins.map(p=>{
    const m=p.metadata||{},issues=p.issues||[];
    const operations=(m.operations||[]).filter(op=>op==='read'||op==='read_many');
    const automatic=m.default_enabled&&operations.length>0;
    const status=issues.length?t('不可用','Unavailable'):automatic?t('默认启用','Enabled by default'):t('按需启用','On demand');
    const scope=p.root?p.root+'/':t('整个仓库','Entire repository');
    const children=plugins.filter(other=>other.name===p.name&&other.root!==p.root&&(!p.root||other.root.startsWith(p.root+'/')));
    const exclusions=children.length?'<p class="text-muted text-[12px] leading-relaxed mt-2">'+t('同名子插件覆盖：','Overridden by child plugins: ')+children.map(child=>'<span class="font-mono break-all">'+esc(child.root)+'/</span>').join(' · ')+'</p>':'';
    return '<article class="bg-sand rounded-doc p-5 sm:p-6 min-w-0 flex flex-col">'+
     '<div class="flex items-start justify-between gap-4"><div class="min-w-0"><h3 class="font-mono text-[15px] break-words">'+esc(p.name)+'</h3>'+(m.title&&m.title!==p.name?'<p class="text-[14px] mt-1.5 break-words">'+esc(m.title)+'</p>':'')+'</div><span class="shrink-0 text-[12px] '+(issues.length?'text-danger':'text-muted')+'">'+status+'</span></div>'+
     (m.description?'<p class="text-muted text-[14px] leading-relaxed mt-3 break-words">'+esc(m.description)+'</p>':'')+
     '<dl class="mt-5 grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-4">'+
     field(t('生效范围','Applies to'),'<span class="'+(p.root?'font-mono':'')+'">'+esc(scope)+'</span>'+(p.root?'<span class="text-muted">'+t(' 及子目录',' and descendants')+'</span>':'')+exclusions)+
     field(t('读取方法','Read methods'),'<span class="font-mono">'+esc((operations.length?operations:['read','read_many']).join(' / '))+'</span>')+
     (m.version?field(t('版本','Version'),'<span class="font-mono">'+esc(m.version)+'</span>'):'')+
     '</dl><div class="mt-5 pt-4 border-t border-ink/10">'+
     '<p class="font-mono text-muted text-[12px] leading-relaxed break-all">'+esc(p.path)+'</p></div>'+
     issues.map(i=>'<p class="text-danger text-[13px] leading-relaxed mt-3">'+esc(i.message)+'</p>').join('')+'</article>';
   }).join('')+'</div>':'<div class="bg-sand rounded-doc px-6 py-12 text-center"><p class="text-[15px]">'+(scanIncomplete?t('暂时无法确认仓库插件','Repository plugins could not be determined'):t('当前仓库范围未发现插件','No plugins found within this repository’s scope'))+'</p><p class="text-muted text-[13px] mt-2">'+(scanIncomplete?t('若重试后仍未恢复，请检查仓库同步状态；悬停上方提示可查看原因','If retrying does not help, check repository sync status; hover over the notice for details'):t('同步包含插件文件的提交后，将在这里展示','Plugins appear here after syncing a commit that contains them'))+'</p></div>');
  };
  async function load(force){
   if(refresh.disabled)return;
   if(!force&&cache.has(key)){draw(cache.get(key));return;}
   refresh.disabled=true;
   refresh.textContent=t('刷新中…','Refreshing…');
   list.setAttribute('aria-busy','true');
   if(!list.children.length)list.textContent=t('读取插件元信息…','Loading plugin metadata…');
   try{const data=await root.API.request('/v1/repo/plugins?repo='+encodeURIComponent(repo.name)+(selected?'&revision='+encodeURIComponent(selected):''));if(!(data.issues||[]).length)cache.set(key,data);else cache.delete(key);draw(data);}
   catch(e){if(list.isConnected){list.innerHTML='<p role="alert" class="text-danger text-[14px]">'+esc(e.message)+'</p>';}}
   finally{refresh.disabled=false;refresh.textContent=t('刷新插件','Refresh plugins');list.removeAttribute('aria-busy');}
  }
  refresh.onclick=()=>load(true);load(false);
 }};
})(window);
